package httpapi

import (
	_ "embed"
	"net/http"
)

//go:embed web/index.html
var indexHTML []byte

// Server 承载 HTTP（只有一个静态页面）与 WebSocket（唯一的数据通道）。
type Server struct {
	svc *Service
	hub *Hub
}

// NewServer 组装服务与分发中心。
func NewServer(svc *Service) *Server {
	hub := NewHub()
	svc.SetBroadcaster(hub)
	return &Server{svc: svc, hub: hub}
}

// Handler 返回路由。只有两个端点：页面本身与 WS。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /ws", s.serveWS)
	mux.HandleFunc("GET /", s.serveIndex)
	return mux
}

func (s *Server) serveIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("content-type", "text/html; charset=utf-8")
	w.Header().Set("cache-control", "no-store")
	_, _ = w.Write(indexHTML)
}
