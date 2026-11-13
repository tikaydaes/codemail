// Package smtpd is the inbound SMTP receiver. It accepts mail only for
// addresses that resolve to a user, checks the sender, extracts the code
// or link, stores the message and notifies the user's connected clients.
package smtpd

import (
	"context"
	"crypto/tls"
	"errors"
	"io"
	"log/slog"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-smtp"

	"github.com/tikaydaes/codemail/internal/extract"
	"github.com/tikaydaes/codemail/internal/hub"
	"github.com/tikaydaes/codemail/internal/ingest"
	"github.com/tikaydaes/codemail/internal/mailauth"
	"github.com/tikaydaes/codemail/internal/mailmsg"
	"github.com/tikaydaes/codemail/internal/store"
)

type Config struct {
	Hostname   string        // name announced in the SMTP banner
	Retention  time.Duration // default; a user's own setting overrides it
	MaxBytes   int64
	TLS        *tls.Config // enables STARTTLS when set
	PerIPRate  int         // connections per minute from one address, 0 = unlimited
	DailyLimit int         // messages per user per day, 0 = unlimited
}

type Backend struct {
	Store store.Store
	Hub   *hub.Hub
	Model extract.Model
	Auth  mailauth.Checker // nil skips sender checks
	Cfg   Config
	Log   *slog.Logger

	mu     sync.Mutex
	recent map[string][]time.Time
}

// NewServer returns a server ready for Serve or ListenAndServe.
func NewServer(b *Backend) *smtp.Server {
	s := smtp.NewServer(b)
	s.Domain = b.Cfg.Hostname
	s.MaxMessageBytes = b.Cfg.MaxBytes
	s.MaxRecipients = 50
	s.ReadTimeout = 60 * time.Second
	s.WriteTimeout = 60 * time.Second
	s.TLSConfig = b.Cfg.TLS
	return s
}

var (
	errBusy     = &smtp.SMTPError{Code: 421, EnhancedCode: smtp.EnhancedCode{4, 7, 0}, Message: "too many connections, try again later"}
	errTemp     = &smtp.SMTPError{Code: 451, EnhancedCode: smtp.EnhancedCode{4, 3, 0}, Message: "temporary failure"}
	errNoRcpt   = &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 1, 1}, Message: "no such recipient"}
	errOverQuo  = &smtp.SMTPError{Code: 452, EnhancedCode: smtp.EnhancedCode{4, 2, 2}, Message: "recipient has received too much mail today"}
	errAuthFail = &smtp.SMTPError{Code: 550, EnhancedCode: smtp.EnhancedCode{5, 7, 1}, Message: "message failed sender authentication"}
)

func (b *Backend) NewSession(c *smtp.Conn) (smtp.Session, error) {
	s := &session{b: b, helo: c.Hostname()}
	if tcp, ok := c.Conn().RemoteAddr().(*net.TCPAddr); ok {
		s.remote = tcp.IP
	}
	if b.Cfg.PerIPRate > 0 && s.remote != nil && !b.allow(s.remote.String()) {
		return nil, errBusy
	}
	return s, nil
}

// allow is a sliding one-minute window per remote address.
func (b *Backend) allow(ip string) bool {
	now := time.Now()
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.recent == nil {
		b.recent = map[string][]time.Time{}
	}
	if len(b.recent) > 10000 {
		b.recent = map[string][]time.Time{} // crude bound on memory
	}
	keep := b.recent[ip][:0]
	for _, t := range b.recent[ip] {
		if now.Sub(t) < time.Minute {
			keep = append(keep, t)
		}
	}
	if len(keep) >= b.Cfg.PerIPRate {
		b.recent[ip] = keep
		return false
	}
	b.recent[ip] = append(keep, now)
	return true
}

type rcpt struct {
	addr  string
	alias *store.Alias
}

type session struct {
	b      *Backend
	remote net.IP
	helo   string
	from   string
	rcpts  map[int64]rcpt // one copy per user
}

func (s *session) Mail(from string, _ *smtp.MailOptions) error {
	s.from = from
	s.rcpts = map[int64]rcpt{}
	return nil
}

func (s *session) Rcpt(to string, _ *smtp.RcptOptions) error {
	ctx := context.Background()
	r, err := s.b.Store.Resolve(ctx, to)
	if errors.Is(err, store.ErrNoRecipient) {
		return errNoRcpt
	}
	if err != nil {
		s.b.Log.Error("resolve recipient", "rcpt", to, "err", err)
		return errTemp
	}
	if _, dup := s.rcpts[r.UserID]; dup {
		return nil
	}
	if s.b.Cfg.DailyLimit > 0 {
		n, err := s.b.Store.CountMessagesSince(ctx, r.UserID, time.Now().Add(-24*time.Hour))
		if err != nil {
			return errTemp
		}
		if n >= s.b.Cfg.DailyLimit {
			return errOverQuo
		}
	}
	s.rcpts[r.UserID] = rcpt{addr: strings.ToLower(to), alias: r.Alias}
	return nil
}

func (s *session) Data(r io.Reader) error {
	ctx := context.Background()
	raw, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	in, err := mailmsg.Parse(raw)
	if err != nil {
		// Keep the message anyway so the user can still read it.
		s.b.Log.Warn("parse message", "from", s.from, "err", err)
	}
	headers := mailmsg.Headers(raw)
	pipe := s.b.pipeline()

	// Evaluated at most once each, and only if some recipient needs it.
	var direct, relayed *mailauth.Result
	var found, foundInner *ingest.Found
	check := func(viaMailbox bool, from string) mailauth.Result {
		switch {
		case s.b.Auth == nil:
			return mailauth.Result{Verdict: mailauth.None, Domain: mailauth.FromDomain(from)}
		case viaMailbox:
			if relayed == nil {
				r := s.b.Auth.CheckRelayed(s.remote, from, raw)
				relayed = &r
			}
			return *relayed
		}
		if direct == nil {
			r := s.b.Auth.Check(s.remote, s.helo, s.from, from, raw)
			direct = &r
		}
		return *direct
	}

	// Decide every recipient's copy first, so that a rejection is for the
	// whole message and nothing is stored twice on a retry.
	type copyFor struct {
		d ingest.Delivery
		f ingest.Found
	}
	var copies []copyFor
	for uid, rc := range s.rcpts {
		d := ingest.Delivery{UserID: uid, Rcpt: rc.addr, Alias: rc.alias, MailFrom: s.from, In: in, Raw: raw}
		src, byHand := s.b.forwardedFrom(ctx, uid, s.from, in.From, headers)
		switch {
		case src != nil && byHand:
			// The user's own mail program wrapped the original, so nothing
			// about the original sender can be checked.
			d.Via = ingest.FlagForwarded
			d.In, _ = mailmsg.Unforward(in)
			d.Auth = mailauth.Result{Verdict: mailauth.None, Domain: mailauth.FromDomain(d.In.From),
				Detail: "forwarded by hand from " + src.Address + "; the original sender cannot be verified"}
			if foundInner == nil {
				f := pipe.Extract(d.In)
				foundInner = &f
			}
			copies = append(copies, copyFor{d, *foundInner})
		default:
			if src != nil {
				d.Via = ingest.FlagForwarded
			}
			d.Auth = check(src != nil, in.From)
			if d.Auth.Verdict == mailauth.Fail && src == nil {
				if v, _ := s.b.Store.GetSetting(ctx, "reject_auth_fail"); v == "1" {
					return errAuthFail
				}
			}
			if found == nil {
				f := pipe.Extract(in)
				found = &f
			}
			copies = append(copies, copyFor{d, *found})
		}
		// A mailbox that forwards everything would otherwise fill the
		// account with ordinary mail. Accept it, but keep only codes.
		if src != nil && src.CodesOnly && !copies[len(copies)-1].f.HasCode() {
			copies = copies[:len(copies)-1]
			s.b.Log.Info("forwarded message without a code dropped", "rcpt", rc.addr, "source", src.Address)
		}
	}
	for _, c := range copies {
		if _, err := pipe.Deliver(ctx, c.d, c.f); err != nil {
			s.b.Log.Error("save message", "rcpt", c.d.Rcpt, "err", err)
			return errTemp
		}
	}
	return nil
}

func (b *Backend) pipeline() *ingest.Pipeline {
	return &ingest.Pipeline{Store: b.Store, Hub: b.Hub, Model: b.Model, Log: b.Log, Retention: b.Cfg.Retention}
}

// The headers a forwarding mailbox leaves its own address in.
var forwardHeaders = []string{"Delivered-To", "X-Forwarded-For", "X-Forwarded-To", "X-Original-To", "Resent-From",
	"Resent-To", "X-Forwarded-Encrypted", "To", "Cc"}

// forwardedFrom reports whether a message for the user came through one
// of the mailboxes they have linked, and whether they forwarded it by
// hand (so that it is From their own address).
func (b *Backend) forwardedFrom(ctx context.Context, userID int64, mailFrom, fromHdr string, headers []mailmsg.Field) (src *store.Source, byHand bool) {
	sources, err := b.Store.ListSources(ctx, userID)
	if err != nil || len(sources) == 0 {
		return nil, false
	}
	from := mailmsg.Address(fromHdr)
	for i := range sources {
		if from != "" && from == strings.ToLower(sources[i].Address) {
			return &sources[i], true
		}
	}
	for i := range sources {
		addr := sources[i].Address
		if mailmsg.Mentions(mailFrom, addr) {
			return &sources[i], false
		}
		for _, f := range headers {
			for _, name := range forwardHeaders {
				if f.Is(name) && mailmsg.Mentions(f.Value, addr) {
					return &sources[i], false
				}
			}
		}
	}
	return nil, false
}

func (s *session) Reset()        { s.from, s.rcpts = "", nil }
func (s *session) Logout() error { return nil }
