package fetch

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// jmapState is how far the mailbox has been read: the arrival time of
// the newest message seen, and the IDs seen at about that time.
type jmapState struct {
	Since string   `json:"since"`
	Seen  []string `json:"seen"`
}

type jmapSession struct {
	http      *http.Client
	auth      string // Authorization header value
	apiURL    string
	download  string // URL template
	account   string
	mailboxes []string // the inbox and the junk folder
}

func (s *jmapSession) do(ctx context.Context, method, target string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, rd)
	if err != nil {
		return errors.New("that is not a usable address")
	}
	req.Header.Set("Authorization", s.auth)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := s.http.Do(req)
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return friendly(err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return errors.New("the mail server refused the sign-in")
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("the mail server answered %s", resp.Status)
	}
	if raw, isRaw := out.(*[]byte); isRaw {
		*raw, err = io.ReadAll(io.LimitReader(resp.Body, 64<<20))
		return err
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 16<<20)).Decode(out); err != nil {
		return errors.New("the server's answer was not JMAP; check the address")
	}
	return nil
}

// call makes one JMAP method call and decodes its response arguments.
func (s *jmapSession) call(ctx context.Context, calls []any, outs ...any) error {
	var resp struct {
		MethodResponses [][]json.RawMessage `json:"methodResponses"`
	}
	req := map[string]any{"using": []string{"urn:ietf:params:jmap:core", "urn:ietf:params:jmap:mail"}, "methodCalls": calls}
	if err := s.do(ctx, "POST", s.apiURL, req, &resp); err != nil {
		return err
	}
	if len(resp.MethodResponses) < len(outs) {
		return errors.New("the mail server gave an incomplete answer")
	}
	for i, out := range outs {
		r := resp.MethodResponses[i]
		var name string
		if len(r) < 2 || json.Unmarshal(r[0], &name) != nil {
			return errors.New("the mail server gave an unreadable answer")
		}
		if name == "error" {
			var e struct{ Type, Description string }
			json.Unmarshal(r[1], &e)
			return fmt.Errorf("the mail server reported %s %s", e.Type, e.Description)
		}
		if err := json.Unmarshal(r[1], out); err != nil {
			return errors.New("the mail server gave an unreadable answer")
		}
	}
	return nil
}

// jmapConnect reads the session and finds the mailboxes to watch. A
// token alone is sent as a bearer token; with a username it is a
// password.
func jmapConnect(ctx context.Context, client *http.Client, sessionURL, username, secret string) (*jmapSession, error) {
	s := &jmapSession{http: client, auth: "Bearer " + secret}
	if username != "" {
		req, _ := http.NewRequest("GET", "http://x", nil)
		req.SetBasicAuth(username, secret)
		s.auth = req.Header.Get("Authorization")
	}
	var session struct {
		APIURL          string            `json:"apiUrl"`
		DownloadURL     string            `json:"downloadUrl"`
		PrimaryAccounts map[string]string `json:"primaryAccounts"`
	}
	if err := s.do(ctx, "GET", sessionURL, nil, &session); err != nil {
		return nil, err
	}
	s.account = session.PrimaryAccounts["urn:ietf:params:jmap:mail"]
	if session.APIURL == "" || session.DownloadURL == "" || s.account == "" {
		return nil, errors.New("that server has no mail account for this sign-in")
	}
	base, _ := url.Parse(sessionURL)
	resolve := func(ref string) string {
		if u, err := base.Parse(ref); err == nil {
			return u.String()
		}
		return ref
	}
	// The download URL is a template; its braces must survive resolving.
	s.apiURL, s.download = resolve(session.APIURL), session.DownloadURL
	if !strings.Contains(s.download, "://") {
		s.download = strings.TrimSuffix(base.Scheme+"://"+base.Host, "/") + "/" + strings.TrimPrefix(s.download, "/")
	}
	var boxes struct {
		List []struct{ ID, Role, Name string }
	}
	err := s.call(ctx, []any{[]any{"Mailbox/get", map[string]any{"accountId": s.account, "properties": []string{"role", "name"}}, "m"}}, &boxes)
	if err != nil {
		return nil, err
	}
	inbox, junk := "", ""
	for _, b := range boxes.List {
		switch {
		case b.Role == "inbox":
			inbox = b.ID
		case b.Role == "junk", junk == "" && junkNames[strings.ToLower(b.Name)]:
			junk = b.ID
		}
	}
	if inbox == "" {
		return nil, errors.New("that mail account has no inbox")
	}
	s.mailboxes = []string{inbox}
	if junk != "" {
		s.mailboxes = append(s.mailboxes, junk)
	}
	return s, nil
}

// poll returns the messages that arrived since state, and the new state.
// The first look only notes the time: older mail is left alone.
func (s *jmapSession) poll(ctx context.Context, state jmapState, maxBytes int64) ([][]byte, jmapState, error) {
	if state.Since == "" {
		return nil, jmapState{Since: time.Now().UTC().Format(time.RFC3339)}, nil
	}
	in := make([]any, len(s.mailboxes))
	for i, id := range s.mailboxes {
		in[i] = map[string]any{"inMailbox": id}
	}
	filter := map[string]any{"operator": "AND", "conditions": []any{
		map[string]any{"after": state.Since},
		map[string]any{"operator": "OR", "conditions": in},
	}}
	var got struct {
		List []struct {
			ID         string
			BlobID     string `json:"blobId"`
			ReceivedAt string `json:"receivedAt"`
			Size       int64
		}
	}
	var query struct{ IDs []string }
	err := s.call(ctx, []any{
		[]any{"Email/query", map[string]any{"accountId": s.account, "filter": filter,
			"sort": []any{map[string]any{"property": "receivedAt", "isAscending": false}}, "limit": maxPerPoll}, "q"},
		[]any{"Email/get", map[string]any{"accountId": s.account,
			"#ids":       map[string]any{"resultOf": "q", "name": "Email/query", "path": "/ids"},
			"properties": []string{"blobId", "receivedAt", "size"}}, "g"},
	}, &query, &got)
	if err != nil {
		return nil, state, err
	}
	seen := map[string]bool{}
	for _, id := range state.Seen {
		seen[id] = true
	}
	next := jmapState{Since: state.Since}
	var out [][]byte
	for _, m := range got.List {
		next.Seen = append(next.Seen, m.ID)
		if m.ReceivedAt > next.Since {
			next.Since = m.ReceivedAt // RFC 3339 in UTC sorts as text
		}
		if seen[m.ID] || m.Size > maxBytes {
			continue
		}
		target := strings.NewReplacer("{accountId}", url.PathEscape(s.account), "{blobId}", url.PathEscape(m.BlobID),
			"{name}", "message.eml", "{type}", url.QueryEscape("message/rfc822")).Replace(s.download)
		var raw []byte
		if err := s.do(ctx, "GET", target, nil, &raw); err != nil {
			return nil, state, err
		}
		out = append(out, raw)
	}
	// Remember what was seen at the newest moment, so that it is not
	// taken again next time "after" includes that moment.
	if len(next.Seen) > 200 {
		next.Seen = next.Seen[:200]
	}
	return out, next, nil
}
