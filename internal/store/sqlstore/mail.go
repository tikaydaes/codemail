package sqlstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tikaydaes/codemail/internal/store"
)

// --- domains ---

const domainCols = `id, name, COALESCE(owner_id, 0), verified, verify_token, allow_mailbox, allow_aliases,
	allow_subdomains, max_aliases, max_generated, last_checked, failing_since, created_at`

func scanDomain(row interface{ Scan(...any) error }) (store.Domain, error) {
	var d store.Domain
	var checked, failing, created int64
	err := row.Scan(&d.ID, &d.Name, &d.OwnerID, &d.Verified, &d.VerifyToken, &d.AllowMailbox, &d.AllowAliases,
		&d.AllowSubdomains, &d.MaxAliases, &d.MaxGenerated, &checked, &failing, &created)
	d.LastChecked, d.FailingSince, d.CreatedAt = tm(checked), tm(failing), tm(created)
	return d, one(err)
}

func (s *DB) domains(ctx context.Context, where string, args ...any) ([]store.Domain, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+domainCols+` FROM domains `+where+` ORDER BY name`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Domain
	for rows.Next() {
		d, err := scanDomain(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func (s *DB) AddDomain(ctx context.Context, d *store.Domain) error {
	d.Name = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(d.Name), "."))
	if !strings.Contains(d.Name, ".") || strings.ContainsAny(d.Name, "@ /:") {
		return fmt.Errorf("%w: domain %q", store.ErrInvalidName, d.Name)
	}
	var owner any
	if d.OwnerID != 0 {
		owner = d.OwnerID
	}
	d.CreatedAt = time.Now()
	return taken(s.db.QueryRowContext(ctx, `
		INSERT INTO domains (name, owner_id, verified, verify_token, allow_mailbox, allow_aliases,
			allow_subdomains, max_aliases, max_generated, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		d.Name, owner, d.Verified, d.VerifyToken, d.AllowMailbox, d.AllowAliases,
		d.AllowSubdomains, d.MaxAliases, d.MaxGenerated, ts(d.CreatedAt)).Scan(&d.ID))
}

func (s *DB) DomainByID(ctx context.Context, id int64) (store.Domain, error) {
	return scanDomain(s.db.QueryRowContext(ctx, `SELECT `+domainCols+` FROM domains WHERE id = ?`, id))
}

func (s *DB) DomainByName(ctx context.Context, name string) (store.Domain, error) {
	return scanDomain(s.db.QueryRowContext(ctx, `SELECT `+domainCols+` FROM domains WHERE name = ?`, name))
}

func (s *DB) ListDomains(ctx context.Context, ownerID int64) ([]store.Domain, error) {
	if ownerID == 0 {
		return s.domains(ctx, `WHERE owner_id IS NULL`)
	}
	return s.domains(ctx, `WHERE owner_id = ?`, ownerID)
}

func (s *DB) AllDomains(ctx context.Context) ([]store.Domain, error) { return s.domains(ctx, ``) }

func (s *DB) UpdateDomain(ctx context.Context, d store.Domain) error {
	return changed(s.db.ExecContext(ctx, `
		UPDATE domains SET verified = ?, allow_mailbox = ?, allow_aliases = ?, allow_subdomains = ?,
			max_aliases = ?, max_generated = ?, last_checked = ?, failing_since = ? WHERE id = ?`,
		d.Verified, d.AllowMailbox, d.AllowAliases, d.AllowSubdomains, d.MaxAliases, d.MaxGenerated,
		ts(d.LastChecked), ts(d.FailingSince), d.ID))
}

func (s *DB) DeleteDomain(ctx context.Context, id int64) error {
	return changed(s.db.ExecContext(ctx, `DELETE FROM domains WHERE id = ?`, id))
}

// --- aliases ---

const aliasCols = `a.id, a.domain_id, d.name, a.local, a.user_id, a.label, a.generated, a.expected_sender, a.created_at`

func scanAlias(row interface{ Scan(...any) error }) (store.Alias, error) {
	var a store.Alias
	var created int64
	err := row.Scan(&a.ID, &a.DomainID, &a.Domain, &a.Local, &a.UserID, &a.Label, &a.Generated, &a.ExpectedSender, &created)
	a.CreatedAt = tm(created)
	return a, one(err)
}

func (s *DB) ClaimAlias(ctx context.Context, a *store.Alias) error {
	a.Local = strings.ToLower(strings.TrimSpace(a.Local))
	if !store.ValidName(a.Local) {
		return fmt.Errorf("%w: address name %q", store.ErrInvalidName, a.Local)
	}
	d, err := s.DomainByID(ctx, a.DomainID)
	if err != nil {
		return err
	}
	a.Domain = d.Name
	own := d.OwnerID == a.UserID
	if !d.Verified || (!own && (d.OwnerID != 0 || !d.AllowAliases)) {
		return fmt.Errorf("%w: aliases on %s", store.ErrNotAllowed, d.Name)
	}
	if !own {
		if d.AllowMailbox {
			// A mailbox name always belongs to the user with that username.
			if u, err := s.UserByName(ctx, a.Local); err == nil && u.ID != a.UserID {
				return store.ErrTaken
			}
		}
		limit := d.MaxAliases
		if a.Generated {
			limit = d.MaxGenerated
		}
		if limit > 0 {
			var n int
			if err := s.db.QueryRowContext(ctx,
				`SELECT COUNT(*) FROM aliases WHERE domain_id = ? AND user_id = ? AND generated = ?`,
				d.ID, a.UserID, a.Generated).Scan(&n); err != nil {
				return err
			}
			if n >= limit {
				return store.ErrLimit
			}
		}
	}
	a.CreatedAt = time.Now()
	return taken(s.db.QueryRowContext(ctx, `
		INSERT INTO aliases (domain_id, local, user_id, label, generated, created_at)
		VALUES (?, ?, ?, ?, ?, ?) RETURNING id`,
		d.ID, a.Local, a.UserID, a.Label, a.Generated, ts(a.CreatedAt)).Scan(&a.ID))
}

func (s *DB) ListAliases(ctx context.Context, userID int64) ([]store.Alias, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+aliasCols+` FROM aliases a JOIN domains d ON d.id = a.domain_id
		WHERE a.user_id = ? ORDER BY a.created_at DESC, a.id DESC`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Alias
	for rows.Next() {
		a, err := scanAlias(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *DB) DeleteAlias(ctx context.Context, userID, id int64) error {
	return changed(s.db.ExecContext(ctx, `DELETE FROM aliases WHERE id = ? AND user_id = ?`, id, userID))
}

func (s *DB) SetAliasSender(ctx context.Context, id int64, sender string) error {
	return changed(s.db.ExecContext(ctx, `UPDATE aliases SET expected_sender = ? WHERE id = ?`, sender, id))
}

func (s *DB) SetAliasLabel(ctx context.Context, userID, id int64, label string) error {
	return changed(s.db.ExecContext(ctx, `UPDATE aliases SET label = ? WHERE id = ? AND user_id = ?`, label, id, userID))
}

func (s *DB) aliasAt(ctx context.Context, domainID int64, local string) (*store.Alias, error) {
	a, err := scanAlias(s.db.QueryRowContext(ctx, `
		SELECT `+aliasCols+` FROM aliases a JOIN domains d ON d.id = a.domain_id
		WHERE a.domain_id = ? AND a.local = ?`, domainID, local))
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	return &a, err
}

// Resolve maps a recipient address to the user who receives it.
//
//	anything@owned.example   -> the domain's owner
//	alias(+tag)@shared       -> whoever claimed the alias
//	username(+tag)@shared    -> that user
//	postmaster@shared        -> the first admin
//	anything@username.shared -> that user
func (s *DB) Resolve(ctx context.Context, addr string) (store.Recipient, error) {
	none := store.Recipient{}
	local, host, ok := store.SplitAddress(addr)
	if !ok {
		return none, store.ErrNoRecipient
	}
	if i := strings.IndexByte(local, '+'); i > 0 {
		local = local[:i]
	}
	d, err := s.DomainByName(ctx, host)
	switch {
	case err == nil:
		if !d.Verified {
			return none, store.ErrNoRecipient
		}
		var alias *store.Alias
		if d.OwnerID != 0 || d.AllowAliases {
			if alias, err = s.aliasAt(ctx, d.ID, local); err != nil {
				return none, err
			}
		}
		if d.OwnerID != 0 {
			return store.Recipient{UserID: d.OwnerID, Alias: alias}, nil
		}
		if alias != nil {
			return store.Recipient{UserID: alias.UserID, Alias: alias}, nil
		}
		if local == "postmaster" {
			var id int64
			err := s.db.QueryRowContext(ctx, `SELECT id FROM users WHERE is_admin = ? ORDER BY id LIMIT 1`, true).Scan(&id)
			if err == nil {
				return store.Recipient{UserID: id}, nil
			}
		}
		if d.AllowMailbox {
			return s.recipientByName(ctx, local)
		}
		return none, store.ErrNoRecipient
	case errors.Is(err, store.ErrNotFound):
		dot := strings.IndexByte(host, '.')
		if dot <= 0 {
			return none, store.ErrNoRecipient
		}
		p, err := s.DomainByName(ctx, host[dot+1:])
		if errors.Is(err, store.ErrNotFound) {
			return none, store.ErrNoRecipient
		}
		if err != nil {
			return none, err
		}
		if p.OwnerID != 0 || !p.Verified || !p.AllowSubdomains {
			return none, store.ErrNoRecipient
		}
		return s.recipientByName(ctx, host[:dot])
	default:
		return none, err
	}
}

func (s *DB) recipientByName(ctx context.Context, username string) (store.Recipient, error) {
	u, err := s.UserByName(ctx, username)
	if errors.Is(err, store.ErrNotFound) {
		return store.Recipient{}, store.ErrNoRecipient
	}
	return store.Recipient{UserID: u.ID}, err
}

// --- messages ---

const messageCols = `id, user_id, rcpt, mail_from, from_hdr, subject, service, code, link, confidence,
	auth, auth_detail, flags, received_at, expires_at, used_at`

func scanMessage(row interface{ Scan(...any) error }, raw *[]byte) (store.Message, error) {
	var m store.Message
	var recv, exp, used int64
	dest := []any{&m.ID, &m.UserID, &m.Rcpt, &m.MailFrom, &m.FromHdr, &m.Subject, &m.Service, &m.Code, &m.Link,
		&m.Confidence, &m.Auth, &m.AuthDetail, &m.Flags, &recv, &exp, &used}
	if raw != nil {
		dest = append(dest, raw)
	}
	err := row.Scan(dest...)
	m.ReceivedAt, m.ExpiresAt, m.UsedAt = tm(recv), tm(exp), tm(used)
	return m, one(err)
}

func (s *DB) SaveMessage(ctx context.Context, m *store.Message) error {
	if m.Auth == "" {
		m.Auth = "none"
	}
	return s.db.QueryRowContext(ctx, `
		INSERT INTO messages (user_id, rcpt, mail_from, from_hdr, subject, service, code, link, confidence,
			auth, auth_detail, flags, raw, received_at, expires_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?) RETURNING id`,
		m.UserID, m.Rcpt, m.MailFrom, m.FromHdr, m.Subject, m.Service, m.Code, m.Link, m.Confidence,
		m.Auth, m.AuthDetail, m.Flags, m.Raw, ts(m.ReceivedAt), ts(m.ExpiresAt)).Scan(&m.ID)
}

func (s *DB) ListMessages(ctx context.Context, q store.MessageQuery) ([]store.Message, error) {
	where, args := []string{`user_id = ?`}, []any{q.UserID}
	switch q.Kind {
	case "code":
		where = append(where, `(code <> '' OR link <> '')`)
	case "other":
		where = append(where, `code = '' AND link = ''`)
	}
	if q.Search != "" {
		// Lower-cased on both sides: LIKE is case-sensitive on PostgreSQL.
		like := "%" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(strings.ToLower(q.Search)) + "%"
		where = append(where, `(LOWER(service) LIKE ? ESCAPE '\' OR LOWER(subject) LIKE ? ESCAPE '\' OR LOWER(rcpt) LIKE ? ESCAPE '\' OR LOWER(code) LIKE ? ESCAPE '\')`)
		args = append(args, like, like, like, like)
	}
	order := `id DESC`
	if q.After > 0 {
		where, args, order = append(where, `id > ?`), append(args, q.After), `id ASC`
	}
	if q.Before > 0 {
		where, args = append(where, `id < ?`), append(args, q.Before)
	}
	if q.Limit <= 0 || q.Limit > 200 {
		q.Limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+messageCols+` FROM messages WHERE `+
		strings.Join(where, ` AND `)+` ORDER BY `+order+` LIMIT ?`, append(args, q.Limit)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Message
	for rows.Next() {
		m, err := scanMessage(rows, nil)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func (s *DB) GetMessage(ctx context.Context, userID, id int64) (store.Message, error) {
	var raw []byte
	m, err := scanMessage(s.db.QueryRowContext(ctx,
		`SELECT `+messageCols+`, raw FROM messages WHERE id = ? AND user_id = ?`, id, userID), &raw)
	m.Raw = raw
	return m, err
}

func (s *DB) UpdateMessage(ctx context.Context, m store.Message) error {
	return changed(s.db.ExecContext(ctx, `UPDATE messages SET code = ?, link = ?, used_at = ? WHERE id = ? AND user_id = ?`,
		m.Code, m.Link, ts(m.UsedAt), m.ID, m.UserID))
}

func (s *DB) DeleteMessage(ctx context.Context, userID, id int64) error {
	return changed(s.db.ExecContext(ctx, `DELETE FROM messages WHERE id = ? AND user_id = ?`, id, userID))
}

// DeleteExpired removes messages, sessions and grants past their time.
func (s *DB) DeleteExpired(ctx context.Context, now time.Time) (int64, error) {
	s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ?`, ts(now))
	s.db.ExecContext(ctx, `DELETE FROM grants WHERE expires_at <= ?`, ts(now))
	res, err := s.db.ExecContext(ctx, `DELETE FROM messages WHERE expires_at <= ?`, ts(now))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func (s *DB) CountMessagesSince(ctx context.Context, userID int64, since time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE user_id = ? AND received_at >= ?`,
		userID, ts(since)).Scan(&n)
	return n, err
}

// --- corrections and site links ---

// AddCorrection keeps its own copy of the message so the label outlives
// the message's expiry.
func (s *DB) AddCorrection(ctx context.Context, c store.Correction) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO corrections (message_id, user_id, kind, value, raw, created_at)
		SELECT id, user_id, CAST(? AS TEXT), CAST(? AS TEXT), raw, CAST(? AS BIGINT)
		FROM messages WHERE id = ? AND user_id = ?`,
		c.Kind, c.Value, ts(time.Now()), c.MessageID, c.UserID)
	return err
}

func (s *DB) ListCorrections(ctx context.Context) ([]store.Correction, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, message_id, user_id, kind, value, raw, created_at FROM corrections ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []store.Correction
	for rows.Next() {
		var c store.Correction
		var created int64
		if err := rows.Scan(&c.ID, &c.MessageID, &c.UserID, &c.Kind, &c.Value, &c.Raw, &created); err != nil {
			return nil, err
		}
		c.CreatedAt = tm(created)
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *DB) AddSiteLink(ctx context.Context, userID int64, senderDomain, siteDomain string) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO site_links (user_id, sender_domain, site_domain) VALUES (?, ?, ?)
		ON CONFLICT DO NOTHING`, userID, senderDomain, siteDomain)
	return err
}

func (s *DB) SiteSenders(ctx context.Context, userID int64, siteDomain string) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT sender_domain FROM site_links WHERE user_id = ? AND site_domain = ?`,
		userID, siteDomain)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var d string
		if err := rows.Scan(&d); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
