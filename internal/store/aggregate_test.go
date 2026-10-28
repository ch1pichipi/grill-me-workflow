package store

import (
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	st, err := Open(filepath.Join(t.TempDir(), "store.db"))
	if err != nil {
		t.Fatalf("打开临时数据库失败: %v", err)
	}
	t.Cleanup(func() { st.Close() })
	return st
}

// insertJudgment 让测试读起来像“这个玩家某时刻产生了一条判定”。
func insertJudgment(t *testing.T, st *Store, playerID int64, violation bool, at time.Time) {
	t.Helper()
	if _, err := st.InsertJudgment(Judgment{
		PlayerID:    playerID,
		Severity:    1,
		IsViolation: violation,
		Disposition: "allow",
	}, at); err != nil {
		t.Fatalf("写入判定失败: %v", err)
	}
}

// 批量计数必须与逐玩家查询逐一对账：它不是另一套语义，只是换了一种读法。
func TestViolationCountsByPlayerMatchesPerPlayerQueries(t *testing.T) {
	st := newTestStore(t)
	p1, err := st.CreatePlayer("alice", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	p2, err := st.CreatePlayer("bob", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	p3, err := st.CreatePlayer("carol", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	now := time.Now()
	insertJudgment(t, st, p1.ID, true, now.Add(-time.Hour))      // 7 天内
	insertJudgment(t, st, p1.ID, true, now.Add(-8*24*time.Hour)) // 窗口外，只进终身
	insertJudgment(t, st, p1.ID, false, now.Add(-time.Minute))   // 非违规，两个计数都不算
	insertJudgment(t, st, p3.ID, true, now.Add(-10*24*time.Hour))

	const violationWindow = 7 * 24 * time.Hour
	since := now.Add(-violationWindow)

	got7d, err := st.ViolationCountsSinceByPlayer(since)
	if err != nil {
		t.Fatal(err)
	}
	gotLife, err := st.ViolationCountsLifetimeByPlayer()
	if err != nil {
		t.Fatal(err)
	}

	// 干净的玩家不该出现在 map 里：零值即正确值。
	if _, ok := got7d[p2.ID]; ok {
		t.Fatalf("无违规记录的玩家不应出现在 7 天计数里: %+v", got7d)
	}
	if got7d[p1.ID] != 1 || got7d[p3.ID] != 0 {
		t.Fatalf("7 天计数不对: %+v", got7d)
	}
	if gotLife[p1.ID] != 2 || gotLife[p3.ID] != 1 {
		t.Fatalf("终身计数不对: %+v", gotLife)
	}

	for _, p := range []Player{p1, p2, p3} {
		want7d, err := st.CountViolationsSince(p.ID, since)
		if err != nil {
			t.Fatal(err)
		}
		wantLife, err := st.CountViolationsLifetime(p.ID)
		if err != nil {
			t.Fatal(err)
		}
		if got7d[p.ID] != want7d || gotLife[p.ID] != wantLife {
			t.Fatalf("玩家 %s 批量结果与单玩家查询不一致: 批量(%d,%d) 单查(%d,%d)",
				p.Name, got7d[p.ID], gotLife[p.ID], want7d, wantLife)
		}
	}
}

// 只返回 ended_at IS NULL；到期与否交给调用方惰性判定（与 ActiveMute 的约定一致）。
func TestActiveMutesByPlayerReturnsOnlyActiveRows(t *testing.T) {
	st := newTestStore(t)
	permanent, err := st.CreatePlayer("perm", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	expiring, err := st.CreatePlayer("expiring", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	ended, err := st.CreatePlayer("ended", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	if _, err := st.CreateMute(Mute{PlayerID: permanent.ID, StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	future := time.Now().Add(time.Hour)
	if _, err := st.CreateMute(Mute{PlayerID: expiring.ID, StartedAt: time.Now(), ExpiresAt: &future}); err != nil {
		t.Fatal(err)
	}
	endedID, err := st.CreateMute(Mute{PlayerID: ended.ID, StartedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if err := st.EndMute(endedID, EndReasonLifted, time.Now(), "测试"); err != nil {
		t.Fatal(err)
	}

	got, err := st.ActiveMutesByPlayer()
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("应只有 2 条活跃禁言，得到 %d: %+v", len(got), got)
	}
	if m, ok := got[permanent.ID]; !ok || !m.Permanent() {
		t.Fatalf("永久禁言应出现在 map 里且 ExpiresAt 为 nil: %+v", got[permanent.ID])
	}
	if m, ok := got[expiring.ID]; !ok || m.ExpiresAt == nil {
		t.Fatalf("有期限的活跃禁言应带 ExpiresAt: %+v", got[expiring.ID])
	}
	if _, ok := got[ended.ID]; ok {
		t.Fatal("已结束的禁言不应出现")
	}

	// store 不做时间判断：已过期但未 EndMute 的行仍然返回。
	past := time.Now().Add(-time.Minute)
	stale, err := st.CreatePlayer("stale", time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st.CreateMute(Mute{PlayerID: stale.ID, StartedAt: past.Add(-time.Hour), ExpiresAt: &past}); err != nil {
		t.Fatal(err)
	}
	got, err = st.ActiveMutesByPlayer()
	if err != nil {
		t.Fatal(err)
	}
	m, ok := got[stale.ID]
	if !ok || m.ExpiresAt == nil || !m.ExpiresAt.Equal(past.Truncate(time.Millisecond)) {
		t.Fatalf("已过期但未 EndMute 的禁言仍应返回，交给调用方惰性判定: %+v", m)
	}
}

// 计数器存在的唯一目的：让测试能钉住“单条消息的成本与玩家数无关”。
func TestQueryCountTracksPlayerScopedReads(t *testing.T) {
	st := newTestStore(t)
	p, err := st.CreatePlayer("alice", time.Now())
	if err != nil {
		t.Fatal(err)
	}

	before := st.QueryCount()
	if _, err := st.Players(); err != nil {
		t.Fatal(err)
	}
	if got := st.QueryCount() - before; got != 1 {
		t.Fatalf("Players() 应记 1 条查询，实际 %d", got)
	}

	before = st.QueryCount()
	if _, err := st.CountViolationsSince(p.ID, time.Now().Add(-time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := st.CountViolationsLifetime(p.ID); err != nil {
		t.Fatal(err)
	}
	if got := st.QueryCount() - before; got != 2 {
		t.Fatalf("两次逐玩家计数应记 2 条查询，实际 %d", got)
	}

	before = st.QueryCount()
	if _, err := st.ActiveMutesByPlayer(); err != nil {
		t.Fatal(err)
	}
	if got := st.QueryCount() - before; got != 1 {
		t.Fatalf("批量读禁言应记 1 条查询，实际 %d", got)
	}
}
