package api

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"net/http"
	"net/url"
	"strings"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/tikaydaes/codemail/internal/store"
)

// Native apps and the browser extension cannot use passkeys directly, so
// they obtain tokens in one of two ways:
//
//   - OAuth 2.0 authorization code with PKCE: the app opens
//     /oauth/authorize in a browser, the signed-in user approves, and the
//     app exchanges the returned code at /oauth/token.
//   - QR pairing: the web app shows a one-time code; the app claims it at
//     /oauth/token and receives tokens once the user confirms the device.

const (
	accessTTL   = time.Hour
	grantTTL    = 2 * time.Minute
	grantAuth   = "authorization"
	grantPair   = "pairing"
	pairingType = "urn:codemail:params:oauth:grant-type:pairing-code"
)

func (s *Server) handleOAuthMetadata(w http.ResponseWriter, r *http.Request) {
	ok(w, map[string]any{
		"issuer":                                s.cfg.Origin,
		"authorization_endpoint":                s.cfg.Origin + "/oauth/authorize",
		"token_endpoint":                        s.cfg.Origin + "/oauth/token",
		"revocation_endpoint":                   s.cfg.Origin + "/oauth/revoke",
		"response_types_supported":              []string{"code"},
		"grant_types_supported":                 []string{"authorization_code", "refresh_token", pairingType},
		"code_challenge_methods_supported":      []string{"S256"},
		"token_endpoint_auth_methods_supported": []string{"none"},
	})
}

// redirectAllowed limits where an authorization code may be sent: back to
// an app on this machine, to a browser extension, or to the mobile apps.
// Clients are public and unregistered, so this is what stops a web page
// from collecting codes.
func redirectAllowed(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Fragment != "" {
		return false
	}
	host := u.Hostname()
	switch u.Scheme {
	case "http":
		return host == "127.0.0.1" || host == "localhost" || host == "::1"
	case "https":
		return strings.HasSuffix(host, ".chromiumapp.org") || strings.HasSuffix(host, ".extensions.allizom.org")
	case "codemail":
		return true
	}
	return false
}

// handleOAuthApprove is called by the /oauth/authorize page once the
// signed-in user approves the device.
func (s *Server) handleOAuthApprove(w http.ResponseWriter, r *http.Request, u store.User) {
	var body struct {
		ClientID      string `json:"client_id"`
		RedirectURI   string `json:"redirect_uri"`
		CodeChallenge string `json:"code_challenge"`
		Method        string `json:"code_challenge_method"`
		State         string
		DeviceName    string `json:"device_name"`
		Platform      string
	}
	if !decode(w, r, &body) {
		return
	}
	if !redirectAllowed(body.RedirectURI) {
		fail(w, http.StatusBadRequest, "this app's return address is not allowed")
		return
	}
	if body.Method != "S256" || len(body.CodeChallenge) < 43 {
		fail(w, http.StatusBadRequest, "the app must use PKCE with S256")
		return
	}
	code, hash := newToken()
	err := s.Store.PutGrant(r.Context(), store.Grant{
		CodeHash: hash, Kind: grantAuth, UserID: u.ID, ClientID: body.ClientID, RedirectURI: body.RedirectURI,
		Challenge: body.CodeChallenge, DeviceName: deviceName(body.DeviceName), Platform: body.Platform,
		Confirmed: true, ExpiresAt: time.Now().Add(grantTTL),
	})
	if err != nil {
		s.failErr(w, err)
		return
	}
	ru, _ := url.Parse(body.RedirectURI)
	q := ru.Query()
	q.Set("code", code)
	if body.State != "" {
		q.Set("state", body.State)
	}
	ru.RawQuery = q.Encode()
	ok(w, map[string]any{"redirect": ru.String()})
}

func deviceName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "Unnamed device"
	}
	if len(name) > 60 {
		name = name[:60]
	}
	return name
}

func oauthError(w http.ResponseWriter, code, desc string) {
	reply(w, http.StatusBadRequest, map[string]string{"error": code, "error_description": desc})
}

func (s *Server) handleOAuthToken(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<16)
	if err := r.ParseForm(); err != nil {
		oauthError(w, "invalid_request", "expected a form-encoded body")
		return
	}
	ctx := r.Context()
	switch r.PostForm.Get("grant_type") {
	case "authorization_code":
		hash := hashToken(r.PostForm.Get("code"))
		g, err := s.Store.GetGrant(ctx, hash)
		if err != nil || g.Kind != grantAuth {
			oauthError(w, "invalid_grant", "unknown or expired code")
			return
		}
		s.Store.DeleteGrant(ctx, hash) // single use, even when the checks below fail
		sum := sha256.Sum256([]byte(r.PostForm.Get("code_verifier")))
		if base64.RawURLEncoding.EncodeToString(sum[:]) != g.Challenge || r.PostForm.Get("redirect_uri") != g.RedirectURI {
			oauthError(w, "invalid_grant", "code verifier or redirect does not match")
			return
		}
		s.issueTokens(w, r, g)

	case pairingType:
		hash := hashToken(r.PostForm.Get("code"))
		g, err := s.Store.GetGrant(ctx, hash)
		if err != nil || g.Kind != grantPair {
			oauthError(w, "invalid_grant", "unknown or expired pairing code")
			return
		}
		if !g.Confirmed {
			// The first call names the device; the app then polls until the
			// user confirms that name in the web app.
			if g.DeviceName == "" {
				g.DeviceName, g.Platform = deviceName(r.PostForm.Get("device_name")), r.PostForm.Get("platform")
				if err := s.Store.PutGrant(ctx, g); err != nil {
					s.failErr(w, err)
					return
				}
			}
			oauthError(w, "authorization_pending", "waiting for confirmation in the web app")
			return
		}
		s.Store.DeleteGrant(ctx, hash)
		s.issueTokens(w, r, g)

	case "refresh_token":
		d, err := s.Store.DeviceByRefresh(ctx, hashToken(r.PostForm.Get("refresh_token")))
		if err != nil {
			oauthError(w, "invalid_grant", "unknown refresh token")
			return
		}
		access, refresh := "", ""
		access, d.AccessHash = newToken()
		refresh, d.RefreshHash = newToken()
		d.AccessExpires = time.Now().Add(accessTTL)
		if err := s.Store.RotateDevice(ctx, d); err != nil {
			s.failErr(w, err)
			return
		}
		tokenReply(w, access, refresh)

	default:
		oauthError(w, "unsupported_grant_type", "")
	}
}

func (s *Server) issueTokens(w http.ResponseWriter, r *http.Request, g store.Grant) {
	d := store.Device{UserID: g.UserID, Name: g.DeviceName, Platform: g.Platform, AccessExpires: time.Now().Add(accessTTL)}
	access, refresh := "", ""
	access, d.AccessHash = newToken()
	refresh, d.RefreshHash = newToken()
	if err := s.Store.CreateDevice(r.Context(), &d); err != nil {
		s.failErr(w, err)
		return
	}
	s.Store.Audit(r.Context(), g.UserID, "add_device", d.Name)
	tokenReply(w, access, refresh)
}

func tokenReply(w http.ResponseWriter, access, refresh string) {
	ok(w, map[string]any{
		"access_token": access, "refresh_token": refresh,
		"token_type": "Bearer", "expires_in": int(accessTTL / time.Second),
	})
}

// handleOAuthRevoke signs a device out. Either of its tokens identifies it.
func (s *Server) handleOAuthRevoke(w http.ResponseWriter, r *http.Request) {
	r.ParseForm()
	ctx := r.Context()
	hash := hashToken(r.PostForm.Get("token"))
	d, err := s.Store.DeviceByRefresh(ctx, hash)
	if err != nil {
		d, err = s.Store.DeviceByAccess(ctx, hash, time.Now())
	}
	if err == nil {
		s.Store.DeleteDevice(ctx, d.UserID, d.ID)
	}
	ok(w, map[string]any{}) // RFC 7009: always succeed
}

// --- devices and QR pairing ---

func (s *Server) handleDevices(w http.ResponseWriter, r *http.Request, u store.User) {
	devices, err := s.Store.ListDevices(r.Context(), u.ID)
	if err != nil {
		s.failErr(w, err)
		return
	}
	out := []map[string]any{}
	for _, d := range devices {
		out = append(out, map[string]any{
			"id": d.ID, "name": d.Name, "platform": d.Platform,
			"created_at": unix(d.CreatedAt), "last_seen": unix(d.LastSeen),
		})
	}
	ok(w, map[string]any{"devices": out})
}

func (s *Server) handleDeleteDevice(w http.ResponseWriter, r *http.Request, u store.User) {
	if err := s.Store.DeleteDevice(r.Context(), u.ID, pathID(r)); err != nil {
		s.failErr(w, err)
		return
	}
	ok(w, map[string]any{})
}

func (s *Server) pairingURI(code string) string {
	return "codemail://pair?server=" + url.QueryEscape(s.cfg.Origin) + "&code=" + code
}

func (s *Server) handleNewPairing(w http.ResponseWriter, r *http.Request, u store.User) {
	code, hash := newToken()
	g := store.Grant{CodeHash: hash, Kind: grantPair, UserID: u.ID, ExpiresAt: time.Now().Add(grantTTL)}
	if err := s.Store.PutGrant(r.Context(), g); err != nil {
		s.failErr(w, err)
		return
	}
	// The web app refers to the pairing by the hash, which is useless to
	// anyone who has not seen the code itself.
	reply(w, http.StatusCreated, map[string]any{
		"id": hex.EncodeToString(hash), "code": code, "uri": s.pairingURI(code), "expires_at": unix(g.ExpiresAt),
	})
}

func (s *Server) pairing(w http.ResponseWriter, r *http.Request, u store.User) (store.Grant, bool) {
	hash, err := hex.DecodeString(r.PathValue("id"))
	if err != nil {
		fail(w, http.StatusNotFound, "not found")
		return store.Grant{}, false
	}
	g, err := s.Store.GetGrant(r.Context(), hash)
	if err == nil && (g.Kind != grantPair || g.UserID != u.ID) {
		err = store.ErrNotFound
	}
	if err != nil {
		s.failErr(w, err)
		return g, false
	}
	return g, true
}

func (s *Server) handlePairingStatus(w http.ResponseWriter, r *http.Request, u store.User) {
	g, found := s.pairing(w, r, u)
	if !found {
		return
	}
	ok(w, map[string]any{
		"claimed": g.DeviceName != "", "device_name": g.DeviceName, "platform": g.Platform,
		"confirmed": g.Confirmed, "expires_at": unix(g.ExpiresAt),
	})
}

func (s *Server) handleConfirmPairing(w http.ResponseWriter, r *http.Request, u store.User) {
	g, found := s.pairing(w, r, u)
	if !found {
		return
	}
	if g.DeviceName == "" {
		fail(w, http.StatusConflict, "no device has scanned this code yet")
		return
	}
	g.Confirmed = true
	if err := s.Store.PutGrant(r.Context(), g); err != nil {
		s.failErr(w, err)
		return
	}
	ok(w, map[string]any{})
}

// handlePairingQR renders the pairing link as a QR code. The link holds
// the code, which the page already has, so it is passed in the query.
func (s *Server) handlePairingQR(w http.ResponseWriter, r *http.Request, u store.User) {
	if _, found := s.pairing(w, r, u); !found {
		return
	}
	code := r.URL.Query().Get("code")
	if hex.EncodeToString(hashToken(code)) != r.PathValue("id") {
		fail(w, http.StatusBadRequest, "code does not match")
		return
	}
	png, err := qrcode.Encode(s.pairingURI(code), qrcode.Medium, 360)
	if err != nil {
		s.failErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", "no-store")
	w.Write(png)
}
