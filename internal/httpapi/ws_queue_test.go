package httpapi

import (
	"strings"
	"testing"
	"time"
)

// 广播队列满时必须断开连接让客户端重连拿 bootstrap 全量状态，
// 不能像以前那样静默丢事件、让界面停在过期状态。
func TestBroadcastOverflowForcesResync(t *testing.T) {
	s := &session{out: make(chan []byte, 1), done: make(chan struct{})}
	s.out <- []byte(`{"type":"stream"}`)

	s.enqueueBroadcast([]byte(`{"type":"players"}`))

	select {
	case <-s.done:
	case <-time.After(time.Second):
		t.Fatal("广播队列满时应断开连接触发重连同步，而不是静默丢弃")
	}
}

// 自己的判定结果（result）走直接回包：队列满时要背压，不能丢。
func TestDirectReplyBlocksInsteadOfBeingDropped(t *testing.T) {
	s := &session{out: make(chan []byte, 1), done: make(chan struct{})}
	s.out <- []byte(`{"type":"stream"}`)

	sent := make(chan struct{})
	go func() {
		s.send(ackEvent{Type: "ack", Note: "ok"})
		close(sent)
	}()

	select {
	case <-sent:
		t.Fatal("队列满时直接回包不应被立即丢弃")
	case <-time.After(50 * time.Millisecond):
	}

	<-s.out // 模拟写协程腾出一个位置
	select {
	case <-sent:
	case <-time.After(time.Second):
		t.Fatal("队列腾出后直接回包应送达")
	}

	data := <-s.out
	if !strings.Contains(string(data), `"type":"ack"`) {
		t.Fatalf("直接回包内容不对: %s", data)
	}
}
