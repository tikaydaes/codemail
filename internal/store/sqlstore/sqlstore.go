// Package sqlstore implements store.Store on embedded SQLite. Queries use
// portable SQL (integer timestamps and flags, RETURNING for new IDs) so a
// PostgreSQL backend can share them.
package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver

	"github.com/tikaydaes/codemail/internal/store"
)

type DB struct {
	db   *sql.DB
	path string
}

var _ store.Store = (*DB)(nil)

// migrations are applied in order; PRAGMA user_version records progress.
var migrations = []string{`
CREATE TABLE users (
	id         INTEGER PRIMARY KEY,
	username   TEXT NOT NULL UNIQUE,
	handle     BLOB NOT NULL UNIQUE,
	is_admin   INTEGER NOT NULL DEFAULT 0,
	retention  INTEGER NOT NULL DEFAULT 0,
	created_at INTEGER NOT NULL
);
CREATE TABLE passkeys (
	id         INTEGER PRIMARY KEY,
	user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	cred_id    BLOB NOT NULL UNIQUE,
	data       BLOB NOT NULL,
	name       TEXT NOT NULL,
	created_at INTEGER NOT NULL,
	last_used  INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE recovery_codes (
	user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	hash    BLOB NOT NULL,
	PRIMARY KEY (user_id, hash)
);
CREATE TABLE sessions (
	hash       BLOB PRIMARY KEY,
	user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	expires_at INTEGER NOT NULL
);
CREATE TABLE devices (
	id             INTEGER PRIMARY KEY,
	user_id        INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	name           TEXT NOT NULL,
	platform       TEXT NOT NULL,
	access_hash    BLOB NOT NULL UNIQUE,
	refresh_hash   BLOB NOT NULL UNIQUE,
	access_expires INTEGER NOT NULL,
	created_at     INTEGER NOT NULL,
	last_seen      INTEGER NOT NULL
);
CREATE TABLE grants (
	hash         BLOB PRIMARY KEY,
	kind         TEXT NOT NULL,
	user_id      INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	client_id    TEXT NOT NULL,
	redirect_uri TEXT NOT NULL,
	challenge    TEXT NOT NULL,
	device_name  TEXT NOT NULL,
	platform     TEXT NOT NULL,
	confirmed    INTEGER NOT NULL,
	expires_at   INTEGER NOT NULL
);
CREATE TABLE invites (
	code       TEXT PRIMARY KEY,
	max_uses   INTEGER NOT NULL,
	uses       INTEGER NOT NULL DEFAULT 0,
	expires_at INTEGER NOT NULL,
	created_by INTEGER NOT NULL,
	created_at INTEGER NOT NULL
);
CREATE TABLE domains (
	id               INTEGER PRIMARY KEY,
	name             TEXT NOT NULL UNIQUE,
	owner_id         INTEGER REFERENCES users(id) ON DELETE CASCADE, -- NULL = shared
	verified         INTEGER NOT NULL DEFAULT 0,
	verify_token     TEXT NOT NULL DEFAULT '',
	allow_mailbox    INTEGER NOT NULL DEFAULT 1,
	allow_aliases    INTEGER NOT NULL DEFAULT 1,
	allow_subdomains INTEGER NOT NULL DEFAULT 1,
	max_aliases      INTEGER NOT NULL DEFAULT 0,
	max_generated    INTEGER NOT NULL DEFAULT 0,
	last_checked     INTEGER NOT NULL DEFAULT 0,
	failing_since    INTEGER NOT NULL DEFAULT 0,
	created_at       INTEGER NOT NULL
);
CREATE TABLE aliases (
	id              INTEGER PRIMARY KEY,
	domain_id       INTEGER NOT NULL REFERENCES domains(id) ON DELETE CASCADE,
	local           TEXT NOT NULL,
	user_id         INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	label           TEXT NOT NULL DEFAULT '',
	generated       INTEGER NOT NULL DEFAULT 0,
	expected_sender TEXT NOT NULL DEFAULT '',
	created_at      INTEGER NOT NULL,
	UNIQUE (domain_id, local)
);
CREATE TABLE messages (
	id          INTEGER PRIMARY KEY,
	user_id     INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	rcpt        TEXT NOT NULL,
	mail_from   TEXT NOT NULL,
	from_hdr    TEXT NOT NULL,
	subject     TEXT NOT NULL,
	service     TEXT NOT NULL,
	code        TEXT NOT NULL DEFAULT '',
	link        TEXT NOT NULL DEFAULT '',
	confidence  REAL NOT NULL DEFAULT 0,
	auth        TEXT NOT NULL DEFAULT 'none',
	auth_detail TEXT NOT NULL DEFAULT '',
	flags       TEXT NOT NULL DEFAULT '',
	raw         BLOB NOT NULL,
	received_at INTEGER NOT NULL,
	expires_at  INTEGER NOT NULL,
	used_at     INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX messages_user ON messages (user_id, id DESC);
CREATE INDEX messages_expiry ON messages (expires_at);
CREATE TABLE corrections (
	id         INTEGER PRIMARY KEY,
	message_id INTEGER NOT NULL,
	user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	kind       TEXT NOT NULL,
	value      TEXT NOT NULL,
	raw        BLOB NOT NULL,
	created_at INTEGER NOT NULL
);
CREATE TABLE site_links (
	user_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	sender_domain TEXT NOT NULL,
	site_domain   TEXT NOT NULL,
	PRIMARY KEY (user_id, sender_domain, site_domain)
);
CREATE TABLE settings (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE audit_log (
	id     INTEGER PRIMARY KEY,
	actor  INTEGER NOT NULL,
	action TEXT NOT NULL,
	target TEXT NOT NULL,
	at     INTEGER NOT NULL
);
`}

func Open(path string) (*DB, error) {
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	s := &DB{db: db, path: path}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *DB) Close() error { return s.db.Close() }

func (s *DB) migrate() error {
	var v int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&v); err != nil {
		return err
	}
	for ; v < len(migrations); v++ {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(migrations[v]); err != nil {
			tx.Rollback()
			return err
		}
		if _, err := tx.Exec(fmt.Sprintf(`PRAGMA user_version = %d`, v+1)); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

func ts(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}

func tm(i int64) time.Time {
	if i == 0 {
		return time.Time{}
	}
	return time.Unix(i, 0)
}

// one maps "no rows" to store.ErrNotFound.
func one(err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return store.ErrNotFound
	}
	return err
}

// changed reports store.ErrNotFound when a statement touched no rows.
func changed(res sql.Result, err error) error {
	if err != nil {
		return taken(err)
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return store.ErrNotFound
	}
	return nil
}

// taken maps a uniqueness violation to store.ErrTaken.
func taken(err error) error {
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint") {
		return store.ErrTaken
	}
	return err
}

// --- users ---

const userCols = `id, username, handle, is_admin, retention, created_at`

func scanUser(row interface{ Scan(...any) error }) (store.User, error) {
	var u store.User
	var ret, created int64
	err := row.Scan(&u.ID, &u.Username, &u.Handle, &u.IsAdmin, &ret, &created)
	u.Retention, u.CreatedAt = time.Duration(ret)*time.Second, tm(created)
	return u, one(err)
}

func (s *DB) CreateUser(ctx context.Context, u *store.User) error {
	u.Username = strings.ToLower(u.Username)
	if !store.ValidName(u.Username) {
		return fmt.Errorf("%w: username %q", store.ErrInvalidName, u.Username)
	}
	if u.CreatedAt.IsZero() {
		u.CreatedAt = time.Now()
	}
	err := s.db.QueryRowContext(ctx, `
		INSERT INTO users (username, handle, is_admin, retention, created_at)
		VALUES (?, ?, ?, ?, ?) RETURNING id`,
		u.Username, u.Handle, u.IsAdmin, int64(u.Retention/time.Second), ts(u.CreatedAt)).Scan(&u.ID)
	return taken(err)
}

func (s *DB) UserByID(ctx context.Context, id int64) (store.User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE id = ?`, id))
}

func (s *DB) UserByName(ctx context.Context, name string) (store.User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE username = ?`, strings.ToLower(name)))
}

func (s *DB) UserByHandle(ctx context.Context, handle []byte) (store.User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userCols+` FROM users WHERE handle = ?`, handle))
}

func (s *DB) ListUsers(ctx context.Context) ([]store.User, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+userCols+` FROM users ORDER BY username`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.User
	for rows.Next() {
		u, err := scanUser(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (s *DB) UpdateUser(ctx context.Context, u store.User) error {
	return changed(s.db.ExecContext(ctx, `UPDATE users SET is_admin = ?, retention = ? WHERE id = ?`,
		u.IsAdmin, int64(u.Retention/time.Second), u.ID))
}

func (s *DB) DeleteUser(ctx context.Context, id int64) error {
	return changed(s.db.ExecContext(ctx, `DELETE FROM users WHERE id = ?`, id))
}

// --- passkeys and recovery codes ---

func (s *DB) AddPasskey(ctx context.Context, p *store.Passkey) error {
	if p.CreatedAt.IsZero() {
		p.CreatedAt = time.Now()
	}
	return taken(s.db.QueryRowContext(ctx, `
		INSERT INTO passkeys (user_id, cred_id, data, name, created_at)
		VALUES (?, ?, ?, ?, ?) RETURNING id`,
		p.UserID, p.CredID, p.Data, p.Name, ts(p.CreatedAt)).Scan(&p.ID))
}

func (s *DB) PasskeysByUser(ctx context.Context, userID int64) ([]store.Passkey, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, cred_id, data, name, created_at, last_used FROM passkeys
		WHERE user_id = ? ORDER BY id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Passkey
	for rows.Next() {
		p := store.Passkey{UserID: userID}
		var created, used int64
		if err := rows.Scan(&p.ID, &p.CredID, &p.Data, &p.Name, &created, &used); err != nil {
			return nil, err
		}
		p.CreatedAt, p.LastUsed = tm(created), tm(used)
		out = append(out, p)
	}
	return out, rows.Err()
}

func (s *DB) UpdatePasskey(ctx context.Context, p store.Passkey) error {
	return changed(s.db.ExecContext(ctx, `UPDATE passkeys SET data = ?, last_used = ? WHERE id = ?`,
		p.Data, ts(p.LastUsed), p.ID))
}

func (s *DB) DeletePasskey(ctx context.Context, userID, id int64) error {
	return changed(s.db.ExecContext(ctx, `DELETE FROM passkeys WHERE id = ? AND user_id = ?`, id, userID))
}

func (s *DB) ReplaceRecoveryCodes(ctx context.Context, userID int64, hashes [][]byte) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `DELETE FROM recovery_codes WHERE user_id = ?`, userID); err != nil {
		return err
	}
	for _, h := range hashes {
		if _, err := tx.ExecContext(ctx, `INSERT INTO recovery_codes (user_id, hash) VALUES (?, ?)`, userID, h); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *DB) AddRecoveryCode(ctx context.Context, userID int64, hash []byte) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO recovery_codes (user_id, hash) VALUES (?, ?)`, userID, hash)
	return err
}

func (s *DB) UseRecoveryCode(ctx context.Context, userID int64, hash []byte) (bool, error) {
	err := changed(s.db.ExecContext(ctx, `DELETE FROM recovery_codes WHERE user_id = ? AND hash = ?`, userID, hash))
	if errors.Is(err, store.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}

// --- sessions ---

func (s *DB) CreateSession(ctx context.Context, hash []byte, userID int64, expires time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions (hash, user_id, expires_at) VALUES (?, ?, ?)`,
		hash, userID, ts(expires))
	return err
}

func (s *DB) SessionUser(ctx context.Context, hash []byte, now time.Time) (store.User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `
		SELECT u.id, u.username, u.handle, u.is_admin, u.retention, u.created_at
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.hash = ? AND s.expires_at > ?`, hash, ts(now)))
}

func (s *DB) DeleteSession(ctx context.Context, hash []byte) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE hash = ?`, hash)
	return err
}

// --- devices ---

const deviceCols = `id, user_id, name, platform, access_hash, refresh_hash, access_expires, created_at, last_seen`

func scanDevice(row interface{ Scan(...any) error }) (store.Device, error) {
	var d store.Device
	var exp, created, seen int64
	err := row.Scan(&d.ID, &d.UserID, &d.Name, &d.Platform, &d.AccessHash, &d.RefreshHash, &exp, &created, &seen)
	d.AccessExpires, d.CreatedAt, d.LastSeen = tm(exp), tm(created), tm(seen)
	return d, one(err)
}

func (s *DB) CreateDevice(ctx context.Context, d *store.Device) error {
	now := time.Now()
	d.CreatedAt, d.LastSeen = now, now
	return s.db.QueryRowContext(ctx, `
		INSERT INTO devices (user_id, name, platform, access_hash, refresh_hash, access_expires, created_at, last_seen)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		d.UserID, d.Name, d.Platform, d.AccessHash, d.RefreshHash, ts(d.AccessExpires), ts(now), ts(now)).Scan(&d.ID)
}

// DeviceByAccess also records that the device was seen, at most once a
// minute.
func (s *DB) DeviceByAccess(ctx context.Context, hash []byte, now time.Time) (store.Device, error) {
	d, err := scanDevice(s.db.QueryRowContext(ctx,
		`SELECT `+deviceCols+` FROM devices WHERE access_hash = ? AND access_expires > ?`, hash, ts(now)))
	if err == nil && now.Sub(d.LastSeen) > time.Minute {
		s.db.ExecContext(ctx, `UPDATE devices SET last_seen = ? WHERE id = ?`, ts(now), d.ID)
	}
	return d, err
}

func (s *DB) DeviceByRefresh(ctx context.Context, hash []byte) (store.Device, error) {
	return scanDevice(s.db.QueryRowContext(ctx, `SELECT `+deviceCols+` FROM devices WHERE refresh_hash = ?`, hash))
}

func (s *DB) RotateDevice(ctx context.Context, d store.Device) error {
	return changed(s.db.ExecContext(ctx, `
		UPDATE devices SET access_hash = ?, refresh_hash = ?, access_expires = ?, last_seen = ? WHERE id = ?`,
		d.AccessHash, d.RefreshHash, ts(d.AccessExpires), ts(time.Now()), d.ID))
}

func (s *DB) ListDevices(ctx context.Context, userID int64) ([]store.Device, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+deviceCols+` FROM devices WHERE user_id = ? ORDER BY last_seen DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Device
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *DB) DeleteDevice(ctx context.Context, userID, id int64) error {
	return changed(s.db.ExecContext(ctx, `DELETE FROM devices WHERE id = ? AND user_id = ?`, id, userID))
}

// --- grants ---

func (s *DB) PutGrant(ctx context.Context, g store.Grant) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO grants (hash, kind, user_id, client_id, redirect_uri, challenge, device_name, platform, confirmed, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (hash) DO UPDATE SET device_name = excluded.device_name,
			platform = excluded.platform, confirmed = excluded.confirmed`,
		g.CodeHash, g.Kind, g.UserID, g.ClientID, g.RedirectURI, g.Challenge, g.DeviceName, g.Platform, g.Confirmed, ts(g.ExpiresAt))
	return err
}

func (s *DB) GetGrant(ctx context.Context, hash []byte) (store.Grant, error) {
	g := store.Grant{CodeHash: hash}
	var exp int64
	err := s.db.QueryRowContext(ctx, `
		SELECT kind, user_id, client_id, redirect_uri, challenge, device_name, platform, confirmed, expires_at
		FROM grants WHERE hash = ?`, hash).Scan(
		&g.Kind, &g.UserID, &g.ClientID, &g.RedirectURI, &g.Challenge, &g.DeviceName, &g.Platform, &g.Confirmed, &exp)
	g.ExpiresAt = tm(exp)
	if err == nil && time.Now().After(g.ExpiresAt) {
		return g, store.ErrNotFound
	}
	return g, one(err)
}

func (s *DB) DeleteGrant(ctx context.Context, hash []byte) error {
	return changed(s.db.ExecContext(ctx, `DELETE FROM grants WHERE hash = ?`, hash))
}

// --- invites ---

func (s *DB) CreateInvite(ctx context.Context, inv store.Invite) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO invites (code, max_uses, expires_at, created_by, created_at) VALUES (?, ?, ?, ?, ?)`,
		inv.Code, inv.MaxUses, ts(inv.ExpiresAt), inv.CreatedBy, ts(time.Now()))
	return taken(err)
}

func (s *DB) ListInvites(ctx context.Context) ([]store.Invite, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT code, max_uses, uses, expires_at, created_by, created_at FROM invites ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Invite
	for rows.Next() {
		var inv store.Invite
		var exp, created int64
		if err := rows.Scan(&inv.Code, &inv.MaxUses, &inv.Uses, &exp, &inv.CreatedBy, &created); err != nil {
			return nil, err
		}
		inv.ExpiresAt, inv.CreatedAt = tm(exp), tm(created)
		out = append(out, inv)
	}
	return out, rows.Err()
}

func (s *DB) DeleteInvite(ctx context.Context, code string) error {
	return changed(s.db.ExecContext(ctx, `DELETE FROM invites WHERE code = ?`, code))
}

func (s *DB) UseInvite(ctx context.Context, code string, now time.Time) error {
	return changed(s.db.ExecContext(ctx, `
		UPDATE invites SET uses = uses + 1
		WHERE code = ? AND (max_uses = 0 OR uses < max_uses) AND (expires_at = 0 OR expires_at > ?)`,
		code, ts(now)))
}

// --- settings, audit, stats ---

func (s *DB) GetSetting(ctx context.Context, key string) (string, error) {
	var v string
	err := s.db.QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (s *DB) SetSetting(ctx context.Context, key, value string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO settings (key, value) VALUES (?, ?)
		ON CONFLICT (key) DO UPDATE SET value = excluded.value`, key, value)
	return err
}

func (s *DB) Audit(ctx context.Context, actor int64, action, target string) {
	s.db.ExecContext(ctx, `INSERT INTO audit_log (actor, action, target, at) VALUES (?, ?, ?, ?)`,
		actor, action, target, ts(time.Now()))
}

func (s *DB) Stats(ctx context.Context) (store.Stats, error) {
	var st store.Stats
	var last sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
		SELECT (SELECT COUNT(*) FROM users), (SELECT COUNT(*) FROM messages),
			(SELECT COUNT(*) FROM corrections), (SELECT MAX(received_at) FROM messages)`).Scan(
		&st.Users, &st.Messages, &st.Corrections, &last)
	st.LastMessage = tm(last.Int64)
	for _, suffix := range []string{"", "-wal"} {
		if fi, err := os.Stat(s.path + suffix); err == nil {
			st.Bytes += fi.Size()
		}
	}
	return st, err
}
