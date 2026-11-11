package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"

	"github.com/tikaydaes/codemail/internal/store"
)

// ceremony is a passkey registration or sign-in waiting for the browser's
// answer. Ceremonies live in memory; a restart only makes the user retry.
type ceremony struct {
	session webauthn.SessionData
	user    store.User // ID 0 means an account that does not exist yet
	invite  string
	expires time.Time
}

func (s *Server) putCeremony(c ceremony) string {
	id, _ := newToken()
	c.expires = time.Now().Add(5 * time.Minute)
	s.mu.Lock()
	for k, v := range s.ceremonies {
		if time.Now().After(v.expires) {
			delete(s.ceremonies, k)
		}
	}
	s.ceremonies[id] = c
	s.mu.Unlock()
	return id
}

func (s *Server) takeCeremony(id string) (ceremony, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, found := s.ceremonies[id]
	delete(s.ceremonies, id)
	return c, found && time.Now().Before(c.expires)
}

// waUser adapts a user and their passkeys to the WebAuthn library.
type waUser struct {
	u     store.User
	creds []webauthn.Credential
}

func (w waUser) WebAuthnID() []byte                         { return w.u.Handle }
func (w waUser) WebAuthnName() string                       { return w.u.Username }
func (w waUser) WebAuthnDisplayName() string                { return w.u.Username }
func (w waUser) WebAuthnCredentials() []webauthn.Credential { return w.creds }

func (s *Server) loadWAUser(ctx context.Context, u store.User) (waUser, []store.Passkey, error) {
	keys, err := s.Store.PasskeysByUser(ctx, u.ID)
	if err != nil {
		return waUser{}, nil, err
	}
	w := waUser{u: u}
	for _, k := range keys {
		var c webauthn.Credential
		if json.Unmarshal(k.Data, &c) == nil {
			w.creds = append(w.creds, c)
		}
	}
	return w, keys, nil
}

// EnsureSetupToken returns the one-time token that lets the first account
// be created, or "" once any account exists.
func (s *Server) EnsureSetupToken(ctx context.Context) (string, error) {
	users, err := s.Store.ListUsers(ctx)
	if err != nil || len(users) > 0 {
		return "", err
	}
	token, err := s.Store.GetSetting(ctx, "setup_token")
	if err != nil || token != "" {
		return token, err
	}
	token, _ = newToken()
	return token, s.Store.SetSetting(ctx, "setup_token", token)
}

func (s *Server) handleServer(w http.ResponseWriter, r *http.Request) {
	users, err := s.Store.ListUsers(r.Context())
	if err != nil {
		s.failErr(w, err)
		return
	}
	ok(w, map[string]any{
		"registration": s.registrationMode(r.Context()),
		"needs_setup":  len(users) == 0,
		"mail_host":    s.cfg.MailHost,
	})
}

func (s *Server) handleRegisterBegin(w http.ResponseWriter, r *http.Request) {
	var body struct{ Username, Invite, Setup string }
	if !decode(w, r, &body) {
		return
	}
	ctx := r.Context()
	c := ceremony{}
	var exclude []protocol.CredentialDescriptor

	if u, viaSession, err := s.identify(r); err == nil && viaSession {
		// A signed-in user is adding another passkey.
		wu, _, err := s.loadWAUser(ctx, u)
		if err != nil {
			s.failErr(w, err)
			return
		}
		c.user = u
		for _, cred := range wu.creds {
			exclude = append(exclude, cred.Descriptor())
		}
	} else {
		name := strings.ToLower(strings.TrimSpace(body.Username))
		if !store.ValidName(name) {
			s.failErr(w, store.ErrInvalidName)
			return
		}
		if _, err := s.Store.UserByName(ctx, name); err == nil {
			s.failErr(w, store.ErrTaken)
			return
		}
		users, err := s.Store.ListUsers(ctx)
		if err != nil {
			s.failErr(w, err)
			return
		}
		switch {
		case len(users) == 0:
			want, _ := s.Store.GetSetting(ctx, "setup_token")
			if want == "" || body.Setup != want {
				fail(w, http.StatusForbidden, "this server is not set up yet; use the setup link from its log")
				return
			}
			c.user.IsAdmin = true
		case s.registrationMode(ctx) == RegClosed:
			fail(w, http.StatusForbidden, "this server is not accepting new accounts")
			return
		case s.registrationMode(ctx) == RegInvite:
			c.invite = strings.TrimSpace(body.Invite)
			if !s.inviteUsable(ctx, c.invite) {
				fail(w, http.StatusForbidden, "that invite code is not valid")
				return
			}
		}
		c.user.Username = name
		c.user.Handle = make([]byte, 16)
		rand.Read(c.user.Handle)
	}

	options, session, err := s.wa.BeginRegistration(waUser{u: c.user}, webauthn.WithExclusions(exclude))
	if err != nil {
		s.failErr(w, err)
		return
	}
	c.session = *session
	ok(w, map[string]any{"session": s.putCeremony(c), "publicKey": options.Response})
}

func (s *Server) inviteUsable(ctx context.Context, code string) bool {
	invites, err := s.Store.ListInvites(ctx)
	if err != nil {
		return false
	}
	for _, inv := range invites {
		if inv.Code == code {
			return (inv.MaxUses == 0 || inv.Uses < inv.MaxUses) &&
				(inv.ExpiresAt.IsZero() || inv.ExpiresAt.After(time.Now()))
		}
	}
	return false
}

func (s *Server) handleRegisterFinish(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Session    string
		Name       string
		Credential json.RawMessage
	}
	if !decode(w, r, &body) {
		return
	}
	ctx := r.Context()
	c, found := s.takeCeremony(body.Session)
	if !found {
		fail(w, http.StatusBadRequest, "that took too long; start again")
		return
	}
	parsed, err := protocol.ParseCredentialCreationResponseBody(bytes.NewReader(body.Credential))
	if err != nil {
		fail(w, http.StatusBadRequest, "the passkey response could not be read")
		return
	}
	cred, err := s.wa.CreateCredential(waUser{u: c.user}, c.session, parsed)
	if err != nil {
		s.Log.Warn("passkey registration rejected", "err", err)
		fail(w, http.StatusBadRequest, "the passkey could not be verified")
		return
	}

	out := map[string]any{}
	isNew := c.user.ID == 0
	if isNew {
		if c.invite != "" {
			if err := s.Store.UseInvite(ctx, c.invite, time.Now()); err != nil {
				fail(w, http.StatusForbidden, "that invite code is no longer valid")
				return
			}
		}
		if err := s.Store.CreateUser(ctx, &c.user); err != nil {
			s.failErr(w, err)
			return
		}
		if c.user.IsAdmin {
			s.Store.SetSetting(ctx, "setup_token", "")
			s.Log.Info("admin account created; the setup link no longer works", "username", c.user.Username)
		}
	}
	data, _ := json.Marshal(cred)
	name := strings.TrimSpace(body.Name)
	if name == "" {
		name = "Passkey added " + time.Now().Format("2 Jan 2006")
	}
	if err := s.Store.AddPasskey(ctx, &store.Passkey{UserID: c.user.ID, CredID: cred.ID, Data: data, Name: name}); err != nil {
		s.failErr(w, err)
		return
	}
	if isNew {
		codes, err := s.issueRecoveryCodes(ctx, c.user.ID)
		if err != nil {
			s.failErr(w, err)
			return
		}
		out["recovery_codes"] = codes
		if err := s.startSession(w, r, c.user.ID); err != nil {
			s.failErr(w, err)
			return
		}
		s.Store.Audit(ctx, c.user.ID, "register", c.user.Username)
	}
	out["user"] = userJSON(c.user)
	ok(w, out)
}

func (s *Server) handleLoginBegin(w http.ResponseWriter, r *http.Request) {
	options, session, err := s.wa.BeginDiscoverableLogin()
	if err != nil {
		s.failErr(w, err)
		return
	}
	ok(w, map[string]any{"session": s.putCeremony(ceremony{session: *session}), "publicKey": options.Response})
}

func (s *Server) handleLoginFinish(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Session    string
		Credential json.RawMessage
	}
	if !decode(w, r, &body) {
		return
	}
	ctx := r.Context()
	c, found := s.takeCeremony(body.Session)
	if !found {
		fail(w, http.StatusBadRequest, "that took too long; start again")
		return
	}
	parsed, err := protocol.ParseCredentialRequestResponseBody(bytes.NewReader(body.Credential))
	if err != nil {
		fail(w, http.StatusBadRequest, "the passkey response could not be read")
		return
	}
	var user store.User
	var keys []store.Passkey
	cred, err := s.wa.ValidateDiscoverableLogin(func(rawID, handle []byte) (webauthn.User, error) {
		u, err := s.Store.UserByHandle(ctx, handle)
		if err != nil {
			return nil, err
		}
		wu, ks, err := s.loadWAUser(ctx, u)
		user, keys = u, ks
		return wu, err
	}, c.session, parsed)
	if err != nil {
		s.Log.Warn("passkey sign-in rejected", "err", err)
		fail(w, http.StatusUnauthorized, "that passkey is not registered here")
		return
	}
	for _, k := range keys {
		if bytes.Equal(k.CredID, cred.ID) {
			k.Data, _ = json.Marshal(cred) // keeps the signature counter current
			k.LastUsed = time.Now()
			s.Store.UpdatePasskey(ctx, k)
		}
	}
	if err := s.startSession(w, r, user.ID); err != nil {
		s.failErr(w, err)
		return
	}
	ok(w, map[string]any{"user": userJSON(user)})
}

// --- recovery codes ---

const recoveryAlphabet = "ABCDEFGHJKMNPQRSTVWXYZ23456789" // no look-alikes

func newRecoveryCode() string {
	b := make([]byte, 8)
	rand.Read(b)
	out := make([]byte, 0, 9)
	for i, v := range b {
		if i == 4 {
			out = append(out, '-')
		}
		out = append(out, recoveryAlphabet[int(v)%len(recoveryAlphabet)])
	}
	return string(out)
}

func recoveryHash(code string) []byte {
	return hashToken(strings.ToUpper(strings.NewReplacer("-", "", " ", "").Replace(code)))
}

// IssueRecoveryCode adds one recovery code for a user and returns it.
func IssueRecoveryCode(ctx context.Context, st store.Store, username string) (string, error) {
	u, err := st.UserByName(ctx, username)
	if err != nil {
		return "", err
	}
	code := newRecoveryCode()
	return code, st.AddRecoveryCode(ctx, u.ID, recoveryHash(code))
}

func (s *Server) issueRecoveryCodes(ctx context.Context, userID int64) ([]string, error) {
	codes := make([]string, 8)
	hashes := make([][]byte, 8)
	for i := range codes {
		codes[i] = newRecoveryCode()
		hashes[i] = recoveryHash(codes[i])
	}
	return codes, s.Store.ReplaceRecoveryCodes(ctx, userID, hashes)
}

func (s *Server) handleRecovery(w http.ResponseWriter, r *http.Request) {
	var body struct{ Username, Code string }
	if !decode(w, r, &body) {
		return
	}
	ctx := r.Context()
	u, err := s.Store.UserByName(ctx, strings.TrimSpace(body.Username))
	used := false
	if err == nil {
		used, err = s.Store.UseRecoveryCode(ctx, u.ID, recoveryHash(body.Code))
	}
	if err != nil && !errors.Is(err, store.ErrNotFound) {
		s.failErr(w, err)
		return
	}
	if !used {
		fail(w, http.StatusUnauthorized, "that username and recovery code do not match")
		return
	}
	if err := s.startSession(w, r, u.ID); err != nil {
		s.failErr(w, err)
		return
	}
	s.Store.Audit(ctx, u.ID, "recovery", u.Username)
	ok(w, map[string]any{"user": userJSON(u)})
}

func (s *Server) handleNewRecoveryCodes(w http.ResponseWriter, r *http.Request, u store.User) {
	codes, err := s.issueRecoveryCodes(r.Context(), u.ID)
	if err != nil {
		s.failErr(w, err)
		return
	}
	ok(w, map[string]any{"recovery_codes": codes})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil {
		s.Store.DeleteSession(r.Context(), hashToken(c.Value))
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1})
	ok(w, map[string]any{})
}

// --- account ---

func userJSON(u store.User) map[string]any {
	return map[string]any{
		"id": u.ID, "username": u.Username, "is_admin": u.IsAdmin,
		"retention_hours": hours(u.Retention), "created_at": unix(u.CreatedAt),
	}
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request, u store.User) {
	ok(w, map[string]any{
		"user": userJSON(u),
		"server": map[string]any{
			"mail_host":               s.cfg.MailHost,
			"origin":                  s.cfg.Origin,
			"default_retention_hours": hours(s.cfg.DefaultRetention),
			"max_retention_hours":     hours(s.maxRetention(r.Context())),
		},
	})
}

func (s *Server) handleUpdateMe(w http.ResponseWriter, r *http.Request, u store.User) {
	var body struct {
		RetentionHours int `json:"retention_hours"`
	}
	if !decode(w, r, &body) {
		return
	}
	d := time.Duration(body.RetentionHours) * time.Hour
	if d < 0 || d > s.maxRetention(r.Context()) {
		fail(w, http.StatusBadRequest, "retention is outside the range this server allows")
		return
	}
	u.Retention = d
	if err := s.Store.UpdateUser(r.Context(), u); err != nil {
		s.failErr(w, err)
		return
	}
	ok(w, map[string]any{"user": userJSON(u)})
}

func (s *Server) handleDeleteMe(w http.ResponseWriter, r *http.Request, u store.User) {
	if u.IsAdmin && s.lastAdmin(r.Context(), u.ID) {
		fail(w, http.StatusConflict, "make someone else an admin before deleting the only admin account")
		return
	}
	if err := s.Store.DeleteUser(r.Context(), u.ID); err != nil {
		s.failErr(w, err)
		return
	}
	s.Store.Audit(r.Context(), u.ID, "delete_account", u.Username)
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Path: "/", MaxAge: -1})
	ok(w, map[string]any{})
}

func (s *Server) lastAdmin(ctx context.Context, id int64) bool {
	users, _ := s.Store.ListUsers(ctx)
	for _, u := range users {
		if u.IsAdmin && u.ID != id {
			return false
		}
	}
	return true
}

func (s *Server) handlePasskeys(w http.ResponseWriter, r *http.Request, u store.User) {
	keys, err := s.Store.PasskeysByUser(r.Context(), u.ID)
	if err != nil {
		s.failErr(w, err)
		return
	}
	out := []map[string]any{}
	for _, k := range keys {
		out = append(out, map[string]any{"id": k.ID, "name": k.Name, "created_at": unix(k.CreatedAt), "last_used": unix(k.LastUsed)})
	}
	ok(w, map[string]any{"passkeys": out})
}

func (s *Server) handleDeletePasskey(w http.ResponseWriter, r *http.Request, u store.User) {
	keys, err := s.Store.PasskeysByUser(r.Context(), u.ID)
	if err != nil {
		s.failErr(w, err)
		return
	}
	if len(keys) <= 1 {
		fail(w, http.StatusConflict, "add another passkey before removing your only one")
		return
	}
	if err := s.Store.DeletePasskey(r.Context(), u.ID, pathID(r)); err != nil {
		s.failErr(w, err)
		return
	}
	ok(w, map[string]any{})
}

// --- signed-in browsers ---

func (s *Server) handleSessions(w http.ResponseWriter, r *http.Request, u store.User) {
	_, current, _ := s.identifySession(r)
	sessions, err := s.Store.ListSessions(r.Context(), u.ID, time.Now())
	if err != nil {
		s.failErr(w, err)
		return
	}
	out := []map[string]any{}
	for _, se := range sessions {
		out = append(out, map[string]any{
			"id": se.ID, "label": se.Label, "created_at": unix(se.CreatedAt), "last_seen": unix(se.LastSeen),
			"current": current != nil && current.ID == se.ID,
		})
	}
	ok(w, map[string]any{"sessions": out})
}

// handleDeleteSession signs another browser out.
func (s *Server) handleDeleteSession(w http.ResponseWriter, r *http.Request, u store.User) {
	if err := s.Store.DeleteSessionByID(r.Context(), u.ID, pathID(r)); err != nil {
		s.failErr(w, err)
		return
	}
	s.Store.Audit(r.Context(), u.ID, "revoke_session", r.PathValue("id"))
	ok(w, map[string]any{})
}
