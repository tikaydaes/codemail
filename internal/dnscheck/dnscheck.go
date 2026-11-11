// Package dnscheck verifies that a user really controls a domain and has
// pointed its mail at this server.
package dnscheck

import (
	"context"
	"net"
	"strings"
)

// Resolver is the part of net.Resolver that is used; tests substitute it.
type Resolver interface {
	LookupMX(ctx context.Context, name string) ([]*net.MX, error)
	LookupTXT(ctx context.Context, name string) ([]string, error)
}

// TXTName and TXTValue describe the ownership record a user must publish.
func TXTName(domain string) string { return "_codemail." + domain }
func TXTValue(token string) string { return "codemail-verify=" + token }

// Check reports whether the domain's MX points at mailHost and whether the
// ownership record carries the token.
func Check(ctx context.Context, r Resolver, domain, mailHost, token string) (mx, txt bool) {
	if recs, err := r.LookupMX(ctx, domain); err == nil {
		for _, rec := range recs {
			if strings.EqualFold(strings.TrimSuffix(rec.Host, "."), mailHost) {
				mx = true
			}
		}
	}
	if recs, err := r.LookupTXT(ctx, TXTName(domain)); err == nil {
		for _, rec := range recs {
			if strings.TrimSpace(rec) == TXTValue(token) {
				txt = true
			}
		}
	}
	return mx, txt
}
