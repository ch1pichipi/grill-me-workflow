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
	Type  string `json:"type"`
	Draft *Draft `json:"draft"`
}

// draftStartedEvent 让界面在等待模型（动辄十几秒）时能立刻显示"正在运行"。
type draftStartedEvent struct {
	Type           string    `json:"type"`
	Instruction    string    `json:"instruction"`
	IncludeSamples bool      `json:"includeSamples"`
	Model          string    `json:"model"`
	StartedAt      time.Time `json:"startedAt"`
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
	Type     string           `json:"type"`
	Message  string           `json:"message"`
	Problems []corpus.Problem `json:"problems,omitempty"`
	// Source 标明错误来自哪条链路（例如 draft），界面据此把错误显示在 agent 区。
	Source string `json:"source,omitempty"`
}
