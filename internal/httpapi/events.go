package httpapi

import (
	"time"

	"chatmod/internal/corpus"
	"chatmod/internal/drafter"
)

// 服务端 → 客户端事件的信封。所有实时变化都通过这几类事件推送，
// 页面上没有需要刷新才能看到的变化。

type bootstrapEvent struct {
	Type string `json:"type"`
	Bootstrap
}

type playersEvent struct {
	Type    string       `json:"type"`
	Players []PlayerView `json:"players"`
}

// playersChangedEvent 只带发生变动的那几行，客户端按 id 合并。
// 消息热路径只发它：一条消息不该让每个客户端重收一份完整玩家列表（ADR 0004）。
// 它不带删除语义——玩家增删改仍然走整份 players。
type playersChangedEvent struct {
	Type    string       `json:"type"`
	Players []PlayerView `json:"players"`
}

type muteEvent struct {
	Type      string      `json:"type"`
	PlayerID  int64       `json:"playerId"`
	Mute      *PlayerMute `json:"mute"`
	EndReason string      `json:"endReason,omitempty"`
}

type streamEvent struct {
	Type  string      `json:"type"`
	Entry StreamEntry `json:"entry"`
}

type resultEvent struct {
	Type   string     `json:"type"`
	Result SendResult `json:"result"`
}

type corpusEvent struct {
	Type      string         `json:"type"`
	Corpus    corpus.Corpus  `json:"corpus"`
	Revisions []RevisionView `json:"revisions"`
}

type draftEvent struct {
	Type      string `json:"type"`
	RequestID string `json:"requestId,omitempty"`
	Draft     *Draft `json:"draft"`
}

// draftStartedEvent 让界面在等待模型（动辄十几秒）时能立刻显示"正在运行"。
// RequestID 用于把取消、失败与结果对回同一次请求。
type draftStartedEvent struct {
	Type           string    `json:"type"`
	RequestID      string    `json:"requestId,omitempty"`
	Instruction    string    `json:"instruction"`
	IncludeSamples bool      `json:"includeSamples"`
	Model          string    `json:"model"`
	StartedAt      time.Time `json:"startedAt"`
}

// draftBusyEvent 表示服务端已经有一次生成在途：不会再次调用模型。
// 界面据此把"生成中"状态恢复成那条在途请求，而不是把按钮放回可点。
type draftBusyEvent struct {
	Type           string    `json:"type"`
	RequestID      string    `json:"requestId,omitempty"`
	Instruction    string    `json:"instruction"`
	IncludeSamples bool      `json:"includeSamples"`
	Model          string    `json:"model"`
	StartedAt      time.Time `json:"startedAt"`
	ElapsedMS      int64     `json:"elapsedMs"`
}

// draftCancelledEvent 确认服务端已中止在途的模型请求（请求上下文被取消）。
type draftCancelledEvent struct {
	Type      string `json:"type"`
	RequestID string `json:"requestId,omitempty"`
	Note      string `json:"note,omitempty"`
}

// agentLogEvent 推送一次 agent 往返（请求体/响应体），供界面上的排障日志使用。
type agentLogEvent struct {
	Type     string           `json:"type"`
	Exchange drafter.Exchange `json:"exchange"`
}

type draftProblemsEvent struct {
	Type     string           `json:"type"`
	Problems []corpus.Problem `json:"problems"`
}

type dryRunEvent struct {
	Type   string       `json:"type"`
	Result DryRunResult `json:"result"`
}

type ackEvent struct {
	Type     string `json:"type"`
	PlayerID int64  `json:"playerId,omitempty"`
	Note     string `json:"note,omitempty"`
}

type errorEvent struct {
	Type      string           `json:"type"`
	Message   string           `json:"message"`
	RequestID string           `json:"requestId,omitempty"`
	Problems  []corpus.Problem `json:"problems,omitempty"`
	// Source 标明错误来自哪条链路（例如 draft），界面据此把错误显示在 agent 区。
	Source string `json:"source,omitempty"`
}
