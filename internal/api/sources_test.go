package api_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/descope/virtualwebauthn"
	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapserver"
	"github.com/emersion/go-imap/v2/imapserver/imapmemserver"
	"github.com/emersion/go-smtp"

	"github.com/tikaydaes/codemail/internal/api"
	"github.com/tikaydaes/codemail/internal/extract"
	"github.com/tikaydaes/codemail/internal/fetch"
	"github.com/tikaydaes/codemail/internal/hub"
	"github.com/tikaydaes/codemail/internal/ingest"
	"github.com/tikaydaes/codemail/internal/smtpd"
	"github.com/tikaydaes/codemail/internal/store/sqlstore"
	"github.com/tikaydaes/codemail/internal/web"
)

type literal struct{ *bytes.Reader }

func (l literal) Size() int64 { return l.Reader.Size() }

func eml(from, to, subject, body string, extra ...string) string {
	head := strings.Join(append(extra, "From: "+from, "To: "+to, "Subject: "+subject, "Message-ID: <"+subject+"@test>"), "\r\n")
	return head + "\r\n\r\n" + body + "\r\n"
}

// fakeJMAP is just enough of a JMAP server: one account, an inbox and a
// junk folder, and messages that can be added while it runs.
type fakeJMAP struct {
	mu   sync.Mutex
	msgs []struct {
		id, received string
		raw          string
	}
}

func (f *fakeJMAP) add(raw string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.msgs = append(f.msgs, struct {
		id, received string
		raw          string
	}{fmt.Sprintf("m%d", len(f.msgs)+1), time.Now().Add(2 * time.Second).UTC().Format(time.RFC3339), raw})
}

func (f *fakeJMAP) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer good-token" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	base := "http://" + r.Host
	switch {
	case r.URL.Path == "/.well-known/jmap":
		json.NewEncoder(w).Encode(map[string]any{
			"apiUrl": base + "/api", "downloadUrl": base + "/dl/{accountId}/{blobId}/{name}?type={type}",
			"primaryAccounts": map[string]string{"urn:ietf:params:jmap:mail": "acct"},
		})
	case r.URL.Path == "/api":
		var req struct {
			MethodCalls [][]json.RawMessage `json:"methodCalls"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		var out [][]any
		var ids []string
		for _, call := range req.MethodCalls {
			var name string
			json.Unmarshal(call[0], &name)
			switch name {
			case "Mailbox/get":
				out = append(out, []any{name, map[string]any{"list": []any{
					map[string]any{"id": "in", "role": "inbox", "name": "Inbox"},
					map[string]any{"id": "jk", "role": "junk", "name": "Spam"}}}, "m"})
			case "Email/query":
				var args struct {
					Filter struct {
						Conditions []struct{ After string }
					}
				}
				json.Unmarshal(call[1], &args)
				after := args.Filter.Conditions[0].After
				ids = nil
				for _, m := range f.msgs {
					if m.received >= after {
						ids = append(ids, m.id)
					}
				}
				out = append(out, []any{name, map[string]any{"ids": ids}, "q"})
			case "Email/get":
				list := []any{}
				for _, m := range f.msgs {
					for _, id := range ids {
						if id == m.id {
							list = append(list, map[string]any{"id": m.id, "blobId": "b" + m.id, "receivedAt": m.received, "size": len(m.raw)})
						}
					}
				}
				out = append(out, []any{name, map[string]any{"list": list}, "g"})
			}
		}
		json.NewEncoder(w).Encode(map[string]any{"methodResponses": out})
	case strings.HasPrefix(r.URL.Path, "/dl/acct/b"):
		id := strings.Split(strings.TrimPrefix(r.URL.Path, "/dl/acct/b"), "/")[0]
		for _, m := range f.msgs {
			if m.id == id {
				w.Write([]byte(m.raw))
			}
		}
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func TestSources(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st, err := sqlstore.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	log := slog.New(slog.NewTextHandler(testWriter{t}, &slog.HandlerOptions{Level: slog.LevelError}))

	httpLn, _ := net.Listen("tcp", "127.0.0.1:0")
	origin := fmt.Sprintf("http://localhost:%d", httpLn.Addr().(*net.TCPAddr).Port)
	h := hub.New()
	srv, err := api.New(api.Config{Origin: origin, MailHost: "mx.test", DefaultRetention: time.Hour}, st, h, extract.Default, web.Files(), log)
	if err != nil {
		t.Fatal(err)
	}
	sealer, _ := fetch.NewSealer(bytes.Repeat([]byte{7}, 32))
	fetcher := &fetch.Manager{Store: st, Auth: passAuth{}, Sealer: sealer, Log: log,
		Pipe: &ingest.Pipeline{Store: st, Hub: h, Model: extract.Default, Log: log, Retention: time.Hour},
		Cfg:  fetch.Config{Interval: 150 * time.Millisecond, MaxBytes: 1 << 20, AllowPrivate: true, Insecure: true}}
	srv.Fetch = fetcher
	go fetcher.Run(ctx)
	go http.Serve(httpLn, srv.Handler())

	smtpLn, _ := net.Listen("tcp", "127.0.0.1:0")
	mta := smtpd.NewServer(&smtpd.Backend{Store: st, Hub: h, Model: extract.Default, Auth: passAuth{}, Log: log,
		Cfg: smtpd.Config{Hostname: "mx.test", Retention: time.Hour, MaxBytes: 1 << 20}})
	go mta.Serve(smtpLn)
	defer mta.Close()

	e := &env{t: t, origin: origin, smtp: smtpLn.Addr().String(),
		rp: virtualwebauthn.RelyingParty{Name: "codemail", ID: "localhost", Origin: origin}}
	setup, _ := srv.EnsureSetupToken(ctx)
	sam := e.browser()
	if status, out := sam.register("sam", map[string]any{"setup": setup}); status != 200 {
		t.Fatalf("register = %d %v", status, out)
	}
	sam.must(201, "POST", "/api/v1/admin/domains", map[string]any{"name": "codes.example"})

	deliver := func(envelopeFrom, raw string) {
		t.Helper()
		c, err := smtp.Dial(e.smtp)
		if err != nil {
			t.Fatal(err)
		}
		defer c.Close()
		if err := c.SendMail(envelopeFrom, []string{"sam@codes.example"}, strings.NewReader(raw)); err != nil {
			t.Fatal(err)
		}
	}
	// messages returns everything stored, newest first, keyed by subject.
	messages := func() map[string]map[string]any {
		out := map[string]map[string]any{}
		for _, m := range list(sam.must(200, "GET", "/api/v1/messages", nil), "messages") {
			m := m.(map[string]any)
			out[m["subject"].(string)] = m
		}
		return out
	}
	flagged := func(m map[string]any, flag string) bool {
		for _, f := range list(m, "flags") {
			if f == flag {
				return true
			}
		}
		return false
	}
	waitFor := func(what string, ready func() bool) {
		t.Helper()
		for i := 0; i < 100; i++ {
			if ready() {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %s", what)
	}
	source := func(id any) map[string]any {
		for _, s := range list(sam.must(200, "GET", "/api/v1/sources", nil), "sources") {
			if s.(map[string]any)["id"] == id {
				return s.(map[string]any)
			}
		}
		return nil
	}
	const code = "Your Northwind verification code is 482913. It expires in 10 minutes."
	const news = "This month at Northwind: new branch opening hours and a note from our chief executive."

	// --- forwarding from another mailbox ---
	fwd := sam.must(201, "POST", "/api/v1/sources", map[string]any{"address": "Sam@Mailbox.example", "kind": "forward"})["source"].(map[string]any)
	if fwd["address"] != "sam@mailbox.example" || fwd["codes_only"] != true {
		t.Fatalf("forward source = %v", fwd)
	}
	sam.must(409, "POST", "/api/v1/sources", map[string]any{"address": "sam@mailbox.example", "kind": "forward"})
	deliver("bounce@northwind.example", eml("Northwind <otp@northwind.example>", "sam@mailbox.example", "direct", code))
	// A rule forwards two messages: the mailbox leaves its address on them.
	deliver("sam+caf_=sam=codes.example@mailbox.example", eml("Northwind <otp@northwind.example>", "someone@else.example", "fwd-code", code, "Delivered-To: sam@mailbox.example"))
	deliver("bounce@northwind.example", eml("Northwind <news@northwind.example>", "sam@mailbox.example", "fwd-news", news))
	got := messages()
	if m := got["fwd-code"]; m == nil || !flagged(m, "forwarded") || m["code"] != "482913" || m["auth"] != "pass" {
		t.Errorf("forwarded code = %v", m)
	}
	if got["fwd-news"] != nil {
		t.Error("a forwarded message without a code was kept although the mailbox is set to codes only")
	}
	// "To: sam@mailbox.example" marks the first message as forwarded too.
	if m := got["direct"]; m == nil || !flagged(m, "forwarded") {
		t.Errorf("message addressed to the linked mailbox = %v", m)
	}
	// The user may choose to keep everything they forward.
	sam.must(200, "PATCH", fmt.Sprintf("/api/v1/sources/%v", fwd["id"]), map[string]any{"codes_only": false})
	deliver("bounce@northwind.example", eml("Northwind <news@northwind.example>", "sam@mailbox.example", "fwd-news-kept", news))
	if messages()["fwd-news-kept"] == nil {
		t.Error("a forwarded message without a code was dropped although the mailbox keeps everything")
	}
	// Forwarded by hand: the message is From the user, quoting the original.
	deliver("sam@mailbox.example", eml("Sam <sam@mailbox.example>", "sam@codes.example", "Fwd: by-hand",
		"---------- Forwarded message ---------\r\nFrom: Northwind Bank <otp@northwind.example>\r\nDate: Mon, 2 Nov 2026\r\nSubject: Your code\r\nTo: <sam@mailbox.example>\r\n\r\n"+code))
	if m := messages()["Your code"]; m == nil || m["code"] != "482913" || m["sender_domain"] != "northwind.example" || m["auth"] != "none" || !flagged(m, "forwarded") {
		t.Errorf("message forwarded by hand = %v (all: %v)", m, messages())
	}
	// Mail that has nothing to do with the mailbox is untouched.
	deliver("bounce@northwind.example", eml("Northwind <otp@northwind.example>", "sam@codes.example", "plain", code))
	if m := messages()["plain"]; m == nil || flagged(m, "forwarded") {
		t.Errorf("ordinary message = %v", m)
	}

	// --- polling over IMAP ---
	mem := imapmemserver.New()
	box := imapmemserver.NewUser("sam@mailbox.example", "app-password")
	box.Create("INBOX", nil)
	box.Create("Junk", nil)
	mem.AddUser(box)
	put := func(folder, raw string) {
		t.Helper()
		if _, err := box.Append(folder, literal{bytes.NewReader([]byte(raw))}, &imap.AppendOptions{}); err != nil {
			t.Fatal(err)
		}
	}
	put("INBOX", eml("Northwind <otp@northwind.example>", "sam@mailbox.example", "imap-old", code))
	imapLn, _ := net.Listen("tcp", "127.0.0.1:0")
	imapSrv := imapserver.New(&imapserver.Options{
		NewSession: func(*imapserver.Conn) (imapserver.Session, *imapserver.GreetingData, error) {
			return mem.NewSession(), nil, nil
		},
		InsecureAuth: true,
	})
	go imapSrv.Serve(imapLn)
	defer imapSrv.Close()
	imapAddr := imapLn.Addr().String()

	bad := map[string]any{"address": "sam@mailbox.example", "kind": "imap", "host": imapAddr, "password": "wrong"}
	if status, out := sam.do("POST", "/api/v1/sources", bad); status != 400 || !strings.Contains(fmt.Sprint(out["error"]), "refused the sign-in") {
		t.Fatalf("linking with a wrong password = %d %v", status, out)
	}
	bad["password"] = "app-password"
	polled := sam.must(201, "POST", "/api/v1/sources", bad)["source"].(map[string]any)
	if _, leaked := polled["password"]; leaked || polled["username"] != "sam@mailbox.example" {
		t.Fatalf("imap source = %v", polled)
	}
	waitFor("the first look at the IMAP mailbox", func() bool { return source(polled["id"])["last_checked"].(float64) > 0 })
	put("INBOX", eml("Northwind <otp@northwind.example>", "sam@mailbox.example", "imap-code", code))
	put("INBOX", eml("Northwind <news@northwind.example>", "sam@mailbox.example", "imap-news", news))
	put("Junk", eml("Northwind <otp@northwind.example>", "sam@mailbox.example", "imap-junk-code", code))
	waitFor("fetched IMAP messages", func() bool { m := messages(); return m["imap-code"] != nil && m["imap-junk-code"] != nil })
	got = messages()
	if m := got["imap-code"]; !flagged(m, "fetched") || m["rcpt"] != "sam@mailbox.example" || m["code"] != "482913" {
		t.Errorf("fetched message = %v", m)
	}
	if got["imap-news"] != nil || got["imap-old"] != nil {
		t.Errorf("fetched more than new codes: news=%v old=%v", got["imap-news"] != nil, got["imap-old"] != nil)
	}
	checked := sam.must(200, "POST", fmt.Sprintf("/api/v1/sources/%v/check", polled["id"]), nil)["source"].(map[string]any)
	if checked["last_error"] != "" {
		t.Errorf("check reported %v", checked["last_error"])
	}

	// --- polling over JMAP ---
	jm := &fakeJMAP{}
	jmapSrv := httptest.NewServer(jm)
	defer jmapSrv.Close()
	if status, _ := sam.do("POST", "/api/v1/sources", map[string]any{"address": "sam@fast.example", "kind": "jmap", "host": jmapSrv.URL, "password": "bad-token"}); status != 400 {
		t.Fatalf("linking JMAP with a bad token = %d", status)
	}
	jsrc := sam.must(201, "POST", "/api/v1/sources", map[string]any{"address": "sam@fast.example", "kind": "jmap", "host": jmapSrv.URL, "password": "good-token"})["source"].(map[string]any)
	waitFor("the first look at the JMAP mailbox", func() bool { return source(jsrc["id"])["last_checked"].(float64) > 0 })
	jm.add(eml("Northwind <otp@northwind.example>", "sam@fast.example", "jmap-code", code))
	jm.add(eml("Northwind <news@northwind.example>", "sam@fast.example", "jmap-news", news))
	waitFor("the fetched JMAP message", func() bool { return messages()["jmap-code"] != nil })
	time.Sleep(400 * time.Millisecond) // a few more looks must not take it twice
	n := 0
	for _, m := range list(sam.must(200, "GET", "/api/v1/messages", nil), "messages") {
		if m.(map[string]any)["subject"] == "jmap-code" {
			n++
		}
	}
	if n != 1 || messages()["jmap-news"] != nil {
		t.Errorf("JMAP: code stored %d times, news kept: %v", n, messages()["jmap-news"] != nil)
	}

	// --- removal, and other users ---
	sam.must(200, "DELETE", fmt.Sprintf("/api/v1/sources/%v", polled["id"]), nil)
	if source(polled["id"]) != nil {
		t.Error("removed source still listed")
	}
	sam.must(404, "PATCH", "/api/v1/sources/9999", map[string]any{"codes_only": true})
}
