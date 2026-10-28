package store

import (
	"database/sql"
	"encoding/json"
	"time"
)

// InsertMessage 落库一条消息。text 由调用方截断到 2000 字符。
func (s *Store) InsertMessage(playerID int64, text, normalized string, truncated bool, at time.Time) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO messages (player_id, text, normalized_text, truncated, created_at)
		 VALUES (?, ?, ?, ?, ?)`,
		playerID, text, normalized, boolInt(truncated), toMillis(at),
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// InsertJudgment 追加一条不可变判定。
func (s *Store) InsertJudgment(j Judgment, at time.Time) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO judgments
		   (message_id, player_id, severity, is_violation, disposition, rung, duration_ms, permanent, hit_count, hits, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		j.MessageID, j.PlayerID, j.Severity, boolInt(j.IsViolation), j.Disposition,
		j.Rung, j.DurationMS, boolInt(j.Permanent), j.HitCount, j.HitsJSON, toMillis(at),
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// CountViolationsSince 现算滚动窗口内的违规次数（只统计 is_violation）。
// 7 天计数不存冗余列：违规历史只有一个事实来源——判定表（技术选型 §15）。
func (s *Store) CountViolationsSince(playerID int64, since time.Time) (int, error) {
	s.countQuery()
	var n int
	err := s.db.QueryRow(
		`SELECT count(*) FROM judgments
		 WHERE player_id = ? AND is_violation = 1 AND created_at >= ?`,
		playerID, toMillis(since),
	).Scan(&n)
	return n, err
}

// CountViolationsLifetime 统计终身违规次数。
func (s *Store) CountViolationsLifetime(playerID int64) (int, error) {
	s.countQuery()
	var n int
	err := s.db.QueryRow(
		`SELECT count(*) FROM judgments WHERE player_id = ? AND is_violation = 1`,
		playerID,
	).Scan(&n)
	return n, err
}

// RecentMessages 取最近 limit 条消息及其判定（老到新）。
func (s *Store) RecentMessages(limit int) ([]RecentMessage, error) {
	rows, err := s.db.Query(
		`SELECT m.id, m.player_id, p.name, m.text, m.normalized_text, m.truncated, m.created_at,
		        COALESCE(j.severity, 0), COALESCE(j.disposition, ''), COALESCE(j.rung, 0),
		        COALESCE(j.duration_ms, 0), COALESCE(j.permanent, 0), COALESCE(j.hits, '[]')
		 FROM messages m
		 JOIN players p ON p.id = m.player_id
		 LEFT JOIN judgments j ON j.message_id = m.id
		 ORDER BY m.id DESC LIMIT ?`,
		limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []RecentMessage
	for rows.Next() {
		var (
			rm       RecentMessage
			created  int64
			truncInt int
			permInt  int
		)
		if err := rows.Scan(&rm.ID, &rm.PlayerID, &rm.PlayerName, &rm.Text, &rm.NormalizedText,
			&truncInt, &created, &rm.Severity, &rm.Disposition, &rm.Rung,
			&rm.DurationMS, &permInt, &rm.HitsJSON); err != nil {
			return nil, err
		}
		rm.Truncated = truncInt != 0
		rm.Permanent = permInt != 0
		rm.CreatedAt = fromMillis(created)
		out = append(out, rm)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	// 反转为老到新，方便展示。
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

// MutesForPlayer 返回某玩家的全部禁言记录（新到旧），用于历史回溯。
func (s *Store) MutesForPlayer(playerID int64) ([]Mute, error) {
	rows, err := s.db.Query(
		`SELECT id, player_id, started_at, expires_at, ended_at, COALESCE(end_reason,''), COALESCE(lift_note,'')
		 FROM mutes WHERE player_id = ? ORDER BY id DESC`,
		playerID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Mute
	for rows.Next() {
		m, err := scanMute(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func scanMute(sc scanner) (Mute, error) {
	var (
		m              Mute
		started        int64
		expires, ended sql.NullInt64
	)
	if err := sc.Scan(&m.ID, &m.PlayerID, &started, &expires, &ended, &m.EndReason, &m.LiftNote); err != nil {
		return Mute{}, err
	}
	m.StartedAt = fromMillis(started)
	m.ExpiresAt = nullMillis(expires)
	m.EndedAt = nullMillis(ended)
	return m, nil
}

// MarshalHits 把规则快照编码成 JSON 存进判定行。
func MarshalHits(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "[]"
	}
	return string(b)
}

// ViolationTimes 返回某玩家全部违规判定的时间（升序），用于重建历史流时
// 还原"当时"的 7 天/终身计数。
func (s *Store) ViolationTimes(playerID int64) ([]time.Time, error) {
	rows, err := s.db.Query(
		`SELECT created_at FROM judgments
		 WHERE player_id = ? AND is_violation = 1 ORDER BY created_at`,
		playerID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []time.Time
	for rows.Next() {
		var ms int64
		if err := rows.Scan(&ms); err != nil {
			return nil, err
		}
		out = append(out, fromMillis(ms))
	}
	return out, rows.Err()
}
