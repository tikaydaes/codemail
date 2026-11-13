package sqlstore

import (
	"context"
	"time"

	"github.com/tikaydaes/codemail/internal/store"
)

const sourceCols = `id, user_id, address, kind, codes_only, host, username, secret, state, last_checked, last_error, created_at`

func scanSource(row interface{ Scan(...any) error }) (store.Source, error) {
	var src store.Source
	var checked, created int64
	err := row.Scan(&src.ID, &src.UserID, &src.Address, &src.Kind, &src.CodesOnly, &src.Host, &src.Username,
		&src.Secret, &src.State, &checked, &src.LastError, &created)
	src.LastChecked, src.CreatedAt = tm(checked), tm(created)
	return src, one(err)
}

func (s *DB) sources(ctx context.Context, where string, args ...any) ([]store.Source, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+sourceCols+` FROM sources `+where+` ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Source
	for rows.Next() {
		src, err := scanSource(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, src)
	}
	return out, rows.Err()
}

func (s *DB) AddSource(ctx context.Context, src *store.Source) error {
	var n int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sources WHERE user_id = ?`, src.UserID).Scan(&n); err != nil {
		return err
	}
	if n >= store.MaxSources {
		return store.ErrLimit
	}
	src.CreatedAt = time.Now()
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO sources (user_id, address, kind, codes_only, host, username, secret, state, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		src.UserID, src.Address, src.Kind, src.CodesOnly, src.Host, src.Username, src.Secret, src.State, ts(src.CreatedAt)).Scan(&src.ID)
	return taken(err)
}

func (s *DB) ListSources(ctx context.Context, userID int64) ([]store.Source, error) {
	return s.sources(ctx, `WHERE user_id = ?`, userID)
}

func (s *DB) PolledSources(ctx context.Context) ([]store.Source, error) {
	return s.sources(ctx, `WHERE kind <> ?`, store.SourceForward)
}

func (s *DB) GetSource(ctx context.Context, userID, id int64) (store.Source, error) {
	return scanSource(s.db.QueryRowContext(ctx, `SELECT `+sourceCols+` FROM sources WHERE id = ? AND user_id = ?`, id, userID))
}

func (s *DB) UpdateSource(ctx context.Context, src store.Source) error {
	return changed(s.db.ExecContext(ctx, `UPDATE sources SET codes_only = ?, host = ?, username = ?, secret = ? WHERE id = ? AND user_id = ?`,
		src.CodesOnly, src.Host, src.Username, src.Secret, src.ID, src.UserID))
}

func (s *DB) SetSourceState(ctx context.Context, id int64, state string, checked time.Time, lastError string) error {
	return changed(s.db.ExecContext(ctx, `UPDATE sources SET state = ?, last_checked = ?, last_error = ? WHERE id = ?`,
		state, ts(checked), lastError, id))
}

func (s *DB) DeleteSource(ctx context.Context, userID, id int64) error {
	return changed(s.db.ExecContext(ctx, `DELETE FROM sources WHERE id = ? AND user_id = ?`, id, userID))
}
