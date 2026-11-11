// Package api is the HTTP interface used by every client: the web app,
// native apps and the browser extension.
package api

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/tikaydaes/codemail/internal/dnscheck"
	"github.com/tikaydaes/codemail/internal/extract"
	"github.com/tikaydaes/codemail/internal/hub"
	"github.com/tikaydaes/codemail/internal/store"
)

type Config struct {
	Origin           string        // public base URL, e.g. https://mail.example.com
	MailHost         string        // hostname that MX records must point at
	DefaultRetention time.Duration // used when neither user nor admin set one
	TrustProxy       bool          // a reverse proxy in front sets X-Real-IP / X-Forwarded-For
}

type Server struct {
	Store store.Store
	Hub   *hub.Hub
	Model extract.Model
	DNS   dnscheck.Resolver
	Log   *slog.Logger
	Web   fs.FS // the built web app
	cfg   Config
	wa    *webauthn.WebAuthn

	mu         sync.Mutex
	ceremonies map[string]ceremony    // passkey ceremonies in progress
	attempts   map[string][]time.Time // rate limiting of sign-in endpoints
}

func New(cfg Config, st store.Store, h *hub.Hub, model extract.Model, web fs.FS, log *slog.Logger) (*Server, error) {
	u, err := url.Parse(cfg.Origin)
	if err != nil || u.Hostname() == "" {
		return nil, errors.New("api: origin must be a URL such as https://mail.example.com")
	}
	cfg.Origin = strings.TrimSuffix(cfg.Origin, "/")
	wa, err := webauthn.New(&webauthn.Config{
		RPID:          u.Hostname(),
		RPDisplayName: "Codemail",
		RPOrigins:     []string{cfg.Origin},
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementRequired,
			UserVerification: protocol.VerificationRequired,
		},
	})
	if err != nil {
		return nil, err
	}
	return &Server{
		Store: st, Hub: h, Model: model, DNS: net.DefaultResolver, Log: log, Web: web, cfg: cfg, wa: wa,
		ceremonies: map[string]ceremony{}, attempts: map[string][]time.Time{},
	}, nil
}

// Handler returns the routes. Everything under /api/v1 speaks JSON.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	a := "/api/v1"

	mux.HandleFunc("GET "+a+"/server", s.handleServer)
	mux.HandleFunc("POST "+a+"/passkey/register/begin", s.limited(s.handleRegisterBegin))
	mux.HandleFunc("POST "+a+"/passkey/register/finish", s.handleRegisterFinish)
	mux.HandleFunc("POST "+a+"/passkey/login/begin", s.limited(s.handleLoginBegin))
	mux.HandleFunc("POST "+a+"/passkey/login/finish", s.handleLoginFinish)
	mux.HandleFunc("POST "+a+"/recovery", s.limited(s.handleRecovery))
	mux.HandleFunc("POST "+a+"/logout", s.handleLogout)

	mux.HandleFunc("GET "+a+"/me", s.auth(s.handleMe))
	mux.HandleFunc("PATCH "+a+"/me", s.session(s.handleUpdateMe))
	mux.HandleFunc("DELETE "+a+"/me", s.session(s.handleDeleteMe))
	mux.HandleFunc("GET "+a+"/passkeys", s.session(s.handlePasskeys))
	mux.HandleFunc("DELETE "+a+"/passkeys/{id}", s.session(s.handleDeletePasskey))
	mux.HandleFunc("POST "+a+"/recovery-codes", s.session(s.handleNewRecoveryCodes))

	mux.HandleFunc("GET "+a+"/messages", s.auth(s.handleMessages))
	mux.HandleFunc("GET "+a+"/messages/{id}", s.auth(s.handleMessage))
	mux.HandleFunc("GET "+a+"/messages/{id}/raw", s.auth(s.handleMessageRaw))
	mux.HandleFunc("GET "+a+"/messages/{id}/candidates", s.auth(s.handleCandidates))
	mux.HandleFunc("PATCH "+a+"/messages/{id}", s.auth(s.handleUpdateMessage))
	mux.HandleFunc("DELETE "+a+"/messages/{id}", s.auth(s.handleDeleteMessage))
	mux.HandleFunc("POST "+a+"/messages/{id}/correction", s.auth(s.handleCorrection))
	mux.HandleFunc("GET "+a+"/events", s.auth(s.handleEvents))
	mux.HandleFunc("GET "+a+"/codes/for-site", s.auth(s.handleForSite))
	mux.HandleFunc("POST "+a+"/site-links", s.auth(s.handleSiteLink))

	mux.HandleFunc("GET "+a+"/addresses", s.auth(s.handleAddresses))
	mux.HandleFunc("POST "+a+"/aliases", s.auth(s.handleClaimAlias))
	mux.HandleFunc("POST "+a+"/aliases/generate", s.auth(s.handleGenerateAlias))
	mux.HandleFunc("DELETE "+a+"/aliases/{id}", s.auth(s.handleDeleteAlias))
	mux.HandleFunc("POST "+a+"/domains", s.session(s.handleAddDomain))
	mux.HandleFunc("POST "+a+"/domains/{id}/check", s.session(s.handleCheckDomain))
	mux.HandleFunc("DELETE "+a+"/domains/{id}", s.session(s.handleDeleteDomain))

	mux.HandleFunc("GET "+a+"/devices", s.auth(s.handleDevices))
	mux.HandleFunc("DELETE "+a+"/devices/{id}", s.session(s.handleDeleteDevice))
	mux.HandleFunc("POST "+a+"/devices/pairing-code", s.session(s.handleNewPairing))
	mux.HandleFunc("GET "+a+"/devices/pairing-code/{id}", s.session(s.handlePairingStatus))
	mux.HandleFunc("GET "+a+"/devices/pairing-code/{id}/qr.png", s.session(s.handlePairingQR))
	mux.HandleFunc("POST "+a+"/devices/pairing-code/{id}/confirm", s.session(s.handleConfirmPairing))
	mux.HandleFunc("POST "+a+"/oauth/approve", s.session(s.handleOAuthApprove))
	mux.HandleFunc("POST /oauth/token", s.limited(s.handleOAuthToken))
	mux.HandleFunc("POST /oauth/revoke", s.handleOAuthRevoke)
	mux.HandleFunc("GET /.well-known/oauth-authorization-server", s.handleOAuthMetadata)

	mux.HandleFunc("GET "+a+"/admin/status", s.admin(s.handleAdminStatus))
	mux.HandleFunc("GET "+a+"/admin/settings", s.admin(s.handleAdminSettings))
	mux.HandleFunc("PUT "+a+"/admin/settings", s.admin(s.handleAdminPutSettings))
	mux.HandleFunc("GET "+a+"/admin/invites", s.admin(s.handleAdminInvites))
	mux.HandleFunc("POST "+a+"/admin/invites", s.admin(s.handleAdminNewInvite))
	mux.HandleFunc("DELETE "+a+"/admin/invites/{code}", s.admin(s.handleAdminDeleteInvite))
	mux.HandleFunc("GET "+a+"/admin/users", s.admin(s.handleAdminUsers))
	mux.HandleFunc("PATCH "+a+"/admin/users/{id}", s.admin(s.handleAdminUpdateUser))
	mux.HandleFunc("DELETE "+a+"/admin/users/{id}", s.admin(s.handleAdminDeleteUser))
	mux.HandleFunc("POST "+a+"/admin/users/{id}/recovery-code", s.admin(s.handleAdminRecoveryCode))
	mux.HandleFunc("GET "+a+"/admin/domains", s.admin(s.handleAdminDomains))
	mux.HandleFunc("POST "+a+"/admin/domains", s.admin(s.handleAdminAddDomain))
	mux.HandleFunc("PATCH "+a+"/admin/domains/{id}", s.admin(s.handleAdminUpdateDomain))
	mux.HandleFunc("DELETE "+a+"/admin/domains/{id}", s.admin(s.handleAdminDeleteDomain))
	mux.HandleFunc("GET "+a+"/admin/corrections", s.admin(s.handleAdminCorrections))

	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) { fail(w, http.StatusNotFound, "not found") })
	mux.Handle("/", s.webHandler())
	return secure(mux)
}

// secure sets the headers every response should carry.
func secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Content-Security-Policy",
			"default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; frame-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'")
		next.ServeHTTP(w, r)
	})
}

// webHandler serves the web app, falling back to index.html so that
// client-side routes such as /oauth/authorize load the app.
func (s *Server) webHandler() http.Handler {
	files := http.FileServerFS(s.Web)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			fail(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(s.Web, path); err != nil {
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})
}

// --- request helpers ---

type apiError struct {
	Error string `json:"error"`
}

func reply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func ok(w http.ResponseWriter, v any) { reply(w, http.StatusOK, v) }

func fail(w http.ResponseWriter, status int, msg string) { reply(w, status, apiError{msg}) }

// failErr maps store errors to HTTP statuses.
func (s *Server) failErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		fail(w, http.StatusNotFound, "not found")
	case errors.Is(err, store.ErrTaken):
		fail(w, http.StatusConflict, "that name is already taken")
	case errors.Is(err, store.ErrInvalidName):
		fail(w, http.StatusBadRequest, "names use lowercase letters, digits and hyphens, and some names are reserved")
	case errors.Is(err, store.ErrLimit):
		fail(w, http.StatusConflict, "you have reached the limit for this domain")
	case errors.Is(err, store.ErrNotAllowed):
		fail(w, http.StatusForbidden, "that is not allowed on this domain")
	default:
		s.Log.Error("request failed", "err", err)
		fail(w, http.StatusInternalServerError, "internal error")
	}
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		fail(w, http.StatusBadRequest, "invalid JSON body")
		return false
	}
	return true
}

func pathID(r *http.Request) int64 {
	id, _ := strconv.ParseInt(r.PathValue("id"), 10, 64)
	return id
}

// newToken returns a random token and the hash stored in its place.
func newToken() (token string, hash []byte) {
	b := make([]byte, 32)
	rand.Read(b)
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, hashToken(token)
}

func hashToken(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}

// --- authentication ---

const (
	sessionCookie = "cm_session"
	sessionTTL    = 30 * 24 * time.Hour
)

type handler func(w http.ResponseWriter, r *http.Request, u store.User)

// identify returns the signed-in user and whether a browser session (as
// opposed to a device token) authenticated the request.
func (s *Server) identify(r *http.Request) (u store.User, viaSession bool, err error) {
	ctx := r.Context()
	if bearer, found := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); found {
		d, err := s.Store.DeviceByAccess(ctx, hashToken(bearer), time.Now())
		if err != nil {
			return u, false, err
		}
		u, err = s.Store.UserByID(ctx, d.UserID)
		return u, false, err
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil {
		return u, false, store.ErrNotFound
	}
	u, err = s.Store.SessionUser(ctx, hashToken(c.Value), time.Now())
	return u, true, err
}

func (s *Server) guard(next handler, needSession, needAdmin bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		u, viaSession, err := s.identify(r)
		if err != nil {
			fail(w, http.StatusUnauthorized, "sign in required")
			return
		}
		if (needSession || needAdmin) && !viaSession {
			fail(w, http.StatusForbidden, "this action needs a browser sign-in")
			return
		}
		if needAdmin && !u.IsAdmin {
			fail(w, http.StatusForbidden, "admins only")
			return
		}
		// Cookies are sent automatically, so a state-changing request made
		// with one must come from our own pages.
		if viaSession && r.Method != http.MethodGet && r.Method != http.MethodHead {
			if o := r.Header.Get("Origin"); o != "" && o != s.cfg.Origin {
				fail(w, http.StatusForbidden, "cross-site request refused")
				return
			}
		}
		next(w, r, u)
	}
}

func (s *Server) auth(next handler) http.HandlerFunc    { return s.guard(next, false, false) }
func (s *Server) session(next handler) http.HandlerFunc { return s.guard(next, true, false) }
func (s *Server) admin(next handler) http.HandlerFunc   { return s.guard(next, true, true) }

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, userID int64) error {
	token, hash := newToken()
	if err := s.Store.CreateSession(r.Context(), hash, userID, time.Now().Add(sessionTTL)); err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", MaxAge: int(sessionTTL / time.Second),
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: strings.HasPrefix(s.cfg.Origin, "https://"),
	})
	return nil
}

// clientIP is the address of whoever made the request. Behind a reverse
// proxy every connection comes from the proxy, so its headers are used;
// without one they are ignored, since any client could forge them.
func (s *Server) clientIP(r *http.Request) string {
	if s.cfg.TrustProxy {
		if ip := strings.TrimSpace(r.Header.Get("X-Real-IP")); ip != "" {
			return ip
		}
		if first, _, _ := strings.Cut(r.Header.Get("X-Forwarded-For"), ","); strings.TrimSpace(first) != "" {
			return strings.TrimSpace(first)
		}
	}
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	return ip
}

// limited allows each client address 20 calls a minute, to slow guessing
// of recovery codes, invite codes and pairing codes.
func (s *Server) limited(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := s.clientIP(r)
		now := time.Now()
		s.mu.Lock()
		if len(s.attempts) > 10000 {
			s.attempts = map[string][]time.Time{}
		}
		keep := s.attempts[ip][:0]
		for _, t := range s.attempts[ip] {
			if now.Sub(t) < time.Minute {
				keep = append(keep, t)
			}
		}
		allowed := len(keep) < 20
		if allowed {
			keep = append(keep, now)
		}
		s.attempts[ip] = keep
		s.mu.Unlock()
		if !allowed {
			fail(w, http.StatusTooManyRequests, "too many attempts, wait a minute")
			return
		}
		next(w, r)
	}
}

// --- settings ---

// Registration modes.
const (
	RegOpen   = "open"
	RegInvite = "invite"
	RegClosed = "closed"
)

func (s *Server) registrationMode(ctx context.Context) string {
	switch v, _ := s.Store.GetSetting(ctx, "registration"); v {
	case RegOpen, RegClosed:
		return v
	}
	return RegInvite
}

// maxRetention is the admin's cap on how long users may keep mail.
func (s *Server) maxRetention(ctx context.Context) time.Duration {
	v, _ := s.Store.GetSetting(ctx, "max_retention_hours")
	if h, err := strconv.Atoi(v); err == nil && h > 0 {
		return time.Duration(h) * time.Hour
	}
	return 30 * 24 * time.Hour
}

func hours(d time.Duration) int { return int(d / time.Hour) }

func unix(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return t.Unix()
}
