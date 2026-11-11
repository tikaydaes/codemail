// Package sqlstore implements store.Store on SQL databases: embedded
// SQLite (the default) and PostgreSQL. Both run the same queries, written
// in portable SQL (integer timestamps, RETURNING for new IDs, "?"
// placeholders that are renumbered for PostgreSQL).
package sqlstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib" // PostgreSQL driver
	_ "modernc.org/sqlite"             // pure-Go SQLite driver

	"github.com/tikaydaes/codemail/internal/store"
)

// conn wraps the database handle so every query passes through rebind.
type conn struct {
	raw *sql.DB
	pg  bool
}

// rebind turns "?" placeholders into PostgreSQL's "$1", "$2", ...
func (c *conn) rebind(q string) string {
	if !c.pg {
		return q
	}
	var b strings.Builder
	n := 0
	for _, r := range q {
		if r == '?' {
			n++
			b.WriteString("$" + strconv.Itoa(n))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (c *conn) ExecContext(ctx context.Context, q string, args ...any) (sql.Result, error) {
	return c.raw.ExecContext(ctx, c.rebind(q), args...)
}

func (c *conn) QueryContext(ctx context.Context, q string, args ...any) (*sql.Rows, error) {
	return c.raw.QueryContext(ctx, c.rebind(q), args...)
}

func (c *conn) QueryRowContext(ctx context.Context, q string, args ...any) *sql.Row {
	return c.raw.QueryRowContext(ctx, c.rebind(q), args...)
}

type DB struct {
	db   *conn
	pg   bool
	path string // SQLite file, for size reporting
}

var _ store.Store = (*DB)(nil)

// migrations are applied in order; PRAGMA user_version records progress.
var migrations = []string{`
CREATE TABLE users (
	id         INTEGER PRIMARY KEY,
	username   TEXT NOT NULL UNIQUE,
	handle     BLOB NOT NULL UNIQUE,
	is_admin   BOOLEAN NOT NULL DEFAULT FALSE,
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
	confirmed    BOOLEAN NOT NULL,
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
	verified         BOOLEAN NOT NULL DEFAULT FALSE,
	verify_token     TEXT NOT NULL DEFAULT '',
	allow_mailbox    BOOLEAN NOT NULL DEFAULT TRUE,
	allow_aliases    BOOLEAN NOT NULL DEFAULT TRUE,
	allow_subdomains BOOLEAN NOT NULL DEFAULT TRUE,
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
	generated       BOOLEAN NOT NULL DEFAULT FALSE,
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
`, `
-- Sessions get an ID and details so a user can list and revoke them.
CREATE TABLE sessions_new (
	id         INTEGER PRIMARY KEY,
	hash       BLOB NOT NULL UNIQUE,
	user_id    INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
	expires_at INTEGER NOT NULL,
	created_at INTEGER NOT NULL DEFAULT 0,
	last_seen  INTEGER NOT NULL DEFAULT 0,
	label      TEXT NOT NULL DEFAULT ''
);
INSERT INTO sessions_new (hash, user_id, expires_at) SELECT hash, user_id, expires_at FROM sessions;
DROP TABLE sessions;
ALTER TABLE sessions_new RENAME TO sessions;
-- A device's previous refresh token stays valid briefly after rotation,
-- so two parts of an app renewing at the same moment do not sign it out.
ALTER TABLE devices ADD COLUMN prev_refresh_hash BLOB;
ALTER TABLE devices ADD COLUMN rotated_at INTEGER NOT NULL DEFAULT 0;
`}

// forPostgres translates the SQLite-flavoured schema above.
func forPostgres(schema string) string {
	return strings.NewReplacer(
		// Listed first, and with the same leading space as the next pair,
		// so it wins where both match.
		" INTEGER PRIMARY KEY", " BIGSERIAL PRIMARY KEY",
		" INTEGER", " BIGINT",
		" BLOB", " BYTEA",
		" REAL", " DOUBLE PRECISION",
	).Replace(schema)
}

// Open opens (creating if needed) a SQLite database file.
func Open(path string) (*DB, error) {
	dsn := "file:" + path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	return finishOpen(&DB{db: &conn{raw: db}, path: path})
}

// OpenPostgres connects to a PostgreSQL database given its URL, such as
// postgres://user:password@host/dbname.
func OpenPostgres(url string) (*DB, error) {
	db, err := sql.Open("pgx", url)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(10)
	return finishOpen(&DB{db: &conn{raw: db, pg: true}, pg: true})
}

func finishOpen(s *DB) (*DB, error) {
	if err := s.migrate(); err != nil {
		s.db.raw.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}
	return s, nil
}

func (s *DB) Close() error { return s.db.raw.Close() }

// Postgres reports whether this store is a PostgreSQL database.
func (s *DB) Postgres() bool { return s.pg }

// schemaVersion is PRAGMA user_version on SQLite and a one-row table on
// PostgreSQL.
func (s *DB) schemaVersion() (int, error) {
	var v int
	if !s.pg {
		return v, s.db.raw.QueryRow(`PRAGMA user_version`).Scan(&v)
	}
	if _, err := s.db.raw.Exec(`CREATE TABLE IF NOT EXISTS schema_version (version BIGINT NOT NULL)`); err != nil {
		return 0, err
	}
	err := s.db.raw.QueryRow(`SELECT version FROM schema_version`).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = s.db.raw.Exec(`INSERT INTO schema_version (version) VALUES (0)`)
	}
	return v, err
}

func (s *DB) migrate() error {
	v, err := s.schemaVersion()
	if err != nil {
		return err
	}
	for ; v < len(migrations); v++ {
		schema, bump := migrations[v], fmt.Sprintf(`PRAGMA user_version = %d`, v+1)
		if s.pg {
			schema, bump = forPostgres(schema), fmt.Sprintf(`UPDATE schema_version SET version = %d`, v+1)
		}
		tx, err := s.db.raw.Begin()
		if err != nil {
			return err
		}
		if _, err := tx.Exec(schema); err != nil {
			tx.Rollback()
			return fmt.Errorf("version %d: %w", v+1, err)
		}
		if _, err := tx.Exec(bump); err != nil {
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
	if err != nil && (strings.Contains(err.Error(), "UNIQUE constraint") || strings.Contains(err.Error(), "23505")) {
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
	tx, err := s.db.raw.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, s.db.rebind(`DELETE FROM recovery_codes WHERE user_id = ?`), userID); err != nil {
		return err
	}
	for _, h := range hashes {
		if _, err := tx.ExecContext(ctx, s.db.rebind(`INSERT INTO recovery_codes (user_id, hash) VALUES (?, ?)`), userID, h); err != nil {
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

func (s *DB) CreateSession(ctx context.Context, hash []byte, se *store.Session) error {
	now := time.Now()
	se.CreatedAt, se.LastSeen = now, now
	return s.db.QueryRowContext(ctx, `
		INSERT INTO sessions (hash, user_id, expires_at, created_at, last_seen, label)
		VALUES (?, ?, ?, ?, ?, ?) RETURNING id`,
		hash, se.UserID, ts(se.ExpiresAt), ts(now), ts(now), se.Label).Scan(&se.ID)
}

// SessionUser also records that the session was seen, at most once a
// minute.
func (s *DB) SessionUser(ctx context.Context, hash []byte, now time.Time) (store.User, store.Session, error) {
	var u store.User
	var se store.Session
	var ret, ucreated, exp, created, seen int64
	err := s.db.QueryRowContext(ctx, `
		SELECT u.id, u.username, u.handle, u.is_admin, u.retention, u.created_at,
			s.id, s.expires_at, s.created_at, s.last_seen, s.label
		FROM sessions s JOIN users u ON u.id = s.user_id
		WHERE s.hash = ? AND s.expires_at > ?`, hash, ts(now)).Scan(
		&u.ID, &u.Username, &u.Handle, &u.IsAdmin, &ret, &ucreated,
		&se.ID, &exp, &created, &seen, &se.Label)
	if err != nil {
		return u, se, one(err)
	}
	u.Retention, u.CreatedAt = time.Duration(ret)*time.Second, tm(ucreated)
	se.UserID, se.ExpiresAt, se.CreatedAt, se.LastSeen = u.ID, tm(exp), tm(created), tm(seen)
	if now.Sub(se.LastSeen) > time.Minute {
		s.db.ExecContext(ctx, `UPDATE sessions SET last_seen = ? WHERE id = ?`, ts(now), se.ID)
		se.LastSeen = now
	}
	return u, se, nil
}

func (s *DB) ListSessions(ctx context.Context, userID int64, now time.Time) ([]store.Session, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, expires_at, created_at, last_seen, label FROM sessions
		WHERE user_id = ? AND expires_at > ? ORDER BY last_seen DESC, id DESC`, userID, ts(now))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Session
	for rows.Next() {
		se := store.Session{UserID: userID}
		var exp, created, seen int64
		if err := rows.Scan(&se.ID, &exp, &created, &seen, &se.Label); err != nil {
			return nil, err
		}
		se.ExpiresAt, se.CreatedAt, se.LastSeen = tm(exp), tm(created), tm(seen)
		out = append(out, se)
	}
	return out, rows.Err()
}

func (s *DB) DeleteSession(ctx context.Context, hash []byte) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE hash = ?`, hash)
	return err
}

func (s *DB) DeleteSessionByID(ctx context.Context, userID, id int64) error {
	return changed(s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ? AND user_id = ?`, id, userID))
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

// refreshLeeway is how long a device's previous refresh token still works
// after it has been rotated.
const refreshLeeway = time.Minute

func (s *DB) DeviceByRefresh(ctx context.Context, hash []byte, now time.Time) (store.Device, error) {
	return scanDevice(s.db.QueryRowContext(ctx, `SELECT `+deviceCols+` FROM devices
		WHERE refresh_hash = ? OR (prev_refresh_hash = ? AND rotated_at > ?)`,
		hash, hash, ts(now.Add(-refreshLeeway))))
}

// RotateDevice stores new tokens, remembering prevRefresh for the leeway.
func (s *DB) RotateDevice(ctx context.Context, d store.Device, prevRefresh []byte) error {
	now := time.Now()
	return changed(s.db.ExecContext(ctx, `
		UPDATE devices SET access_hash = ?, refresh_hash = ?, access_expires = ?, last_seen = ?,
			prev_refresh_hash = ?, rotated_at = ? WHERE id = ?`,
		d.AccessHash, d.RefreshHash, ts(d.AccessExpires), ts(now), prevRefresh, ts(now), d.ID))
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
	if s.pg {
		s.db.QueryRowContext(ctx, `SELECT pg_database_size(current_database())`).Scan(&st.Bytes)
		return st, err
	}
	for _, suffix := range []string{"", "-wal"} {
		if fi, err := os.Stat(s.path + suffix); err == nil {
			st.Bytes += fi.Size()
		}
	}
	return st, err
}
