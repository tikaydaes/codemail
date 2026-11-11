package extract

import (
	"math"
	"regexp"
	"strings"
)

// codeOwnFeatures documents the candidate-level code features, in order.
var codeOwnFeatures = []string{
	"keyword_near",     // closeness to words like "code" or "verify", 0..1
	"in_subject",       // candidate is in the subject line
	"own_line",         // candidate is alone on its line
	"six_digits",       // exactly six digits
	"other_digits",     // 4, 5, 7 or 8 digits
	"alphanumeric",     // mixed upper-case letters and digits
	"letters_only",     // upper-case letters only
	"year_like",        // four digits between 1900 and 2099
	"money_or_percent", // currency sign before or percent sign after
	"negative_keyword", // "order", "invoice", "#" and similar just before
	"adjacent_numeric", // part of a date, phone number or decimal
	"in_url_or_email",  // inside a URL or an email address
	"grouped",          // written in groups, like "123 456" or "1 2 3 4"
	"glued",            // touches a letter, as in "911063This"
	"promo_near",       // discount vocabulary close by
	"repeated",         // appears in both the subject and the body
	"own_line_auth",    // alone on its line in a message that talks about codes
	"keyword_auth",     // keyword_near in a message that talks about codes
	"after_label",      // directly follows "code:", "code is" or similar
	"mixed_case",       // mixed-case token, such as "DuVTgb"
	"url_parameter",    // follows "=", "/", "&", "?" or "%", as in "token=225924"
}

// CodeFeatureNames is the full code feature vector, in order.
var CodeFeatureNames = append(append([]string{}, codeOwnFeatures...), MessageFeatureNames...)

var (
	digitRe  = regexp.MustCompile(`\d{4}[ -]\d{4}|\d{3}[ -]\d{3}|\d(?: \d){3,7}|\d{4,8}`)
	alnumRe  = regexp.MustCompile(`\b(?:[A-Z0-9]{5,10}|[A-Z0-9]{3,5}-[A-Z0-9]{3,5})\b`)
	letterRe = regexp.MustCompile(`\b[A-Z]{5,8}\b`)
	// labelRe finds the token after a label such as "code:" or "code is".
	labelRe  = regexp.MustCompile(`(?i)\b(?:code|otp|passcode|password|pin)(?:\s+is)?\s*:?\s+([A-Za-z0-9]{4,12})\b`)
	labelEnd = regexp.MustCompile(`(?i)\b(?:code|otp|passcode|password|pin)(?:\s+is)?\s*:?\s*$`)
	posKwRe  = regexp.MustCompile(`(?i)\b(?:verification|verify|code|otp|passcode|one[- ]time|pin|password|security|log[- ]?in|sign[- ]?in|authentication|2fa|confirm\w*)\b`)
	negKwRe  = regexp.MustCompile(`(?i)(?:\border\b|invoice|tracking|reference|\bref\b|receipt|account (?:number|ending)|phone|\btel\b|\bzip\b|postcode|©|copyright|customer (?:id|number)|\babn\b|\bacn\b|\bcall\b|\bfax\b|#)`)
)

// shoutedWords are upper-case words common on buttons and headings in
// sign-in mail, which are never the code.
var shoutedWords = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(`VERIFY LOGIN LOGON CONFIRM DETAILS PLEASE SECURITY ACCOUNT
		PASSWORD NOTIFICATION NOTICE EMAIL ADDRESS RESET ACCESS SIGNIN ACTIVATE VALIDATE CONTINUE
		IMPORTANT ALERT WELCOME HELLO THANK THANKS BEGIN SIGNED MESSAGE LOCATION DEVICE REQUEST
		CLICK BUTTON BELOW ABOVE ENTER SHARE NEVER ANYONE EXPIRES MINUTES WARNING ACTION REQUIRED`) {
		m[w] = true
	}
	return m
}()

const (
	kwWindow    = 200 // characters beyond which a keyword does not count
	kwScale     = 40  // decay length of keyword closeness
	negKwWindow = 25
	promoWindow = 60
)

type rawCode struct {
	value                  string
	s, e                   int
	alnum, letters, spaced bool
	mixed                  bool
}

// findCodes lists code-shaped tokens in text. Letter-only tokens are common
// words in most mail, so they are listed only when asked for.
func findCodes(text string, withLetters bool) []rawCode {
	var out []rawCode
	for _, loc := range digitRe.FindAllStringIndex(text, -1) {
		s, e := loc[0], loc[1]
		if (s > 0 && isDigit(text[s-1])) || (e < len(text) && isDigit(text[e])) {
			continue // part of a longer number
		}
		raw := text[s:e]
		value := strings.NewReplacer(" ", "", "-", "").Replace(raw)
		out = append(out, rawCode{value: value, s: s, e: e, spaced: value != raw})
	}
	for _, loc := range alnumRe.FindAllStringIndex(text, -1) {
		raw := text[loc[0]:loc[1]]
		if strings.ContainsAny(raw, "0123456789") && strings.ContainsAny(raw, "ABCDEFGHIJKLMNOPQRSTUVWXYZ") {
			out = append(out, rawCode{value: raw, s: loc[0], e: loc[1], alnum: true})
		}
	}
	// Mixed-case codes look like ordinary words, so they are listed only
	// straight after a label and only when they are not a plain word.
	for _, loc := range labelRe.FindAllStringSubmatchIndex(text, -1) {
		raw := text[loc[2]:loc[3]]
		if raw != strings.ToUpper(raw) && raw != strings.ToLower(raw) &&
			(strings.ContainsAny(raw, "0123456789") || raw[1:] != strings.ToLower(raw[1:])) {
			out = append(out, rawCode{value: raw, s: loc[2], e: loc[3], mixed: true})
		}
	}
	if withLetters {
		for _, loc := range letterRe.FindAllStringIndex(text, -1) {
			if shoutedWords[text[loc[0]:loc[1]]] {
				continue
			}
			out = append(out, rawCode{value: text[loc[0]:loc[1]], s: loc[0], e: loc[1], letters: true})
		}
	}
	return out
}

func codeCandidates(in Input, htmlBody string, msg []float64) []Candidate {
	auth := math.Max(msg[0], b2f(msg[1] > 0))
	withLetters := auth > 0

	inSubject := map[string]bool{}
	for _, c := range findCodes(in.Subject, withLetters) {
		inSubject[c.value] = true
	}
	inBody := map[string]bool{}
	var bodies []string
	for _, b := range []string{in.Text, htmlBody} {
		if strings.TrimSpace(b) == "" {
			continue
		}
		bodies = append(bodies, b)
		for _, c := range findCodes(b, withLetters) {
			inBody[c.value] = true
		}
	}
	if len(bodies) == 0 {
		bodies = []string{""}
	}

	var out []Candidate
	// Plain-text parts often run the code into the next word, and HTML
	// parts often hide it in markup, so both renderings are scanned. The
	// subject is prepended so keywords there count towards the first lines.
	for bi, body := range bodies {
		text := in.Subject + "\n" + body
		subjEnd := len(in.Subject)
		pos := posKwRe.FindAllStringIndex(text, -1)
		neg := negKwRe.FindAllStringIndex(text, -1)
		promo := promoPhraseRe.FindAllStringIndex(text, -1)
		for _, c := range findCodes(text, withLetters) {
			if c.s < subjEnd && bi > 0 {
				continue // subject candidates were emitted with the first body
			}
			digits := !c.alnum && !c.letters && !c.mixed
			before := strings.TrimRight(text[:c.s], " ")
			after := strings.TrimLeft(text[c.e:], " ")
			money := strings.HasPrefix(after, "%")
			for _, sym := range []string{"$", "€", "£", "¥"} {
				money = money || strings.HasSuffix(before, sym)
			}
			kw := near(pos, c.s, c.e, kwWindow, kwScale)
			ownLine := b2f(strings.TrimSpace(lineAt(text, c.s, c.e)) == text[c.s:c.e])
			own := []float64{
				kw,
				b2f(c.s < subjEnd),
				ownLine,
				b2f(digits && len(c.value) == 6),
				b2f(digits && len(c.value) != 6),
				b2f(c.alnum),
				b2f(c.letters),
				b2f(digits && len(c.value) == 4 && c.value >= "1900" && c.value <= "2099"),
				b2f(money),
				b2f(keywordBefore(neg, c.s)),
				b2f(adjacentNumeric(text, c.s, c.e)),
				b2f(inURLOrEmail(text, c.s, c.e)),
				b2f(c.spaced),
				b2f(glued(text, c.s, c.e)),
				b2f(near(promo, c.s, c.e, promoWindow, promoWindow) > 0),
				b2f(inSubject[c.value] && inBody[c.value]),
				ownLine * auth,
				kw * auth,
				b2f(labelEnd.MatchString(text[max(0, c.s-40):c.s])),
				b2f(c.mixed),
				b2f(c.s > 0 && strings.IndexByte("=/&?%", text[c.s-1]) >= 0),
			}
			out = append(out, Candidate{
				Kind:     "code",
				Value:    c.value,
				Features: append(own, msg...),
				Context:  snippet(text, c.s, c.e),
			})
		}
	}
	return out
}

// near is 1 when a keyword touches [s,e) and decays towards 0 with
// distance, reaching exactly 0 beyond window.
func near(kws [][]int, s, e, window int, scale float64) float64 {
	best := window + 1
	for _, k := range kws {
		gap := 0
		switch {
		case k[1] <= s:
			gap = s - k[1]
		case k[0] >= e:
			gap = k[0] - e
		}
		if gap < best {
			best = gap
		}
	}
	if best > window {
		return 0
	}
	return math.Exp(-float64(best) / scale)
}

func keywordBefore(kws [][]int, s int) bool {
	for _, k := range kws {
		if k[1] <= s && s-k[1] <= negKwWindow {
			return true
		}
	}
	return false
}

func isDigit(b byte) bool { return b >= '0' && b <= '9' }

func isLetter(b byte) bool { return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') }

func glued(text string, s, e int) bool {
	return (s > 0 && isLetter(text[s-1])) || (e < len(text) && isLetter(text[e]))
}

// adjacentNumeric reports whether more digits sit right next to the
// candidate, separated only by a space or numeric punctuation.
func adjacentNumeric(text string, s, e int) bool {
	const sep = " .,/:-+"
	if s >= 2 && strings.IndexByte(sep, text[s-1]) >= 0 && isDigit(text[s-2]) {
		return true
	}
	return e+1 < len(text) && strings.IndexByte(sep, text[e]) >= 0 && isDigit(text[e+1])
}

func inURLOrEmail(text string, s, e int) bool {
	a := strings.LastIndexAny(text[:s], " \t\r\n") + 1
	b := strings.IndexAny(text[e:], " \t\r\n")
	if b < 0 {
		b = len(text)
	} else {
		b += e
	}
	tok := text[a:b]
	return strings.Contains(tok, "://") || strings.Contains(tok, "www.") || strings.Contains(tok, "@")
}

func lineAt(text string, s, e int) string {
	a := strings.LastIndexByte(text[:s], '\n') + 1
	b := strings.IndexByte(text[e:], '\n')
	if b < 0 {
		return text[a:]
	}
	return text[a : e+b]
}

func snippet(text string, s, e int) string {
	a, b := max(0, s-40), min(len(text), e+40)
	return strings.Join(strings.Fields(strings.ToValidUTF8(text[a:b], "")), " ")
}
