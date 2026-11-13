// Package mailauth checks whether a message really comes from the domain
// in its From header, using SPF, DKIM and the sender's DMARC record.
package mailauth

import (
	"bytes"
	"fmt"
	"net"
	"net/mail"
	"strings"

	"blitiri.com.ar/go/spf"
	"github.com/emersion/go-msgauth/dkim"
	"github.com/emersion/go-msgauth/dmarc"
	"golang.org/x/net/publicsuffix"
)

// Verdicts.
const (
	Pass = "pass" // SPF or DKIM passed for the From domain
	Fail = "fail" // the From domain publishes DMARC and nothing passed
	None = "none" // not enough information either way
)

type Result struct {
	Verdict string
	Detail  string // e.g. "spf=pass dkim=pass dmarc=pass"
	Domain  string // organisational domain of the From header
}

// Checker evaluates one received message.
type Checker interface {
	Check(remote net.IP, helo, mailFrom, fromHdr string, raw []byte) Result
	// CheckRelayed is for mail that came through the recipient's own
	// mailbox: forwarded from it (remote is the forwarding server) or
	// fetched from it (remote is nil). See relayed.go.
	CheckRelayed(remote net.IP, fromHdr string, raw []byte) Result
}

// OrgDomain returns the registrable domain of a host, e.g.
// "mail.example.co.uk" -> "example.co.uk".
func OrgDomain(host string) string {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if d, err := publicsuffix.EffectiveTLDPlusOne(host); err == nil {
		return d
	}
	return host
}

// FromDomain returns the organisational domain of a From header.
func FromDomain(fromHdr string) string {
	a, err := mail.ParseAddress(fromHdr)
	if err != nil {
		return ""
	}
	at := strings.LastIndexByte(a.Address, '@')
	if at < 0 {
		return ""
	}
	return OrgDomain(a.Address[at+1:])
}

// TrustAll treats every message as genuinely from its From domain. It
// exists only for local development and tests, where there is no real
// sender to verify.
type TrustAll struct{}

func (TrustAll) Check(_ net.IP, _, _, fromHdr string, _ []byte) Result {
	return Result{Verdict: Pass, Detail: "not checked (development)", Domain: FromDomain(fromHdr)}
}

// DNS is the Checker that performs real DNS lookups.
type DNS struct{}

func (DNS) Check(remote net.IP, helo, mailFrom, fromHdr string, raw []byte) Result {
	res := Result{Verdict: None, Domain: FromDomain(fromHdr)}
	if res.Domain == "" {
		res.Detail = "no From domain"
		return res
	}
	aligned := false

	spfRes := "none"
	if remote != nil {
		r, _ := spf.CheckHostWithSender(remote, helo, mailFrom)
		spfRes = string(r)
		spfDomain := helo
		if at := strings.LastIndexByte(mailFrom, '@'); at >= 0 {
			spfDomain = mailFrom[at+1:]
		}
		if r == spf.Pass && OrgDomain(spfDomain) == res.Domain {
			aligned = true
		}
	}

	dkimRes := "none"
	if vs, err := dkim.Verify(bytes.NewReader(raw)); err == nil && len(vs) > 0 {
		dkimRes = "fail"
		for _, v := range vs {
			if v.Err == nil {
				dkimRes = "pass"
				if OrgDomain(v.Domain) == res.Domain {
					aligned = true
				}
			}
		}
	}

	dmarcRes := "none"
	switch {
	case aligned:
		res.Verdict, dmarcRes = Pass, "pass"
	default:
		if _, err := dmarc.Lookup(res.Domain); err == nil {
			res.Verdict, dmarcRes = Fail, "fail"
		}
	}
	res.Detail = fmt.Sprintf("spf=%s dkim=%s dmarc=%s", spfRes, dkimRes, dmarcRes)
	return res
}
