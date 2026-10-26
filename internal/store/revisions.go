package store

import (
	"database/sql"
	"encoding/json"
	"time"

	"chatmod/internal/corpus"
)

// InsertRevision 追加一条修订：本次操作列表 + 提交后的完整语料库快照。
// 修订只追加，历史不被删除或改写（技术选型 §21）。
func (s *Store) InsertRevision(ops []corpus.Operation, snap corpus.Corpus, note string, at time.Time) (int64, error) {
	if ops == nil {
		ops = []corpus.Operation{}
	}
	opsJSON, err := json.Marshal(ops)
	if err != nil {
		return 0, err
	}
	snapJSON, err := json.Marshal(snap)
	if err != nil {
		return 0, err
	}
	res, err := s.db.Exec(
		`INSERT INTO revisions (operations, snapshot, note, created_at) VALUES (?, ?, ?, ?)`,
		string(opsJSON), string(snapJSON), note, toMillis(at),
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// Revisions 按提交顺序返回全部修订。
func (s *Store) Revisions() ([]Revision, error) {
	return s.queryRevisions(
		`SELECT id, operations, snapshot, note, created_at FROM revisions ORDER BY id`,
	)
}

// Revision 按 id 读一条修订。
func (s *Store) Revision(id int64) (Revision, bool, error) {
	list, err := s.queryRevisions(
		`SELECT id, operations, snapshot, note, created_at FROM revisions WHERE id = ? ORDER BY id`, id,
	)
	if err != nil {
		return Revision{}, false, err
	}
	if len(list) == 0 {
		return Revision{}, false, nil
	}
	return list[0], true, nil
}

// LatestRevision 返回最新一条修订。
func (s *Store) LatestRevision() (Revision, bool, error) {
	list, err := s.queryRevisions(
		`SELECT id, operations, snapshot, note, created_at FROM revisions ORDER BY id DESC LIMIT 1`,
	)
	if err != nil {
		return Revision{}, false, err
	}
	if len(list) == 0 {
		return Revision{}, false, nil
	}
	return list[0], true, nil
}

func (s *Store) queryRevisions(query string, args ...any) ([]Revision, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Revision
	for rows.Next() {
		var (
			rev      Revision
			opsJSON  string
			snapJSON string
			created  int64
		)
		if err := rows.Scan(&rev.ID, &opsJSON, &snapJSON, &rev.Note, &created); err != nil {
			return nil, err
		}
		rev.CreatedAt = fromMillis(created)
		rev.Operations = []corpus.Operation{}
		if err := json.Unmarshal([]byte(opsJSON), &rev.Operations); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(snapJSON), &rev.Snapshot); err != nil {
			return nil, err
		}
		out = append(out, rev)
	}
	return out, rows.Err()
}

// CountRevisions 用于启动时判断是否需要写入种子语料库。
func (s *Store) CountRevisions() (int, error) {
	var n int
	err := s.db.QueryRow(`SELECT count(*) FROM revisions`).Scan(&n)
	if err != nil && err != sql.ErrNoRows {
		return 0, err
	}
	return n, nil
}
