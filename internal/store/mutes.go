package store

import (
	"database/sql"
	"time"
)

// ActiveMute 返回玩家当前活跃的禁言（ended_at IS NULL）。至多一条。
// 到期与否由调用方按惰性判定比较时间，这里不隐式修改数据。
func (s *Store) ActiveMute(playerID int64) (*Mute, error) {
	s.countQuery()
	row := s.db.QueryRow(
		`SELECT id, player_id, started_at, expires_at, ended_at, COALESCE(end_reason,''), COALESCE(lift_note,'')
		 FROM mutes WHERE player_id = ? AND ended_at IS NULL ORDER BY id DESC LIMIT 1`,
		playerID,
	)
	m, err := scanMute(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// ActiveMutes 返回全部活跃禁言，启动时用于恢复到期推送的定时器。
func (s *Store) ActiveMutes() ([]Mute, error) {
	s.countQuery()
	rows, err := s.db.Query(
		`SELECT id, player_id, started_at, expires_at, ended_at, COALESCE(end_reason,''), COALESCE(lift_note,'')
		 FROM mutes WHERE ended_at IS NULL ORDER BY id`,
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

// CreateMute 开启一段禁言并返回 id。
func (s *Store) CreateMute(m Mute) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO mutes (player_id, started_at, expires_at, ended_at, end_reason, lift_note)
		 VALUES (?, ?, ?, NULL, NULL, NULL)`,
		m.PlayerID, toMillis(m.StartedAt), nullableMillis(m.ExpiresAt),
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// EndMute 结束一段禁言。reason 取 expired / lifted / replaced。
func (s *Store) EndMute(id int64, reason string, at time.Time, note string) error {
	_, err := s.db.Exec(
		`UPDATE mutes SET ended_at = ?, end_reason = ?, lift_note = ?
		 WHERE id = ? AND ended_at IS NULL`,
		toMillis(at), reason, note, id,
	)
	return err
}
