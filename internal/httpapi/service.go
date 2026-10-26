package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"chatmod/internal/corpus"
	"chatmod/internal/drafter"
	"chatmod/internal/moderation"
	"chatmod/internal/punishment"
	"chatmod/internal/store"
)

const (
	// MaxMessageRunes 是消息原文上限；超出截断并标记 truncated。
	MaxMessageRunes = 2000
	// SampleLimit 是"附带最近消息样本"的条数。
	SampleLimit = 30
	// streamLimit 是服务端内存里保留的全局流条数（供新连接回看）。
	streamLimit = 200
	// violationWindow 是滚动 7 天窗口。
	violationWindow = 7 * 24 * time.Hour
)

// Broadcaster 是服务层向所有连接广播事件的窄接口。
type Broadcaster interface {
	Broadcast(v any)
}

// PlayerMute 是玩家视图里的禁言状态。
type PlayerMute struct {
	ID          int64      `json:"id"`
	StartedAt   time.Time  `json:"startedAt"`
	ExpiresAt   *time.Time `json:"expiresAt"`
	Permanent   bool       `json:"permanent"`
	RemainingMS int64      `json:"remainingMs"`
	EndReason   string     `json:"endReason,omitempty"`
}

// PlayerView 是玩家列表的一行：名字 + 当前禁言 + 7 天/终身违规次数。
// 这四个数字正好是档位公式的全部输入。
type PlayerView struct {
	ID        int64       `json:"id"`
	Name      string      `json:"name"`
	CreatedAt time.Time   `json:"createdAt"`
	Mute      *PlayerMute `json:"mute"`
	Count7d   int         `json:"count7d"`
	Lifetime  int         `json:"lifetime"`
}

// StreamEntry 是管理员全局流里的一条：消息判定或被提前解除的禁言。
type StreamEntry struct {
	Kind     string `json:"kind"` // message | lift
	Seq      int64  `json:"seq"`
	At       int64  `json:"at"` // Unix 毫秒
	PlayerID int64  `json:"playerId"`
	// PlayerName 是判定当时的名字快照，改名不影响回看。
	PlayerName     string           `json:"playerName"`
	Text           string           `json:"text,omitempty"`
	NormalizedText string           `json:"normalizedText,omitempty"`
	Acrostic       string           `json:"acrostic,omitempty"`
	Truncated      bool             `json:"truncated"`
	Rejected       bool             `json:"rejected"`
	RejectReason   string           `json:"rejectReason,omitempty"`
	MuteRemaining  int64            `json:"muteRemainingMs,omitempty"`
	Hits           []moderation.Hit `json:"hits"`
	HitCount       int              `json:"hitCount"`
	Severity       int              `json:"severity"`
	IsViolation    bool             `json:"isViolation"`
	Disposition    string           `json:"disposition"`
	Rung           int              `json:"rung"`
	DurationMS     int64            `json:"durationMs"`
	Permanent      bool             `json:"permanent"`
	Count7d        int              `json:"count7d"`
	Lifetime       int              `json:"lifetime"`
	MuteStarted    *PlayerMute      `json:"muteStarted,omitempty"`
	MuteReplaced   bool             `json:"muteReplaced,omitempty"`
	MuteID         int64            `json:"muteId,omitempty"`
	Note           string           `json:"note,omitempty"`
}

// SendResult 是一次发送的结果，回给发送方。
type SendResult struct {
	Entry           StreamEntry        `json:"entry"`
	Verdict         moderation.Verdict `json:"verdict"`
	Rejected        bool               `json:"rejected"`
	RejectReason    string             `json:"rejectReason,omitempty"`
	MuteRemainingMS int64              `json:"muteRemainingMs,omitempty"`
}

// Change 是一条修订带来的差异（人可读）。
type Change struct {
	Target string `json:"target"` // rule | mapping
	Op     string `json:"op"`     // add | update | delete
	ID     string `json:"id"`
	Detail string `json:"detail"`
}

// RevisionView 是修订列表的一行。
type RevisionView struct {
	ID         int64              `json:"id"`
	CreatedAt  time.Time          `json:"createdAt"`
	Note       string             `json:"note"`
	Summary    string             `json:"summary"`
	Operations []corpus.Operation `json:"operations"`
	Changes    []Change           `json:"changes"`
}

// StairRung 是只读展示的一个档位。
type StairRung struct {
	Rung       int    `json:"rung"`
	DurationMS int64  `json:"durationMs"`
	Label      string `json:"label"`
}

// StairView 是当前阶梯，前端只读展示（数据来自后端而非前端常量）。
type StairView struct {
	Durations          []StairRung `json:"durations"`
	PermanentThreshold int         `json:"permanentThreshold"`
	Formula            string      `json:"formula"`
}

// Draft 是一份变更草案，只存在于服务端内存。
type Draft struct {
	Instruction    string             `json:"instruction"`
	IncludeSamples bool               `json:"includeSamples"`
	Operations     []corpus.Operation `json:"operations"`
	Problems       []corpus.Problem   `json:"problems"`
	Samples        []drafter.Sample   `json:"samples"`
	CreatedAt      time.Time          `json:"createdAt"`
}

// DryRunResult 是试跑结果。试跑不写库、不产生判定。
type DryRunResult struct {
	Input          string           `json:"input"`
	Truncated      bool             `json:"truncated"`
	NormalizedText string           `json:"normalizedText"`
	RawView        string           `json:"rawView"`
	AcrosticView   string           `json:"acrosticView"`
	Hits           []moderation.Hit `json:"hits"`
	HitCount       int              `json:"hitCount"`
	Severity       int              `json:"severity"`
	IsViolation    bool             `json:"isViolation"`
	Count7d        int              `json:"count7d"`
	Lifetime       int              `json:"lifetime"`
	Disposition    string           `json:"disposition"`
	Rung           int              `json:"rung"`
	DurationMS     int64            `json:"durationMs"`
	Permanent      bool             `json:"permanent"`
	CorpusSource   string           `json:"corpusSource"` // draft | current
}

// Bootstrap 是新连接拿到的完整初始状态。
type Bootstrap struct {
	Players      []PlayerView       `json:"players"`
	Corpus       corpus.Corpus      `json:"corpus"`
	Revisions    []RevisionView     `json:"revisions"`
	Stair        StairView          `json:"stair"`
	AgentEnabled bool               `json:"agentEnabled"`
	AgentModel   string             `json:"agentModel"`
	AgentLog     []drafter.Exchange `json:"agentLog"`
	Stream       []StreamEntry      `json:"stream"`
}

// Service 是编排层：玩家、发送、判定、禁言状态机、语料库与草案。
// 所有写操作在一把锁内串行化，保证"7 天计数"这类现算值与判定写入一致。
type Service struct {
	st      *store.Store
	drafter drafter.Drafter
	now     func() time.Time

	snap atomic.Pointer[corpus.Snapshot]

	mu     sync.Mutex
	hub    Broadcaster
	timers map[int64]*time.Timer
	stream []StreamEntry
	seq    int64

	// agent 往返日志（只用于排障）：最新的在最后一条。
	logMu    sync.Mutex
	agentLog []drafter.Exchange
}

// agentLogLimit 是在内存里保留的 agent 往返记录条数。
const agentLogLimit = 10

// NewService 载入当前语料库（必要时写入种子语料库）并恢复禁言到期定时器。
func NewService(st *store.Store, d drafter.Drafter, now func() time.Time) (*Service, error) {
	if now == nil {
		now = time.Now
	}
	s := &Service{
		st:      st,
		drafter: d,
		now:     now,
		timers:  make(map[int64]*time.Timer),
	}

	n, err := st.CountRevisions()
	if err != nil {
		return nil, err
	}
	var current corpus.Corpus
	if n == 0 {
		current = SeedCorpus()
		if _, err := st.InsertRevision(nil, current, "初始化语料库", now()); err != nil {
			return nil, err
		}
	} else {
		rev, ok, err := st.LatestRevision()
		if err != nil {
			return nil, err
		}
		if !ok {
			current = SeedCorpus()
		} else {
			current = rev.Snapshot
		}
	}
	snap, err := corpus.Build(current)
	if err != nil {
		return nil, fmt.Errorf("载入语料库失败: %w", err)
	}
	s.snap.Store(snap)

	s.restoreMutes()

	// agent 每次往返都留档：进程日志 + 界面上的“请求/响应日志”面板。
	if rec, ok := d.(drafter.ExchangeRecorder); ok {
		rec.ObserveExchange(s.recordAgentExchange)
	}
	return s, nil
}

// Close 停止所有禁言定时器（进程退出或测试用）。
func (s *Service) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, t := range s.timers {
		t.Stop()
		delete(s.timers, id)
	}
}

// SetBroadcaster 由 http 层在启动时注入。
func (s *Service) SetBroadcaster(b Broadcaster) { s.hub = b }

// AgentEnabled 报告 agent 是否可用（无 key 时按钮禁用）。
func (s *Service) AgentEnabled() bool { return s.drafter != nil && s.drafter.Enabled() }

// AgentModel 返回当前模型名（未配置时为空串）。
func (s *Service) AgentModel() string {
	if n, ok := s.drafter.(interface{ ModelName() string }); ok {
		return n.ModelName()
	}
	return ""
}

// AgentLog 返回最近的 agent 往返记录（旧的在前）。
func (s *Service) AgentLog() []drafter.Exchange {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	return append([]drafter.Exchange(nil), s.agentLog...)
}

// recordAgentExchange 把一次往返落到进程日志与内存环形缓冲区，并推给所有管理员页面。
func (s *Service) recordAgentExchange(ex drafter.Exchange) {
	s.logMu.Lock()
	s.agentLog = append(s.agentLog, ex)
	if len(s.agentLog) > agentLogLimit {
		s.agentLog = append([]drafter.Exchange(nil), s.agentLog[len(s.agentLog)-agentLogLimit:]...)
	}
	s.logMu.Unlock()

	log.Printf("agent → POST %s model=%s 指令=%q 样本=%d 请求体=%d 字节",
		ex.URL, ex.Model, clipLog(ex.Instruction, 120), ex.Samples, len(ex.Request))
	log.Printf("agent ← status=%d 用时=%dms 响应体=%d 字节 操作=%d 错误=%v",
		ex.Status, ex.ElapsedMS, len(ex.Response), ex.Operations, ex.Error)
	if ex.Warning != "" {
		log.Printf("agent 警告: %s", ex.Warning)
	}
	log.Printf("agent 请求体（截断）: %s", clipLog(ex.Request, 2000))
	log.Printf("agent 响应体: %s", clipLog(ex.Response, 2000))

	s.emit(agentLogEvent{Type: "agent.log", Exchange: ex})
}

// clipLog 把日志里的长文本截断，完整内容留在界面的 agent 日志面板里。
func clipLog(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…（截断，完整内容见界面 agent 日志）"
}

func (s *Service) emit(v any) {
	if s.hub != nil {
		s.hub.Broadcast(v)
	}
}

func (s *Service) nextSeq() int64 {
	s.seq++
	return s.seq
}

func (s *Service) appendStreamLocked(e StreamEntry) {
	s.stream = append(s.stream, e)
	if len(s.stream) > streamLimit {
		s.stream = append([]StreamEntry(nil), s.stream[len(s.stream)-streamLimit:]...)
	}
}

func (s *Service) emitMuteEndedLocked(playerID int64, reason string) {
	s.emit(muteEvent{Type: "mute", PlayerID: playerID, Mute: nil, EndReason: reason})
}

// ---------- 玩家 ----------

// CreatePlayer 创建玩家并广播列表。
func (s *Service) CreatePlayer(name string) (store.Player, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return store.Player{}, fmt.Errorf("玩家名字不能为空")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	p, err := s.st.CreatePlayer(name, s.now())
	if err != nil {
		return store.Player{}, err
	}
	s.broadcastPlayersLocked()
	return p, nil
}

// RenamePlayer 改名；历史判定与禁言仍归属同一玩家。
func (s *Service) RenamePlayer(id int64, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("玩家名字不能为空")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := s.st.RenamePlayer(id, name); err != nil {
		return err
	}
	s.broadcastPlayersLocked()
	return nil
}

// DeletePlayer 软删除：从列表消失，历史全部保留。
func (s *Service) DeletePlayer(id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.st.SoftDeletePlayer(id, s.now()); err != nil {
		return err
	}
	s.cancelTimer(id)
	s.broadcastPlayersLocked()
	return nil
}

// Players 返回玩家列表视图（含禁言剩余时间与两个违规计数）。
func (s *Service) Players() ([]PlayerView, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.playersLocked()
}

func (s *Service) playersLocked() ([]PlayerView, error) {
	ps, err := s.st.Players()
	if err != nil {
		return nil, err
	}
	now := s.now()
	out := make([]PlayerView, 0, len(ps))
	for _, p := range ps {
		m, _ := s.resolveMuteLocked(p.ID, now)
		v := PlayerView{ID: p.ID, Name: p.Name, CreatedAt: p.CreatedAt}
		if m != nil {
			v.Mute = muteView(*m, now)
		}
		if v.Count7d, err = s.st.CountViolationsSince(p.ID, now.Add(-violationWindow)); err != nil {
			return nil, err
		}
		if v.Lifetime, err = s.st.CountViolationsLifetime(p.ID); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, nil
}

func (s *Service) broadcastPlayersLocked() {
	views, err := s.playersLocked()
	if err != nil {
		log.Printf("读取玩家列表失败: %v", err)
		return
	}
	s.emit(playersEvent{Type: "players", Players: views})
}

// ---------- 禁言状态机 ----------

func muteView(m store.Mute, now time.Time) *PlayerMute {
	v := &PlayerMute{
		ID:        m.ID,
		StartedAt: m.StartedAt,
		ExpiresAt: m.ExpiresAt,
		Permanent: m.Permanent(),
	}
	if m.ExpiresAt != nil {
		rem := m.ExpiresAt.Sub(now)
		if rem < 0 {
			rem = 0
		}
		v.RemainingMS = rem.Milliseconds()
	}
	return v
}

// resolveMuteLocked 做惰性到期判定：now > expiresAt 即视为已结束，
// 不会误报"仍在禁言"。
func (s *Service) resolveMuteLocked(playerID int64, now time.Time) (*store.Mute, bool) {
	m, err := s.st.ActiveMute(playerID)
	if err != nil {
		log.Printf("读取禁言失败: %v", err)
		return nil, false
	}
	if m == nil {
		return nil, false
	}
	if m.ExpiresAt == nil || now.Before(*m.ExpiresAt) {
		return m, false
	}
	if err := s.st.EndMute(m.ID, store.EndReasonExpired, now, ""); err != nil {
		log.Printf("结束到期禁言失败: %v", err)
		return nil, false
	}
	s.cancelTimer(playerID)
	s.emitMuteEndedLocked(playerID, store.EndReasonExpired)
	return nil, true
}

func (s *Service) cancelTimer(playerID int64) {
	if t, ok := s.timers[playerID]; ok {
		t.Stop()
		delete(s.timers, playerID)
	}
}

// scheduleLocked 在到期那一刻主动推送"你可以说话了"，不必等玩家再发消息。
func (s *Service) scheduleLocked(m store.Mute) {
	if m.ExpiresAt == nil {
		return
	}
	delay := m.ExpiresAt.Sub(s.now())
	if delay < 0 {
		delay = 0
	}
	playerID := m.PlayerID
	t := time.AfterFunc(delay, func() { s.expireMute(playerID) })
	s.timers[playerID] = t
}

func (s *Service) expireMute(playerID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	m, err := s.st.ActiveMute(playerID)
	if err != nil || m == nil {
		return
	}
	if m.ExpiresAt == nil {
		return
	}
	if now.Before(*m.ExpiresAt) {
		s.scheduleLocked(*m)
		return
	}
	if err := s.st.EndMute(m.ID, store.EndReasonExpired, now, ""); err != nil {
		log.Printf("结束到期禁言失败: %v", err)
		return
	}
	s.cancelTimer(playerID)
	s.emitMuteEndedLocked(playerID, store.EndReasonExpired)
	s.broadcastPlayersLocked()
}

func (s *Service) restoreMutes() {
	mutes, err := s.st.ActiveMutes()
	if err != nil {
		log.Printf("恢复禁言定时器失败: %v", err)
		return
	}
	now := s.now()
	for _, m := range mutes {
		if m.ExpiresAt != nil && !now.Before(*m.ExpiresAt) {
			if err := s.st.EndMute(m.ID, store.EndReasonExpired, now, ""); err != nil {
				log.Printf("清理过期禁言失败: %v", err)
			}
			continue
		}
		s.scheduleLocked(m)
	}
}

// Lift 提前解除禁言：原因必填，操作人固定为 admin。
func (s *Service) Lift(playerID int64, reason string) error {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return fmt.Errorf("提前解除必须填写原因")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	p, ok, err := s.st.Player(playerID)
	if err != nil {
		return err
	}
	if !ok || p.DeletedAt != nil {
		return fmt.Errorf("玩家不存在")
	}
	m, ended := s.resolveMuteLocked(playerID, now)
	if ended {
		s.emitMuteEndedLocked(playerID, store.EndReasonExpired)
	}
	if m == nil {
		return fmt.Errorf("该玩家当前没有生效的禁言")
	}
	if err := s.st.EndMute(m.ID, store.EndReasonLifted, now, reason); err != nil {
		return err
	}
	s.cancelTimer(playerID)

	entry := StreamEntry{
		Kind:       "lift",
		Seq:        s.nextSeq(),
		At:         now.UnixMilli(),
		PlayerID:   p.ID,
		PlayerName: p.Name,
		MuteID:     m.ID,
		Note:       reason,
	}
	s.appendStreamLocked(entry)
	s.emit(streamEvent{Type: "stream", Entry: entry})
	s.emitMuteEndedLocked(playerID, store.EndReasonLifted)
	s.broadcastPlayersLocked()
	return nil
}

// ---------- 发送与判定 ----------

// Send 处理一次聊天发送。
func (s *Service) Send(playerID int64, rawText string) (SendResult, error) {
	text, truncated := truncateRunes(strings.TrimSpace(rawText), MaxMessageRunes)
	if text == "" {
		return SendResult{}, fmt.Errorf("消息不能为空")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := s.now()
	p, ok, err := s.st.Player(playerID)
	if err != nil {
		return SendResult{}, err
	}
	if !ok || p.DeletedAt != nil {
		return SendResult{}, fmt.Errorf("玩家不存在")
	}

	mute, ended := s.resolveMuteLocked(playerID, now)
	if ended {
		s.emitMuteEndedLocked(playerID, store.EndReasonExpired)
		s.broadcastPlayersLocked()
	}
	if mute != nil {
		// 禁言期间的消息不入库、不产生判定、不发送。
		rem := int64(0)
		reason := "处于禁言中"
		if mute.ExpiresAt == nil {
			reason = "处于禁言中（永久）"
		} else {
			rem = mute.ExpiresAt.Sub(now).Milliseconds()
			if rem < 0 {
				rem = 0
			}
			reason = "处于禁言中，剩余 " + punishment.DurationLabel(time.Duration(rem)*time.Millisecond)
		}
		entry := StreamEntry{
			Kind:          "message",
			Seq:           s.nextSeq(),
			At:            now.UnixMilli(),
			PlayerID:      p.ID,
			PlayerName:    p.Name,
			Text:          text,
			Truncated:     truncated,
			Rejected:      true,
			RejectReason:  reason,
			MuteRemaining: rem,
			Disposition:   "rejected",
			Hits:          []moderation.Hit{},
		}
		s.appendStreamLocked(entry)
		s.emit(streamEvent{Type: "stream", Entry: entry})
		return SendResult{Entry: entry, Rejected: true, RejectReason: reason, MuteRemainingMS: rem}, nil
	}

	snap := s.snap.Load()
	if snap == nil {
		return SendResult{}, fmt.Errorf("语料库未就绪")
	}
	// 单条消息只使用一个快照版本：不会出现"半新半旧"的匹配结果。
	verdict := snap.Analyze(text)

	msgID, err := s.st.InsertMessage(p.ID, text, verdict.NormalizedText, truncated, now)
	if err != nil {
		return SendResult{}, err
	}

	entry := StreamEntry{
		Kind:           "message",
		Seq:            s.nextSeq(),
		At:             now.UnixMilli(),
		PlayerID:       p.ID,
		PlayerName:     p.Name,
		Text:           text,
		NormalizedText: verdict.NormalizedText,
		Acrostic:       verdict.AcrosticView,
		Truncated:      truncated,
		Hits:           nonNilHits(verdict.Hits),
		HitCount:       len(verdict.Hits),
		Severity:       verdict.Severity,
		IsViolation:    verdict.IsViolation,
		Disposition:    string(punishment.DispositionAllow),
	}

	decision := punishment.Decision{Disposition: punishment.DispositionAllow, Severity: verdict.Severity}
	if verdict.IsViolation {
		base7d, err := s.st.CountViolationsSince(p.ID, now.Add(-violationWindow))
		if err != nil {
			return SendResult{}, err
		}
		baseLifetime, err := s.st.CountViolationsLifetime(p.ID)
		if err != nil {
			return SendResult{}, err
		}
		// 两个计数都包含本条。
		entry.Count7d = base7d + 1
		entry.Lifetime = baseLifetime + 1
		decision = punishment.Decide(verdict.Severity, entry.Count7d, entry.Lifetime)
	} else {
		if entry.Count7d, err = s.st.CountViolationsSince(p.ID, now.Add(-violationWindow)); err != nil {
			return SendResult{}, err
		}
		if entry.Lifetime, err = s.st.CountViolationsLifetime(p.ID); err != nil {
			return SendResult{}, err
		}
	}
	entry.Disposition = string(decision.Disposition)
	entry.Rung = decision.Rung
	entry.DurationMS = decision.Duration.Milliseconds()
	entry.Permanent = decision.Permanent

	if _, err := s.st.InsertJudgment(store.Judgment{
		MessageID:   msgID,
		PlayerID:    p.ID,
		Severity:    verdict.Severity,
		IsViolation: verdict.IsViolation,
		Disposition: string(decision.Disposition),
		Rung:        decision.Rung,
		DurationMS:  decision.Duration.Milliseconds(),
		Permanent:   decision.Permanent,
		HitCount:    len(verdict.Hits),
		HitsJSON:    store.MarshalHits(verdict.Hits),
	}, now); err != nil {
		return SendResult{}, err
	}

	if decision.Disposition == punishment.DispositionMute {
		mv, replaced, err := s.applyMuteLocked(p.ID, decision, now)
		if err != nil {
			return SendResult{}, err
		}
		entry.MuteStarted = mv
		entry.MuteReplaced = replaced
		if mv != nil {
			entry.MuteID = mv.ID
		}
	}

	s.appendStreamLocked(entry)
	s.emit(streamEvent{Type: "stream", Entry: entry})
	s.broadcastPlayersLocked()

	return SendResult{Entry: entry, Verdict: verdict}, nil
}

// applyMuteLocked 开启/替换禁言：每个玩家至多一条活跃禁言。
func (s *Service) applyMuteLocked(playerID int64, decision punishment.Decision, now time.Time) (*PlayerMute, bool, error) {
	var newExpiry *time.Time
	if !decision.Permanent {
		e := now.Add(decision.Duration)
		newExpiry = &e
	}

	existing, err := s.st.ActiveMute(playerID)
	if err != nil {
		return nil, false, err
	}
	replaced := false
	if existing != nil {
		// 取更严格者：max(剩余时间, 新时长)；任一方为永久则永久。
		if existing.ExpiresAt == nil || newExpiry == nil {
			newExpiry = nil
		} else if existing.ExpiresAt.After(*newExpiry) {
			e := *existing.ExpiresAt
			newExpiry = &e
		}
		if err := s.st.EndMute(existing.ID, store.EndReasonReplaced, now, ""); err != nil {
			return nil, false, err
		}
		replaced = true
	}

	m := store.Mute{PlayerID: playerID, StartedAt: now, ExpiresAt: newExpiry}
	id, err := s.st.CreateMute(m)
	if err != nil {
		return nil, false, err
	}
	m.ID = id

	s.cancelTimer(playerID)
	s.scheduleLocked(m)
	return muteView(m, now), replaced, nil
}

// ---------- 语料库 / 修订 / 试跑 ----------

// Corpus 返回当前语料库副本。
func (s *Service) Corpus() corpus.Corpus {
	snap := s.snap.Load()
	if snap == nil {
		return corpus.Corpus{}
	}
	return snap.Corpus()
}

// Revisions 返回修订列表（新到旧），带每条的前后差异。
func (s *Service) Revisions() ([]RevisionView, error) {
	revs, err := s.st.Revisions()
	if err != nil {
		return nil, err
	}
	views := make([]RevisionView, 0, len(revs))
	var prev corpus.Corpus
	for _, r := range revs {
		v := RevisionView{
			ID:         r.ID,
			CreatedAt:  r.CreatedAt,
			Note:       r.Note,
			Operations: r.Operations,
			Summary:    summarizeOps(r.Operations),
			Changes:    computeDiff(prev, r.Snapshot),
		}
		views = append(views, v)
		prev = r.Snapshot
	}
	for i, j := 0, len(views)-1; i < j; i, j = i+1, j-1 {
		views[i], views[j] = views[j], views[i]
	}
	return views, nil
}

// ValidateOps 供前端在编辑 after 后即时校验。
func (s *Service) ValidateOps(ops []corpus.Operation) []corpus.Problem {
	s.mu.Lock()
	defer s.mu.Unlock()
	return corpus.CheckOps(s.snap.Load().Corpus(), ops)
}

// CommitOps 提交一条修订。存在校验失败的操作时整次拒绝。
func (s *Service) CommitOps(ops []corpus.Operation, note string) (corpus.Corpus, []corpus.Problem, error) {
	if len(ops) == 0 {
		return corpus.Corpus{}, nil, fmt.Errorf("没有选中任何变更")
	}
	note = strings.TrimSpace(note)
	if note == "" {
		note = "手工调整语料库"
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	cur := s.snap.Load().Corpus()
	ops = corpus.EnrichBefore(cur, ops)
	next, problems, applied := corpus.ApplyChecked(cur, ops)
	if applied != len(ops) || hasProblem(problems) {
		return cur, problems, fmt.Errorf("存在未通过校验的变更，本次提交已拒绝")
	}
	snap, err := corpus.Build(next)
	if err != nil {
		return cur, nil, err
	}
	if _, err := s.st.InsertRevision(ops, next, note, s.now()); err != nil {
		return cur, nil, err
	}
	// 原子替换：提交后的下一条消息即按新语料库判定。
	s.snap.Store(snap)
	s.emitCorpusLocked()
	return next, problems, nil
}

// Rollback 把历史快照作为新的一次修订提交回去；历史修订不被删除或改写。
func (s *Service) Rollback(revisionID int64, note string) (corpus.Corpus, error) {
	note = strings.TrimSpace(note)
	if note == "" {
		return corpus.Corpus{}, fmt.Errorf("回滚必须填写原因")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	rev, ok, err := s.st.Revision(revisionID)
	if err != nil {
		return corpus.Corpus{}, err
	}
	if !ok {
		return corpus.Corpus{}, fmt.Errorf("修订 #%d 不存在", revisionID)
	}
	snap, err := corpus.Build(rev.Snapshot)
	if err != nil {
		return corpus.Corpus{}, err
	}
	op := corpus.Operation{
		Op:           corpus.OpRollback,
		Target:       corpus.TargetCorpus,
		FromRevision: revisionID,
		Reason:       note,
	}
	if _, err := s.st.InsertRevision([]corpus.Operation{op}, rev.Snapshot, note, s.now()); err != nil {
		return corpus.Corpus{}, err
	}
	s.snap.Store(snap)
	s.emitCorpusLocked()
	return rev.Snapshot, nil
}

func (s *Service) emitCorpusLocked() {
	views, err := s.Revisions()
	if err != nil {
		log.Printf("读取修订列表失败: %v", err)
		views = []RevisionView{}
	}
	s.emit(corpusEvent{Type: "corpus", Corpus: s.snap.Load().Corpus(), Revisions: views})
}

// DryRun 用草稿状态的语料库跑一遍，不写库、不产生判定。
func (s *Service) DryRun(playerID int64, rawText string, ops []corpus.Operation) (DryRunResult, error) {
	text, truncated := truncateRunes(strings.TrimSpace(rawText), MaxMessageRunes)
	if text == "" {
		return DryRunResult{}, fmt.Errorf("试跑文本不能为空")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	cur := s.snap.Load()
	target := cur
	source := "current"
	if len(ops) > 0 {
		next, _, applied := corpus.ApplyChecked(cur.Corpus(), ops)
		if applied > 0 {
			snap, err := corpus.Build(next)
			if err != nil {
				return DryRunResult{}, err
			}
			target = snap
			source = "draft"
		}
	}

	verdict := target.Analyze(text)
	now := s.now()

	res := DryRunResult{
		Input:          text,
		Truncated:      truncated,
		NormalizedText: verdict.NormalizedText,
		RawView:        verdict.RawView,
		AcrosticView:   verdict.AcrosticView,
		Hits:           nonNilHits(verdict.Hits),
		HitCount:       len(verdict.Hits),
		Severity:       verdict.Severity,
		IsViolation:    verdict.IsViolation,
		Disposition:    string(punishment.DispositionAllow),
		CorpusSource:   source,
	}
	if playerID > 0 {
		res.Count7d, _ = s.st.CountViolationsSince(playerID, now.Add(-violationWindow))
		res.Lifetime, _ = s.st.CountViolationsLifetime(playerID)
	}
	if verdict.IsViolation {
		res.Count7d++
		res.Lifetime++
		d := punishment.Decide(verdict.Severity, res.Count7d, res.Lifetime)
		res.Disposition = string(d.Disposition)
		res.Rung = d.Rung
		res.DurationMS = d.Duration.Milliseconds()
		res.Permanent = d.Permanent
	}
	return res, nil
}

// ---------- agent 草案 ----------

// Draft 让 agent 产出一份变更草案（不落库、不写语料库）。
func (s *Service) Draft(ctx context.Context, instruction string, includeSamples bool) (*Draft, error) {
	if !s.AgentEnabled() {
		return nil, fmt.Errorf("未配置 AGENT_API_KEY，请手工编辑语料库")
	}
	instruction = strings.TrimSpace(instruction)
	if instruction == "" {
		return nil, fmt.Errorf("请先说明你想让 agent 做什么")
	}

	var samples []drafter.Sample
	if includeSamples {
		var err error
		if samples, err = s.recentSamples(SampleLimit); err != nil {
			return nil, err
		}
	}

	cur := s.Corpus()
	ops, err := s.drafter.Draft(ctx, drafter.Request{
		Instruction: instruction,
		Corpus:      cur,
		Samples:     samples,
	})
	if err != nil {
		return nil, err
	}

	// 未附带样本时不带任何消息内容，草案也就不该引用样本。
	allowed := make(map[int64]bool, len(samples))
	for _, smp := range samples {
		allowed[smp.MessageID] = true
	}
	ops = corpus.EnrichBefore(cur, ops)
	for i := range ops {
		if !includeSamples {
			ops[i].Samples = nil
			continue
		}
		var kept []int64
		for _, id := range ops[i].Samples {
			if allowed[id] {
				kept = append(kept, id)
			}
		}
		ops[i].Samples = kept
	}

	return &Draft{
		Instruction:    instruction,
		IncludeSamples: includeSamples,
		Operations:     ops,
		Problems:       s.ValidateOps(ops),
		Samples:        samples,
		CreatedAt:      s.now(),
	}, nil
}

func (s *Service) recentSamples(limit int) ([]drafter.Sample, error) {
	msgs, err := s.st.RecentMessages(limit)
	if err != nil {
		return nil, err
	}
	out := make([]drafter.Sample, 0, len(msgs))
	for _, m := range msgs {
		var hits []moderation.Hit
		if m.HitsJSON != "" {
			_ = json.Unmarshal([]byte(m.HitsJSON), &hits)
		}
		out = append(out, drafter.Sample{
			MessageID:   m.ID,
			Player:      m.PlayerName,
			Text:        m.Text,
			Severity:    m.Severity,
			Disposition: m.Disposition,
			Hits:        hits,
		})
	}
	return out, nil
}

// ---------- 初始状态 ----------

// Bootstrap 组装新连接需要的全部状态。
func (s *Service) Bootstrap() (Bootstrap, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	players, err := s.playersLocked()
	if err != nil {
		return Bootstrap{}, err
	}
	revs, err := s.Revisions()
	if err != nil {
		return Bootstrap{}, err
	}
	return Bootstrap{
		Players:      players,
		Corpus:       s.snap.Load().Corpus(),
		Revisions:    revs,
		Stair:        Stair(),
		AgentEnabled: s.AgentEnabled(),
		AgentModel:   s.AgentModel(),
		AgentLog:     s.AgentLog(),
		Stream:       s.historyLocked(100),
	}, nil
}

// historyLocked 重建全局流：库里留存的消息判定 + 本进程内未落库的拒绝/解除记录。
// 这样重启后管理员仍能回看历史，试跑也能从历史消息里选一条。
func (s *Service) historyLocked(limit int) []StreamEntry {
	mem := make([]StreamEntry, 0, 8)
	for _, e := range s.stream {
		if e.Rejected || e.Kind == "lift" {
			mem = append(mem, e)
		}
	}

	msgs, err := s.st.RecentMessages(limit)
	if err != nil {
		log.Printf("读取历史消息失败: %v", err)
		return mem
	}

	violationTimes := make(map[int64][]time.Time, 4)
	entries := make([]StreamEntry, 0, len(msgs)+len(mem))
	for _, m := range msgs {
		var hits []moderation.Hit
		if m.HitsJSON != "" {
			_ = json.Unmarshal([]byte(m.HitsJSON), &hits)
		}
		e := StreamEntry{
			Kind:           "message",
			Seq:            -m.ID,
			At:             m.CreatedAt.UnixMilli(),
			PlayerID:       m.PlayerID,
			PlayerName:     m.PlayerName,
			Text:           m.Text,
			NormalizedText: m.NormalizedText,
			Truncated:      m.Truncated,
			Hits:           nonNilHits(hits),
			HitCount:       len(hits),
			Severity:       m.Severity,
			IsViolation:    m.Severity > 0,
			Disposition:    m.Disposition,
			Rung:           m.Rung,
			DurationMS:     m.DurationMS,
			Permanent:      m.Permanent,
		}
		if m.Severity > 0 {
			vt, ok := violationTimes[m.PlayerID]
			if !ok {
				if vt, err = s.st.ViolationTimes(m.PlayerID); err != nil {
					log.Printf("读取违规历史失败: %v", err)
				}
				violationTimes[m.PlayerID] = vt
			}
			e.Count7d, e.Lifetime = countsAt(vt, m.CreatedAt)
		}
		entries = append(entries, e)
	}

	entries = append(entries, mem...)
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].At < entries[j].At })
	if len(entries) > streamLimit {
		entries = entries[len(entries)-streamLimit:]
	}
	return entries
}

// countsAt 还原"当时"的滚动 7 天与终身违规次数（都包含那一条）。
func countsAt(times []time.Time, at time.Time) (count7d, lifetime int) {
	window := at.Add(-violationWindow)
	for _, t := range times {
		if t.After(at) {
			break
		}
		lifetime++
		if !t.Before(window) {
			count7d++
		}
	}
	return count7d, lifetime
}

// Stair 返回只读展示的当前阶梯。
func Stair() StairView {
	durations := make([]StairRung, 0, len(punishment.Durations))
	for i, d := range punishment.Durations {
		durations = append(durations, StairRung{
			Rung:       i,
			DurationMS: d.Milliseconds(),
			Label:      punishment.DurationLabel(d),
		})
	}
	return StairView{
		Durations:          durations,
		PermanentThreshold: punishment.PermanentLifetimeThreshold,
		Formula:            "rung = clamp(severity + 7天内第几次 - 2, 0, 3)；终身违规 ≥ 5 次则永久禁言",
	}
}

// ---------- 小工具 ----------

func nonNilHits(hits []moderation.Hit) []moderation.Hit {
	if hits == nil {
		return []moderation.Hit{}
	}
	return hits
}

func hasProblem(problems []corpus.Problem) bool {
	for _, p := range problems {
		if !p.OK() {
			return true
		}
	}
	return false
}

func summarizeOps(ops []corpus.Operation) string {
	if len(ops) == 0 {
		return "初始化语料库"
	}
	parts := make([]string, 0, len(ops))
	for i, op := range ops {
		if i >= 4 {
			parts = append(parts, fmt.Sprintf("…共 %d 条", len(ops)))
			break
		}
		parts = append(parts, op.Summary())
	}
	return strings.Join(parts, "；")
}

func computeDiff(before, after corpus.Corpus) []Change {
	var changes []Change

	beforeRules := make(map[string]moderation.Rule, len(before.Rules))
	for _, r := range before.Rules {
		beforeRules[r.ID] = r
	}
	for _, r := range after.Rules {
		old, ok := beforeRules[r.ID]
		if !ok {
			changes = append(changes, Change{Target: "rule", Op: "add", ID: r.ID, Detail: describeRule(r)})
			continue
		}
		delete(beforeRules, r.ID)
		if d := describeRuleChange(old, r); d != "" {
			changes = append(changes, Change{Target: "rule", Op: "update", ID: r.ID, Detail: d})
		}
	}
	for id, r := range beforeRules {
		changes = append(changes, Change{Target: "rule", Op: "delete", ID: id, Detail: describeRule(r)})
	}

	beforeMappings := make(map[string]moderation.Mapping, len(before.Mappings))
	for _, m := range before.Mappings {
		beforeMappings[m.ID] = m
	}
	for _, m := range after.Mappings {
		old, ok := beforeMappings[m.ID]
		if !ok {
			changes = append(changes, Change{Target: "mapping", Op: "add", ID: m.ID, Detail: m.From + " → " + m.To})
			continue
		}
		delete(beforeMappings, m.ID)
		if d := describeMappingChange(old, m); d != "" {
			changes = append(changes, Change{Target: "mapping", Op: "update", ID: m.ID, Detail: d})
		}
	}
	for id, m := range beforeMappings {
		changes = append(changes, Change{Target: "mapping", Op: "delete", ID: id, Detail: m.From + " → " + m.To})
	}
	return changes
}

func describeRule(r moderation.Rule) string {
	state := "启用"
	if !r.Enabled {
		state = "停用"
	}
	return fmt.Sprintf("L%d %s %s/%s(%s) %s", r.Severity, r.Description, r.View, r.Matcher, r.Pattern, state)
}

func describeRuleChange(before, after moderation.Rule) string {
	var parts []string
	if before.Description != after.Description {
		parts = append(parts, fmt.Sprintf("说明 %q → %q", before.Description, after.Description))
	}
	if before.Severity != after.Severity {
		parts = append(parts, fmt.Sprintf("severity %d → %d", before.Severity, after.Severity))
	}
	if before.View != after.View {
		parts = append(parts, fmt.Sprintf("view %s → %s", before.View, after.View))
	}
	if before.Matcher != after.Matcher {
		parts = append(parts, fmt.Sprintf("matcher %s → %s", before.Matcher, after.Matcher))
	}
	if before.Pattern != after.Pattern {
		parts = append(parts, fmt.Sprintf("pattern %q → %q", before.Pattern, after.Pattern))
	}
	if before.Enabled != after.Enabled {
		parts = append(parts, fmt.Sprintf("enabled %v → %v", before.Enabled, after.Enabled))
	}
	if len(before.Examples) != len(after.Examples) {
		parts = append(parts, fmt.Sprintf("examples %d 条 → %d 条", len(before.Examples), len(after.Examples)))
	}
	return strings.Join(parts, "，")
}

func describeMappingChange(before, after moderation.Mapping) string {
	var parts []string
	if before.From != after.From {
		parts = append(parts, fmt.Sprintf("from %q → %q", before.From, after.From))
	}
	if before.To != after.To {
		parts = append(parts, fmt.Sprintf("to %q → %q", before.To, after.To))
	}
	if before.Note != after.Note {
		parts = append(parts, fmt.Sprintf("note %q → %q", before.Note, after.Note))
	}
	return strings.Join(parts, "，")
}

func truncateRunes(s string, limit int) (string, bool) {
	if utf8.RuneCountInString(s) <= limit {
		return s, false
	}
	runes := []rune(s)
	return string(runes[:limit]), true
}

// SeedCorpus 是首次启动写入的语料库：几条人看得懂的规则 + 两条火星文映射。
func SeedCorpus() corpus.Corpus {
	return corpus.Corpus{
		Mappings: []moderation.Mapping{
			{ID: "m01", From: "亻尔", To: "你", Note: "火星文"},
			{ID: "m02", From: "尔", To: "你", Note: "形近字"},
			{ID: "m03", From: "嘚", To: "的", Note: "火星文"},
			{ID: "m04", From: "皒", To: "我", Note: "火星文"},
			{ID: "m05", From: "彳艮", To: "很", Note: "火星文"},
		},
		Rules: []moderation.Rule{
			{
				ID:          "r001",
				Description: "拉人加微信的广告",
				View:        moderation.ViewRaw,
				Matcher:     moderation.MatcherLiteral,
				Pattern:     "加微信",
				Severity:    2,
				Enabled:     true,
				Examples:    []string{"低价代充，加微信详谈"},
			},
			{
				ID:          "r002",
				Description: "直接辱骂",
				View:        moderation.ViewRaw,
				Matcher:     moderation.MatcherLiteral,
				Pattern:     "傻逼",
				Severity:    3,
				Enabled:     true,
				Examples:    []string{"你是不是傻逼"},
			},
			{
				ID:          "r003",
				Description: "正文里的手机号",
				View:        moderation.ViewRaw,
				Matcher:     moderation.MatcherRegex,
				Pattern:     `1[3-9]\d{9}`,
				Severity:    2,
				Enabled:     true,
				Examples:    []string{"有事打我电话13800138000"},
			},
			{
				ID:          "r004",
				Description: "藏头辱骂",
				View:        moderation.ViewAcrostic,
				Matcher:     moderation.MatcherLiteral,
				Pattern:     "你是傻逼",
				Severity:    4,
				Enabled:     true,
				Examples:    []string{"你真行\n是个人才\n傻得可爱\n逼样百出"},
			},
			{
				ID:          "r005",
				Description: "代充广告",
				View:        moderation.ViewRaw,
				Matcher:     moderation.MatcherLiteral,
				Pattern:     "代充",
				Severity:    2,
				Enabled:     true,
				Examples:    []string{"低价代充，秒到账"},
			},
		},
	}
}
