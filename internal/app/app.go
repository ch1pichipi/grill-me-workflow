// Package app 是进程入口的接线：打开数据库、构造 agent、启动 HTTP/WS 服务。
package app

import (
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"chatmod/internal/drafter"
	"chatmod/internal/httpapi"
	"chatmod/internal/store"
)

// Run 按环境变量启动单进程演示服务。
//
//	ADDR          监听地址（默认 :8043）
//	DB_PATH       SQLite 文件路径（默认 chatmod.db）
//	AGENT_BASE_URL / AGENT_API_KEY / AGENT_MODEL  agent 中转站配置（有默认值）
//	AGENT_API_KEY 显式设为空时禁用 agent，其余功能完全可用
func Run() error {
	addr := envOr("ADDR", ":8043")
	dbPath := envOr("DB_PATH", "chatmod.db")

	st, err := store.Open(dbPath)
	if err != nil {
		return err
	}
	defer st.Close()

	agent := drafter.NewAgentFromEnv()

	svc, err := httpapi.NewService(st, agent, time.Now)
	if err != nil {
		return err
	}
	srv := httpapi.NewServer(svc)

	log.Printf("聊天审核系统已启动 %s db=%s agent=%s(%v) model=%s",
		displayAddr(addr), dbPath, agent.Name, svc.AgentEnabled(), agent.Model)
	return http.ListenAndServe(addr, srv.Handler())
}

func displayAddr(addr string) string {
	if strings.HasPrefix(addr, ":") {
		return "http://localhost" + addr
	}
	return "http://" + addr
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
