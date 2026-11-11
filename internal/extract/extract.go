// Package extract finds sign-in codes and links in an email.
//
// It works in two steps. Deterministic code finds every plausible candidate
// (digit runs, short upper-case tokens, URLs) and describes each one with a
// feature vector: facts about the candidate followed by facts about the
// whole message. A logistic model then scores the candidates. The weights
// live in weights.json and come from training on labelled mail (see
// Train); no other code changes when they are retrained.
package extract

import (
	_ "embed"
	"encoding/json"
	"math"
	"net/mail"
	"regexp"
	"sort"
	"strings"
)

type Input struct {
	From    string // From header
	Subject string
	Text    string // plain-text body
	HTML    string
	Bulk    bool // carries a List-Unsubscribe header
}

type Candidate struct {
	Kind     string    `json:"kind"` // "code" or "link"
	Value    string    `json:"value"`
	Score    float64   `json:"score"`
	Features []float64 `json:"features,omitempty"`
	Context  string    `json:"context,omitempty"`
}

type Result struct {
	Service    string      `json:"service"`
	Code       string      `json:"code,omitempty"`
	Link       string      `json:"link,omitempty"`
	Candidates []Candidate `json:"candidates,omitempty"` // best first within each kind
}

// Scorer is one logistic model: score = sigmoid(Bias + W.x), reported when
// it reaches Threshold.
type Scorer struct {
	Bias      float64   `json:"bias"`
	W         []float64 `json:"w"`
	Threshold float64   `json:"threshold"`
}

func (s Scorer) Score(x []float64) float64 {
	z := s.Bias
	for i, v := range x {
		z += s.W[i] * v
	}
	return 1 / (1 + math.Exp(-z))
}

type Model struct {
	Code Scorer `json:"code"`
	Link Scorer `json:"link"`
}

//go:embed weights.json
var weightsJSON []byte

// Default is the model built into the binary.
var Default = mustLoad(weightsJSON)

func mustLoad(b []byte) Model {
	var m Model
	if err := json.Unmarshal(b, &m); err != nil {
		panic("extract: weights.json: " + err.Error())
	}
	if len(m.Code.W) != len(CodeFeatureNames) || len(m.Link.W) != len(LinkFeatureNames) {
		panic("extract: weights.json does not match the feature set; retrain")
	}
	return m
}

// Candidates returns every unscored candidate in a message.
func Candidates(in Input) (codes, links []Candidate) {
	htmlBody := ""
	if in.HTML != "" {
		htmlBody = htmlText(in.HTML)
	}
	as := anchors(in.HTML)
	msg := messageFeatures(in, htmlBody, len(as))
	return codeCandidates(in, htmlBody, msg), linkCandidates(in, as, msg)
}

func (m Model) Extract(in Input) Result {
	codes, links := Candidates(in)
	for i := range codes {
		codes[i].Score = m.Code.Score(codes[i].Features)
	}
	for i := range links {
		links[i].Score = m.Link.Score(links[i].Features)
	}
	codes, links = rank(codes), rank(links)

	res := Result{Service: service(in.From)}
	if len(codes) > 0 && codes[0].Score >= m.Code.Threshold {
		res.Code = codes[0].Value
	}
	if len(links) > 0 && links[0].Score >= m.Link.Threshold {
		res.Link = links[0].Value
	}
	res.Candidates = append(codes, links...)
	return res
}

// rank sorts best first and keeps the best-scoring instance of each value.
func rank(cs []Candidate) []Candidate {
	sort.SliceStable(cs, func(i, j int) bool { return cs[i].Score > cs[j].Score })
	seen := make(map[string]bool, len(cs))
	out := cs[:0]
	for _, c := range cs {
		if !seen[c.Value] {
			seen[c.Value] = true
			out = append(out, c)
		}
	}
	return out
}

// MessageFeatureNames documents the message-level features appended to
// every candidate's vector.
var MessageFeatureNames = []string{
	"msg_subject_auth", // subject mentions a code, sign-in or verification
	"msg_code_phrases", // body phrases such as "expires in", "do not share"
	"msg_link_phrases", // body phrases such as "confirm your email"
	"msg_promo",        // discount and shopping vocabulary
	"msg_bulk",         // has a List-Unsubscribe header
	"msg_short",        // short body
	"msg_many_links",   // many links, as in newsletters
}

var (
	subjAuthRe    = regexp.MustCompile(`(?i)\b(?:code|otp|passcode|one[- ]time|verification|verify|2fa|two[- ]factor|sign[- ]?in|log[- ]?in|security|authenticat\w*|pin)\b`)
	codePhraseRe  = regexp.MustCompile(`(?i)(?:expires? in|will expire|valid for|did(?:n['’]?t| not) request|do(?:n['’]?t| not) share|never share|one[- ]time|verification code|security code|log[- ]?in code|sign[- ]?in code|access code|authentication code|passcode|ignore this (?:email|message))`)
	linkPhraseRe  = regexp.MustCompile(`(?i)(?:confirm your (?:email|e-mail|account)|verify your (?:email|e-mail|account)|reset (?:your )?password|magic link|click (?:the|this|on the) (?:link|button)|link (?:will )?expires?|(?:button|link) below|sign in to|log in to)`)
	promoPhraseRe = regexp.MustCompile(`(?i)(?:% off|\boff\b|discount|promo|coupon|voucher|checkout|\bsale\b|shop now|free shipping|\boffers?\b|\bsave\b|rewards?\b|\bdeals?\b)`)
)

func messageFeatures(in Input, htmlBody string, nAnchors int) []float64 {
	body := in.Text
	if len(htmlBody) > len(body) {
		body = htmlBody
	}
	distinct := func(re *regexp.Regexp, limit int) float64 {
		seen := map[string]bool{}
		for _, m := range re.FindAllString(body, 200) {
			seen[strings.ToLower(m)] = true
		}
		return math.Min(1, float64(len(seen))/float64(limit))
	}
	return []float64{
		b2f(subjAuthRe.MatchString(in.Subject)),
		distinct(codePhraseRe, 3),
		distinct(linkPhraseRe, 2),
		distinct(promoPhraseRe, 5),
		b2f(in.Bulk),
		math.Min(1, 1200/float64(len(body)+1)),
		math.Min(1, float64(nAnchors)/30),
	}
}

// service names the sender: the From display name, else its domain.
func service(from string) string {
	a, err := mail.ParseAddress(from)
	if err != nil {
		return strings.TrimSpace(from)
	}
	if a.Name != "" {
		return a.Name
	}
	if at := strings.LastIndexByte(a.Address, '@'); at >= 0 {
		return a.Address[at+1:]
	}
	return a.Address
}

func b2f(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
