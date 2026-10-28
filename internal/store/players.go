package store

import (
	"database/sql"
	"strings"
	"time"
)

// CreatePlayer 创建玩家。名字在未删除玩家范围内唯一。
func (s *Store) CreatePlayer(name string, at time.Time) (Player, error) {
	name = strings.TrimSpace(name)
	res, err := s.db.Exec(
		`INSERT INTO players (name, created_at, deleted_at) VALUES (?, ?, NULL)`,
		name, toMillis(at),
	)
	if err != nil {
		if isUniqueViolation(err) {
			return Player{}, ErrDuplicateName
		}
		return Player{}, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return Player{}, err
	}
	return Player{ID: id, Name: name, CreatedAt: at.UTC()}, nil
}

// RenamePlayer 改名。历史判定与禁言按 id 归属，不受影响。
func (s *Store) RenamePlayer(id int64, name string) (Player, error) {
	name = strings.TrimSpace(name)
	res, err := s.db.Exec(
		`UPDATE players SET name = ? WHERE id = ? AND deleted_at IS NULL`,
		name, id,
	)
	if err != nil {
		if isUniqueViolation(err) {
			return Player{}, ErrDuplicateName
		}
		return Player{}, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return Player{}, ErrNotFound
	}
	p, ok, err := s.Player(id)
	if err != nil {
		return Player{}, err
	}
	if !ok {
		return Player{}, ErrNotFound
	}
	return p, nil
}

// SoftDeletePlayer 软删除：从列表与切换框消失，历史全部保留。
func (s *Store) SoftDeletePlayer(id int64, at time.Time) error {
	res, err := s.db.Exec(
		`UPDATE players SET deleted_at = ? WHERE id = ? AND deleted_at IS NULL`,
		toMillis(at), id,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return ErrNotFound
	}
	return nil
}

// Player 按 id 读玩家（包含已软删除的，用于历史查询）。
func (s *Store) Player(id int64) (Player, bool, error) {
	row := s.db.QueryRow(`SELECT id, name, created_at, deleted_at FROM players WHERE id = ?`, id)
	return scanPlayer(row)
}

// Players 列出未删除玩家。
func (s *Store) Players() ([]Player, error) {
	s.countQuery()
	rows, err := s.db.Query(
		`SELECT id, name, created_at, deleted_at FROM players WHERE deleted_at IS NULL ORDER BY id`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Player
	for rows.Next() {
		p, ok, err := scanPlayer(rows)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, p)
		}
	}
	return out, rows.Err()
}

type scanner interface {
	Scan(dest ...any) error
}

func scanPlayer(sc scanner) (Player, bool, error) {
	var (
		p       Player
		created int64
		deleted sql.NullInt64
	)
	if err := sc.Scan(&p.ID, &p.Name, &created, &deleted); err != nil {
		if err == sql.ErrNoRows {
			return Player{}, false, nil
		}
		return Player{}, false, err
	}
	p.CreatedAt = fromMillis(created)
	p.DeletedAt = nullMillis(deleted)
	return p, true, nil
}
