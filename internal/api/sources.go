package api

import (
	"context"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/tikaydaes/codemail/internal/store"
)

// Fetcher polls linked mailboxes. It is implemented by fetch.Manager.
type Fetcher interface {
	Normalize(kind, host string) (string, error)
	Seal(secret string) ([]byte, error)
	Try(ctx context.Context, src store.Source) error
	Changed()
	Restart(id int64)
	Kick(userID int64)
	CheckNow(ctx context.Context, id int64) error
}

// sourceJSON never includes the password.
func sourceJSON(src store.Source) map[string]any {
	return map[string]any{
		"id": src.ID, "address": src.Address, "kind": src.Kind, "codes_only": src.CodesOnly,
		"host": src.Host, "username": src.Username,
		"last_checked": unix(src.LastChecked), "last_error": src.LastError, "created_at": unix(src.CreatedAt),
	}
}

func (s *Server) handleSources(w http.ResponseWriter, r *http.Request, u store.User) {
	sources, err := s.Store.ListSources(r.Context(), u.ID)
	if err != nil {
		s.failErr(w, err)
		return
	}
	out := make([]map[string]any, 0, len(sources))
	for _, src := range sources {
		out = append(out, sourceJSON(src))
	}
	ok(w, map[string]any{"sources": out, "polling": s.Fetch != nil, "max": store.MaxSources})
}

type sourceBody struct {
	Address   string
	Kind      string
	CodesOnly *bool `json:"codes_only"`
	Host      *string
	Username  *string
	Password  *string
}

// applyPolling fills in and checks the connection details of a polled
// source: it signs in to the mailbox before anything is saved.
func (s *Server) applyPolling(w http.ResponseWriter, r *http.Request, src *store.Source, body sourceBody) bool {
	if s.Fetch == nil {
		fail(w, http.StatusServiceUnavailable, "this server cannot poll mailboxes")
		return false
	}
	if body.Host != nil {
		host, err := s.Fetch.Normalize(src.Kind, *body.Host)
		if err != nil {
			fail(w, http.StatusBadRequest, err.Error())
			return false
		}
		src.Host = host
	}
	if body.Username != nil {
		src.Username = strings.TrimSpace(*body.Username)
	}
	if src.Kind == store.SourceIMAP && src.Username == "" {
		src.Username = src.Address
	}
	if body.Password != nil && *body.Password != "" {
		sealed, err := s.Fetch.Seal(*body.Password)
		if err != nil {
			s.failErr(w, err)
			return false
		}
		src.Secret = sealed
	}
	if src.Host == "" || len(src.Secret) == 0 {
		fail(w, http.StatusBadRequest, "the server and the password are both needed")
		return false
	}
	ctx, cancel := context.WithTimeout(r.Context(), 50*time.Second)
	defer cancel()
	if err := s.Fetch.Try(ctx, *src); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return false
	}
	return true
}

func (s *Server) handleAddSource(w http.ResponseWriter, r *http.Request, u store.User) {
	var body sourceBody
	if !decode(w, r, &body) {
		return
	}
	addr, err := mail.ParseAddress(strings.TrimSpace(body.Address))
	if err != nil || !strings.Contains(addr.Address, "@") {
		fail(w, http.StatusBadRequest, "enter the mailbox's email address")
		return
	}
	src := store.Source{UserID: u.ID, Address: strings.ToLower(addr.Address), Kind: body.Kind, CodesOnly: true}
	switch body.Kind {
	case store.SourceForward:
		if body.CodesOnly != nil {
			src.CodesOnly = *body.CodesOnly
		}
	case store.SourceIMAP, store.SourceJMAP:
		// Polling only ever takes messages with a code or link.
		if !s.applyPolling(w, r, &src, body) {
			return
		}
	default:
		fail(w, http.StatusBadRequest, "kind must be forward, imap or jmap")
		return
	}
	if err := s.Store.AddSource(r.Context(), &src); err != nil {
		switch err {
		case store.ErrTaken:
			fail(w, http.StatusConflict, "you have already linked that mailbox this way")
		case store.ErrLimit:
			fail(w, http.StatusConflict, "you have linked as many mailboxes as this server allows")
		default:
			s.failErr(w, err)
		}
		return
	}
	s.Store.Audit(r.Context(), u.ID, "source.add", src.Kind+" "+src.Address)
	if s.Fetch != nil {
		s.Fetch.Changed()
	}
	reply(w, http.StatusCreated, map[string]any{"source": sourceJSON(src)})
}

func (s *Server) handleUpdateSource(w http.ResponseWriter, r *http.Request, u store.User) {
	var body sourceBody
	if !decode(w, r, &body) {
		return
	}
	src, err := s.Store.GetSource(r.Context(), u.ID, pathID(r))
	if err != nil {
		s.failErr(w, err)
		return
	}
	if src.Kind == store.SourceForward {
		if body.CodesOnly != nil {
			src.CodesOnly = *body.CodesOnly
		}
	} else if body.Host != nil || body.Username != nil || body.Password != nil {
		if !s.applyPolling(w, r, &src, body) {
			return
		}
	}
	if err := s.Store.UpdateSource(r.Context(), src); err != nil {
		s.failErr(w, err)
		return
	}
	if s.Fetch != nil && src.Kind != store.SourceForward {
		s.Fetch.Restart(src.ID)
	}
	ok(w, map[string]any{"source": sourceJSON(src)})
}

func (s *Server) handleDeleteSource(w http.ResponseWriter, r *http.Request, u store.User) {
	src, err := s.Store.GetSource(r.Context(), u.ID, pathID(r))
	if err == nil {
		err = s.Store.DeleteSource(r.Context(), u.ID, src.ID)
	}
	if err != nil {
		s.failErr(w, err)
		return
	}
	s.Store.Audit(r.Context(), u.ID, "source.remove", src.Kind+" "+src.Address)
	if s.Fetch != nil {
		s.Fetch.Changed()
	}
	ok(w, map[string]any{})
}

// handleCheckSource looks at a polled mailbox now and reports the result.
func (s *Server) handleCheckSource(w http.ResponseWriter, r *http.Request, u store.User) {
	src, err := s.Store.GetSource(r.Context(), u.ID, pathID(r))
	if err != nil {
		s.failErr(w, err)
		return
	}
	if s.Fetch == nil || src.Kind == store.SourceForward {
		fail(w, http.StatusBadRequest, "only polled mailboxes can be checked")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
	defer cancel()
	// The outcome is recorded on the source either way.
	s.Fetch.CheckNow(ctx, src.ID)
	if src, err = s.Store.GetSource(r.Context(), u.ID, src.ID); err != nil {
		s.failErr(w, err)
		return
	}
	ok(w, map[string]any{"source": sourceJSON(src)})
}
