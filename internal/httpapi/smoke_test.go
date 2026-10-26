package httpapi_test

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"chatmod/internal/httpapi"
	"chatmod/internal/store"
)

// wsClient 是端到端冒烟测试用的最小客户端。
type wsClient struct {
	t    *testing.T
	ctx  context.Context
	conn *websocket.Conn
}

func (c *wsClient) send(v any) {
	c.t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		c.t.Fatalf("序列化请求失败: %v", err)
	}
	if err := c.conn.Write(c.ctx, websocket.MessageText, data); err != nil {
		c.t.Fatalf("写 WebSocket 失败: %v", err)
	}
}

// waitFor 跳过无关的广播事件，直到收到指定 type。
func (c *wsClient) waitFor(typ string) map[string]any {
	c.t.Helper()
	for i := 0; i < 100; i++ {
		rctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
		_, data, err := c.conn.Read(rctx)
		cancel()
		if err != nil {
			c.t.Fatalf("读取 WebSocket 事件失败: %v", err)
		}
		var m map[string]any
		if err := json.Unmarshal(data, &m); err != nil {
			c.t.Fatalf("事件不是合法 JSON: %v", err)
		}
		if m["type"] == typ {
			return m
		}
	}
	c.t.Fatalf("没等到事件 %q", typ)
	return nil
}

// TestSmokeEndToEnd 是规格要求的那一条冒烟：
// 起进程 → 连 WS → 创建玩家 → 发一条违规消息 → 断言收到判定与禁言事件
// → 断言禁言期间再发被拒。使用临时 SQLite 文件，跑完删除。
func TestSmokeEndToEnd(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "smoke.db")
	st, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("打开临时数据库失败: %v", err)
	}
	defer st.Close()

	svc, err := httpapi.NewService(st, nil, time.Now)
	if err != nil {
		t.Fatalf("初始化服务失败: %v", err)
	}
	ts := httptest.NewServer(httpapi.NewServer(svc).Handler())
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	wsURL := "ws" + strings.TrimPrefix(ts.URL, "http") + "/ws"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("连接 WebSocket 失败: %v", err)
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	c := &wsClient{t: t, ctx: ctx, conn: conn}

	boot := c.waitFor("bootstrap")
	if boot["agentEnabled"] != false {
		t.Fatalf("未配置 key 时 agent 应禁用: %v", boot["agentEnabled"])
	}
	stair, _ := boot["stair"].(map[string]any)
	if stair == nil || len(stair["durations"].([]any)) != 4 {
		t.Fatalf("bootstrap 应带只读阶梯数据: %v", boot["stair"])
	}

	// 创建玩家
	c.send(map[string]any{"type": "player.create", "name": "alice"})
	players := c.waitFor("players")["players"].([]any)
	if len(players) != 1 {
		t.Fatalf("应有一个玩家，实际 %d", len(players))
	}
	alice := players[0].(map[string]any)
	aliceID := int64(alice["id"].(float64))
	if alice["name"] != "alice" {
		t.Fatalf("玩家名 = %v", alice["name"])
	}

	// 切换当前玩家
	c.send(map[string]any{"type": "set_player", "playerId": aliceID})
	c.waitFor("player_selected")

	// 发一条命中 L3 规则的消息：count7d=1 → rung 2 → 1 天
	c.send(map[string]any{"type": "send", "text": "你是不是傻逼"})
	res := c.waitFor("result")["result"].(map[string]any)
	if res["rejected"] == true {
		t.Fatalf("第一次发送不应被拒绝: %v", res)
	}
	entry := res["entry"].(map[string]any)
	if entry["isViolation"] != true {
		t.Fatalf("应判为违规: %v", entry)
	}
	if entry["severity"] != float64(3) {
		t.Fatalf("severity = %v, want 3", entry["severity"])
	}
	if entry["disposition"] != "mute" {
		t.Fatalf("disposition = %v, want mute", entry["disposition"])
	}
	if entry["durationMs"] != float64((24 * time.Hour).Milliseconds()) {
		t.Fatalf("L3 首次应禁 1 天，实际 %v ms", entry["durationMs"])
	}
	muteStarted, ok := entry["muteStarted"].(map[string]any)
	if !ok {
		t.Fatal("判定结果应带上新禁言")
	}
	if muteStarted["permanent"] == true {
		t.Fatal("此处不应是永久禁言")
	}
	if muteStarted["expiresAt"] == nil || muteStarted["expiresAt"] == "" {
		t.Fatal("限时禁言必须有到期时间")
	}

	// 玩家列表与计数：判定结果里带的是含本条的两个计数
	if entry["count7d"] != float64(1) || entry["lifetime"] != float64(1) {
		t.Fatalf("计数不对: %v", entry)
	}
	if entry["hitCount"] != float64(1) {
		t.Fatalf("命中条数应写入判定: %v", entry["hitCount"])
	}

	// 禁言期间再发：被拒绝，且不入库、不产生判定
	c.send(map[string]any{"type": "send", "text": "再说一句"})
	rej := c.waitFor("result")["result"].(map[string]any)
	if rej["rejected"] != true {
		t.Fatalf("禁言期间的消息应被拒绝: %v", rej)
	}
	if !strings.Contains(rej["rejectReason"].(string), "禁言") {
		t.Fatalf("拒绝原因应说明禁言: %v", rej["rejectReason"])
	}

	msgs, err := st.RecentMessages(10)
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != 1 {
		t.Fatalf("被拒绝的消息不应入库，实际 %d 条", len(msgs))
	}
	lifetime, err := st.CountViolationsLifetime(aliceID)
	if err != nil {
		t.Fatal(err)
	}
	if lifetime != 1 {
		t.Fatalf("被拒绝的消息不应影响计数，实际终身 %d 次", lifetime)
	}
	active, err := st.ActiveMute(aliceID)
	if err != nil {
		t.Fatal(err)
	}
	if active == nil {
		t.Fatal("应有一条活跃禁言")
	}

	// 提前解除：空原因被拒绝
	c.send(map[string]any{"type": "lift", "playerId": aliceID, "reason": "  "})
	if errMsg := c.waitFor("error")["message"].(string); !strings.Contains(errMsg, "原因") {
		t.Fatalf("空原因应被拒绝，实际: %v", errMsg)
	}

	// 填了原因则生效，玩家立刻能发言
	c.send(map[string]any{"type": "lift", "playerId": aliceID, "reason": "测试解除"})
	c.waitFor("ack")
	if m, _ := st.ActiveMute(aliceID); m != nil {
		t.Fatal("提前解除后不应再有活跃禁言")
	}

	c.send(map[string]any{"type": "send", "text": "今天天气不错"})
	okRes := c.waitFor("result")["result"].(map[string]any)
	if okRes["rejected"] == true {
		t.Fatalf("解除禁言后应能发言: %v", okRes)
	}
	if okRes["entry"].(map[string]any)["isViolation"] == true {
		t.Fatal("正常聊天不应被判违规")
	}
}
