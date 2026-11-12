package api

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/tikaydaes/codemail/internal/dnscheck"
	"github.com/tikaydaes/codemail/internal/store"
)

func aliasJSON(a store.Alias) map[string]any {
	return map[string]any{
		"id": a.ID, "address": a.Address(), "label": a.Label, "generated": a.Generated,
		"expected_sender": a.ExpectedSender, "created_at": unix(a.CreatedAt),
	}
}

// domainJSON includes the DNS records the owner must publish. mx and txt
// are nil when the records have not just been looked up.
func (s *Server) domainJSON(d store.Domain, mx, txt *bool) map[string]any {
	out := map[string]any{
		"id": d.ID, "name": d.Name, "shared": d.OwnerID == 0, "verified": d.Verified,
		"allow_mailbox": d.AllowMailbox, "allow_aliases": d.AllowAliases, "allow_subdomains": d.AllowSubdomains,
		"max_aliases": d.MaxAliases, "max_generated": d.MaxGenerated,
		"last_checked": unix(d.LastChecked), "failing_since": unix(d.FailingSince),
	}
	records := []map[string]any{{"type": "MX", "name": d.Name, "value": "10 " + s.cfg.MailHost, "found": mx}}
	if d.OwnerID != 0 {
		records = append(records, map[string]any{
			"type": "TXT", "name": dnscheck.TXTName(d.Name), "value": dnscheck.TXTValue(d.VerifyToken), "found": txt,
		})
	} else if d.AllowSubdomains {
		records = append(records, map[string]any{"type": "MX", "name": "*." + d.Name, "value": "10 " + s.cfg.MailHost, "found": nil})
	}
	out["records"] = records
	return out
}

// handleAddresses returns everything the Addresses screen shows: the
// shared domains and their rules, the user's aliases and their own domains.
func (s *Server) handleAddresses(w http.ResponseWriter, r *http.Request, u store.User) {
	ctx := r.Context()
	shared, err := s.Store.ListDomains(ctx, 0)
	if err != nil {
		s.failErr(w, err)
		return
	}
	own, err := s.Store.ListDomains(ctx, u.ID)
	if err != nil {
		s.failErr(w, err)
		return
	}
	aliases, err := s.Store.ListAliases(ctx, u.ID)
	if err != nil {
		s.failErr(w, err)
		return
	}
	out := map[string]any{"username": u.Username, "shared_domains": []any{}, "domains": []any{}, "aliases": []any{}}
	for _, d := range shared {
		if d.Verified {
			out["shared_domains"] = append(out["shared_domains"].([]any), s.domainJSON(d, nil, nil))
		}
	}
	for _, d := range own {
		out["domains"] = append(out["domains"].([]any), s.domainJSON(d, nil, nil))
	}
	for _, a := range aliases {
		out["aliases"] = append(out["aliases"].([]any), aliasJSON(a))
	}
	ok(w, out)
}

func (s *Server) handleClaimAlias(w http.ResponseWriter, r *http.Request, u store.User) {
	var body struct {
		Local    string
		DomainID int64 `json:"domain_id"`
		Label    string
	}
	if !decode(w, r, &body) {
		return
	}
	a := store.Alias{UserID: u.ID, DomainID: body.DomainID, Local: body.Local, Label: strings.TrimSpace(body.Label)}
	if err := s.Store.ClaimAlias(r.Context(), &a); err != nil {
		s.failErr(w, err)
		return
	}
	reply(w, http.StatusCreated, map[string]any{"alias": aliasJSON(a)})
}

var (
	aliasAdjectives = strings.Fields(`amber quiet brisk calm clever copper coral crisp dusky early fallow gentle
		golden hazel hidden ivory jolly keen lively lunar mellow misty modest nimble noble olive pale plain
		proud quick rapid rustic sandy silent silver simple sleepy smooth snowy solar spare steady still
		stony sunny swift tidy timid upper urban velvet vivid warm wild windy wintry young`)
	aliasNouns = strings.Fields(`acorn anchor badger basin beacon birch brook canyon cedar cliff clover comet
		coral crane creek delta dune ember falcon fern field finch fjord forest garden glade harbor heron
		hollow island juniper lagoon lantern maple marsh meadow mesa oak orchard otter pebble pine plover
		prairie quartz reef ridge river sparrow spruce summit thicket tundra valley willow wren`)
)

func pick(words []string) string {
	n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(words))))
	return words[n.Int64()]
}

// handleGenerateAlias makes an unguessable address for one named service.
func (s *Server) handleGenerateAlias(w http.ResponseWriter, r *http.Request, u store.User) {
	var body struct {
		Label    string
		DomainID int64 `json:"domain_id"`
	}
	if !decode(w, r, &body) {
		return
	}
	ctx := r.Context()
	label := strings.TrimSpace(body.Label)
	if label == "" {
		fail(w, http.StatusBadRequest, "give the service a name")
		return
	}
	if body.DomainID == 0 {
		shared, err := s.Store.ListDomains(ctx, 0)
		if err != nil {
			s.failErr(w, err)
			return
		}
		for _, d := range shared {
			if d.Verified && d.AllowAliases && body.DomainID == 0 {
				body.DomainID = d.ID
			}
		}
		if body.DomainID == 0 {
			fail(w, http.StatusConflict, "no domain on this server allows generated addresses")
			return
		}
	}
	for try := 0; ; try++ {
		n, _ := rand.Int(rand.Reader, big.NewInt(9000))
		a := store.Alias{
			UserID: u.ID, DomainID: body.DomainID, Label: label, Generated: true,
			Local: fmt.Sprintf("%s-%s-%d", pick(aliasAdjectives), pick(aliasNouns), 1000+n.Int64()),
		}
		err := s.Store.ClaimAlias(ctx, &a)
		if errors.Is(err, store.ErrTaken) && try < 5 {
			continue
		}
		if err != nil {
			s.failErr(w, err)
			return
		}
		reply(w, http.StatusCreated, map[string]any{"alias": aliasJSON(a)})
		return
	}
}

// handleUpdateAlias changes an address's label, the name shown on mail
// that arrives there.
func (s *Server) handleUpdateAlias(w http.ResponseWriter, r *http.Request, u store.User) {
	var body struct{ Label string }
	if !decode(w, r, &body) {
		return
	}
	label := strings.TrimSpace(body.Label)
	if len(label) > 80 {
		fail(w, http.StatusBadRequest, "labels can be up to 80 characters")
		return
	}
	if err := s.Store.SetAliasLabel(r.Context(), u.ID, pathID(r), label); err != nil {
		s.failErr(w, err)
		return
	}
	ok(w, map[string]any{})
}

func (s *Server) handleDeleteAlias(w http.ResponseWriter, r *http.Request, u store.User) {
	if err := s.Store.DeleteAlias(r.Context(), u.ID, pathID(r)); err != nil {
		s.failErr(w, err)
		return
	}
	ok(w, map[string]any{})
}

// --- the user's own domains ---

func (s *Server) handleAddDomain(w http.ResponseWriter, r *http.Request, u store.User) {
	var body struct{ Name string }
	if !decode(w, r, &body) {
		return
	}
	token, _ := newToken()
	d := store.Domain{Name: body.Name, OwnerID: u.ID, VerifyToken: token[:20]}
	if err := s.Store.AddDomain(r.Context(), &d); err != nil {
		if errors.Is(err, store.ErrTaken) {
			fail(w, http.StatusConflict, "that domain is already registered on this server")
			return
		}
		s.failErr(w, err)
		return
	}
	s.Store.Audit(r.Context(), u.ID, "add_domain", d.Name)
	reply(w, http.StatusCreated, map[string]any{"domain": s.domainJSON(d, nil, nil)})
}

func (s *Server) ownDomain(w http.ResponseWriter, r *http.Request, u store.User) (store.Domain, bool) {
	d, err := s.Store.DomainByID(r.Context(), pathID(r))
	if err == nil && d.OwnerID != u.ID {
		err = store.ErrNotFound
	}
	if err != nil {
		s.failErr(w, err)
		return d, false
	}
	return d, true
}

// checkDomain looks the domain up and records the outcome. A verified
// domain is given a week of failures before it stops receiving mail, so a
// DNS outage does not drop it.
func (s *Server) checkDomain(ctx context.Context, d *store.Domain) (mx, txt bool, err error) {
	mx, txt = dnscheck.Check(ctx, s.DNS, d.Name, s.cfg.MailHost, d.VerifyToken)
	now := time.Now()
	d.LastChecked = now
	good := mx && (txt || d.OwnerID == 0)
	switch {
	case good:
		d.Verified, d.FailingSince = true, time.Time{}
	case d.OwnerID == 0:
		// Shared domains are the admin's responsibility; only report.
	case d.Verified && d.FailingSince.IsZero():
		d.FailingSince = now
	case d.Verified && now.Sub(d.FailingSince) > 7*24*time.Hour:
		d.Verified = false
	}
	return mx, txt, s.Store.UpdateDomain(ctx, *d)
}

func (s *Server) handleCheckDomain(w http.ResponseWriter, r *http.Request, u store.User) {
	d, found := s.ownDomain(w, r, u)
	if !found {
		return
	}
	mx, txt, err := s.checkDomain(r.Context(), &d)
	if err != nil {
		s.failErr(w, err)
		return
	}
	ok(w, map[string]any{"domain": s.domainJSON(d, &mx, &txt)})
}

func (s *Server) handleDeleteDomain(w http.ResponseWriter, r *http.Request, u store.User) {
	d, found := s.ownDomain(w, r, u)
	if !found {
		return
	}
	if err := s.Store.DeleteDomain(r.Context(), d.ID); err != nil {
		s.failErr(w, err)
		return
	}
	s.Store.Audit(r.Context(), u.ID, "delete_domain", d.Name)
	ok(w, map[string]any{})
}

// RecheckDomains re-verifies every user-owned domain; run it daily.
func (s *Server) RecheckDomains(ctx context.Context) {
	domains, err := s.Store.AllDomains(ctx)
	if err != nil {
		s.Log.Error("recheck domains", "err", err)
		return
	}
	for _, d := range domains {
		if d.OwnerID == 0 {
			continue
		}
		was := d.Verified
		if _, _, err := s.checkDomain(ctx, &d); err != nil {
			s.Log.Error("recheck domain", "domain", d.Name, "err", err)
		} else if was != d.Verified {
			s.Log.Info("domain verification changed", "domain", d.Name, "verified", d.Verified)
		}
	}
}
