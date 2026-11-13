// Package fetch polls the mailboxes users have linked, over IMAP or JMAP,
// and takes in the messages that carry a sign-in code or link. Nothing
// else is copied, and nothing in the mailbox is changed: folders are
// opened read-only and messages are not marked as read.
package fetch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/tikaydaes/codemail/internal/ingest"
	"github.com/tikaydaes/codemail/internal/mailauth"
	"github.com/tikaydaes/codemail/internal/mailmsg"
	"github.com/tikaydaes/codemail/internal/store"
)

// maxPerPoll bounds how many messages one look at a mailbox takes in.
const maxPerPoll = 50

type Config struct {
	Interval     time.Duration // between looks at each mailbox; 0 means 20 seconds
	MaxBytes     int64         // larger messages are skipped
	AllowPrivate bool          // connect to private and local addresses too
	Insecure     bool          // development: allow IMAP and JMAP without TLS
}

type Manager struct {
	Store  store.Store
	Pipe   *ingest.Pipeline
	Auth   mailauth.Checker // nil skips sender checks
	Sealer *Sealer
	Log    *slog.Logger
	Cfg    Config

	mu      sync.Mutex
	workers map[int64]*worker
	changed chan struct{}
}

type worker struct {
	userID int64
	stop   context.CancelFunc
	kick   chan chan error
	kicked time.Time // guarded by Manager.mu
}

func (m *Manager) interval() time.Duration {
	if m.Cfg.Interval > 0 {
		return m.Cfg.Interval
	}
	return 20 * time.Second
}

func (m *Manager) httpClient() *http.Client {
	return &http.Client{
		Timeout:   60 * time.Second,
		Transport: &http.Transport{DialContext: dialer(m.Cfg.AllowPrivate).DialContext, TLSHandshakeTimeout: 15 * time.Second},
	}
}

// Seal encrypts a password or token for storage.
func (m *Manager) Seal(secret string) ([]byte, error) { return m.Sealer.Seal(secret) }

// Normalize checks and tidies the server address a user typed for a
// source of the given kind.
func (m *Manager) Normalize(kind, host string) (string, error) {
	host = strings.TrimSpace(host)
	switch kind {
	case store.SourceIMAP:
		host = strings.TrimPrefix(strings.TrimPrefix(host, "imaps://"), "imap://")
		if host == "" || strings.ContainsAny(host, "/ @") {
			return "", errors.New("enter the IMAP server's name, such as imap.example.com")
		}
		if !strings.Contains(host, ":") {
			host += ":993"
		}
		return strings.ToLower(host), nil
	case store.SourceJMAP:
		if !strings.Contains(host, "://") {
			host = "https://" + host
		}
		u, err := url.Parse(host)
		if err != nil || u.Hostname() == "" || u.Scheme != "https" && !(m.Cfg.Insecure && u.Scheme == "http") {
			return "", errors.New("enter the JMAP server's address, such as https://mail.example.com")
		}
		if u.Path == "" || u.Path == "/" {
			u.Path = "/.well-known/jmap"
		}
		return u.String(), nil
	}
	return "", errors.New("unknown kind of mailbox")
}

// session is a signed-in connection of either kind.
type session interface {
	poll(ctx context.Context, state string, maxBytes int64) (msgs [][]byte, next string, err error)
	close()
}

type imapPoller struct{ *imapSession }

func (p imapPoller) poll(_ context.Context, state string, maxBytes int64) ([][]byte, string, error) {
	var st imapState
	json.Unmarshal([]byte(state), &st)
	msgs, next, err := p.imapSession.poll(st, maxBytes)
	b, _ := json.Marshal(next)
	return msgs, string(b), err
}

type jmapPoller struct{ *jmapSession }

func (p jmapPoller) poll(ctx context.Context, state string, maxBytes int64) ([][]byte, string, error) {
	var st jmapState
	json.Unmarshal([]byte(state), &st)
	msgs, next, err := p.jmapSession.poll(ctx, st, maxBytes)
	b, _ := json.Marshal(next)
	return msgs, string(b), err
}

func (jmapPoller) close() {}

func (m *Manager) connect(ctx context.Context, src store.Source) (session, error) {
	secret, err := m.Sealer.Open(src.Secret)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	switch src.Kind {
	case store.SourceIMAP:
		s, err := imapConnect(ctx, dialer(m.Cfg.AllowPrivate), src.Host, src.Username, secret, m.Cfg.Insecure)
		if err != nil {
			return nil, err
		}
		return imapPoller{s}, nil
	case store.SourceJMAP:
		s, err := jmapConnect(ctx, m.httpClient(), src.Host, src.Username, secret)
		if err != nil {
			return nil, err
		}
		return jmapPoller{s}, nil
	}
	return nil, fmt.Errorf("mailbox kind %q cannot be polled", src.Kind)
}

// Try signs in to a mailbox once, to check what the user entered.
func (m *Manager) Try(ctx context.Context, src store.Source) error {
	s, err := m.connect(ctx, src)
	if err != nil {
		return err
	}
	s.close()
	return nil
}

// Changed tells the manager that sources were added, edited or removed.
func (m *Manager) Changed() {
	select {
	case m.changed <- struct{}{}:
	default:
	}
}

// Kick looks at all of a user's mailboxes now instead of at the next
// interval. Clients call it when a site has just asked for a code.
func (m *Manager) Kick(userID int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, w := range m.workers {
		// A client waiting for a code asks every few seconds; that is as
		// often as a mailbox needs looking at.
		if w.userID == userID && time.Since(w.kicked) > 3*time.Second {
			w.kicked = time.Now()
			select {
			case w.kick <- nil:
			default:
			}
		}
	}
}

// CheckNow looks at one mailbox now and reports how that went.
func (m *Manager) CheckNow(ctx context.Context, id int64) error {
	m.mu.Lock()
	w := m.workers[id]
	m.mu.Unlock()
	if w == nil {
		return errors.New("that mailbox is not being polled yet; try again in a moment")
	}
	done := make(chan error, 1)
	select {
	case w.kick <- done:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Run polls until ctx ends. One worker per mailbox keeps its connection
// open between looks.
func (m *Manager) Run(ctx context.Context) {
	m.mu.Lock()
	m.workers = map[int64]*worker{}
	m.changed = make(chan struct{}, 1)
	m.mu.Unlock()
	refresh := time.NewTicker(time.Minute)
	defer refresh.Stop()
	for {
		m.sync(ctx)
		select {
		case <-ctx.Done():
			m.mu.Lock()
			for _, w := range m.workers {
				w.stop()
			}
			m.mu.Unlock()
			return
		case <-m.changed:
		case <-refresh.C:
		}
	}
}

// sync starts workers for new sources and stops those of removed or
// edited ones.
func (m *Manager) sync(ctx context.Context) {
	sources, err := m.Store.PolledSources(ctx)
	if err != nil {
		m.Log.Error("list mailboxes to poll", "err", err)
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	want := map[int64]bool{}
	for _, src := range sources {
		want[src.ID] = true
		if m.workers[src.ID] == nil {
			wctx, stop := context.WithCancel(ctx)
			w := &worker{userID: src.UserID, stop: stop, kick: make(chan chan error, 1)}
			m.workers[src.ID] = w
			go m.work(wctx, src.UserID, src.ID, w)
		}
	}
	for id, w := range m.workers {
		if !want[id] {
			w.stop()
			delete(m.workers, id)
		}
	}
}

// Restart drops a mailbox's worker so that the next sync starts a fresh
// one with the edited settings.
func (m *Manager) Restart(id int64) {
	m.mu.Lock()
	if w := m.workers[id]; w != nil {
		w.stop()
		delete(m.workers, id)
	}
	m.mu.Unlock()
	m.Changed()
}

func (m *Manager) work(ctx context.Context, userID, id int64, w *worker) {
	var sess session
	defer func() {
		if sess != nil {
			sess.close()
		}
	}()
	failures := 0
	for {
		err := m.look(ctx, userID, id, &sess)
		if err != nil {
			failures++
			if sess != nil {
				sess.close()
				sess = nil
			}
		} else {
			failures = 0
		}
		// Back off from a mailbox that keeps failing, up to 15 minutes.
		wait := m.interval()
		for i := 0; i < failures && wait < 15*time.Minute; i++ {
			wait *= 2
		}
		if wait > 15*time.Minute {
			wait = 15 * time.Minute
		}
		var reply chan error
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
			continue
		case reply = <-w.kick:
		}
		if reply != nil {
			// Someone is waiting to hear how this look goes.
			err := m.look(ctx, userID, id, &sess)
			if err != nil && sess != nil {
				sess.close()
				sess = nil
			}
			reply <- err
			select {
			case <-ctx.Done():
				return
			case <-time.After(m.interval()):
			}
		}
	}
}

// look checks one mailbox once, reusing the open connection if there is
// one, and records the outcome.
func (m *Manager) look(ctx context.Context, userID, id int64, sess *session) error {
	src, err := m.Store.GetSource(ctx, userID, id)
	if err != nil {
		return err // removed; sync will stop this worker
	}
	if *sess == nil {
		if *sess, err = m.connect(ctx, src); err != nil {
			m.Store.SetSourceState(ctx, id, src.State, time.Now(), err.Error())
			return err
		}
	}
	msgs, next, err := (*sess).poll(ctx, src.State, m.Cfg.MaxBytes)
	if err != nil {
		m.Store.SetSourceState(ctx, id, src.State, time.Now(), err.Error())
		return err
	}
	kept := 0
	for _, raw := range msgs {
		if m.take(ctx, src, raw) {
			kept++
		}
	}
	if len(msgs) > 0 {
		m.Log.Info("mailbox polled", "source", id, "new", len(msgs), "kept", kept)
	}
	return m.Store.SetSourceState(ctx, id, next, time.Now(), "")
}

// take stores one fetched message if it carries a code or link.
func (m *Manager) take(ctx context.Context, src store.Source, raw []byte) bool {
	in, err := mailmsg.Parse(raw)
	if err != nil {
		return false
	}
	found := m.Pipe.Extract(in)
	if !found.HasCode() {
		return false
	}
	auth := mailauth.Result{Verdict: mailauth.None, Domain: mailauth.FromDomain(in.From)}
	if m.Auth != nil {
		auth = m.Auth.CheckRelayed(nil, in.From, raw)
	}
	mailFrom := ""
	for _, f := range mailmsg.Headers(raw) {
		if f.Is("Return-Path") {
			mailFrom = strings.Trim(f.Value, "<> ")
			break
		}
	}
	_, err = m.Pipe.Deliver(ctx, ingest.Delivery{
		UserID: src.UserID, Rcpt: src.Address, MailFrom: mailFrom, In: in, Raw: raw, Auth: auth, Via: ingest.FlagFetched,
	}, found)
	if err != nil {
		m.Log.Error("save fetched message", "source", src.ID, "err", err)
	}
	return err == nil
}
