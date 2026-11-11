package api_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/descope/virtualwebauthn"
	"github.com/emersion/go-smtp"

	"github.com/tikaydaes/codemail/internal/api"
	"github.com/tikaydaes/codemail/internal/dnscheck"
	"github.com/tikaydaes/codemail/internal/extract"
	"github.com/tikaydaes/codemail/internal/hub"
	"github.com/tikaydaes/codemail/internal/mailauth"
	"github.com/tikaydaes/codemail/internal/smtpd"
	"github.com/tikaydaes/codemail/internal/store/sqlstore"
	"github.com/tikaydaes/codemail/internal/web"
)

// fakeDNS answers domain verification lookups from maps.
type fakeDNS struct {
	mx  map[string]string
	txt map[string]string
}

func (f *fakeDNS) LookupMX(_ context.Context, name string) ([]*net.MX, error) {
	if h, found := f.mx[name]; found {
		return []*net.MX{{Host: h + ".", Pref: 10}}, nil
	}
	return nil, &net.DNSError{IsNotFound: true}
}

func (f *fakeDNS) LookupTXT(_ context.Context, name string) ([]string, error) {
	if v, found := f.txt[name]; found {
		return []string{v}, nil
	}
	return nil, &net.DNSError{IsNotFound: true}
}

// passAuth treats every message as genuinely from its From domain.
type passAuth struct{}

func (passAuth) Check(_ net.IP, _, _, fromHdr string, _ []byte) mailauth.Result {
	return mailauth.Result{Verdict: mailauth.Pass, Detail: "test", Domain: mailauth.FromDomain(fromHdr)}
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) {
	w.t.Log(strings.TrimSpace(string(p)))
	return len(p), nil
}

type env struct {
	t      *testing.T
	origin string
	smtp   string
	dns    *fakeDNS
	rp     virtualwebauthn.RelyingParty
}

// client is one browser (cookie jar) or one app (bearer token).
type client struct {
	e      *env
	http   *http.Client
	bearer string
	auth   virtualwebauthn.Authenticator
	cred   virtualwebauthn.Credential
}

func (e *env) browser() *client {
	jar, _ := cookiejar.New(nil)
	return &client{e: e, http: &http.Client{Jar: jar}, auth: virtualwebauthn.NewAuthenticator(),
		cred: virtualwebauthn.NewCredential(virtualwebauthn.KeyTypeEC2)}
}

// do sends a JSON request and decodes the JSON reply into a map.
func (c *client) do(method, path string, body any) (int, map[string]any) {
	c.e.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, c.e.origin+path, rd)
	req.Header.Set("Content-Type", "application/json")
	if c.bearer != "" {
		req.Header.Set("Authorization", "Bearer "+c.bearer)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		c.e.t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

// must is do, failing the test unless the status is as expected.
func (c *client) must(want int, method, path string, body any) map[string]any {
	c.e.t.Helper()
	got, out := c.do(method, path, body)
	if got != want {
		c.e.t.Fatalf("%s %s = %d %v, want %d", method, path, got, out, want)
	}
	return out
}

func (c *client) register(username string, extra map[string]any) (int, map[string]any) {
	c.e.t.Helper()
	body := map[string]any{"username": username}
	for k, v := range extra {
		body[k] = v
	}
	status, begin := c.do("POST", "/api/v1/passkey/register/begin", body)
	if status != 200 {
		return status, begin
	}
	raw, _ := json.Marshal(map[string]any{"publicKey": begin["publicKey"]})
	opts, err := virtualwebauthn.ParseAttestationOptions(string(raw))
	if err != nil {
		c.e.t.Fatal(err)
	}
	handle := begin["publicKey"].(map[string]any)["user"].(map[string]any)["id"].(string)
	c.auth.Options.UserHandle, _ = base64.RawURLEncoding.DecodeString(handle)
	resp := virtualwebauthn.CreateAttestationResponse(c.e.rp, c.auth, c.cred, *opts)
	c.auth.AddCredential(c.cred)
	return c.do("POST", "/api/v1/passkey/register/finish",
		map[string]any{"session": begin["session"], "credential": json.RawMessage(resp)})
}

func (c *client) login() (int, map[string]any) {
	c.e.t.Helper()
	begin := c.must(200, "POST", "/api/v1/passkey/login/begin", map[string]any{})
	raw, _ := json.Marshal(map[string]any{"publicKey": begin["publicKey"]})
	opts, err := virtualwebauthn.ParseAssertionOptions(string(raw))
	if err != nil {
		c.e.t.Fatal(err)
	}
	resp := virtualwebauthn.CreateAssertionResponse(c.e.rp, c.auth, c.cred, *opts)
	return c.do("POST", "/api/v1/passkey/login/finish",
		map[string]any{"session": begin["session"], "credential": json.RawMessage(resp)})
}

func (e *env) send(from, to, subject, body string) error {
	msg := fmt.Sprintf("From: %s\r\nTo: %s\r\nSubject: %s\r\n\r\n%s\r\n", from, to, subject, body)
	c, err := smtp.Dial(e.smtp)
	if err != nil {
		return err
	}
	defer c.Close()
	return c.SendMail("bounce@sender.example", []string{to}, strings.NewReader(msg))
}

func (e *env) token(form url.Values) (int, map[string]any) {
	resp, err := http.PostForm(e.origin+"/oauth/token", form)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	out := map[string]any{}
	json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func list(m map[string]any, key string) []any { l, _ := m[key].([]any); return l }

func TestEverything(t *testing.T) {
	ctx := context.Background()
	// The same test runs against PostgreSQL when CODEMAIL_TEST_POSTGRES
	// names an empty database, e.g. postgres://localhost:5432/codemail_test
	var st *sqlstore.DB
	var err error
	if url := os.Getenv("CODEMAIL_TEST_POSTGRES"); url != "" {
		wipe, werr := sql.Open("pgx", url)
		if werr != nil {
			t.Fatal(werr)
		}
		if _, werr := wipe.Exec(`DROP SCHEMA public CASCADE; CREATE SCHEMA public`); werr != nil {
			t.Fatal(werr)
		}
		wipe.Close()
		st, err = sqlstore.OpenPostgres(url)
	} else {
		st, err = sqlstore.Open(filepath.Join(t.TempDir(), "test.db"))
	}
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	t.Logf("database: postgres=%v", st.Postgres())
	// Server errors are shown in the test output; routine lines are not.
	log := slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelError}))

	httpLn, _ := net.Listen("tcp", "127.0.0.1:0")
	origin := fmt.Sprintf("http://localhost:%d", httpLn.Addr().(*net.TCPAddr).Port)
	h := hub.New()
	srv, err := api.New(api.Config{Origin: origin, MailHost: "mx.test", DefaultRetention: time.Hour}, st, h, extract.Default, web.Files(), log)
	if err != nil {
		t.Fatal(err)
	}
	dns := &fakeDNS{mx: map[string]string{}, txt: map[string]string{}}
	srv.DNS = dnscheck.Resolver(dns)
	go http.Serve(httpLn, srv.Handler())

	smtpLn, _ := net.Listen("tcp", "127.0.0.1:0")
	mta := smtpd.NewServer(&smtpd.Backend{Store: st, Hub: h, Model: extract.Default, Auth: passAuth{}, Log: log,
		Cfg: smtpd.Config{Hostname: "mx.test", Retention: time.Hour, MaxBytes: 1 << 20}})
	go mta.Serve(smtpLn)
	defer mta.Close()

	e := &env{t: t, origin: origin, smtp: smtpLn.Addr().String(), dns: dns,
		rp: virtualwebauthn.RelyingParty{Name: "codemail", ID: "localhost", Origin: origin}}

	// --- setup: the first account needs the setup token and becomes admin ---
	setup, err := srv.EnsureSetupToken(ctx)
	if err != nil || setup == "" {
		t.Fatalf("setup token = %q, %v", setup, err)
	}
	kaity := e.browser()
	if status, _ := kaity.register("kaity", nil); status != 403 {
		t.Fatalf("registering without the setup token = %d, want 403", status)
	}
	status, out := kaity.register("kaity", map[string]any{"setup": setup})
	if status != 200 || len(list(out, "recovery_codes")) != 8 {
		t.Fatalf("admin registration = %d %v", status, out)
	}
	me := kaity.must(200, "GET", "/api/v1/me", nil)
	if me["user"].(map[string]any)["is_admin"] != true {
		t.Fatalf("first user is not admin: %v", me)
	}
	if again, _ := srv.EnsureSetupToken(ctx); again != "" {
		t.Error("setup token still valid after setup")
	}

	// --- admin: shared domain, invite-only registration ---
	shared := kaity.must(201, "POST", "/api/v1/admin/domains", map[string]any{"name": "codes.example", "max_generated": 2})
	sharedID := shared["domain"].(map[string]any)["id"]
	sam := e.browser()
	if status, _ := sam.register("sam", nil); status != 403 {
		t.Fatalf("registering without an invite = %d, want 403", status)
	}
	invite := kaity.must(201, "POST", "/api/v1/admin/invites", map[string]any{"max_uses": 1})["invite"].(map[string]any)["code"]
	status, out = sam.register("sam", map[string]any{"invite": invite})
	if status != 200 {
		t.Fatalf("invited registration = %d %v", status, out)
	}
	samCodes := list(out, "recovery_codes")
	if status, _ := e.browser().register("robin", map[string]any{"invite": invite}); status != 403 {
		t.Errorf("reusing a single-use invite = %d, want 403", status)
	}
	if status, _ := e.browser().register("admin", map[string]any{"invite": invite}); status != 400 {
		t.Errorf("reserved username = %d, want 400", status)
	}
	if status, _ := sam.do("GET", "/api/v1/admin/users", nil); status != 403 {
		t.Errorf("non-admin reading admin API = %d, want 403", status)
	}

	// --- sign in again with the passkey, and with a recovery code ---
	kaity2 := &client{e: e, auth: kaity.auth, cred: kaity.cred}
	jar, _ := cookiejar.New(nil)
	kaity2.http = &http.Client{Jar: jar}
	if status, out := kaity2.login(); status != 200 || out["user"].(map[string]any)["username"] != "kaity" {
		t.Fatalf("passkey sign-in = %d %v", status, out)
	}
	sessions := list(kaity.must(200, "GET", "/api/v1/sessions", nil), "sessions")
	if len(sessions) != 2 {
		t.Fatalf("kaity has %d sessions, want 2", len(sessions))
	}
	for _, x := range sessions {
		se := x.(map[string]any)
		if se["current"] == true {
			continue
		}
		// Sign the other browser out from this one.
		kaity.must(200, "DELETE", fmt.Sprintf("/api/v1/sessions/%v", se["id"]), nil)
	}
	if status, _ := kaity2.do("GET", "/api/v1/me", nil); status != 401 {
		t.Errorf("a revoked browser session = %d, want 401", status)
	}
	kaity.must(200, "GET", "/api/v1/me", nil)
	if status, _ := sam.do("DELETE", fmt.Sprintf("/api/v1/sessions/%v", sessions[0].(map[string]any)["id"]), nil); status != 404 {
		t.Errorf("revoking another user's session = %d, want 404", status)
	}

	rec := e.browser()
	rec.must(200, "POST", "/api/v1/recovery", map[string]any{"username": "sam", "code": samCodes[0]})
	if status, _ := e.browser().do("POST", "/api/v1/recovery", map[string]any{"username": "sam", "code": samCodes[0]}); status != 401 {
		t.Errorf("reusing a recovery code = %d, want 401", status)
	}
	if status, _ := kaity.do("DELETE", fmt.Sprintf("/api/v1/passkeys/%v",
		list(kaity.must(200, "GET", "/api/v1/passkeys", nil), "passkeys")[0].(map[string]any)["id"]), nil); status != 409 {
		t.Errorf("deleting the only passkey = %d, want 409", status)
	}

	// --- mail arrives and is streamed live ---
	req, _ := http.NewRequest("GET", origin+"/api/v1/events", nil)
	stream, err := kaity.http.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	events := make(chan string, 16)
	go func() {
		sc := bufio.NewScanner(stream.Body)
		for sc.Scan() {
			if data, found := strings.CutPrefix(sc.Text(), "data: "); found {
				events <- data
			}
		}
	}()
	time.Sleep(100 * time.Millisecond) // let the subscription start

	if err := e.send("Foo <no-reply@foo.example>", "kaity+bank@codes.example", "Your Foo code", "Your verification code is 551234. It expires in 10 minutes."); err != nil {
		t.Fatal(err)
	}
	select {
	case data := <-events:
		if !strings.Contains(data, `"code":"551234"`) {
			t.Errorf("event = %s", data)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no live event")
	}
	if err := e.send("News <news@shop.example>", "anything@kaity.codes.example", "November news", "We shipped 12 features."); err != nil {
		t.Fatal(err)
	}
	if err := e.send("X <x@x.example>", "nobody@codes.example", "hi", "hi"); err == nil {
		t.Error("mail to an unknown address was accepted")
	}
	codes := list(kaity.must(200, "GET", "/api/v1/messages?kind=code", nil), "messages")
	other := list(kaity.must(200, "GET", "/api/v1/messages?kind=other", nil), "messages")
	if len(codes) != 1 || len(other) != 1 {
		t.Fatalf("codes = %d, other = %d, want 1 and 1", len(codes), len(other))
	}
	msg := codes[0].(map[string]any)
	id := int64(msg["id"].(float64))
	if msg["service"] != "Foo" || msg["auth"] != "pass" || msg["sender_domain"] != "foo.example" {
		t.Errorf("message = %v", msg)
	}
	detail := kaity.must(200, "GET", fmt.Sprintf("/api/v1/messages/%d", id), nil)["message"].(map[string]any)
	if !strings.Contains(detail["markdown"].(string), "551234") {
		t.Errorf("detail markdown = %q", detail["markdown"])
	}
	if status, _ := sam.do("GET", fmt.Sprintf("/api/v1/messages/%d", id), nil); status != 404 {
		t.Errorf("reading another user's message = %d, want 404", status)
	}
	for _, q := range []string{"551", "FOO", "your foo"} {
		if found := list(kaity.must(200, "GET", "/api/v1/messages?q="+url.QueryEscape(q), nil), "messages"); len(found) != 1 {
			t.Errorf("search %q found %d, want 1", q, len(found))
		}
	}

	// --- the extension is offered the code only on the sender's site ---
	site := func(c *client, domain string) string {
		l := list(c.must(200, "GET", "/api/v1/codes/for-site?domain="+domain, nil), "messages")
		if len(l) == 0 {
			return "absent"
		}
		return l[0].(map[string]any)["match"].(string)
	}
	if got := site(kaity, "login.foo.example"); got != "sender" {
		t.Errorf("match on the sender's site = %q, want sender", got)
	}
	if got := site(kaity, "evil.example"); got != "" {
		t.Errorf("match on another site = %q, want none", got)
	}
	kaity.must(200, "POST", "/api/v1/site-links", map[string]any{"message_id": id, "domain": "foo-accounts.example"})
	if got := site(kaity, "foo-accounts.example"); got != "learned" {
		t.Errorf("match on a learned site = %q, want learned", got)
	}

	// --- used state and corrections ---
	kaity.must(200, "PATCH", fmt.Sprintf("/api/v1/messages/%d", id), map[string]any{"used": true})
	if got := site(kaity, "foo.example"); got != "absent" {
		t.Errorf("a used code is still offered: %q", got)
	}
	cands := list(kaity.must(200, "GET", fmt.Sprintf("/api/v1/messages/%d/candidates", id), nil), "candidates")
	if len(cands) == 0 {
		t.Error("no candidates")
	}
	if status, _ := kaity.do("POST", fmt.Sprintf("/api/v1/messages/%d/correction", id), map[string]any{"kind": "code", "value": "999999"}); status != 400 {
		t.Errorf("correcting to a value not in the message = %d, want 400", status)
	}
	kaity.must(200, "POST", fmt.Sprintf("/api/v1/messages/%d/correction", id), map[string]any{"kind": "code", "value": ""})
	if n := kaity.must(200, "GET", "/api/v1/admin/status", nil)["corrections"]; n != float64(1) {
		t.Errorf("corrections = %v, want 1", n)
	}
	if left := list(kaity.must(200, "GET", "/api/v1/messages?kind=code", nil), "messages"); len(left) != 0 {
		t.Errorf("message still listed as a code after correction")
	}

	// --- aliases: claimed, generated, limits, leak detection ---
	kaity.must(201, "POST", "/api/v1/aliases", map[string]any{"local": "shop", "domain_id": sharedID, "label": "Shopping"})
	if status, _ := sam.do("POST", "/api/v1/aliases", map[string]any{"local": "shop", "domain_id": sharedID}); status != 409 {
		t.Errorf("claiming a taken alias = %d, want 409", status)
	}
	if status, _ := sam.do("POST", "/api/v1/aliases", map[string]any{"local": "kaity", "domain_id": sharedID}); status != 409 {
		t.Errorf("claiming another user's mailbox name = %d, want 409", status)
	}
	gen := kaity.must(201, "POST", "/api/v1/aliases/generate", map[string]any{"label": "Northwind Bank"})["alias"].(map[string]any)
	addr := gen["address"].(string)
	if !strings.HasSuffix(addr, "@codes.example") || strings.Count(addr, "-") != 2 {
		t.Errorf("generated address = %q", addr)
	}
	kaity.must(201, "POST", "/api/v1/aliases/generate", map[string]any{"label": "Second"})
	if status, _ := kaity.do("POST", "/api/v1/aliases/generate", map[string]any{"label": "Third"}); status != 409 {
		t.Errorf("generating past the limit = %d, want 409", status)
	}
	e.send("Bank <otp@northwind.example>", addr, "Your code", "Your verification code is 111222.")
	e.send("Spam <x@spammer.example>", addr, "Your code", "Your verification code is 333444.")
	latest := list(kaity.must(200, "GET", "/api/v1/messages?kind=code", nil), "messages")
	if len(latest) != 2 {
		t.Fatalf("messages to generated alias = %d, want 2", len(latest))
	}
	leak, first := latest[0].(map[string]any), latest[1].(map[string]any)
	if first["service"] != "Northwind Bank" || len(list(first, "flags")) != 0 {
		t.Errorf("first message to alias = %v", first)
	}
	if fl := list(leak, "flags"); len(fl) != 1 || fl[0] != "unexpected_sender" {
		t.Errorf("message from a second sender is not flagged: %v", leak)
	}

	// --- a user's own domain needs both DNS records ---
	dom := sam.must(201, "POST", "/api/v1/domains", map[string]any{"name": "sam.example"})["domain"].(map[string]any)
	domID := int64(dom["id"].(float64))
	if err := e.send("A <a@a.example>", "x@sam.example", "s", "b"); err == nil {
		t.Error("mail accepted for an unverified domain")
	}
	dns.mx["sam.example"] = "mx.test"
	checked := sam.must(200, "POST", fmt.Sprintf("/api/v1/domains/%d/check", domID), nil)["domain"].(map[string]any)
	if checked["verified"] != false {
		t.Error("domain verified with only the MX record")
	}
	txt := list(dom, "records")[1].(map[string]any)
	dns.txt[txt["name"].(string)] = txt["value"].(string)
	checked = sam.must(200, "POST", fmt.Sprintf("/api/v1/domains/%d/check", domID), nil)["domain"].(map[string]any)
	if checked["verified"] != true {
		t.Fatalf("domain not verified with both records: %v", checked)
	}
	if err := e.send("A <a@a.example>", "whatever@sam.example", "s", "b"); err != nil {
		t.Errorf("mail to a verified own domain: %v", err)
	}
	if status, _ := kaity.do("POST", "/api/v1/domains", map[string]any{"name": "sam.example"}); status != 409 {
		t.Errorf("claiming someone else's domain = %d, want 409", status)
	}
	if status, _ := kaity.do("DELETE", fmt.Sprintf("/api/v1/domains/%d", domID), nil); status != 404 {
		t.Errorf("deleting someone else's domain = %d, want 404", status)
	}

	// --- OAuth with PKCE ---
	verifier := "verifier-verifier-verifier-verifier-verifier-123"
	sum := sha256.Sum256([]byte(verifier))
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	approve := func(redirect string) (int, map[string]any) {
		return kaity.do("POST", "/api/v1/oauth/approve", map[string]any{
			"client_id": "desktop", "redirect_uri": redirect, "code_challenge": challenge,
			"code_challenge_method": "S256", "state": "xyz", "device_name": "Kaity's Mac", "platform": "macos"})
	}
	if status, _ := approve("https://evil.example/cb"); status != 400 {
		t.Errorf("approving a web redirect = %d, want 400", status)
	}
	redirect := "http://127.0.0.1:53124/callback"
	code := func() string {
		status, out := approve(redirect)
		if status != 200 {
			t.Fatalf("approve = %d %v", status, out)
		}
		u, _ := url.Parse(out["redirect"].(string))
		if u.Query().Get("state") != "xyz" {
			t.Errorf("state not returned: %v", out)
		}
		return u.Query().Get("code")
	}
	form := func(c, v string) url.Values {
		return url.Values{"grant_type": {"authorization_code"}, "code": {c}, "code_verifier": {v}, "redirect_uri": {redirect}}
	}
	if status, _ := e.token(form(code(), "wrong")); status != 400 {
		t.Errorf("token with a wrong verifier = %d, want 400", status)
	}
	good := code()
	status, tok := e.token(form(good, verifier))
	if status != 200 {
		t.Fatalf("token = %d %v", status, tok)
	}
	if status, _ := e.token(form(good, verifier)); status != 400 {
		t.Errorf("reusing an authorization code = %d, want 400", status)
	}
	app := &client{e: e, http: http.DefaultClient, bearer: tok["access_token"].(string)}
	if got := list(app.must(200, "GET", "/api/v1/messages", nil), "messages"); len(got) != 4 {
		t.Errorf("app sees %d messages, want 4", len(got))
	}
	if status, _ := app.do("GET", "/api/v1/passkeys", nil); status != 403 {
		t.Errorf("app reaching a browser-only endpoint = %d, want 403", status)
	}
	status, tok2 := e.token(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tok["refresh_token"].(string)}})
	if status != 200 {
		t.Fatalf("refresh = %d %v", status, tok2)
	}
	if status, _ := app.do("GET", "/api/v1/me", nil); status != 401 {
		t.Errorf("old access token after refresh = %d, want 401", status)
	}
	app.bearer = tok2["access_token"].(string)
	app.must(200, "GET", "/api/v1/me", nil)
	// Two parts of an app may renew at the same moment: the token that was
	// just replaced still works briefly, so neither is signed out.
	status, tok3 := e.token(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {tok["refresh_token"].(string)}})
	if status != 200 {
		t.Fatalf("renewing with the just-replaced refresh token = %d %v", status, tok3)
	}
	app.bearer = tok3["access_token"].(string)
	app.must(200, "GET", "/api/v1/me", nil)
	if status, _ := e.token(url.Values{"grant_type": {"refresh_token"}, "refresh_token": {"made-up"}}); status != 400 {
		t.Errorf("renewing with an unknown refresh token = %d, want 400", status)
	}
	http.PostForm(origin+"/oauth/revoke", url.Values{"token": {tok3["refresh_token"].(string)}})
	if status, _ := app.do("GET", "/api/v1/me", nil); status != 401 {
		t.Errorf("revoked token = %d, want 401", status)
	}

	// --- QR pairing ---
	pair := kaity.must(201, "POST", "/api/v1/devices/pairing-code", nil)
	pairForm := url.Values{"grant_type": {"urn:codemail:params:oauth:grant-type:pairing-code"},
		"code": {pair["code"].(string)}, "device_name": {"Pixel"}, "platform": {"android"}}
	if status, out := e.token(pairForm); status != 400 || out["error"] != "authorization_pending" {
		t.Fatalf("pairing before confirmation = %d %v", status, out)
	}
	stat := kaity.must(200, "GET", "/api/v1/devices/pairing-code/"+pair["id"].(string), nil)
	if stat["device_name"] != "Pixel" {
		t.Errorf("pairing status = %v", stat)
	}
	if status, _ := sam.do("POST", "/api/v1/devices/pairing-code/"+pair["id"].(string)+"/confirm", nil); status != 404 {
		t.Errorf("another user confirming a pairing = %d, want 404", status)
	}
	kaity.must(200, "POST", "/api/v1/devices/pairing-code/"+pair["id"].(string)+"/confirm", nil)
	status, ptok := e.token(pairForm)
	if status != 200 {
		t.Fatalf("pairing after confirmation = %d %v", status, ptok)
	}
	devices := list(kaity.must(200, "GET", "/api/v1/devices", nil), "devices")
	if len(devices) != 1 || devices[0].(map[string]any)["name"] != "Pixel" {
		t.Errorf("devices = %v", devices)
	}

	// --- cross-site requests with the session cookie are refused ---
	req, _ = http.NewRequest("DELETE", origin+"/api/v1/me", nil)
	req.Header.Set("Origin", "https://evil.example")
	resp, _ := sam.http.Do(req)
	if resp.StatusCode != 403 {
		t.Errorf("cross-site delete = %d, want 403", resp.StatusCode)
	}

	// --- the whole database copies into another and stays usable ---
	copyPath := filepath.Join(t.TempDir(), "copy.db")
	dst, err := sqlstore.Open(copyPath)
	if err != nil {
		t.Fatal(err)
	}
	defer dst.Close()
	if err := sqlstore.Copy(ctx, st, dst); err != nil {
		t.Fatalf("copy: %v", err)
	}
	want, _ := st.Stats(ctx)
	got, _ := dst.Stats(ctx)
	if got.Users != want.Users || got.Messages != want.Messages || got.Corrections != want.Corrections || want.Users != 2 {
		t.Errorf("copy has %+v, source has %+v", got, want)
	}
	if r, err := dst.Resolve(ctx, "shop@codes.example"); err != nil || r.Alias == nil || r.Alias.Label != "Shopping" {
		t.Errorf("alias did not survive the copy: %+v, %v", r, err)
	}
	if err := sqlstore.Copy(ctx, st, dst); err == nil {
		t.Error("copying into a database that already has data should fail")
	}
	backupPath := filepath.Join(t.TempDir(), "backup.db")
	if err := dst.Backup(ctx, backupPath); err != nil {
		t.Errorf("backup: %v", err)
	} else if b, err := sqlstore.Open(backupPath); err != nil {
		t.Errorf("open backup: %v", err)
	} else {
		if bs, _ := b.Stats(ctx); bs.Messages != want.Messages {
			t.Errorf("backup has %d messages, want %d", bs.Messages, want.Messages)
		}
		b.Close()
	}

	// --- the web app is served, including for client-side routes ---
	for _, path := range []string{"/", "/oauth/authorize?client_id=x"} {
		resp, err := http.Get(origin + path)
		if err != nil || resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
			t.Errorf("GET %s = %v, %v", path, resp, err)
		}
	}
}
