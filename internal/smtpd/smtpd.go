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
	auth := mailauth.Result{Verdict: mailauth.None, Domain: mailauth.FromDomain(in.From)}
	if s.b.Auth != nil {
		auth = s.b.Auth.Check(s.remote, s.helo, s.from, in.From, raw)
		if auth.Verdict == mailauth.Fail {
			if v, _ := s.b.Store.GetSetting(ctx, "reject_auth_fail"); v == "1" {
				return errAuthFail
			}
		}
	}
	res := s.b.Model.Extract(in)
	confidence := 0.0
	for _, c := range res.Candidates {
		if (c.Value == res.Code || c.Value == res.Link) && c.Score > confidence {
			confidence = c.Score
		}
	}
	now := time.Now()
	for uid, rc := range s.rcpts {
		retention := s.b.Cfg.Retention
		if u, err := s.b.Store.UserByID(ctx, uid); err == nil && u.Retention > 0 {
			retention = u.Retention
		}
		service, flags := res.Service, ""
		if a := rc.alias; a != nil {
			if a.Label != "" {
				service = a.Label
			}
			// A generated alias belongs to one service. Remember its first
			// sender and flag anyone else: the address has leaked.
			if a.Generated && auth.Domain != "" {
				switch a.ExpectedSender {
				case "":
					s.b.Store.SetAliasSender(ctx, a.ID, auth.Domain)
				case auth.Domain:
				default:
					flags = "unexpected_sender"
				}
			}
		}
		m := &store.Message{
			UserID: uid, Rcpt: rc.addr, MailFrom: s.from, FromHdr: in.From, Subject: in.Subject,
			Service: service, Code: res.Code, Link: res.Link, Confidence: confidence,
			Auth: auth.Verdict, AuthDetail: auth.Detail, Flags: flags,
			Raw: raw, ReceivedAt: now, ExpiresAt: now.Add(retention),
		}
		if err := s.b.Store.SaveMessage(ctx, m); err != nil {
			s.b.Log.Error("save message", "rcpt", rc.addr, "err", err)
			return errTemp
		}
		s.b.Hub.Publish(hub.Event{UserID: uid, Type: "message", MessageID: m.ID})
		s.b.Log.Info("message stored", "id", m.ID, "rcpt", rc.addr, "service", service,
			"has_code", res.Code != "", "has_link", res.Link != "", "auth", auth.Verdict)
	}
	return nil
}

func (s *session) Reset()        { s.from, s.rcpts = "", nil }
func (s *session) Logout() error { return nil }
