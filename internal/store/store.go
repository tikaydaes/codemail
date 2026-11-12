// Package store defines the storage interface the server depends on.
// Backends live in subpackages; sqlstore is the embedded default.
package store

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"time"
)

var (
	ErrNoRecipient = errors.New("no such recipient")
	ErrNotFound    = errors.New("not found")
	ErrInvalidName = errors.New("invalid name")
	ErrTaken       = errors.New("already taken")
	ErrLimit       = errors.New("limit reached")
	ErrNotAllowed  = errors.New("not allowed")
)

// nameRe limits usernames and alias names to DNS-label-safe names so they
// can double as mailbox names and subdomains.
var nameRe = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,30}[a-z0-9])?$`)

var reserved = map[string]bool{
	"postmaster": true, "abuse": true, "admin": true, "administrator": true,
	"root": true, "hostmaster": true, "webmaster": true, "noreply": true,
	"no-reply": true, "mailer-daemon": true, "security": true, "support": true,
	// Usernames double as subdomains (anything@username.domain), so names
	// that are commonly real hosts on a domain are kept free.
	"www": true, "mail": true, "mx": true, "smtp": true, "imap": true, "pop": true,
	"ns": true, "ns1": true, "ns2": true, "api": true, "app": true, "ftp": true,
	"autoconfig": true, "autodiscover": true, "localhost": true,
}

// ValidName reports whether s can be a username or alias.
func ValidName(s string) bool { return nameRe.MatchString(s) && !reserved[s] }

// SplitAddress lower-cases an address and splits it at the last "@".
func SplitAddress(addr string) (local, host string, ok bool) {
	addr = strings.ToLower(strings.TrimSpace(addr))
	at := strings.LastIndexByte(addr, '@')
	if at <= 0 || at == len(addr)-1 {
		return "", "", false
	}
	return addr[:at], strings.TrimSuffix(addr[at+1:], "."), true
}

type User struct {
	ID        int64
	Username  string
	Handle    []byte // WebAuthn user handle
	IsAdmin   bool
	Retention time.Duration // 0 means the server default
	CreatedAt time.Time
}

type Passkey struct {
	ID        int64
	UserID    int64
	CredID    []byte
	Data      []byte // JSON-encoded webauthn.Credential
	Name      string
	CreatedAt time.Time
	LastUsed  time.Time
}

// Session is one signed-in browser.
type Session struct {
	ID        int64
	UserID    int64
	Label     string // browser and system, from the User-Agent
	CreatedAt time.Time
	LastSeen  time.Time
	ExpiresAt time.Time
}

// Device is a native app or extension holding OAuth tokens.
type Device struct {
	ID            int64
	UserID        int64
	Name          string
	Platform      string
	AccessHash    []byte
	RefreshHash   []byte
	AccessExpires time.Time
	CreatedAt     time.Time
	LastSeen      time.Time
}

// Grant is a short-lived one-time code: an OAuth authorization code or a
// QR pairing code.
type Grant struct {
	CodeHash    []byte
	Kind        string // "authorization" or "pairing"
	UserID      int64
	ClientID    string
	RedirectURI string
	Challenge   string // PKCE S256 challenge
	DeviceName  string // set when a pairing code is claimed by an app
	Platform    string
	Confirmed   bool // pairing only: the user approved the device
	ExpiresAt   time.Time
}

type Invite struct {
	Code      string
	MaxUses   int // 0 means unlimited
	Uses      int
	ExpiresAt time.Time // zero means never
	CreatedBy int64
	CreatedAt time.Time
}

// Domain is a domain the server accepts mail for. OwnerID 0 means a shared
// domain run by the admins; otherwise every address on it belongs to the
// owner.
type Domain struct {
	ID              int64
	Name            string
	OwnerID         int64
	Verified        bool
	VerifyToken     string
	AllowMailbox    bool // username(+tag)@domain
	AllowAliases    bool // claimed and generated aliases
	AllowSubdomains bool // anything@username.domain
	MaxAliases      int  // claimed aliases per user, 0 = unlimited
	MaxGenerated    int  // generated aliases per user, 0 = unlimited
	LastChecked     time.Time
	FailingSince    time.Time
	CreatedAt       time.Time
}

type Alias struct {
	ID             int64
	DomainID       int64
	Domain         string // filled on read
	Local          string
	UserID         int64
	Label          string
	Generated      bool
	ExpectedSender string // generated aliases: the first sender's domain
	CreatedAt      time.Time
}

func (a Alias) Address() string { return a.Local + "@" + a.Domain }

// Recipient is who an address resolves to.
type Recipient struct {
	UserID int64
	Alias  *Alias // set when the address is a claimed or generated alias
}

type Message struct {
	ID         int64
	UserID     int64
	Rcpt       string
	MailFrom   string
	FromHdr    string
	Subject    string
	Service    string
	Code       string
	Link       string
	Confidence float64
	Auth       string // "pass", "fail" or "none"
	AuthDetail string
	Flags      string // space-separated, e.g. "unexpected_sender"
	Raw        []byte // only filled by GetMessage
	ReceivedAt time.Time
	ExpiresAt  time.Time
	UsedAt     time.Time
}

type MessageQuery struct {
	UserID int64
	Kind   string // "code" (has a code or link), "other", or "" for all
	Search string
	Before int64 // only messages with a smaller ID; 0 = newest
	After  int64 // only messages with a larger ID, oldest first
	Limit  int
}

type Correction struct {
	ID        int64
	MessageID int64
	UserID    int64
	Kind      string // "code" or "link"
	Value     string // empty means "there is none"
	Raw       []byte // filled by ListCorrections
	CreatedAt time.Time
}

// AuditEntry is one recorded administrative or security action.
type AuditEntry struct {
	ID        int64
	ActorID   int64
	ActorName string // empty if the account has since been deleted
	Action    string
	Target    string
	At        time.Time
}

type Stats struct {
	Users, Messages, Corrections int
	Bytes                        int64
	LastMessage                  time.Time
}

// Store is everything the server persists. Methods return ErrNotFound when
// a single requested row does not exist.
type Store interface {
	Close() error

	CreateUser(ctx context.Context, u *User) error
	UserByID(ctx context.Context, id int64) (User, error)
	UserByName(ctx context.Context, username string) (User, error)
	UserByHandle(ctx context.Context, handle []byte) (User, error)
	ListUsers(ctx context.Context) ([]User, error)
	UpdateUser(ctx context.Context, u User) error
	DeleteUser(ctx context.Context, id int64) error

	AddPasskey(ctx context.Context, p *Passkey) error
	PasskeysByUser(ctx context.Context, userID int64) ([]Passkey, error)
	UpdatePasskey(ctx context.Context, p Passkey) error
	DeletePasskey(ctx context.Context, userID, id int64) error

	ReplaceRecoveryCodes(ctx context.Context, userID int64, hashes [][]byte) error
	AddRecoveryCode(ctx context.Context, userID int64, hash []byte) error
	UseRecoveryCode(ctx context.Context, userID int64, hash []byte) (bool, error)

	CreateSession(ctx context.Context, hash []byte, s *Session) error
	SessionUser(ctx context.Context, hash []byte, now time.Time) (User, Session, error)
	ListSessions(ctx context.Context, userID int64, now time.Time) ([]Session, error)
	DeleteSession(ctx context.Context, hash []byte) error
	DeleteSessionByID(ctx context.Context, userID, id int64) error

	CreateDevice(ctx context.Context, d *Device) error
	DeviceByAccess(ctx context.Context, hash []byte, now time.Time) (Device, error)
	// DeviceByRefresh also accepts the device's previous refresh token for
	// a short time after rotation.
	DeviceByRefresh(ctx context.Context, hash []byte, now time.Time) (Device, error)
	RotateDevice(ctx context.Context, d Device, prevRefresh []byte) error
	ListDevices(ctx context.Context, userID int64) ([]Device, error)
	DeleteDevice(ctx context.Context, userID, id int64) error

	PutGrant(ctx context.Context, g Grant) error
	GetGrant(ctx context.Context, hash []byte) (Grant, error)
	DeleteGrant(ctx context.Context, hash []byte) error

	CreateInvite(ctx context.Context, inv Invite) error
	ListInvites(ctx context.Context) ([]Invite, error)
	DeleteInvite(ctx context.Context, code string) error
	// UseInvite counts one use, or fails with ErrNotFound if the code is
	// unknown, expired or used up.
	UseInvite(ctx context.Context, code string, now time.Time) error

	AddDomain(ctx context.Context, d *Domain) error
	DomainByID(ctx context.Context, id int64) (Domain, error)
	DomainByName(ctx context.Context, name string) (Domain, error)
	// ListDomains returns shared domains when ownerID is 0, else that
	// user's domains. AllDomains returns every domain.
	ListDomains(ctx context.Context, ownerID int64) ([]Domain, error)
	AllDomains(ctx context.Context) ([]Domain, error)
	UpdateDomain(ctx context.Context, d Domain) error
	DeleteDomain(ctx context.Context, id int64) error

	// ClaimAlias enforces the domain's rules and limits.
	ClaimAlias(ctx context.Context, a *Alias) error
	ListAliases(ctx context.Context, userID int64) ([]Alias, error)
	DeleteAlias(ctx context.Context, userID, id int64) error
	SetAliasSender(ctx context.Context, id int64, sender string) error
	SetAliasLabel(ctx context.Context, userID, id int64, label string) error
	Resolve(ctx context.Context, addr string) (Recipient, error)

	SaveMessage(ctx context.Context, m *Message) error
	ListMessages(ctx context.Context, q MessageQuery) ([]Message, error)
	GetMessage(ctx context.Context, userID, id int64) (Message, error)
	// UpdateMessage stores Code, Link and UsedAt.
	UpdateMessage(ctx context.Context, m Message) error
	DeleteMessage(ctx context.Context, userID, id int64) error
	DeleteExpired(ctx context.Context, now time.Time) (int64, error)
	CountMessagesSince(ctx context.Context, userID int64, since time.Time) (int, error)

	AddCorrection(ctx context.Context, c Correction) error
	ListCorrections(ctx context.Context) ([]Correction, error)

	// Site links record that a user used a sender's codes on a site.
	AddSiteLink(ctx context.Context, userID int64, senderDomain, siteDomain string) error
	SiteSenders(ctx context.Context, userID int64, siteDomain string) ([]string, error)

	GetSetting(ctx context.Context, key string) (string, error) // "" if unset
	SetSetting(ctx context.Context, key, value string) error

	Audit(ctx context.Context, actor int64, action, target string)
	// ListAudit returns the newest entries, optionally only those older
	// than the entry with ID before.
	ListAudit(ctx context.Context, before int64, limit int) ([]AuditEntry, error)
	Stats(ctx context.Context) (Stats, error)
}
