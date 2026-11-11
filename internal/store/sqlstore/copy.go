package sqlstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// copyTables lists every table in an order that satisfies foreign keys,
// with its boolean columns (SQLite hands those back as integers) and
// whether it has a generated id whose sequence must be advanced.
var copyTables = []struct {
	name  string
	bools []string
	hasID bool
}{
	{"users", []string{"is_admin"}, true},
	{"passkeys", nil, true},
	{"recovery_codes", nil, false},
	{"sessions", nil, true},
	{"devices", nil, true},
	{"grants", []string{"confirmed"}, false},
	{"invites", nil, false},
	{"domains", []string{"verified", "allow_mailbox", "allow_aliases", "allow_subdomains"}, true},
	{"aliases", []string{"generated"}, true},
	{"messages", nil, true},
	{"corrections", nil, true},
	{"site_links", nil, false},
	{"settings", nil, false},
	{"audit_log", nil, true},
}

// Copy copies everything from src into dst, which must be empty. It is
// how a deployment moves between SQLite and PostgreSQL. Nothing is
// written unless the whole copy succeeds.
func Copy(ctx context.Context, src, dst *DB) error {
	for _, t := range []string{"users", "domains", "messages"} {
		var n int
		if err := dst.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+t).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return errors.New("the destination database already has data; copy into an empty one")
		}
	}
	tx, err := dst.db.raw.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	// Clear what a fresh database creates for itself (the setup token).
	if _, err := tx.ExecContext(ctx, `DELETE FROM settings`); err != nil {
		return err
	}
	for _, t := range copyTables {
		rows, err := src.db.QueryContext(ctx, `SELECT * FROM `+t.name)
		if err != nil {
			return fmt.Errorf("%s: %w", t.name, err)
		}
		cols, err := rows.Columns()
		if err != nil {
			rows.Close()
			return err
		}
		isBool := make([]bool, len(cols))
		for i, c := range cols {
			for _, b := range t.bools {
				isBool[i] = isBool[i] || c == b
			}
		}
		marks := strings.TrimSuffix(strings.Repeat("?, ", len(cols)), ", ")
		insert := dst.db.rebind(fmt.Sprintf(`INSERT INTO %s (%s) VALUES (%s)`, t.name, strings.Join(cols, ", "), marks))
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := rows.Scan(ptrs...); err != nil {
				rows.Close()
				return err
			}
			for i, v := range vals {
				if n, isInt := v.(int64); isInt && isBool[i] {
					vals[i] = n != 0
				}
			}
			if _, err := tx.ExecContext(ctx, insert, vals...); err != nil {
				rows.Close()
				return fmt.Errorf("%s: %w", t.name, err)
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}
		if dst.pg && t.hasID {
			// Copied rows keep their IDs, so the generator must skip past them.
			q := fmt.Sprintf(`SELECT setval(pg_get_serial_sequence('%s', 'id'), GREATEST((SELECT COALESCE(MAX(id), 0) FROM %s), 1))`, t.name, t.name)
			if _, err := tx.ExecContext(ctx, q); err != nil {
				return fmt.Errorf("%s sequence: %w", t.name, err)
			}
		}
	}
	return tx.Commit()
}

// Backup writes a consistent copy of a SQLite database to a new file
// while the server keeps running.
func (s *DB) Backup(ctx context.Context, path string) error {
	if s.pg {
		return errors.New("this is a PostgreSQL database; back it up with pg_dump")
	}
	_, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, path)
	return err
}
