package httpapi

import "chatmod/internal/corpus"

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
}
