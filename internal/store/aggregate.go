package store

import "time"

// 这个文件是"整份玩家列表"的批量读法：一次查询算出所有玩家的值，
// 成本与玩家数无关。逐玩家查询（ActiveMute / CountViolationsSince /
// CountViolationsLifetime）仍然保留，因为它们服务的是单玩家决策路径，
// 那里要的是精确的一个人，而不是一份列表（ADR 0004）。

// ActiveMutesByPlayer 返回全部仍活跃（ended_at IS NULL）的禁言，按 player_id 索引。
// 与 ActiveMute 的约定一致：到期与否不在 store 判断，交给调用方惰性判定。
func (s *Store) ActiveMutesByPlayer() (map[int64]Mute, error) {
	s.countQuery()
	rows, err := s.db.Query(
		`SELECT id, player_id, started_at, expires_at, ended_at,
		        COALESCE(end_reason,''), COALESCE(lift_note,'')
		 FROM mutes WHERE ended_at IS NULL ORDER BY id`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[int64]Mute)
	for rows.Next() {
		m, err := scanMute(rows)
		if err != nil {
			return nil, err
		}
		out[m.PlayerID] = m
	}
	return out, rows.Err()
}

// ViolationCountsSinceByPlayer 一次算出所有玩家在 since 之后的违规次数。
// 没有违规的玩家不出现在返回的 map 里（零值即正确值）。
func (s *Store) ViolationCountsSinceByPlayer(since time.Time) (map[int64]int, error) {
	return s.violationCountsByPlayer(
		`SELECT player_id, count(*) FROM judgments
		 WHERE is_violation = 1 AND created_at >= ?
		 GROUP BY player_id`,
		toMillis(since),
	)
}

// ViolationCountsLifetimeByPlayer 一次算出所有玩家的终身违规次数。
func (s *Store) ViolationCountsLifetimeByPlayer() (map[int64]int, error) {
	return s.violationCountsByPlayer(
		`SELECT player_id, count(*) FROM judgments
		 WHERE is_violation = 1
		 GROUP BY player_id`,
	)
}

func (s *Store) violationCountsByPlayer(query string, args ...any) (map[int64]int, error) {
	s.countQuery()
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := make(map[int64]int)
	for rows.Next() {
		var playerID int64
		var n int
		if err := rows.Scan(&playerID, &n); err != nil {
			return nil, err
		}
		out[playerID] = n
	}
	return out, rows.Err()
}
