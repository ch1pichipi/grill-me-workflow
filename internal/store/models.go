package store

import (
	"database/sql"
	"fmt"
	"time"

	"chatmod/internal/corpus"
)

// 禁言结束原因。
const (
	EndReasonExpired  = "expired"
	EndReasonLifted   = "lifted"
	EndReasonReplaced = "replaced"
)

// Player 是一名玩家。删除是软删除：历史消息、判定、禁言全部保留。
type Player struct {
	ID        int64      `json:"id"`
	Name      string     `json:"name"`
	CreatedAt time.Time  `json:"createdAt"`
	DeletedAt *time.Time `json:"deletedAt,omitempty"`
}

// Mute 是一段禁言。ExpiresAt 为 nil 表示永久禁言；EndedAt 为 nil 表示仍活跃。
type Mute struct {
	ID         int64      `json:"id"`
	PlayerID   int64      `json:"playerId"`
	StartedAt  time.Time  `json:"startedAt"`
	ExpiresAt  *time.Time `json:"expiresAt"`
	EndedAt    *time.Time `json:"endedAt,omitempty"`
	EndReason  string     `json:"endReason,omitempty"`
	LiftNote   string     `json:"liftNote,omitempty"`
	FromPlayer int64      `json:"-"`
}

// Permanent 报告这段禁言是否没有到期时间。
func (m Mute) Permanent() bool { return m.ExpiresAt == nil }

// Message 是一条入库的聊天消息原文（≤2000 字符）与归一化文本。
type Message struct {
	ID             int64     `json:"id"`
	PlayerID       int64     `json:"playerId"`
	Text           string    `json:"text"`
	NormalizedText string    `json:"normalizedText"`
	Truncated      bool      `json:"truncated"`
	CreatedAt      time.Time `json:"createdAt"`
}

// Judgment 是一条不可变判定：规则快照在这里，而不是引用当前语料库（ADR 0003）。
type Judgment struct {
	ID          int64     `json:"id"`
	MessageID   int64     `json:"messageId"`
	PlayerID    int64     `json:"playerId"`
	Severity    int       `json:"severity"`
	IsViolation bool      `json:"isViolation"`
	Disposition string    `json:"disposition"`
	Rung        int       `json:"rung"`
	DurationMS  int64     `json:"durationMs"`
	Permanent   bool      `json:"permanent"`
	HitCount    int       `json:"hitCount"`
	HitsJSON    string    `json:"-"`
	CreatedAt   time.Time `json:"createdAt"`
}

// Revision 是一次被提交的语料库变更：操作列表 + 提交后的完整快照。
type Revision struct {
	ID         int64              `json:"id"`
	Operations []corpus.Operation `json:"operations"`
	Snapshot   corpus.Corpus      `json:"snapshot"`
	Note       string             `json:"note"`
	CreatedAt  time.Time          `json:"createdAt"`
}

// RecentMessage 是"最近 N 条消息 + 判定"，用于 agent 样本与试跑选条。
type RecentMessage struct {
	ID             int64     `json:"id"`
	PlayerID       int64     `json:"playerId"`
	PlayerName     string    `json:"playerName"`
	Text           string    `json:"text"`
	NormalizedText string    `json:"normalizedText"`
	Truncated      bool      `json:"truncated"`
	Severity       int       `json:"severity"`
	Disposition    string    `json:"disposition"`
	Rung           int       `json:"rung"`
	DurationMS     int64     `json:"durationMs"`
	Permanent      bool      `json:"permanent"`
	HitsJSON       string    `json:"-"`
	CreatedAt      time.Time `json:"createdAt"`
}

func toMillis(t time.Time) int64 { return t.UTC().UnixMilli() }

func fromMillis(v int64) time.Time { return time.UnixMilli(v).UTC() }

func nullMillis(v sql.NullInt64) *time.Time {
	if !v.Valid {
		return nil
	}
	t := fromMillis(v.Int64)
	return &t
}

func nullableMillis(t *time.Time) sql.NullInt64 {
	if t == nil {
		return sql.NullInt64{}
	}
	return sql.NullInt64{Int64: toMillis(*t), Valid: true}
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// ErrDuplicateName 表示未删除玩家范围内名字冲突。
var ErrDuplicateName = fmt.Errorf("玩家名字已存在")

// ErrNotFound 表示目标不存在（或已被软删除）。
var ErrNotFound = fmt.Errorf("未找到")
