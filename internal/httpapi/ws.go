package httpapi

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"

	"chatmod/internal/corpus"
)

// clientMsg 是客户端 → 服务端的统一请求信封。
type clientMsg struct {
	Type string `json:"type"`

	Text     string `json:"text"`
	PlayerID int64  `json:"playerId"`
	Name     string `json:"name"`
	Reason   string `json:"reason"`

	Instruction    string `json:"instruction"`
	IncludeSamples bool   `json:"includeSamples"`

	Operations []corpus.Operation `json:"operations"`
	Note       string             `json:"note"`
	RevisionID int64              `json:"revisionId"`
}

type session struct {
	conn *websocket.Conn
	svc  *Service
	hub  *Hub

	out  chan []byte
	done chan struct{}
	once sync.Once

	playerID int64
	draft    *Draft
}

func newSession(conn *websocket.Conn, svc *Service, hub *Hub) *session {
	return &session{
		conn: conn,
		svc:  svc,
		hub:  hub,
		out:  make(chan []byte, 64),
		done: make(chan struct{}),
	}
}

func (s *session) enqueue(data []byte) {
	select {
	case s.out <- data:
	default:
		// 慢客户端：丢事件比阻塞所有连接好。
	}
}

func (s *session) close() {
	s.once.Do(func() {
		close(s.done)
		_ = s.conn.Close(websocket.StatusNormalClosure, "")
	})
}

func (s *session) send(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		log.Printf("事件序列化失败: %v", err)
		return
	}
	s.enqueue(data)
}

func (s *session) sendErr(msg string, problems ...corpus.Problem) {
	s.send(errorEvent{Type: "error", Message: msg, Problems: problems})
}

func (s *session) writeLoop(ctx context.Context) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.done:
			return
		case data := <-s.out:
			wctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := s.conn.Write(wctx, websocket.MessageText, data)
			cancel()
			if err != nil {
				s.close()
				return
			}
		case <-ticker.C:
			pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err := s.conn.Ping(pctx)
			cancel()
			if err != nil {
				s.close()
				return
			}
		}
	}
}

// serveWS 是 WebSocket 是唯一通道的落点：发消息、审核结果、禁言生效与解除、
// 全局消息流全部走这里。
func (srv *Server) serveWS(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		log.Printf("WebSocket 握手失败: %v", err)
		return
	}
	conn.SetReadLimit(1 << 20)

	sess := newSession(conn, srv.svc, srv.hub)
	srv.hub.add(sess)
	defer srv.hub.remove(sess)
	defer sess.close()

	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go sess.writeLoop(ctx)

	boot, err := srv.svc.Bootstrap()
	if err != nil {
		sess.sendErr("加载初始状态失败: " + err.Error())
		return
	}
	sess.send(bootstrapEvent{Type: "bootstrap", Bootstrap: boot})

	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		if typ != websocket.MessageText {
			continue
		}
		var msg clientMsg
		if err := json.Unmarshal(data, &msg); err != nil {
			sess.sendErr("无法解析请求")
			continue
		}
		sess.handle(ctx, msg)
	}
}

func (s *session) handle(ctx context.Context, msg clientMsg) {
	switch msg.Type {
	case "set_player":
		if msg.PlayerID != s.playerID {
			s.playerID = msg.PlayerID
		}
		s.send(ackEvent{Type: "player_selected", PlayerID: msg.PlayerID})

	case "send":
		res, err := s.svc.Send(s.playerID, msg.Text)
		if err != nil {
			s.sendErr(err.Error())
			return
		}
		// 发送成功（或被拒绝）都把本条判定结果回给发送方。
		s.send(resultEvent{Type: "result", Result: res})

	case "player.create":
		if _, err := s.svc.CreatePlayer(msg.Name); err != nil {
			s.sendErr(err.Error())
			return
		}
		s.send(ackEvent{Type: "ack", Note: "玩家已创建"})

	case "player.rename":
		if err := s.svc.RenamePlayer(msg.PlayerID, msg.Name); err != nil {
			s.sendErr(err.Error())
			return
		}
		s.send(ackEvent{Type: "ack", Note: "玩家已改名"})

	case "player.delete":
		if err := s.svc.DeletePlayer(msg.PlayerID); err != nil {
			s.sendErr(err.Error())
			return
		}
		if s.playerID == msg.PlayerID {
			s.playerID = 0
		}
		s.send(ackEvent{Type: "ack", Note: "玩家已删除"})

	case "lift":
		if err := s.svc.Lift(msg.PlayerID, msg.Reason); err != nil {
			s.sendErr(err.Error())
			return
		}
		s.send(ackEvent{Type: "ack", Note: "已提前解除禁言"})

	case "corpus.manual":
		if _, problems, err := s.svc.CommitOps(msg.Operations, msg.Note); err != nil {
			s.sendErr(err.Error(), problems...)
			return
		}
		s.send(ackEvent{Type: "ack", Note: "语料库已更新"})

	case "draft.request":
		draft, err := s.svc.Draft(ctx, msg.Instruction, msg.IncludeSamples)
		if err != nil {
			s.sendErr(err.Error())
			return
		}
		s.draft = draft
		s.send(draftEvent{Type: "draft", Draft: draft})

	case "draft.validate":
		problems := s.svc.ValidateOps(msg.Operations)
		s.send(draftProblemsEvent{Type: "draft.problems", Problems: problems})

	case "draft.commit":
		if _, problems, err := s.svc.CommitOps(msg.Operations, msg.Note); err != nil {
			s.sendErr(err.Error(), problems...)
			return
		}
		s.draft = nil
		s.send(ackEvent{Type: "draft.committed", Note: "草案已提交为一次修订"})

	case "dryrun":
		playerID := msg.PlayerID
		if playerID == 0 {
			playerID = s.playerID
		}
		ops := msg.Operations
		if len(ops) == 0 && s.draft != nil {
			// 未显式传操作时，用会话里的草稿状态（含管理员编辑过的 after）。
			ops = s.draft.Operations
		}
		res, err := s.svc.DryRun(playerID, msg.Text, ops)
		if err != nil {
			s.sendErr(err.Error())
			return
		}
		s.send(dryRunEvent{Type: "dryrun.result", Result: res})

	case "revision.rollback":
		if _, err := s.svc.Rollback(msg.RevisionID, msg.Note); err != nil {
			s.sendErr(err.Error())
			return
		}
		s.send(ackEvent{Type: "ack", Note: "已回滚为一次新修订"})

	default:
		s.sendErr("未知请求类型: " + msg.Type)
	}
}
