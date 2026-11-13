package mailauth

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"regexp"
	"strings"
	"time"

	"github.com/emersion/go-msgauth/authres"
	"github.com/emersion/go-msgauth/dkim"

	"github.com/tikaydaes/codemail/internal/mailmsg"
)

// Mail that reaches us through the recipient's own mailbox (forwarded by
// a rule there, or fetched from it) no longer comes from the sender's
// server, so SPF says nothing. Two things can still vouch for it:
//
//  1. the sender's DKIM signature, which we verify ourselves, and
//  2. the verdict the mailbox provider recorded when it received the
//     message, in an Authentication-Results header.
//
// A provider's header is only believed if it sits where that provider
// would have put it: below one of the provider's own Received lines, and
// naming the provider. Anything the original sender wrote, including a
// forged verdict, is further down. For forwarded mail the provider must
// also be the server that handed us the message.

// CheckRelayed evaluates a forwarded or fetched message. remote is the
// server that delivered it to us, or nil when we fetched it ourselves.
func (DNS) CheckRelayed(remote net.IP, fromHdr string, raw []byte) Result {
	res := Result{Verdict: None, Domain: FromDomain(fromHdr)}
	if res.Domain == "" {
		res.Detail = "no From domain"
		return res
	}
	dkimRes := "none"
	if vs, err := dkim.Verify(bytes.NewReader(raw)); err == nil && len(vs) > 0 {
		dkimRes = "fail"
		for _, v := range vs {
			if v.Err == nil && OrgDomain(v.Domain) == res.Domain {
				res.Verdict, res.Detail = Pass, "dkim=pass (signature checked here)"
				return res
			}
			if v.Err == nil {
				dkimRes = "pass, but for another domain"
			}
		}
	}
	relay, forwarded := "", remote != nil
	if forwarded {
		relay = RelayDomain(remote)
	}
	verdict, detail := providerVerdict(mailmsg.Headers(raw), res.Domain, relay, forwarded)
	res.Verdict = verdict
	res.Detail = fmt.Sprintf("dkim=%s; %s", dkimRes, detail)
	return res
}

func (TrustAll) CheckRelayed(_ net.IP, fromHdr string, _ []byte) Result {
	return Result{Verdict: Pass, Detail: "not checked (development)", Domain: FromDomain(fromHdr)}
}

// RelayDomain names the organisation a connecting server belongs to, but
// only if its reverse DNS name resolves back to the same address.
func RelayDomain(ip net.IP) string {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	names, err := net.DefaultResolver.LookupAddr(ctx, ip.String())
	if err != nil {
		return ""
	}
	for _, name := range names {
		addrs, err := net.DefaultResolver.LookupIPAddr(ctx, name)
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if a.IP.Equal(ip) {
				return OrgDomain(name)
			}
		}
	}
	return ""
}

var receivedBy = regexp.MustCompile(`(?i)\bby\s+([a-z0-9.-]+\.[a-z]{2,})\b`)

// providerVerdict reads the mailbox provider's own judgement of a
// message whose From domain is from. relay is the verified domain of the
// server that delivered a forwarded message to us.
func providerVerdict(headers []mailmsg.Field, from, relay string, forwarded bool) (verdict, detail string) {
	unverified := "the mailbox provider recorded no verdict that can be trusted"
	if forwarded && relay == "" {
		return None, "the forwarding server could not be identified"
	}
	above := map[string]bool{} // organisations whose servers handled the message after this point
	for _, f := range headers {
		switch {
		case f.Is("Received"):
			if m := receivedBy.FindStringSubmatch(f.Value); m != nil {
				above[OrgDomain(m[1])] = true
			}
		case f.Is("Authentication-Results"), f.Is("ARC-Authentication-Results"):
			value := f.Value
			if f.Is("ARC-Authentication-Results") {
				// "i=1; mx.example.com; dkim=pass ..."
				if _, rest, found := strings.Cut(value, ";"); found {
					value = strings.TrimSpace(rest)
				}
			}
			id, results := parseAuthRes(value)
			who := OrgDomain(id)
			trusted := false
			switch {
			case forwarded:
				// Written by the organisation that handed us the message.
				trusted = above[relay] && (id == "" || who == relay || above[who]) || id != "" && who == relay
			case id != "":
				// Written by a server that handled the message at this point.
				trusted = above[who]
			}
			if !trusted {
				continue
			}
			name := id
			if name == "" {
				name = relay
			}
			switch judge(results, from) {
			case Pass:
				return Pass, fmt.Sprintf("%s says the sender passed", name)
			case Fail:
				return Fail, fmt.Sprintf("%s says the sender failed", name)
			}
			return None, fmt.Sprintf("%s could not verify the sender", name)
		}
	}
	return None, unverified
}

// parseAuthRes parses an Authentication-Results value. Some providers
// leave out the name that should come first; id is then "".
func parseAuthRes(value string) (id string, results []authres.Result) {
	first, _, _ := strings.Cut(value, ";")
	if strings.Contains(first, "=") {
		value = "unnamed.invalid; " + value
	}
	id, results, err := authres.Parse(value)
	if err != nil {
		return "", nil
	}
	if id == "unnamed.invalid" {
		id = ""
	}
	return strings.ToLower(id), results
}

// judge turns a provider's results into a verdict for the From domain.
func judge(results []authres.Result, from string) string {
	verdict := None
	for _, r := range results {
		switch r := r.(type) {
		case *authres.DMARCResult:
			if r.From != "" && OrgDomain(r.From) != from {
				continue
			}
			switch r.Value {
			case authres.ResultPass:
				return Pass
			case authres.ResultFail:
				verdict = Fail
			}
		case *authres.DKIMResult:
			domain := r.Domain
			if domain == "" {
				domain = strings.TrimPrefix(r.Identifier[strings.LastIndexByte(r.Identifier, '@')+1:], "@")
			}
			if r.Value == authres.ResultPass && domain != "" && OrgDomain(domain) == from {
				return Pass
			}
		case *authres.SPFResult:
			domain := r.From
			if at := strings.LastIndexByte(domain, '@'); at >= 0 {
				domain = domain[at+1:]
			}
			if r.Value == authres.ResultPass && domain != "" && OrgDomain(domain) == from {
				return Pass
			}
		}
	}
	return verdict
}
