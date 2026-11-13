package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/tikaydaes/codemail/internal/hub"
	"github.com/tikaydaes/codemail/internal/mailauth"
	"github.com/tikaydaes/codemail/internal/mailmsg"
	"github.com/tikaydaes/codemail/internal/store"
)

func messageJSON(m store.Message) map[string]any {
	kind := "other"
	if m.Code != "" || m.Link != "" {
		kind = "code"
	}
	out := map[string]any{
		"id": m.ID, "kind": kind, "rcpt": m.Rcpt, "from": m.FromHdr, "subject": m.Subject,
		"service": m.Service, "code": m.Code, "link": m.Link, "confidence": m.Confidence,
		"auth": m.Auth, "flags": strings.Fields(m.Flags), "sender_domain": mailauth.FromDomain(m.FromHdr),
		"received_at": unix(m.ReceivedAt), "expires_at": unix(m.ExpiresAt), "used_at": unix(m.UsedAt),
	}
	if u, err := url.Parse(m.Link); m.Link != "" && err == nil {
		out["link_host"] = u.Hostname()
		// A link to the sender's own domain is what a real confirmation
		// email contains; anything else deserves a second look.
		out["link_matches_sender"] = mailauth.OrgDomain(u.Hostname()) == mailauth.FromDomain(m.FromHdr)
	}
	return out
}

func (s *Server) handleMessages(w http.ResponseWriter, r *http.Request, u store.User) {
	q := r.URL.Query()
	before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
	after, _ := strconv.ParseInt(q.Get("after"), 10, 64)
	limit, _ := strconv.Atoi(q.Get("limit"))
	msgs, err := s.Store.ListMessages(r.Context(), store.MessageQuery{
		UserID: u.ID, Kind: q.Get("kind"), Search: strings.TrimSpace(q.Get("q")),
		Before: before, After: after, Limit: limit,
	})
	if err != nil {
		s.failErr(w, err)
		return
	}
	out := make([]map[string]any, 0, len(msgs))
	for _, m := range msgs {
		out = append(out, messageJSON(m))
	}
	ok(w, map[string]any{"messages": out})
}

func (s *Server) handleMessage(w http.ResponseWriter, r *http.Request, u store.User) {
	m, err := s.Store.GetMessage(r.Context(), u.ID, pathID(r))
	if err != nil {
		s.failErr(w, err)
		return
	}
	out := messageJSON(m)
	out["mail_from"], out["auth_detail"] = m.MailFrom, m.AuthDetail
	// The body is always Markdown: the same safe, readable rendering on
	// every client. The original is available from the raw endpoint.
	in, _ := mailmsg.Parse(m.Raw)
	out["markdown"] = mailmsg.Markdown(in)
	ok(w, map[string]any{"message": out})
}

func (s *Server) handleMessageRaw(w http.ResponseWriter, r *http.Request, u store.User) {
	m, err := s.Store.GetMessage(r.Context(), u.ID, pathID(r))
	if err != nil {
		s.failErr(w, err)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="message-%d.eml"`, m.ID))
	w.Write(m.Raw)
}

// handleCandidates lists what else in the message could be the code or
// link, for the "Wrong code?" dialog.
func (s *Server) handleCandidates(w http.ResponseWriter, r *http.Request, u store.User) {
	m, err := s.Store.GetMessage(r.Context(), u.ID, pathID(r))
	if err != nil {
		s.failErr(w, err)
		return
	}
	in, _ := mailmsg.Parse(m.Raw)
	out := []map[string]any{}
	n := map[string]int{}
	for _, c := range s.Model.Extract(in).Candidates {
		if n[c.Kind]++; n[c.Kind] <= 8 {
			out = append(out, map[string]any{"kind": c.Kind, "value": c.Value, "score": c.Score, "context": c.Context})
		}
	}
	ok(w, map[string]any{"candidates": out})
}

func (s *Server) handleUpdateMessage(w http.ResponseWriter, r *http.Request, u store.User) {
	var body struct{ Used *bool }
	if !decode(w, r, &body) {
		return
	}
	m, err := s.Store.GetMessage(r.Context(), u.ID, pathID(r))
	if err != nil {
		s.failErr(w, err)
		return
	}
	if body.Used != nil {
		m.UsedAt = time.Time{}
		if *body.Used {
			m.UsedAt = time.Now()
		}
	}
	if err := s.Store.UpdateMessage(r.Context(), m); err != nil {
		s.failErr(w, err)
		return
	}
	s.Hub.Publish(hub.Event{UserID: u.ID, Type: "update", MessageID: m.ID})
	ok(w, map[string]any{"message": messageJSON(m)})
}

func (s *Server) handleDeleteMessage(w http.ResponseWriter, r *http.Request, u store.User) {
	id := pathID(r)
	if err := s.Store.DeleteMessage(r.Context(), u.ID, id); err != nil {
		s.failErr(w, err)
		return
	}
	s.Hub.Publish(hub.Event{UserID: u.ID, Type: "delete", MessageID: id})
	ok(w, map[string]any{})
}

// handleCorrection records what the code or link really is. The value
// must be empty ("there is none") or appear in the message.
func (s *Server) handleCorrection(w http.ResponseWriter, r *http.Request, u store.User) {
	var body struct{ Kind, Value string }
	if !decode(w, r, &body) {
		return
	}
	if body.Kind != "code" && body.Kind != "link" {
		fail(w, http.StatusBadRequest, `kind must be "code" or "link"`)
		return
	}
	ctx := r.Context()
	m, err := s.Store.GetMessage(ctx, u.ID, pathID(r))
	if err != nil {
		s.failErr(w, err)
		return
	}
	if body.Value != "" {
		in, _ := mailmsg.Parse(m.Raw)
		found := false
		for _, c := range s.Model.Extract(in).Candidates {
			found = found || (c.Kind == body.Kind && c.Value == body.Value)
		}
		if !found {
			fail(w, http.StatusBadRequest, "that value does not appear in the message")
			return
		}
	}
	if body.Kind == "code" {
		m.Code = body.Value
	} else {
		m.Link = body.Value
	}
	if err := s.Store.UpdateMessage(ctx, m); err != nil {
		s.failErr(w, err)
		return
	}
	if err := s.Store.AddCorrection(ctx, store.Correction{MessageID: m.ID, UserID: u.ID, Kind: body.Kind, Value: body.Value}); err != nil {
		s.failErr(w, err)
		return
	}
	s.Hub.Publish(hub.Event{UserID: u.ID, Type: "update", MessageID: m.ID})
	ok(w, map[string]any{"message": messageJSON(m)})
}

// handleEvents is a Server-Sent Events stream of the user's messages.
// Event IDs are message IDs, so a client that reconnects with
// Last-Event-ID receives what it missed.
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request, u store.User) {
	flusher, canFlush := w.(http.Flusher)
	if !canFlush {
		fail(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	http.NewResponseController(w).SetWriteDeadline(time.Time{})
	ctx := r.Context()
	events, cancel := s.Hub.Subscribe(u.ID)
	defer cancel()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, ": connected\n\n")

	send := func(event string, id int64, data any) {
		b, _ := json.Marshal(data)
		if event == "message" {
			fmt.Fprintf(w, "id: %d\n", id)
		}
		fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
	}
	last := r.Header.Get("Last-Event-ID")
	if last == "" {
		last = r.URL.Query().Get("last_id")
	}
	if after, err := strconv.ParseInt(last, 10, 64); err == nil && after > 0 {
		missed, _ := s.Store.ListMessages(ctx, store.MessageQuery{UserID: u.ID, After: after, Limit: 100})
		for _, m := range missed {
			send("message", m.ID, messageJSON(m))
		}
	}
	flusher.Flush()

	heartbeat := time.NewTicker(25 * time.Second)
	defer heartbeat.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-heartbeat.C:
			fmt.Fprint(w, ": ping\n\n")
		case e := <-events:
			if e.Type == "delete" {
				send("delete", e.MessageID, map[string]any{"id": e.MessageID})
				break
			}
			m, err := s.Store.GetMessage(ctx, u.ID, e.MessageID)
			if err != nil {
				continue
			}
			m.Raw = nil
			send(e.Type, m.ID, messageJSON(m))
		}
		flusher.Flush()
	}
}

// --- browser extension ---

// forSiteWindow is how long a code stays on offer to the extension.
const forSiteWindow = 15 * time.Minute

// handleForSite lists recent unused codes for the extension. "match" says
// whether a code may be offered inline on the given site: "sender" when
// the verified sender's domain is the site's, "learned" when the user has
// used that sender's codes there before, and "" otherwise.
func (s *Server) handleForSite(w http.ResponseWriter, r *http.Request, u store.User) {
	ctx := r.Context()
	if s.Fetch != nil {
		// A code has probably just been sent: look at the user's linked
		// mailboxes now rather than at the next interval.
		s.Fetch.Kick(u.ID)
	}
	site := mailauth.OrgDomain(r.URL.Query().Get("domain"))
	learned, err := s.Store.SiteSenders(ctx, u.ID, site)
	if err != nil {
		s.failErr(w, err)
		return
	}
	msgs, err := s.Store.ListMessages(ctx, store.MessageQuery{UserID: u.ID, Kind: "code", Limit: 30})
	if err != nil {
		s.failErr(w, err)
		return
	}
	out := []map[string]any{}
	for _, m := range msgs {
		if time.Since(m.ReceivedAt) > forSiteWindow || !m.UsedAt.IsZero() {
			continue
		}
		j := messageJSON(m)
		sender, match := mailauth.FromDomain(m.FromHdr), ""
		if site != "" && sender != "" && m.Auth == mailauth.Pass {
			if sender == site {
				match = "sender"
			}
			for _, l := range learned {
				if l == sender && match == "" {
					match = "learned"
				}
			}
		}
		j["match"] = match
		out = append(out, j)
	}
	ok(w, map[string]any{"site": site, "messages": out})
}

// handleSiteLink remembers that the user used a message's code on a site.
// Only verified senders are remembered, so a forged sender cannot become
// trusted for a site.
func (s *Server) handleSiteLink(w http.ResponseWriter, r *http.Request, u store.User) {
	var body struct {
		MessageID int64 `json:"message_id"`
		Domain    string
	}
	if !decode(w, r, &body) {
		return
	}
	m, err := s.Store.GetMessage(r.Context(), u.ID, body.MessageID)
	if err != nil {
		s.failErr(w, err)
		return
	}
	sender, site := mailauth.FromDomain(m.FromHdr), mailauth.OrgDomain(body.Domain)
	if m.Auth != mailauth.Pass || sender == "" || site == "" {
		fail(w, http.StatusConflict, "only verified senders can be linked to a site")
		return
	}
	if err := s.Store.AddSiteLink(r.Context(), u.ID, sender, site); err != nil {
		s.failErr(w, err)
		return
	}
	ok(w, map[string]any{})
}
