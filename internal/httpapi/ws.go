package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"chatmod/internal/corpus"
)

const (
	// outQueueSize 是每个连接待发事件的缓冲深度。广播可以丢，但丢的时候
	// 会断开连接让客户端重连拿全量状态（见 enqueueBroadcast）。
	outQueueSize = 512
	// directSendTimeout 是直接回包等待写协程排空的上限；超过就断开，
	// 绝不把 result / draft 这类回包静默丢掉。
	directSendTimeout = 5 * time.Second
	// draftRequestTimeout 与前端 150s 等待上限对齐，并留在中转站 HTTP 180s 超时之内。
	draftRequestTimeout = 150 * time.Second
)

// clientMsg 是客户端 → 服务端的统一请求信封。
type clientMsg struct {
	Type string `json:"type"`
	// RequestID 只用于 agent 草案链路：把 started / busy / draft / error / cancelled
	// 对回同一次请求，避免取消后旧结果覆盖新状态。
	RequestID string `json:"requestId"`

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

// draftRun 是服务端正在执行的一次模型往返。每个连接至多一条，
// 保证“模型返回前不发第二个请求”不只是前端约束。
type draftRun struct {
	RequestID      string
	Instruction    string
	IncludeSamples bool
	Model          string
	StartedAt      time.Time
	cancel         context.CancelFunc
}

type session struct {
	conn *websocket.Conn
	svc  *Service
	hub  *Hub

	out     chan []byte
	done    chan struct{}
	once    sync.Once
	lagOnce sync.Once

	playerID int64

	// draftMu 保护 draft 与 draftRun：草案结果在后台 goroutine 里写入，
	// 而读消息的协程会同时读取/替换草稿。
	draftMu  sync.Mutex
	draft    *Draft
	draftRun *draftRun
}

func newSession(conn *websocket.Conn, svc *Service, hub *Hub) *session {
	return &session{
		conn: conn,
		svc:  svc,
		hub:  hub,
		out:  make(chan []byte, outQueueSize),
		done: make(chan struct{}),
	}
}

// enqueueBroadcast 只用于广播：慢客户端允许丢弃，但绝不允许“静默”丢。
// 队列满时主动断开，浏览器重连后会拿到 bootstrap 全量状态，
// 不会出现界面漏掉禁言/语料库事件却一直以为自己是最新的。
func (s *session) enqueueBroadcast(data []byte) {
	select {
	case s.out <- data:
	default:
		s.lag("广播事件积压")
	}
}

// enqueueDirect 用于本次请求的直接回包（result / draft / error / ack 等）。
// 这些事件绝不能丢：队列满时对当前连接施加背压，超时才断开（客户端重连后重试），
// 而不是让判定结果默默消失。
func (s *session) enqueueDirect(data []byte) {
	select {
	case <-s.done:
		return
	default:
	}
	select {
	case s.out <- data:
		return
	default:
	}
	timer := time.NewTimer(directSendTimeout)
	defer timer.Stop()
	select {
	case s.out <- data:
	case <-s.done:
	case <-timer.C:
		s.lag("直接回包积压")
	}
}

func (s *session) close() {
	s.closeWith(websocket.StatusNormalClosure, "")
}

func (s *session) closeWith(code websocket.StatusCode, reason string) {
	s.once.Do(func() {
		if run, ok := s.cancelDraftRun(""); ok {
			log.Printf("连接关闭，已取消在途草案 requestId=%s", run.RequestID)
		}
		close(s.done)
		if s.conn != nil {
			_ = s.conn.Close(code, reason)
		}
	})
}

// lag 标记这个连接已经漏事件：断开比继续给一份不完整的状态更安全。
// 必须异步关闭：Broadcast 持有 Hub 锁，而 Close 会做最多 10 秒的关闭握手。
func (s *session) lag(reason string) {
	s.lagOnce.Do(func() {
		log.Printf("WebSocket 待发队列积压（%s），断开连接让客户端重连后重新同步", reason)
		go s.closeWith(websocket.StatusTryAgainLater, "事件积压，请重连")
	})
}

func (s *session) send(v any) {
	data, err := json.Marshal(v)
	if err != nil {
		log.Printf("事件序列化失败: %v", err)
		return
	}
	s.enqueueDirect(data)
}

func (s *session) sendErr(msg string, problems ...corpus.Problem) {
	s.send(errorEvent{Type: "error", Message: msg, Problems: problems})
}

// ---------- 会话里的草稿状态 ----------

func (s *session) draftOperations() []corpus.Operation {
	s.draftMu.Lock()
	defer s.draftMu.Unlock()
	if s.draft == nil {
		return nil
	}
	return append([]corpus.Operation(nil), s.draft.Operations...)
}

func (s *session) clearDraft() {
	s.draftMu.Lock()
	s.draft = nil
	s.draftMu.Unlock()
}

func (s *session) replaceDraftOperations(ops []corpus.Operation, problems []corpus.Problem) {
	s.draftMu.Lock()
	defer s.draftMu.Unlock()
	if s.draft == nil {
		s.draft = &Draft{CreatedAt: s.svc.now()}
	}
	s.draft.Operations = ops
	s.draft.Problems = problems
}

// cancelDraftRun 取走并中止在途请求。requestID 为空时取消当前任意一条；
// 指定 requestID 时不匹配则不动（避免旧页面的取消误杀新请求）。
func (s *session) cancelDraftRun(requestID string) (*draftRun, bool) {
	s.draftMu.Lock()
	run := s.draftRun
	if run == nil || (requestID != "" && run.RequestID != requestID) {
		s.draftMu.Unlock()
		return nil, false
	}
	s.draftRun = nil
	s.draftMu.Unlock()
	run.cancel()
	return run, true
}

// ---------- agent 草案：异步 + 服务端单飞 ----------

// startDraft 立即返回，把模型往返放到后台 goroutine。
// 这样同一连接上后发的普通聊天消息不会被 3s（或更久）的模型往返阻塞。
func (s *session) startDraft(ctx context.Context, msg clientMsg) {
	requestID := strings.TrimSpace(msg.RequestID)
	if requestID == "" {
		requestID = fmt.Sprintf("srv-%d", s.svc.now().UnixNano())
	}
	if !s.svc.AgentEnabled() {
		s.send(errorEvent{
			Type: "error", Message: "未配置 AGENT_API_KEY，请手工编辑语料库",
			Source: "draft", RequestID: requestID,
		})
		return
	}

	s.draftMu.Lock()
	if run := s.draftRun; run != nil {
		s.draftMu.Unlock()
		// 服务端已经有一次生成在途：只把状态回给前端，绝不再次调用模型。
		s.send(draftBusyEvent{
			Type:           "draft.busy",
			RequestID:      run.RequestID,
			Instruction:    run.Instruction,
			IncludeSamples: run.IncludeSamples,
			Model:          run.Model,
			StartedAt:      run.StartedAt,
			ElapsedMS:      s.svc.now().Sub(run.StartedAt).Milliseconds(),
		})
		return
	}
	runCtx, cancel := context.WithTimeout(ctx, draftRequestTimeout)
	run := &draftRun{
		RequestID:      requestID,
		Instruction:    msg.Instruction,
		IncludeSamples: msg.IncludeSamples,
		Model:          s.svc.AgentModel(),
		StartedAt:      s.svc.now(),
		cancel:         cancel,
	}
	s.draftRun = run
	s.draftMu.Unlock()

	// 先告诉前端"已经发出去了"：模型往返通常要 10–30 秒，
	// 没有这条事件界面就只能干等，看起来像没反应。
	s.send(draftStartedEvent{
		Type:           "draft.started",
		RequestID:      run.RequestID,
		Instruction:    run.Instruction,
		IncludeSamples: run.IncludeSamples,
		Model:          run.Model,
		StartedAt:      run.StartedAt,
	})
	log.Printf("草案请求：requestId=%s 模型=%s 附带样本=%v 指令=%q",
		run.RequestID, run.Model, run.IncludeSamples, run.Instruction)

	go s.runDraft(runCtx, run)
}

func (s *session) runDraft(ctx context.Context, run *draftRun) {
	defer run.cancel()

	draft, err := s.svc.Draft(ctx, run.Instruction, run.IncludeSamples)

	s.draftMu.Lock()
	if s.draftRun != run {
		// 已经被 draft.cancel / 连接关闭取消：旧结果不再推到新状态上。
		s.draftMu.Unlock()
		return
	}
	s.draftRun = nil
	if err == nil {
		s.draft = draft
	}
	s.draftMu.Unlock()

	if err != nil {
		if errors.Is(err, context.Canceled) {
			return
		}
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			err = fmt.Errorf("等待超过 %s 仍未返回，已中止本次生成；可以重新发送", draftRequestTimeout)
		}
		log.Printf("草案生成失败：requestId=%s %v", run.RequestID, err)
		s.send(errorEvent{
			Type: "error", Message: err.Error(), Source: "draft", RequestID: run.RequestID,
		})
		return
	}
	log.Printf("草案生成完成：requestId=%s %d 条建议", run.RequestID, len(draft.Operations))
	s.send(draftEvent{Type: "draft", Draft: draft, RequestID: run.RequestID})
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
		// 读到请求就返回，模型往返在后台 goroutine 里跑：
		// 同一连接上后发的聊天消息不再被 agent 往返阻塞。
		s.startDraft(ctx, msg)

	case "draft.cancel":
		requestID := strings.TrimSpace(msg.RequestID)
		if run, ok := s.cancelDraftRun(requestID); ok {
			log.Printf("草案已取消：requestId=%s", run.RequestID)
			s.send(draftCancelledEvent{
				Type: "draft.cancelled", RequestID: run.RequestID,
				Note: "已中止在途的模型请求，可以重新发送",
			})
			return
		}
		s.send(ackEvent{Type: "ack", Note: "当前没有进行中的生成"})

	case "draft.validate":
		problems := s.svc.ValidateOps(msg.Operations)
		s.send(draftProblemsEvent{Type: "draft.problems", Problems: problems})

	case "draft.sync":
		// 管理员在前端删掉了部分草案条目：用这份列表替换会话里的草稿，
		// 让后续试跑看到的是同一条草案。草案只在内存，绝不写语料库。
		if len(msg.Operations) == 0 {
			s.clearDraft()
			s.send(draftProblemsEvent{Type: "draft.problems"})
			return
		}
		problems := s.svc.ValidateOps(msg.Operations)
		s.replaceDraftOperations(msg.Operations, problems)
		s.send(draftProblemsEvent{Type: "draft.problems", Problems: problems})

	case "draft.commit":
		if _, problems, err := s.svc.CommitOps(msg.Operations, msg.Note); err != nil {
			s.sendErr(err.Error(), problems...)
			return
		}
		s.clearDraft()
		s.send(ackEvent{Type: "draft.committed", Note: "草案已提交为一次修订"})

	case "dryrun":
		playerID := msg.PlayerID
		if playerID == 0 {
			playerID = s.playerID
		}
		ops := msg.Operations
		if len(ops) == 0 {
			// 未显式传操作时，用会话里的草稿状态（含管理员编辑/删除后的列表）。
			ops = s.draftOperations()
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
