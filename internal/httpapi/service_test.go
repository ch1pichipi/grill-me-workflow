package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"chatmod/internal/corpus"
	"chatmod/internal/drafter"
	"chatmod/internal/moderation"
	"chatmod/internal/punishment"
	"chatmod/internal/store"
)

type fakeHub struct {
	ch chan map[string]any
}

func newFakeHub() *fakeHub { return &fakeHub{ch: make(chan map[string]any, 32)} }

func (f *fakeHub) Broadcast(v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return
	}
	select {
	case f.ch <- m:
	default:
	}
}

func (f *fakeHub) waitFor(t *testing.T, typ string) map[string]any {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for {
		select {
		case ev := <-f.ch:
			if ev["type"] == typ {
				return ev
			}
		case <-deadline:
			t.Fatalf("没等到事件 %q", typ)
			return nil
		}
	}
}

func newTestService(t *testing.T) (*Service, *store.Store) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	st, err := store.Open(path)
	if err != nil {
		t.Fatalf("打开临时数据库失败: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	svc, err := NewService(st, nil, time.Now)
	if err != nil {
		t.Fatalf("初始化服务失败: %v", err)
	}
	t.Cleanup(svc.Close)
	return svc, st
}

func TestSeedCorpusIsValid(t *testing.T) {
	c := SeedCorpus()
	snap, err := corpus.Build(c)
	if err != nil {
		t.Fatalf("种子语料库无法编译: %v", err)
	}
	for _, r := range c.Rules {
		if !corpus.HitsForRule(snap, r.ID, r.Examples[0]) {
			t.Fatalf("种子规则 %s 命中不了自己的 examples", r.ID)
		}
	}
}

func TestMuteExpiryIsLazyAndPushed(t *testing.T) {
	svc, st := newTestService(t)
	hub := newFakeHub()
	svc.SetBroadcaster(hub)

	p, err := svc.CreatePlayer("alice")
	if err != nil {
		t.Fatal(err)
	}

	past := time.Now().Add(-time.Minute)
	if _, err := st.CreateMute(store.Mute{PlayerID: p.ID, StartedAt: past.Add(-time.Hour), ExpiresAt: &past}); err != nil {
		t.Fatal(err)
	}

	players, err := svc.Players()
	if err != nil {
		t.Fatal(err)
	}
	if len(players) != 1 || players[0].Mute != nil {
		t.Fatalf("已过期的禁言不应再显示为生效: %+v", players)
	}

	mutes, err := st.MutesForPlayer(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(mutes) != 1 || mutes[0].EndReason != store.EndReasonExpired {
		t.Fatalf("过期禁言应标记为 expired: %+v", mutes)
	}

	ev := hub.waitFor(t, "mute")
	if ev["mute"] != nil {
		t.Fatalf("到期推送的 mute 事件应带 null: %+v", ev)
	}
	if ev["endReason"] != store.EndReasonExpired {
		t.Fatalf("endReason = %v", ev["endReason"])
	}
}

func TestMuteExpiryTimerPushesEvent(t *testing.T) {
	svc, st := newTestService(t)
	hub := newFakeHub()
	svc.SetBroadcaster(hub)

	p, err := svc.CreatePlayer("bob")
	if err != nil {
		t.Fatal(err)
	}
	expires := time.Now().Add(60 * time.Millisecond)
	id, err := st.CreateMute(store.Mute{PlayerID: p.ID, StartedAt: time.Now(), ExpiresAt: &expires})
	if err != nil {
		t.Fatal(err)
	}
	m := store.Mute{ID: id, PlayerID: p.ID, StartedAt: time.Now(), ExpiresAt: &expires}

	svc.mu.Lock()
	svc.scheduleLocked(m)
	svc.mu.Unlock()

	ev := hub.waitFor(t, "mute")
	if ev["mute"] != nil {
		t.Fatalf("到期那一秒应推送'你可以说话了': %+v", ev)
	}
	mutes, _ := st.MutesForPlayer(p.ID)
	if len(mutes) != 1 || mutes[0].EndReason != store.EndReasonExpired {
		t.Fatalf("到期后应写回 end_reason=expired: %+v", mutes)
	}
}

func TestSendMutesAndRejectsWhileMuted(t *testing.T) {
	svc, st := newTestService(t)
	p, err := svc.CreatePlayer("carol")
	if err != nil {
		t.Fatal(err)
	}

	res, err := svc.Send(p.ID, "你是不是傻逼")
	if err != nil {
		t.Fatal(err)
	}
	if res.Rejected || !res.Entry.IsViolation {
		t.Fatalf("应判为违规: %+v", res)
	}
	if res.Entry.Disposition != "mute" {
		t.Fatalf("L3 首次应禁言，实际 %s", res.Entry.Disposition)
	}
	if res.Entry.DurationMS != (24 * time.Hour).Milliseconds() {
		t.Fatalf("L3 首次（7 天第 1 次）应禁 1 天，实际 %d ms", res.Entry.DurationMS)
	}
	if res.Entry.MuteStarted == nil {
		t.Fatal("应带上新禁言")
	}

	rejected, err := svc.Send(p.ID, "再说一句")
	if err != nil {
		t.Fatal(err)
	}
	if !rejected.Rejected {
		t.Fatal("禁言期间的消息应被拒绝")
	}

	// 被拒绝的消息不入库、不产生判定、不影响计数。
	msgs, err := st.RecentMessages(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("被拒绝的消息不应入库，实际库里 %d 条", len(msgs))
	}
	lifetime, _ := st.CountViolationsLifetime(p.ID)
	if lifetime != 1 {
		t.Fatalf("被拒绝的消息不应影响计数，实际终身 %d", lifetime)
	}
}

func TestDryRunDoesNotWrite(t *testing.T) {
	svc, st := newTestService(t)
	p, err := svc.CreatePlayer("dave")
	if err != nil {
		t.Fatal(err)
	}

	res, err := svc.DryRun(p.ID, "加微信，你是不是傻逼", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsViolation || res.HitCount != 2 {
		t.Fatalf("应命中 2 条规则: %+v", res)
	}
	if res.Severity != 4 {
		t.Fatalf("severity = %d, want 4（max(2,3)+1）", res.Severity)
	}
	if res.Count7d != 1 || res.Lifetime != 1 {
		t.Fatalf("试跑应假设本条计入计数: %+v", res)
	}
	if res.Disposition != "mute" || res.DurationMS != (72*time.Hour).Milliseconds() {
		t.Fatalf("L4 首次应为 3d（档位 3），实际 %s %dms", res.Disposition, res.DurationMS)
	}

	msgs, _ := st.RecentMessages(10)
	if len(msgs) != 0 {
		t.Fatal("试跑不应产生消息")
	}
	lifetime, _ := st.CountViolationsLifetime(p.ID)
	if lifetime != 0 {
		t.Fatal("试跑不应影响计数")
	}
	if m, _ := st.ActiveMute(p.ID); m != nil {
		t.Fatal("试跑不应施加禁言")
	}
}

func TestDryRunUsesDraftCorpus(t *testing.T) {
	svc, _ := newTestService(t)
	p, err := svc.CreatePlayer("erin")
	if err != nil {
		t.Fatal(err)
	}

	ops := []corpus.Operation{{
		Op: corpus.OpAdd, Target: corpus.TargetRule,
		RuleAfter: &moderation.Rule{
			ID: "r100", Description: "试跑用规则", View: moderation.ViewRaw,
			Matcher: moderation.MatcherLiteral, Pattern: "刷屏", Severity: 1, Enabled: true,
			Examples: []string{"刷屏"},
		},
	}}

	current, err := svc.DryRun(p.ID, "刷屏", nil)
	if err != nil {
		t.Fatal(err)
	}
	if current.IsViolation {
		t.Fatal("已提交语料库不应命中草稿才有的规则")
	}

	draftRun, err := svc.DryRun(p.ID, "刷屏", ops)
	if err != nil {
		t.Fatal(err)
	}
	if !draftRun.IsViolation || draftRun.CorpusSource != "draft" {
		t.Fatalf("试跑应使用草稿状态的语料库: %+v", draftRun)
	}
}

func TestCommitRejectsInvalidOps(t *testing.T) {
	svc, st := newTestService(t)
	before := svc.Corpus()

	_, problems, err := svc.CommitOps([]corpus.Operation{{
		Op: corpus.OpAdd, Target: corpus.TargetRule,
		RuleAfter: &moderation.Rule{
			ID: "r900", View: moderation.ViewRaw, Matcher: moderation.MatcherLiteral,
			Pattern: "x", Severity: 9, Enabled: true, Examples: []string{"x"},
		},
	}}, "测试非法提交")
	if err == nil {
		t.Fatal("非法变更应被拒绝")
	}
	if len(problems) != 1 || problems[0].OK() {
		t.Fatalf("应返回逐条问题: %+v", problems)
	}
	after := svc.Corpus()
	if len(after.Rules) != len(before.Rules) {
		t.Fatalf("语料库不应变化: %d → %d", len(before.Rules), len(after.Rules))
	}
	revs, _ := st.Revisions()
	if len(revs) != 1 {
		t.Fatalf("不应产生新修订，实际 %d 条", len(revs))
	}
}

func TestCommitCreatesRevisionAndTakesEffectImmediately(t *testing.T) {
	svc, st := newTestService(t)
	p, err := svc.CreatePlayer("frank")
	if err != nil {
		t.Fatal(err)
	}

	res, err := svc.Send(p.ID, "来刷屏啦")
	if err != nil {
		t.Fatal(err)
	}
	if res.Entry.IsViolation {
		t.Fatal("提交前不应命中")
	}

	ops := []corpus.Operation{{
		Op: corpus.OpAdd, Target: corpus.TargetRule,
		RuleAfter: &moderation.Rule{
			ID: "r200", Description: "刷屏广告", View: moderation.ViewRaw,
			Matcher: moderation.MatcherLiteral, Pattern: "刷屏", Severity: 1, Enabled: true,
			Examples: []string{"来刷屏啦"},
		},
	}}
	if _, problems, err := svc.CommitOps(ops, "加刷屏规则"); err != nil {
		t.Fatalf("合法提交失败: %v %+v", err, problems)
	}

	// 提交后的下一条消息即按新语料库判定。
	res2, err := svc.Send(p.ID, "来刷屏啦")
	if err != nil {
		t.Fatal(err)
	}
	if !res2.Entry.IsViolation {
		t.Fatal("提交后的下一条消息应按新语料库判定")
	}

	revs, _ := st.Revisions()
	if len(revs) != 2 {
		t.Fatalf("应产生一条新修订，实际 %d 条", len(revs))
	}
	if len(revs[1].Operations) != 1 {
		t.Fatal("修订应记录本次操作列表")
	}
	if _, ok := revs[1].Snapshot.RuleByID("r200"); !ok {
		t.Fatal("修订应保存提交后的完整语料库快照")
	}
}

func TestRollbackAndLiftRequireReason(t *testing.T) {
	svc, _ := newTestService(t)
	p, err := svc.CreatePlayer("grace")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Rollback(1, "   "); err == nil {
		t.Fatal("回滚必须要求原因")
	}
	if err := svc.Lift(p.ID, ""); err == nil {
		t.Fatal("提前解除必须要求原因")
	}
}

func TestRollbackKeepsHistory(t *testing.T) {
	svc, st := newTestService(t)
	revsBefore, _ := st.Revisions()
	firstID := revsBefore[0].ID

	ops := []corpus.Operation{{
		Op: corpus.OpAdd, Target: corpus.TargetRule,
		RuleAfter: &moderation.Rule{
			ID: "r300", Description: "临时规则", View: moderation.ViewRaw,
			Matcher: moderation.MatcherLiteral, Pattern: "临时", Severity: 1, Enabled: true,
			Examples: []string{"临时"},
		},
	}}
	if _, _, err := svc.CommitOps(ops, "加临时规则"); err != nil {
		t.Fatal(err)
	}
	if _, ok := svc.Corpus().RuleByID("r300"); !ok {
		t.Fatal("提交后应有 r300")
	}

	if _, err := svc.Rollback(firstID, "试一下回滚"); err != nil {
		t.Fatal(err)
	}
	if _, ok := svc.Corpus().RuleByID("r300"); ok {
		t.Fatal("回滚后不应再有 r300")
	}

	revs, err := st.Revisions()
	if err != nil {
		t.Fatal(err)
	}
	if len(revs) != 3 {
		t.Fatalf("回滚应作为新的一次修订追加，实际 %d 条", len(revs))
	}
	if revs[2].Operations[0].Op != corpus.OpRollback {
		t.Fatalf("回滚修订应记录 rollback 操作: %+v", revs[2].Operations)
	}
}

func TestJudgmentSnapshotSurvivesRuleChange(t *testing.T) {
	svc, st := newTestService(t)
	p, err := svc.CreatePlayer("henry")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Send(p.ID, "你是不是傻逼"); err != nil {
		t.Fatal(err)
	}

	msgs, err := st.RecentMessages(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 || msgs[0].Severity != 3 {
		t.Fatalf("历史判定 severity 应为 3: %+v", msgs)
	}

	// 把 r002 从 L3 改到 L5。
	rule, ok := svc.Corpus().RuleByID("r002")
	if !ok {
		t.Fatal("种子规则 r002 不存在")
	}
	rule.Severity = 5
	ops := []corpus.Operation{{
		Op: corpus.OpUpdate, Target: corpus.TargetRule,
		RuleBefore: &moderation.Rule{ID: "r002"},
		RuleAfter:  &rule,
	}}
	if _, problems, err := svc.CommitOps(ops, "调重 r002"); err != nil {
		t.Fatalf("修改 severity 失败: %v %+v", err, problems)
	}

	after, err := st.RecentMessages(10)
	if err != nil {
		t.Fatal(err)
	}
	if after[0].Severity != 3 {
		t.Fatalf("历史判定的 severity 不应随语料库变化，实际 %d", after[0].Severity)
	}
	if !strings.Contains(after[0].HitsJSON, `"severity":3`) || !strings.Contains(after[0].HitsJSON, `"ruleId":"r002"`) {
		t.Fatalf("判定应保存规则快照: %s", after[0].HitsJSON)
	}

	// 永久禁言门槛只看违规次数，不因规则变更漂移。
	lifetime, err := st.CountViolationsLifetime(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if lifetime != 1 {
		t.Fatalf("终身计数不应变化，实际 %d", lifetime)
	}
}

func TestDraftFlowWithFakeDrafter(t *testing.T) {
	svc, _ := newTestService(t)
	fake := &drafter.Fake{Ops: []corpus.Operation{{
		Op: corpus.OpAdd, Target: corpus.TargetRule,
		Reason:  "新火星文广告",
		Samples: []int64{1, 999}, // 999 是幻觉样本，应被过滤
		RuleAfter: &moderation.Rule{
			ID: "r400", Description: "火星文广告", View: moderation.ViewRaw,
			Matcher: moderation.MatcherLiteral, Pattern: "加微信", Severity: 2, Enabled: true,
			Examples: []string{"加微信领皮肤"},
		},
	}}}
	svc.drafter = fake

	p, err := svc.CreatePlayer("ivy")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Send(p.ID, "今天天气不错"); err != nil {
		t.Fatal(err)
	}

	// 未勾选样本：不传任何消息内容，草案里也不带样本依据。
	draft, err := svc.Draft(context.Background(), "加一条广告规则", false)
	if err != nil {
		t.Fatalf("生成草案失败: %v", err)
	}
	if len(fake.Requests) != 1 {
		t.Fatalf("应调用一次 agent，实际 %d", len(fake.Requests))
	}
	if len(fake.Requests[0].Samples) != 0 {
		t.Fatal("未勾选时不应把消息样本传给 agent")
	}
	if strings.Contains(drafter.BuildPrompt(fake.Requests[0]), "今天天气不错") {
		t.Fatal("未勾选时 prompt 里不应出现聊天内容")
	}
	if len(draft.Samples) != 0 || len(draft.Operations[0].Samples) != 0 {
		t.Fatalf("未勾选时草案不应带样本: %+v", draft.Operations[0])
	}

	// 勾选样本：附带最近消息，并过滤幻觉出来的样本 id。
	draft, err = svc.Draft(context.Background(), "加一条广告规则", true)
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.Requests[1].Samples) != 1 {
		t.Fatalf("勾选时应附带最近消息，实际 %d", len(fake.Requests[1].Samples))
	}
	if len(draft.Operations[0].Samples) != 1 || draft.Operations[0].Samples[0] != 1 {
		t.Fatalf("应过滤不存在的样本 id: %+v", draft.Operations[0].Samples)
	}
	if len(draft.Problems) != 1 || !draft.Problems[0].OK() {
		t.Fatalf("合法草案不应有问题: %+v", draft.Problems)
	}

	// 部分提交：只提交勾选的子集。
	if _, problems, err := svc.CommitOps(draft.Operations, "通过草案"); err != nil {
		t.Fatalf("提交草案失败: %v %+v", err, problems)
	}
	if _, ok := svc.Corpus().RuleByID("r400"); !ok {
		t.Fatal("提交后规则应生效")
	}
}

func TestDraftReportsProblemsAndAgentErrors(t *testing.T) {
	svc, _ := newTestService(t)
	fake := &drafter.Fake{Ops: []corpus.Operation{{
		Op: corpus.OpAdd, Target: corpus.TargetRule,
		RuleAfter: &moderation.Rule{
			ID: "r401", View: moderation.ViewRaw, Matcher: moderation.MatcherLiteral,
			Pattern: "永远不会命中的词", Severity: 2, Enabled: true,
			Examples: []string{"完全无关的内容"},
		},
	}}}
	svc.drafter = fake

	draft, err := svc.Draft(context.Background(), "加规则", false)
	if err != nil {
		t.Fatal(err)
	}
	if len(draft.Problems) != 1 || draft.Problems[0].OK() {
		t.Fatalf("命中不了 examples 的规则应被标红: %+v", draft.Problems)
	}
	if _, problems, err := svc.CommitOps(draft.Operations, "提交坏草案"); err == nil {
		t.Fatalf("含失败项的提交应被拒绝: %+v", problems)
	}

	fake.Err = fmt.Errorf("模型返回非法结构")
	if _, err := svc.Draft(context.Background(), "再来一次", false); err == nil || !strings.Contains(err.Error(), "非法结构") {
		t.Fatalf("agent 报错时应把明确错误抛给前端: %v", err)
	}
}

func TestDraftDisabledWithoutKey(t *testing.T) {
	svc, _ := newTestService(t)
	if svc.AgentEnabled() {
		t.Fatal("未配置 key 时 agent 应禁用")
	}
	if _, err := svc.Draft(context.Background(), "任意指令", false); err == nil {
		t.Fatal("agent 禁用时应返回明确错误")
	}
}

func TestApplyMuteReplacesWithStricterOne(t *testing.T) {
	svc, st := newTestService(t)
	p, err := svc.CreatePlayer("judy")
	if err != nil {
		t.Fatal(err)
	}
	now := svc.now()

	svc.mu.Lock()
	first, replaced, err := svc.applyMuteLocked(p.ID, punishment.Decide(2, 1, 1), now) // 1h
	svc.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if replaced {
		t.Fatal("第一条禁言不应是替换")
	}
	if first.RemainingMS != time.Hour.Milliseconds() {
		t.Fatalf("L2 首次应 1h，实际 %dms", first.RemainingMS)
	}

	// 更轻的新判定：保留更严格的那一个（剩余 1h > 新的 1min）。
	svc.mu.Lock()
	second, replaced, err := svc.applyMuteLocked(p.ID, punishment.Decide(1, 1, 1), now.Add(time.Minute))
	svc.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if !replaced {
		t.Fatal("应标记为 replaced")
	}
	if second.RemainingMS <= 58*time.Minute.Milliseconds() {
		t.Fatalf("应保留更严格的那一个（约 59 分钟），实际 %dms", second.RemainingMS)
	}

	// 永久优先。
	svc.mu.Lock()
	third, replaced, err := svc.applyMuteLocked(p.ID, punishment.Decide(1, 1, 5), now.Add(2*time.Minute))
	svc.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if !replaced || !third.Permanent {
		t.Fatalf("永久禁言应替换旧禁言: %+v replaced=%v", third, replaced)
	}

	mutes, err := st.MutesForPlayer(p.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(mutes) != 3 {
		t.Fatalf("应留下 3 条记录，实际 %d", len(mutes))
	}
	if mutes[0].EndedAt != nil {
		t.Fatal("最新一条应仍然活跃")
	}
	if mutes[1].EndReason != store.EndReasonReplaced || mutes[2].EndReason != store.EndReasonReplaced {
		t.Fatalf("旧记录应以 replaced 结束: %+v", mutes)
	}
}

func TestHistoryIsRebuiltAfterRestart(t *testing.T) {
	path := filepath.Join(t.TempDir(), "restart.db")

	st, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	svc, err := NewService(st, nil, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	p, err := svc.CreatePlayer("alice")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Send(p.ID, "你是不是傻逼"); err != nil {
		t.Fatal(err)
	}
	svc.Close()
	st.Close()

	st2, err := store.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer st2.Close()
	svc2, err := NewService(st2, nil, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	defer svc2.Close()

	boot, err := svc2.Bootstrap()
	if err != nil {
		t.Fatal(err)
	}
	if len(boot.Stream) != 1 {
		t.Fatalf("重启后应能回看历史消息，实际 %d 条", len(boot.Stream))
	}
	e := boot.Stream[0]
	if e.PlayerName != "alice" || e.Severity != 3 || !e.IsViolation || e.Disposition != "mute" {
		t.Fatalf("历史条目不对: %+v", e)
	}
	if e.Count7d != 1 || e.Lifetime != 1 {
		t.Fatalf("历史条目应还原当时的计数: %+v", e)
	}
	if len(boot.Players) != 1 || boot.Players[0].Mute == nil {
		t.Fatalf("重启后应恢复禁言状态: %+v", boot.Players)
	}
}
