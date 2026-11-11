package extract

import (
	"hash/fnv"
	"math"
	"math/rand"
)

// Unknown as a label means the answer was not checked, so the message is
// left out of training and scoring for that kind.
const Unknown = "?"

// Example is one labelled message. Code and Link hold the correct answer,
// are empty when the message has none, or are Unknown. Group keeps messages from one
// sender on the same side of a cross-validation split.
type Example struct {
	In    Input
	Code  string
	Link  string
	Group string
}

// Prepared is an Example with its candidates computed once.
type Prepared struct {
	Code, Link   string
	Group        string
	Codes, Links []Candidate
}

func Prepare(ex Example) Prepared {
	codes, links := Candidates(ex.In)
	for i := range codes {
		codes[i].Context = ""
	}
	for i := range links {
		links[i].Context = ""
	}
	return Prepared{Code: ex.Code, Link: ex.Link, Group: ex.Group, Codes: codes, Links: links}
}

// Metrics counts whole-message outcomes for one kind of extraction.
type Metrics struct {
	Correct int // reported the right value
	Wrong   int // reported a value when there was none, or the wrong one
	Missed  int // reported nothing when there was a value
}

func (m Metrics) Precision() float64 { return ratio(m.Correct, m.Correct+m.Wrong) }

// Recall counts a wrong value on a message that has one as a miss too.
func (m Metrics) Recall(positives int) float64 { return ratio(m.Correct, positives) }

func ratio(a, b int) float64 {
	if b == 0 {
		return 1
	}
	return float64(a) / float64(b)
}

const (
	maxNegatives = 40   // negatives kept per message when fitting
	l2           = 1e-4 // weight decay
	epochs       = 1500
	learnRate    = 1.0
	// The F-beta used to pick each threshold. Codes favour recall: a missed
	// sign-in code defeats the product, while a stray promo code is a minor
	// annoyance. Links are balanced, because ordinary mail is full of
	// links that resemble confirmation links.
	codeBeta = 2.0
	linkBeta = 1.0
)

// Train fits both scorers on the examples.
func Train(data []Prepared) Model {
	return Model{
		Code: fit(data, func(p Prepared) ([]Candidate, string) { return p.Codes, p.Code }, len(CodeFeatureNames), codeBeta),
		Link: fit(data, func(p Prepared) ([]Candidate, string) { return p.Links, p.Link }, len(LinkFeatureNames), linkBeta),
	}
}

type pick func(Prepared) ([]Candidate, string)

func fit(data []Prepared, get pick, dim int, beta float64) Scorer {
	rng := rand.New(rand.NewSource(1))
	var xs [][]float64
	var ys []float64
	var nPos, nNeg float64
	for _, p := range data {
		cands, gold := get(p)
		if gold == Unknown {
			continue
		}
		var negs [][]float64
		for _, c := range cands {
			if gold != "" && c.Value == gold {
				xs, ys = append(xs, c.Features), append(ys, 1)
				nPos++
			} else {
				negs = append(negs, c.Features)
			}
		}
		if len(negs) > maxNegatives {
			rng.Shuffle(len(negs), func(i, j int) { negs[i], negs[j] = negs[j], negs[i] })
			negs = negs[:maxNegatives]
		}
		for _, x := range negs {
			xs, ys = append(xs, x), append(ys, 0)
			nNeg++
		}
	}
	s := Scorer{W: make([]float64, dim), Threshold: 0.5}
	if nPos == 0 {
		s.Bias = -10
		return s
	}
	// Positives are rare, so they are weighted up, but not so far that the
	// model stops caring about false positives.
	posW := math.Min(nNeg/nPos, 10)
	total := nNeg + nPos*posW
	grad := make([]float64, dim)
	for it := 0; it < epochs; it++ {
		for i := range grad {
			grad[i] = 0
		}
		gb := 0.0
		for i, x := range xs {
			d := s.Score(x) - ys[i]
			if ys[i] == 1 {
				d *= posW
			}
			gb += d
			for j, v := range x {
				if v != 0 {
					grad[j] += d * v
				}
			}
		}
		s.Bias -= learnRate * gb / total
		for j := range s.W {
			s.W[j] -= learnRate * (grad[j]/total + l2*s.W[j])
		}
	}
	s.Threshold = bestThreshold(data, s, get, beta)
	return s
}

// bestThreshold picks the cut-off with the best F-beta score.
func bestThreshold(data []Prepared, s Scorer, get pick, beta float64) float64 {
	best, bestF := 0.5, -1.0
	for t := 0.30; t < 0.96; t += 0.05 {
		s.Threshold = t
		m, pos := evaluate(data, s, get)
		p, r := m.Precision(), m.Recall(pos)
		f := 0.0
		if p+r > 0 {
			f = (1 + beta*beta) * p * r / (beta*beta*p + r)
		}
		if f > bestF {
			best, bestF = t, f
		}
	}
	return math.Round(best*100) / 100
}

func evaluate(data []Prepared, s Scorer, get pick) (m Metrics, positives int) {
	for _, p := range data {
		cands, gold := get(p)
		if gold == Unknown {
			continue
		}
		if gold != "" {
			positives++
		}
		got, top := "", -1.0
		for _, c := range cands {
			if sc := s.Score(c.Features); sc > top {
				got, top = c.Value, sc
			}
		}
		if top < s.Threshold {
			got = ""
		}
		switch {
		case got == "" && gold != "":
			m.Missed++
		case got != "" && got == gold:
			m.Correct++
		case got != "":
			m.Wrong++
		}
	}
	return m, positives
}

// Evaluate measures a model on labelled messages.
func Evaluate(data []Prepared, m Model) (code, link Metrics, codePos, linkPos int) {
	code, codePos = evaluate(data, m.Code, func(p Prepared) ([]Candidate, string) { return p.Codes, p.Code })
	link, linkPos = evaluate(data, m.Link, func(p Prepared) ([]Candidate, string) { return p.Links, p.Link })
	return
}

// CrossValidate trains on four fifths of the senders and tests on the
// rest, five times, so every message is scored by a model that never saw
// its sender. This estimates accuracy on services not in the training mail.
func CrossValidate(data []Prepared) (code, link Metrics, codePos, linkPos int) {
	const folds = 5
	fold := func(p Prepared) int {
		h := fnv.New32a()
		h.Write([]byte(p.Group))
		return int(h.Sum32() % folds)
	}
	for k := 0; k < folds; k++ {
		var train, test []Prepared
		for _, p := range data {
			if fold(p) == k {
				test = append(test, p)
			} else {
				train = append(train, p)
			}
		}
		c, l, cp, lp := Evaluate(test, Train(train))
		code.Correct, code.Wrong, code.Missed = code.Correct+c.Correct, code.Wrong+c.Wrong, code.Missed+c.Missed
		link.Correct, link.Wrong, link.Missed = link.Correct+l.Correct, link.Wrong+l.Wrong, link.Missed+l.Missed
		codePos, linkPos = codePos+cp, linkPos+lp
	}
	return
}
