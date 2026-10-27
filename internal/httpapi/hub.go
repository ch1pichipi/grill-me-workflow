package httpapi

import (
	"encoding/json"
	"log"
	"sync"
)

// Hub 是所有 WebSocket 连接的事件分发中心。广播只做入队，不阻塞写操作，
// 因此持锁中的业务代码可以安全地 emit。
type Hub struct {
	mu    sync.Mutex
	conns map[*session]struct{}
}

// NewHub 创建空的分发中心。
func NewHub() *Hub {
	return &Hub{conns: make(map[*session]struct{})}
}

func (h *Hub) add(s *session) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.conns[s] = struct{}{}
}

func (h *Hub) remove(s *session) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.conns, s)
}

// Broadcast 把一个事件发给所有连接。
func (h *Hub) Broadcast(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		log.Printf("事件序列化失败: %v", err)
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.conns {
		s.enqueueBroadcast(data)
	}
}

// Enqueue 是测试用的直连广播入口。
func (h *Hub) Enqueue(data []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.conns {
		s.enqueueBroadcast(data)
	}
}
