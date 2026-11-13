// Package ingest is the last step for every message, however it arrived:
// find the code or link, decide whether to keep the message, store it and
// tell the user's connected clients.
package ingest

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/tikaydaes/codemail/internal/extract"
	"github.com/tikaydaes/codemail/internal/hub"
	"github.com/tikaydaes/codemail/internal/mailauth"
	"github.com/tikaydaes/codemail/internal/store"
)

// Flags on messages that came through another mailbox of the user's.
const (
	FlagForwarded = "forwarded" // forwarded from it to an address here
	FlagFetched   = "fetched"   // fetched from it by this server
)

type Pipeline struct {
	Store     store.Store
	Hub       *hub.Hub
	Model     extract.Model
	Log       *slog.Logger
	Retention time.Duration // default; a user's own setting overrides it
}

// Found is what extraction made of one message. It is computed once and
// shared by every recipient's copy.
type Found struct {
	extract.Result
	Confidence float64
}

func (p *Pipeline) Extract(in extract.Input) Found {
	f := Found{Result: p.Model.Extract(in)}
	for _, c := range f.Candidates {
		if (c.Value == f.Code || c.Value == f.Link) && c.Score > f.Confidence {
			f.Confidence = c.Score
		}
	}
	return f
}

// HasCode reports whether the message carries a sign-in code or link.
func (f Found) HasCode() bool { return f.Code != "" || f.Link != "" }

// Delivery is one user's copy of a message.
type Delivery struct {
	UserID   int64
	Rcpt     string       // the address it was sent to, or the mailbox it was fetched from
	Alias    *store.Alias // set when Rcpt is a claimed or generated alias here
	MailFrom string
	In       extract.Input
	Raw      []byte
	Auth     mailauth.Result
	Via      string // FlagForwarded, FlagFetched or ""
}

// Deliver stores the message and notifies the user. It returns the new
// message's ID.
func (p *Pipeline) Deliver(ctx context.Context, d Delivery, f Found) (int64, error) {
	retention := p.Retention
	if u, err := p.Store.UserByID(ctx, d.UserID); err == nil && u.Retention > 0 {
		retention = u.Retention
	}
	service := f.Service
	var flags []string
	if d.Via != "" {
		flags = append(flags, d.Via)
	}
	if a := d.Alias; a != nil {
		if a.Label != "" {
			service = a.Label
		}
		// A generated alias belongs to one service. Remember its first
		// sender and flag anyone else: the address has leaked. Mail the
		// user forwards to it themselves is from many senders by design.
		if a.Generated && d.Auth.Domain != "" && d.Via == "" {
			switch a.ExpectedSender {
			case "":
				p.Store.SetAliasSender(ctx, a.ID, d.Auth.Domain)
			case d.Auth.Domain:
			default:
				flags = append(flags, "unexpected_sender")
			}
		}
	}
	now := time.Now()
	m := &store.Message{
		UserID: d.UserID, Rcpt: d.Rcpt, MailFrom: d.MailFrom, FromHdr: d.In.From, Subject: d.In.Subject,
		Service: service, Code: f.Code, Link: f.Link, Confidence: f.Confidence,
		Auth: d.Auth.Verdict, AuthDetail: d.Auth.Detail, Flags: strings.Join(flags, " "),
		Raw: d.Raw, ReceivedAt: now, ExpiresAt: now.Add(retention),
	}
	if err := p.Store.SaveMessage(ctx, m); err != nil {
		return 0, err
	}
	p.Hub.Publish(hub.Event{UserID: d.UserID, Type: "message", MessageID: m.ID})
	p.Log.Info("message stored", "id", m.ID, "rcpt", d.Rcpt, "service", service,
		"has_code", f.Code != "", "has_link", f.Link != "", "auth", d.Auth.Verdict, "via", d.Via)
	return m.ID, nil
}
