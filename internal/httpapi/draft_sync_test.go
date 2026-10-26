package httpapi_test

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"chatmod/internal/corpus"
	"chatmod/internal/drafter"
	"chatmod/internal/httpapi"
	"chatmod/internal/moderation"
	"chatmod/internal/store"
)

// TestDraftSyncReplacesSessionDraft 覆盖"管理员在前端删掉部分草案条目"这条路径：
// draft.sync 必须把会话里的草稿换成管理员留下的那几条，
// 否则清空草案之后试跑还会拿旧草案的语料库去跑。
func TestDraftSyncReplacesSessionDraft(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "draft-sync.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("打开临时数据库失败: %v", err)
	}
	defer st.Close()

	op := corpus.Operation{
		Op:     corpus.OpAdd,
		Target: corpus.TargetRule,
		Reason: "刷屏广告",
		RuleAfter: &moderation.Rule{
			ID: "r900", Description: "刷屏广告",
			View: moderation.ViewRaw, Matcher: moderation.MatcherLiteral,
			Pattern: "刷屏测试词", Severity: 1, Enabled: true,
			Examples: []string{"刷屏测试词"},
		},
	}
	svc, err := httpapi.NewService(st, &drafter.Fake{Ops: []corpus.Operation{op}}, time.Now)
	if err != nil {
		t.Fatalf("初始化服务失败: %v", err)
	}
	ts := httptest.NewServer(httpapi.NewServer(svc).Handler())
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatalf("连接 WebSocket 失败: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")
	c := &wsClient{t: t, ctx: ctx, conn: conn}

	if c.waitFor("bootstrap")["agentEnabled"] != true {
		t.Fatal("注入 fake drafter 后 agent 应可用")
	}

	c.send(map[string]any{"type": "draft.request", "instruction": "加一条刷屏规则"})
	draftEvt := c.waitFor("draft")["draft"].(map[string]any)
	ops := draftEvt["operations"].([]any)
	if len(ops) != 1 {
		t.Fatalf("草案应有 1 条建议，实际 %d", len(ops))
	}
	problemsOf := func(evt map[string]any) []any {
		probs, _ := evt["problems"].([]any)
		return probs
	}
	problemMessages := func(p any) []any {
		m := p.(map[string]any)["messages"]
		if m == nil {
			return nil
		}
		return m.([]any)
	}

	if probs := problemsOf(draftEvt); len(probs) != 1 || len(problemMessages(probs[0])) != 0 {
		t.Fatalf("合法草案不应有问题: %+v", probs)
	}

	// 未删除任何条目时，试跑用的是草稿语料库。
	c.send(map[string]any{"type": "dryrun", "text": "刷屏测试词"})
	if run := c.waitFor("dryrun.result")["result"].(map[string]any); run["corpusSource"] != "draft" || run["isViolation"] != true {
		t.Fatalf("试跑应命中草稿里的新规则: %+v", run)
	}

	// 管理员删光了所有草案条目：会话里的草稿必须一起清掉。
	c.send(map[string]any{"type": "draft.sync", "operations": []any{}})
	if probs, ok := c.waitFor("draft.problems")["problems"]; ok && probs != nil {
		t.Fatalf("清空草案后不应再有校验问题: %+v", probs)
	}
	c.send(map[string]any{"type": "dryrun", "text": "刷屏测试词"})
	if run := c.waitFor("dryrun.result")["result"].(map[string]any); run["corpusSource"] != "current" || run["isViolation"] != false {
		t.Fatalf("清空草案后试跑应回到已提交语料库: %+v", run)
	}

	// 留下一条坏条目：draft.sync 要把这条的校验问题按新下标返回给前端。
	c.send(map[string]any{"type": "draft.request", "instruction": "再来一条"})
	c.waitFor("draft")
	bad := op
	bad.RuleAfter = &moderation.Rule{
		ID: "r901", Description: "坏规则",
		View: moderation.ViewRaw, Matcher: moderation.MatcherLiteral,
		Pattern: "", Severity: 1, Enabled: true,
	}
	c.send(map[string]any{"type": "draft.sync", "operations": []any{bad}})
	problems := problemsOf(c.waitFor("draft.problems"))
	if len(problems) != 1 {
		t.Fatalf("应按新列表返回逐条问题: %+v", problems)
	}
	p := problems[0].(map[string]any)
	if p["index"] != float64(0) || len(problemMessages(problems[0])) == 0 {
		t.Fatalf("坏条目应标在 index 0 上: %+v", p)
	}

	// 留下坏条目时试跑仍然用草稿（这里只想确认草稿没被清掉）。
	c.send(map[string]any{"type": "dryrun", "text": "刷屏测试词"})
	if run := c.waitFor("dryrun.result")["result"].(map[string]any); run["corpusSource"] != "current" {
		t.Fatalf("坏条目不会进草稿语料库，应回退到已提交版本: %+v", run)
	}
}
