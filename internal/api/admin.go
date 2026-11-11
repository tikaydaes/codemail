package api

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tikaydaes/codemail/internal/store"
)

func (s *Server) handleAdminStatus(w http.ResponseWriter, r *http.Request, u store.User) {
	st, err := s.Store.Stats(r.Context())
	if err != nil {
		s.failErr(w, err)
		return
	}
	ok(w, map[string]any{
		"users": st.Users, "messages": st.Messages, "corrections": st.Corrections,
		"bytes": st.Bytes, "last_message": unix(st.LastMessage),
		"mail_host": s.cfg.MailHost, "origin": s.cfg.Origin,
	})
}

func (s *Server) settingsJSON(r *http.Request) map[string]any {
	reject, _ := s.Store.GetSetting(r.Context(), "reject_auth_fail")
	return map[string]any{
		"registration":            s.registrationMode(r.Context()),
		"max_retention_hours":     hours(s.maxRetention(r.Context())),
		"default_retention_hours": hours(s.cfg.DefaultRetention),
		"reject_auth_fail":        reject == "1",
	}
}

func (s *Server) handleAdminSettings(w http.ResponseWriter, r *http.Request, u store.User) {
	ok(w, s.settingsJSON(r))
}

func (s *Server) handleAdminPutSettings(w http.ResponseWriter, r *http.Request, u store.User) {
	var body struct {
		Registration   *string
		MaxRetention   *int  `json:"max_retention_hours"`
		RejectAuthFail *bool `json:"reject_auth_fail"`
	}
	if !decode(w, r, &body) {
		return
	}
	ctx := r.Context()
	set := func(key, value string) {
		s.Store.SetSetting(ctx, key, value)
		s.Store.Audit(ctx, u.ID, "set_"+key, value)
	}
	if v := body.Registration; v != nil {
		if *v != RegOpen && *v != RegInvite && *v != RegClosed {
			fail(w, http.StatusBadRequest, "registration must be open, invite or closed")
			return
		}
		set("registration", *v)
	}
	if v := body.MaxRetention; v != nil {
		if *v < 1 || *v > 24*365 {
			fail(w, http.StatusBadRequest, "maximum retention must be between 1 hour and 1 year")
			return
		}
		set("max_retention_hours", strconv.Itoa(*v))
	}
	if v := body.RejectAuthFail; v != nil {
		set("reject_auth_fail", map[bool]string{true: "1", false: "0"}[*v])
	}
	ok(w, s.settingsJSON(r))
}

// --- invites ---

func inviteJSON(inv store.Invite) map[string]any {
	return map[string]any{
		"code": inv.Code, "max_uses": inv.MaxUses, "uses": inv.Uses,
		"expires_at": unix(inv.ExpiresAt), "created_at": unix(inv.CreatedAt),
	}
}

func (s *Server) handleAdminInvites(w http.ResponseWriter, r *http.Request, u store.User) {
	invites, err := s.Store.ListInvites(r.Context())
	if err != nil {
		s.failErr(w, err)
		return
	}
	out := []map[string]any{}
	for _, inv := range invites {
		out = append(out, inviteJSON(inv))
	}
	ok(w, map[string]any{"invites": out})
}

func (s *Server) handleAdminNewInvite(w http.ResponseWriter, r *http.Request, u store.User) {
	var body struct {
		MaxUses     int `json:"max_uses"`
		ExpiresDays int `json:"expires_days"`
	}
	if !decode(w, r, &body) {
		return
	}
	n, _ := rand.Int(rand.Reader, big.NewInt(900))
	suffix, _ := newToken()
	inv := store.Invite{
		Code:    strings.ToUpper(fmt.Sprintf("%s-%s-%d-%s", pick(aliasAdjectives), pick(aliasNouns), 100+n.Int64(), suffix[:6])),
		MaxUses: max(0, body.MaxUses), CreatedBy: u.ID, CreatedAt: time.Now(),
	}
	if body.ExpiresDays > 0 {
		inv.ExpiresAt = time.Now().Add(time.Duration(body.ExpiresDays) * 24 * time.Hour)
	}
	if err := s.Store.CreateInvite(r.Context(), inv); err != nil {
		s.failErr(w, err)
		return
	}
	s.Store.Audit(r.Context(), u.ID, "create_invite", inv.Code)
	reply(w, http.StatusCreated, map[string]any{"invite": inviteJSON(inv)})
}

func (s *Server) handleAdminDeleteInvite(w http.ResponseWriter, r *http.Request, u store.User) {
	if err := s.Store.DeleteInvite(r.Context(), r.PathValue("code")); err != nil {
		s.failErr(w, err)
		return
	}
	s.Store.Audit(r.Context(), u.ID, "delete_invite", r.PathValue("code"))
	ok(w, map[string]any{})
}

// --- users ---

func (s *Server) handleAdminUsers(w http.ResponseWriter, r *http.Request, u store.User) {
	ctx := r.Context()
	users, err := s.Store.ListUsers(ctx)
	if err != nil {
		s.failErr(w, err)
		return
	}
	out := []map[string]any{}
	for _, x := range users {
		j := userJSON(x)
		keys, _ := s.Store.PasskeysByUser(ctx, x.ID)
		domains, _ := s.Store.ListDomains(ctx, x.ID)
		j["passkeys"], j["domains"] = len(keys), len(domains)
		last := time.Time{}
		for _, k := range keys {
			if k.LastUsed.After(last) {
				last = k.LastUsed
			}
		}
		j["last_sign_in"] = unix(last)
		out = append(out, j)
	}
	ok(w, map[string]any{"users": out})
}

func (s *Server) handleAdminUpdateUser(w http.ResponseWriter, r *http.Request, u store.User) {
	var body struct {
		IsAdmin *bool `json:"is_admin"`
	}
	if !decode(w, r, &body) {
		return
	}
	ctx := r.Context()
	target, err := s.Store.UserByID(ctx, pathID(r))
	if err != nil {
		s.failErr(w, err)
		return
	}
	if body.IsAdmin != nil {
		if !*body.IsAdmin && target.IsAdmin && s.lastAdmin(ctx, target.ID) {
			fail(w, http.StatusConflict, "the server needs at least one admin")
			return
		}
		target.IsAdmin = *body.IsAdmin
	}
	if err := s.Store.UpdateUser(ctx, target); err != nil {
		s.failErr(w, err)
		return
	}
	s.Store.Audit(ctx, u.ID, "update_user", target.Username)
	ok(w, map[string]any{"user": userJSON(target)})
}

func (s *Server) handleAdminDeleteUser(w http.ResponseWriter, r *http.Request, u store.User) {
	ctx := r.Context()
	target, err := s.Store.UserByID(ctx, pathID(r))
	if err != nil {
		s.failErr(w, err)
		return
	}
	if target.IsAdmin && s.lastAdmin(ctx, target.ID) {
		fail(w, http.StatusConflict, "the server needs at least one admin")
		return
	}
	if err := s.Store.DeleteUser(ctx, target.ID); err != nil {
		s.failErr(w, err)
		return
	}
	s.Store.Audit(ctx, u.ID, "delete_user", target.Username)
	ok(w, map[string]any{})
}

// handleAdminRecoveryCode issues one extra recovery code for a user who
// has lost every passkey and their own codes.
func (s *Server) handleAdminRecoveryCode(w http.ResponseWriter, r *http.Request, u store.User) {
	ctx := r.Context()
	target, err := s.Store.UserByID(ctx, pathID(r))
	if err != nil {
		s.failErr(w, err)
		return
	}
	code := newRecoveryCode()
	if err := s.Store.AddRecoveryCode(ctx, target.ID, recoveryHash(code)); err != nil {
		s.failErr(w, err)
		return
	}
	s.Store.Audit(ctx, u.ID, "issue_recovery_code", target.Username)
	ok(w, map[string]any{"username": target.Username, "recovery_code": code})
}

// --- shared domains ---

type sharedDomainBody struct {
	Name            string
	AllowMailbox    *bool `json:"allow_mailbox"`
	AllowAliases    *bool `json:"allow_aliases"`
	AllowSubdomains *bool `json:"allow_subdomains"`
	MaxAliases      *int  `json:"max_aliases"`
	MaxGenerated    *int  `json:"max_generated"`
}

func (b sharedDomainBody) apply(d *store.Domain) {
	if b.AllowMailbox != nil {
		d.AllowMailbox = *b.AllowMailbox
	}
	if b.AllowAliases != nil {
		d.AllowAliases = *b.AllowAliases
	}
	if b.AllowSubdomains != nil {
		d.AllowSubdomains = *b.AllowSubdomains
	}
	if b.MaxAliases != nil {
		d.MaxAliases = max(0, *b.MaxAliases)
	}
	if b.MaxGenerated != nil {
		d.MaxGenerated = max(0, *b.MaxGenerated)
	}
}

func (s *Server) handleAdminDomains(w http.ResponseWriter, r *http.Request, u store.User) {
	domains, err := s.Store.ListDomains(r.Context(), 0)
	if err != nil {
		s.failErr(w, err)
		return
	}
	out := []map[string]any{}
	for _, d := range domains {
		out = append(out, s.domainJSON(d, nil, nil))
	}
	ok(w, map[string]any{"domains": out})
}

// Shared domains are trusted on the admin's word and accept mail at once;
// the MX check only reports whether DNS is set up.
func (s *Server) handleAdminAddDomain(w http.ResponseWriter, r *http.Request, u store.User) {
	var body sharedDomainBody
	if !decode(w, r, &body) {
		return
	}
	d := store.Domain{Name: body.Name, Verified: true, AllowMailbox: true, AllowAliases: true, AllowSubdomains: true, MaxAliases: 20}
	body.apply(&d)
	if err := s.Store.AddDomain(r.Context(), &d); err != nil {
		s.failErr(w, err)
		return
	}
	s.Store.Audit(r.Context(), u.ID, "add_shared_domain", d.Name)
	reply(w, http.StatusCreated, map[string]any{"domain": s.domainJSON(d, nil, nil)})
}

func (s *Server) sharedDomain(w http.ResponseWriter, r *http.Request) (store.Domain, bool) {
	d, err := s.Store.DomainByID(r.Context(), pathID(r))
	if err == nil && d.OwnerID != 0 {
		err = store.ErrNotFound
	}
	if err != nil {
		s.failErr(w, err)
		return d, false
	}
	return d, true
}

func (s *Server) handleAdminUpdateDomain(w http.ResponseWriter, r *http.Request, u store.User) {
	var body sharedDomainBody
	if !decode(w, r, &body) {
		return
	}
	d, found := s.sharedDomain(w, r)
	if !found {
		return
	}
	body.apply(&d)
	mx, _, err := s.checkDomain(r.Context(), &d)
	if err != nil {
		s.failErr(w, err)
		return
	}
	s.Store.Audit(r.Context(), u.ID, "update_shared_domain", d.Name)
	ok(w, map[string]any{"domain": s.domainJSON(d, &mx, nil)})
}

func (s *Server) handleAdminDeleteDomain(w http.ResponseWriter, r *http.Request, u store.User) {
	d, found := s.sharedDomain(w, r)
	if !found {
		return
	}
	if err := s.Store.DeleteDomain(r.Context(), d.ID); err != nil {
		s.failErr(w, err)
		return
	}
	s.Store.Audit(r.Context(), u.ID, "delete_shared_domain", d.Name)
	ok(w, map[string]any{})
}

// handleAdminCorrections exports users' corrections with the messages
// they apply to, one JSON object per line, as training data.
func (s *Server) handleAdminCorrections(w http.ResponseWriter, r *http.Request, u store.User) {
	corrections, err := s.Store.ListCorrections(r.Context())
	if err != nil {
		s.failErr(w, err)
		return
	}
	s.Store.Audit(r.Context(), u.ID, "export_corrections", strconv.Itoa(len(corrections)))
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Content-Disposition", `attachment; filename="corrections.jsonl"`)
	enc := json.NewEncoder(w)
	for _, c := range corrections {
		enc.Encode(map[string]any{
			"id": c.ID, "kind": c.Kind, "value": c.Value, "created_at": unix(c.CreatedAt),
			"raw": base64.StdEncoding.EncodeToString(c.Raw),
		})
	}
}
