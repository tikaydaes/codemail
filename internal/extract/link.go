package extract

import (
	"net/url"
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

// linkOwnFeatures documents the candidate-level link features, in order.
var linkOwnFeatures = []string{
	"anchor_keyword",   // link text says verify, confirm, sign in, reset, ...
	"anchor_generic",   // link text is a generic call to action
	"url_keyword",      // URL mentions verify, token, magic, ...
	"negative_keyword", // unsubscribe, privacy, social and similar
	"context_keyword",  // bare URL preceded by a sign-in phrase
	"long_token",       // path or query carries a long opaque token
	"subject_keyword",  // subject suggests a confirmation email
	"few_links",        // 1 / number of links in the message
	"anchor_auth",      // anchor_keyword in a message with link phrases
	"position",         // 0 for the first link in the message, 1 for the last
	"plain_url",        // no query, fragment or long token: not a one-time link
}

// LinkFeatureNames is the full link feature vector, in order.
var LinkFeatureNames = append(append([]string{}, linkOwnFeatures...), MessageFeatureNames...)

var (
	urlRe      = regexp.MustCompile(`https?://[^\s<>"'\])]+`)
	linkPosRe  = regexp.MustCompile(`(?i)(?:verify|confirm|sign[ -]?in|log[ -]?in|activate|reset|magic|validate|authenticate)`)
	linkSubjRe = regexp.MustCompile(`(?i)(?:verify|confirm|activate|reset|validate|magic|\blink\b|complete your)`)
	linkCTARe  = regexp.MustCompile(`(?i)(?:continue|approve|get started|complete|click here|accept)`)
	linkURLRe  = regexp.MustCompile(`(?i)(?:verif|confirm|magic|token|auth|login|signin|sign-in|activat|reset|otp|code=|callback|validate)`)
	linkNegRe  = regexp.MustCompile(`(?i)(?:unsubscribe|opt[- ]?out|preferences|privacy|terms|\bhelp\b|support|facebook\.|twitter\.|//x\.com|instagram\.|linkedin\.|youtube\.|in (?:your |a )?browser|apps\.apple|play\.google|\.(?:png|jpe?g|gif)(?:\?|$))`)
	longTokRe  = regexp.MustCompile(`[A-Za-z0-9_\-]{20,}`)
	linkCtxLen = 100
)

func linkCandidates(in Input, as [][2]string, msg []float64) []Candidate {
	type raw struct{ href, anchor, ctx string }
	var raws []raw
	seen := map[string]bool{}
	add := func(href, anchor, ctx string) {
		u, err := url.Parse(href)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || seen[href] {
			return
		}
		seen[href] = true
		raws = append(raws, raw{href, anchor, ctx})
	}
	for _, a := range as {
		add(a[0], a[1], "")
	}
	for _, loc := range urlRe.FindAllStringIndex(in.Text, -1) {
		href := strings.TrimRight(in.Text[loc[0]:loc[1]], ".,;:!?")
		ctx := strings.ToValidUTF8(in.Text[max(0, loc[0]-linkCtxLen):loc[0]], "")
		add(href, "", ctx)
	}

	subjKw := b2f(linkSubjRe.MatchString(in.Subject))
	out := make([]Candidate, 0, len(raws))
	for i, r := range raws {
		u, _ := url.Parse(r.href)
		anchorKw := b2f(linkPosRe.MatchString(r.anchor))
		own := []float64{
			anchorKw,
			b2f(linkCTARe.MatchString(r.anchor)),
			b2f(linkURLRe.MatchString(r.href)),
			b2f(linkNegRe.MatchString(r.anchor) || linkNegRe.MatchString(r.href)),
			b2f(linkPosRe.MatchString(r.ctx)),
			b2f(longTokRe.MatchString(u.RequestURI())),
			subjKw,
			1 / float64(len(raws)),
			anchorKw * msg[2],
			float64(i) / float64(max(1, len(raws)-1)),
			b2f(u.RawQuery == "" && u.Fragment == "" && !longTokRe.MatchString(u.Path)),
		}
		out = append(out, Candidate{
			Kind:     "link",
			Value:    r.href,
			Features: append(own, msg...),
			Context:  strings.Join(strings.Fields(r.anchor+" "+r.ctx), " "),
		})
	}
	return out
}

// anchors returns the (href, text) of every <a> in an HTML document.
func anchors(doc string) [][2]string {
	if doc == "" {
		return nil
	}
	root, err := html.Parse(strings.NewReader(doc))
	if err != nil {
		return nil
	}
	var out [][2]string
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode && n.Data == "a" {
			for _, at := range n.Attr {
				if at.Key == "href" {
					out = append(out, [2]string{strings.TrimSpace(at.Val), nodeText(n)})
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	return out
}

// nodeText is the visible text under a node, with image alt text included.
func nodeText(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		switch {
		case n.Type == html.TextNode:
			b.WriteString(n.Data)
			b.WriteByte(' ')
		case n.Type == html.ElementNode && n.Data == "img":
			for _, at := range n.Attr {
				if at.Key == "alt" {
					b.WriteString(at.Val)
					b.WriteByte(' ')
				}
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.Join(strings.Fields(b.String()), " ")
}

var blockTags = map[string]bool{
	"br": true, "p": true, "div": true, "tr": true, "li": true, "table": true,
	"td": true, "h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true,
}

// htmlText renders an HTML body as plain text, one block per line.
func htmlText(doc string) string {
	root, err := html.Parse(strings.NewReader(doc))
	if err != nil {
		return ""
	}
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			switch n.Data {
			case "script", "style", "head", "title":
				return
			}
			if blockTags[n.Data] {
				b.WriteByte('\n')
			}
		}
		if n.Type == html.TextNode {
			b.WriteString(n.Data)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if n.Type == html.ElementNode && blockTags[n.Data] {
			b.WriteByte('\n')
		}
	}
	walk(root)
	var lines []string
	for _, l := range strings.Split(b.String(), "\n") {
		if l = strings.Join(strings.Fields(l), " "); l != "" {
			lines = append(lines, l)
		}
	}
	return strings.Join(lines, "\n")
}
