package httpapi_test

import (
	"context"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"chatmod/internal/corpus"
	"chatmod/internal/drafter"
	"chatmod/internal/httpapi"
	"chatmod/internal/store"
)

// gateDrafter 会一直卡在 Draft 里，直到 release 被关闭或 ctx 被取消。
// 用它模拟"模型要 3 秒才回来"，验证 agent 往返不再阻塞同一条连接。
type gateDrafter struct {
	started chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func newGateDrafter() *gateDrafter {
	return &gateDrafter{
		started: make(chan struct{}, 8),
		release: make(chan struct{}),
	}
}

func (g *gateDrafter) Enabled() bool { return true }

func (g *gateDrafter) Draft(ctx context.Context, _ drafter.Request) ([]corpus.Operation, error) {
	g.calls.Add(1)
	select {
	case g.started <- struct{}{}:
	default:
	}
	select {
	case <-g.release:
		return []corpus.Operation{}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func newDraftSession(t *testing.T, d drafter.Drafter) *wsClient {
	t.Helper()

	st, err := store.Open(filepath.Join(t.TempDir(), "draft-async.db"))
	if err != nil {
		t.Fatalf("打开临时数据库失败: %v", err)
	}
	t.Cleanup(func() { st.Close() })

	svc, err := httpapi.NewService(st, d, time.Now)
	if err != nil {
		t.Fatalf("初始化服务失败: %v", err)
	}
	player, err := svc.CreatePlayer("alice")
	if err != nil {
		t.Fatalf("创建玩家失败: %v", err)
	}

	ts := httptest.NewServer(httpapi.NewServer(svc).Handler())
	t.Cleanup(ts.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)

	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(ts.URL, "http")+"/ws", nil)
	if err != nil {
		t.Fatalf("连接 WebSocket 失败: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "") })

	c := &wsClient{t: t, ctx: ctx, conn: conn}
	if c.waitFor("bootstrap")["agentEnabled"] != true {
		t.Fatal("注入 gateDrafter 后 agent 应可用")
	}
	c.send(map[string]any{"type": "set_player", "playerId": player.ID})
	c.waitFor("player_selected")
	return c
}

// agent 往返在后台执行：同一连接上后发的普通聊天消息不能被它挡住。
func TestDraftDoesNotBlockChatOnSameConnection(t *testing.T) {
	g := newGateDrafter()
	c := newDraftSession(t, g)

	c.send(map[string]any{"type": "draft.request", "instruction": "加一条规则", "requestId": "req-1"})
	<-g.started
	c.waitFor("draft.started")

	began := time.Now()
	c.send(map[string]any{"type": "send", "text": "普通聊天消息"})
	c.waitFor("result")
	if elapsed := time.Since(began); elapsed > 2*time.Second {
		t.Fatalf("模型往返期间聊天消息被阻塞了 %s", elapsed)
	}
	if got := g.calls.Load(); got != 1 {
		t.Fatalf("聊天不应触发模型调用，实际 %d 次", got)
	}

	close(g.release)
	c.waitFor("draft")
}

// 规格 3.8 要求"模型返回前不发第二个请求"：服务端必须自己守住这条，
// 不能只靠前端。第二次 draft.request 只能收到 draft.busy，不能真的再打一次模型。
func TestDraftRequestIsSingleFlightPerSession(t *testing.T) {
	g := newGateDrafter()
	c := newDraftSession(t, g)

	c.send(map[string]any{"type": "draft.request", "instruction": "加一条规则", "requestId": "req-1"})
	<-g.started
	if got := c.waitFor("draft.started")["requestId"]; got != "req-1" {
		t.Fatalf("draft.started 应带回 requestId: %v", got)
	}

	c.send(map[string]any{"type": "draft.request", "instruction": "再来一条", "requestId": "req-2"})
	busy := c.waitFor("draft.busy")
	if busy["requestId"] != "req-1" {
		t.Fatalf("draft.busy 应指向在途请求 req-1: %+v", busy)
	}
	if got := g.calls.Load(); got != 1 {
		t.Fatalf("第二次点击不应再次调用模型，实际调用 %d 次", got)
	}

	close(g.release)
	if got := c.waitFor("draft")["requestId"]; got != "req-1" {
		t.Fatalf("完成事件应对回 req-1: %v", got)
	}

	// 上一条完成后必须能立刻重发。
	c.send(map[string]any{"type": "draft.request", "instruction": "完成后重发", "requestId": "req-3"})
	if got := c.waitFor("draft")["requestId"]; got != "req-3" {
		t.Fatalf("完成后的新请求应正常执行: %v", got)
	}
	if got := g.calls.Load(); got != 2 {
		t.Fatalf("完成后重发应调用模型一次，累计应为 2 次，实际 %d", got)
	}
}

// 取消等待必须真的取消服务端模型请求，否则用户以为取消了、实际还在烧额度。
func TestDraftCancelAbortsServerRequestAndAllowsResend(t *testing.T) {
	g := newGateDrafter()
	c := newDraftSession(t, g)

	c.send(map[string]any{"type": "draft.request", "instruction": "加一条规则", "requestId": "req-1"})
	<-g.started
	c.waitFor("draft.started")

	c.send(map[string]any{"type": "draft.cancel", "requestId": "req-1"})
	if got := c.waitFor("draft.cancelled")["requestId"]; got != "req-1" {
		t.Fatalf("draft.cancelled 应对回 req-1: %v", got)
	}

	// 取消后立即重发：服务端应接受，且新请求会重新调用模型。
	c.send(map[string]any{"type": "draft.request", "instruction": "取消后重发", "requestId": "req-2"})
	<-g.started
	if got := g.calls.Load(); got != 2 {
		t.Fatalf("取消后重发应调用模型一次，累计 2 次，实际 %d", got)
	}
	close(g.release)
	if got := c.waitFor("draft")["requestId"]; got != "req-2" {
		t.Fatalf("取消后新请求应正常完成: %v", got)
	}
}
