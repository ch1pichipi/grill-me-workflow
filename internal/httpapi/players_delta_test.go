package httpapi

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"chatmod/internal/store"
)

// drain 收完一段时间内到达的事件。用它而不是 waitFor，是为了能断言
// “某个事件一次都没出现”——只看第一个到达的事件是看不出这一点的。
func (f *fakeHub) drain(d time.Duration) []map[string]any {
	var out []map[string]any
	deadline := time.After(d)
	for {
		select {
		case ev := <-f.ch:
			out = append(out, ev)
		case <-deadline:
			return out
		}
	}
}

// drainUntilIdle 收到“连续 idle 无事件”为止。合并后的份数本质上跟机器快慢有关，
// 所以不能断言“恰好 1 次”，只能断言“不随变化次数线性增长”。
func (f *fakeHub) drainUntilIdle(idle, max time.Duration) []map[string]any {
	var out []map[string]any
	deadline := time.After(max)
	for {
		select {
		case ev := <-f.ch:
			out = append(out, ev)
		case <-time.After(idle):
			return out
		case <-deadline:
			return out
		}
	}
}

func countEvents(events []map[string]any, typ string) int {
	n := 0
	for _, ev := range events {
		if ev["type"] == typ {
			n++
		}
	}
	return n
}

func eventOfType(t *testing.T, events []map[string]any, typ string) map[string]any {
	t.Helper()
	for _, ev := range events {
		if ev["type"] == typ {
			return ev
		}
	}
	t.Fatalf("没等到事件 %q（收到 %v）", typ, eventTypes(events))
	return nil
}

func lastEventOfType(t *testing.T, events []map[string]any, typ string) map[string]any {
	t.Helper()
	for i := len(events) - 1; i >= 0; i-- {
		if events[i]["type"] == typ {
			return events[i]
		}
	}
	t.Fatalf("没等到事件 %q（收到 %v）", typ, eventTypes(events))
	return nil
}

func eventTypes(events []map[string]any) []string {
	out := make([]string, 0, len(events))
	for _, ev := range events {
		s, _ := ev["type"].(string)
		out = append(out, s)
	}
	return out
}

// uniqueTestTime 是固定的玩家创建时刻：广播字节数要能逐字节比较，
// 就不能让 CreatedAt 的纳秒尾数在不同规模下不一样长。
var uniqueTestTime = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

func createStorePlayers(t *testing.T, st *store.Store, from, n int) []int64 {
	t.Helper()
	ids := make([]int64, 0, n)
	for i := from; i < from+n; i++ {
		p, err := st.CreatePlayer(fmt.Sprintf("p%02d", i), uniqueTestTime)
		if err != nil {
			t.Fatalf("创建玩家失败: %v", err)
		}
		ids = append(ids, p.ID)
	}
	return ids
}

// 玩家列表是 bootstrap / CRUD 才走的路，但它仍然不该随玩家数增长。
func TestPlayersListUsesConstantQueries(t *testing.T) {
	svc, st := newTestService(t)

	createStorePlayers(t, st, 0, 1)
	before := st.QueryCount()
	if _, err := svc.Players(); err != nil {
		t.Fatal(err)
	}
	small := st.QueryCount() - before

	createStorePlayers(t, st, 1, 49)
	before = st.QueryCount()
	if _, err := svc.Players(); err != nil {
		t.Fatal(err)
	}
	large := st.QueryCount() - before

	if small != large {
		t.Fatalf("玩家列表查询条数随玩家数增长：1 人 %d 条，50 人 %d 条", small, large)
	}
	if large > 4 {
		t.Fatalf("玩家列表应固定 4 条批量查询，实际 %d 条", large)
	}
}

// 一条消息只该广播变动的那一行，不该再发整份列表。
func TestSendEmitsOnlyTheChangedRow(t *testing.T) {
	svc, st := newTestService(t)
	svc.flushInterval = time.Hour
	hub := newFakeHub()
	svc.SetBroadcaster(hub)

	ids := createStorePlayers(t, st, 0, 3)
	res, err := svc.Send(ids[1], "今天天气不错")
	if err != nil {
		t.Fatal(err)
	}

	events := hub.drain(100 * time.Millisecond)
	if n := countEvents(events, "players"); n != 0 {
		t.Fatalf("发消息不该再广播整份玩家列表，收到 %d 次（%v）", n, eventTypes(events))
	}
	ev := eventOfType(t, events, "players.changed")
	rows, ok := ev["players"].([]any)
	if !ok || len(rows) != 1 {
		t.Fatalf("players.changed 应只带 1 行，实际 %+v", ev["players"])
	}
	row, _ := rows[0].(map[string]any)
	if int64(row["id"].(float64)) != ids[1] {
		t.Fatalf("增量行的玩家不对: %+v", row)
	}
	// 增量行里的两个计数必须和回包一致，否则界面与判定会各说一套。
	if int(row["count7d"].(float64)) != res.Entry.Count7d ||
		int(row["lifetime"].(float64)) != res.Entry.Lifetime {
		t.Fatalf("增量行计数 %v/%v 与判定 %d/%d 不一致",
			row["count7d"], row["lifetime"], res.Entry.Count7d, res.Entry.Lifetime)
	}
	if c := countEvents(events, "stream"); c != 1 {
		t.Fatalf("判定流应恰好一条，实际 %d", c)
	}
}

// 硬闸：玩家数从 1 涨到 50，一条消息的查询条数与广播字节数都不许变。
func TestSendCostIsIndependentOfPlayerCount(t *testing.T) {
	measure := func(players int) (int64, int) {
		svc, st := newTestService(t)
		svc.flushInterval = time.Hour
		hub := newFakeHub()
		svc.SetBroadcaster(hub)

		ids := createStorePlayers(t, st, 0, players)

		before := st.QueryCount()
		if _, err := svc.Send(ids[0], "今天天气不错"); err != nil {
			t.Fatal(err)
		}
		queries := st.QueryCount() - before

		bytes := 0
		for _, ev := range hub.drain(100 * time.Millisecond) {
			switch ev["type"] {
			case "players":
				t.Fatalf("发消息不该广播整份玩家列表（%d 玩家时）", players)
			case "players.changed":
				b, err := json.Marshal(ev)
				if err != nil {
					t.Fatal(err)
				}
				bytes += len(b)
			}
		}
		if bytes == 0 {
			t.Fatalf("%d 玩家时应收到一条 players.changed", players)
		}
		return queries, bytes
	}

	q1, b1 := measure(1)
	q50, b50 := measure(50)
	if q1 != q50 {
		t.Fatalf("单条消息的查询条数随玩家数增长：1 人 %d 条，50 人 %d 条", q1, q50)
	}
	if b1 != b50 {
		t.Fatalf("单条消息的广播字节数随玩家数增长：1 人 %d 字节，50 人 %d 字节", b1, b50)
	}
}

// 禁言在 Send 内被发现已到期时：只补一条 mute 事件 + 一条增量行，
// 不再顺手重算整份列表。
func TestSendAfterMuteExpiryEmitsDeltaOnly(t *testing.T) {
	svc, st := newTestService(t)
	svc.flushInterval = time.Hour
	hub := newFakeHub()
	svc.SetBroadcaster(hub)

	ids := createStorePlayers(t, st, 0, 2)
	past := time.Now().Add(-time.Minute)
	if _, err := st.CreateMute(store.Mute{
		PlayerID: ids[0], StartedAt: past.Add(-time.Hour), ExpiresAt: &past,
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := svc.Send(ids[0], "今天天气不错"); err != nil {
		t.Fatal(err)
	}

	events := hub.drain(100 * time.Millisecond)
	if n := countEvents(events, "players"); n != 0 {
		t.Fatalf("不应广播整份玩家列表，收到 %d 次（%v）", n, eventTypes(events))
	}
	ev := eventOfType(t, events, "mute")
	if ev["mute"] != nil || ev["endReason"] != store.EndReasonExpired {
		t.Fatalf("应推送一次到期解除：%+v", ev)
	}
	row := eventOfType(t, events, "players.changed")["players"].([]any)[0].(map[string]any)
	if row["mute"] != nil {
		t.Fatalf("增量行不该还带着已到期的禁言: %+v", row)
	}
}

// 提前解除同理：只广播那一行。
func TestLiftEmitsDeltaOnly(t *testing.T) {
	svc, st := newTestService(t)
	svc.flushInterval = time.Hour
	hub := newFakeHub()
	svc.SetBroadcaster(hub)

	ids := createStorePlayers(t, st, 0, 2)
	if _, err := st.CreateMute(store.Mute{PlayerID: ids[0], StartedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}

	if err := svc.Lift(ids[0], "误判"); err != nil {
		t.Fatal(err)
	}

	events := hub.drain(100 * time.Millisecond)
	if n := countEvents(events, "players"); n != 0 {
		t.Fatalf("提前解除不该广播整份玩家列表，收到 %d 次（%v）", n, eventTypes(events))
	}
	row := eventOfType(t, events, "players.changed")["players"].([]any)[0].(map[string]any)
	if row["mute"] != nil {
		t.Fatalf("解除后增量行的 mute 应为 null: %+v", row)
	}
	if int64(row["id"].(float64)) != ids[0] {
		t.Fatalf("增量行应是刚被解除的玩家: %+v", row)
	}
}

// 建/改/删玩家的低频全量广播要合并：一个周期内建 50 个玩家只发一份列表。
func TestPlayerCrudCoalescesFullListBroadcast(t *testing.T) {
	svc, _ := newTestService(t)
	svc.flushInterval = 20 * time.Millisecond
	hub := newFakeHub()
	svc.SetBroadcaster(hub)

	for i := 0; i < 50; i++ {
		if _, err := svc.CreatePlayer(fmt.Sprintf("u%02d", i)); err != nil {
			t.Fatal(err)
		}
	}

	events := hub.drainUntilIdle(100*time.Millisecond, 3*time.Second)
	// 50 次变化最多只该产生一个量级的广播，绝不可能是 50 份。
	if n := countEvents(events, "players"); n < 1 || n > 5 {
		t.Fatalf("50 次建玩家应被合并成少数几次广播，实际 %d 次", n)
	}
	last := lastEventOfType(t, events, "players")
	if rows := last["players"].([]any); len(rows) != 50 {
		t.Fatalf("合并后的列表应有 50 行，实际 %d", len(rows))
	}

	// 删除同样要落地，且最终状态完整。
	firstID := int64(last["players"].([]any)[0].(map[string]any)["id"].(float64))
	if err := svc.DeletePlayer(firstID); err != nil {
		t.Fatal(err)
	}
	events = hub.drainUntilIdle(100*time.Millisecond, 3*time.Second)
	if n := countEvents(events, "players"); n < 1 || n > 5 {
		t.Fatalf("删除应被合并成少数几次广播，实际 %d 次", n)
	}
	if rows := lastEventOfType(t, events, "players")["players"].([]any); len(rows) != 49 {
		t.Fatalf("删除后列表应有 49 行，实际 %d", len(rows))
	}
}

// 合并窗口内若没有变化，到期后不应白发一份列表。
func TestPlayerFlushIsNoOpWithoutChanges(t *testing.T) {
	svc, _ := newTestService(t)
	svc.flushInterval = 20 * time.Millisecond
	hub := newFakeHub()
	svc.SetBroadcaster(hub)

	if _, err := svc.CreatePlayer("alice"); err != nil {
		t.Fatal(err)
	}
	if n := countEvents(hub.drainUntilIdle(100*time.Millisecond, 3*time.Second), "players"); n < 1 {
		t.Fatalf("应至少一次全量广播，实际 %d", n)
	}
	// 之后一段时间内没有任何变化，不该再白发。
	if n := countEvents(hub.drain(300*time.Millisecond), "players"); n != 0 {
		t.Fatalf("无变化时不该再有全量广播，实际 %d", n)
	}
}
