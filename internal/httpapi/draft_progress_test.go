package httpapi_test

import (
	"context"
	"errors"
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

// TestDraftRequestProgressAndLog 守住"点了生成以后到底发生了什么"这条链路：
// 先推 draft.started（界面立刻显示运行中）、再推 agent.log（请求体/响应体留档）、
// 最后才是 draft 或带 source=draft 的 error。失败也要留档，不能静默。
func TestDraftRequestProgressAndLog(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "agent-log.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("打开临时数据库失败: %v", err)
	}
	defer st.Close()

	fake := &drafter.Fake{Ops: []corpus.Operation{{
		Op:     corpus.OpAdd,
		Target: corpus.TargetMapping,
		Reason: "去掉插入符号",
		MappingAfter: &moderation.Mapping{
			ID: "m90", From: "@", To: "", Note: "噪声符号",
		},
	}}}
	svc, err := httpapi.NewService(st, fake, time.Now)
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

	boot := c.waitFor("bootstrap")
	if boot["agentModel"] != "fake" {
		t.Fatalf("bootstrap 应带模型名: %v", boot["agentModel"])
	}

	// 成功路径：started → agent.log → draft。
	c.send(map[string]any{"type": "draft.request", "instruction": "加一条去符号映射"})
	started := c.waitFor("draft.started")
	if started["model"] != "fake" || started["instruction"] != "加一条去符号映射" || started["startedAt"] == nil {
		t.Fatalf("draft.started 要带模型/指令/开始时间: %+v", started)
	}

	ex := c.waitFor("agent.log")["exchange"].(map[string]any)
	if ex["status"] != float64(200) || ex["operations"] != float64(1) {
		t.Fatalf("agent.log 应记录状态与操作数: %+v", ex)
	}
	if req, _ := ex["request"].(string); !strings.Contains(req, "加一条去符号映射") {
		t.Fatalf("agent.log 应记录请求体（含指令）: %q", req)
	}
	if resp, _ := ex["response"].(string); !strings.Contains(resp, "m90") {
		t.Fatalf("agent.log 应记录响应体: %q", resp)
	}

	draft := c.waitFor("draft")["draft"].(map[string]any)
	if len(draft["operations"].([]any)) != 1 {
		t.Fatalf("应收到 1 条草案: %+v", draft)
	}

	// 失败路径：错误要带 source=draft（界面据此把错误留在 agent 区），日志也要留档。
	fake.Err = errors.New("模型返回非法结构")
	c.send(map[string]any{"type": "draft.request", "instruction": "再来一次"})
	c.waitFor("draft.started")
	failed := c.waitFor("agent.log")["exchange"].(map[string]any)
	if failed["error"] == nil || !strings.Contains(failed["error"].(string), "非法结构") {
		t.Fatalf("失败的往返也要留档: %+v", failed)
	}
	errEvt := c.waitFor("error")
	if errEvt["source"] != "draft" {
		t.Fatalf("草案链路的错误要带 source=draft: %+v", errEvt)
	}
	if !strings.Contains(errEvt["message"].(string), "非法结构") {
		t.Fatalf("错误要带明确原因: %+v", errEvt)
	}

	// 失败之后同一个会话必须还能再发请求（一次失败不能把 agent 用死）。
	fake.Err = nil
	c.send(map[string]any{"type": "draft.request", "instruction": "失败后再来一次"})
	c.waitFor("draft.started")
	if ops := c.waitFor("draft")["draft"].(map[string]any)["operations"].([]any); len(ops) != 1 {
		t.Fatalf("失败后的下一次请求应正常返回草案: %d 条", len(ops))
	}

	// 新连接能从 bootstrap 里看到历史日志（刷新页面不丢排障信息）。
	if log := svc.AgentLog(); len(log) != 3 {
		t.Fatalf("服务端应保留 3 次往返记录，实际 %d", len(log))
	}
	boot2, err := svc.Bootstrap()
	if err != nil {
		t.Fatal(err)
	}
	if len(boot2.AgentLog) != 3 {
		t.Fatalf("bootstrap 要带上 agent 日志，实际 %d 条", len(boot2.AgentLog))
	}
}
