// Package mailmsg turns a raw RFC 5322 message into extractor input.
package mailmsg

import (
	"bytes"

	"github.com/jhillyerd/enmime"

	"github.com/tikaydaes/codemail/internal/extract"
)

// Parse decodes MIME structure, transfer encodings and charsets.
func Parse(raw []byte) (extract.Input, error) {
	env, err := enmime.ReadEnvelope(bytes.NewReader(raw))
	if err != nil {
		return extract.Input{}, err
	}
	return extract.Input{
		From:    env.GetHeader("From"),
		Subject: env.GetHeader("Subject"),
		Text:    env.Text,
		HTML:    env.HTML,
		Bulk:    env.GetHeader("List-Unsubscribe") != "",
	}, nil
}

// Unforward rewrites the input of a message its owner forwarded by hand,
// so that it reads as the original: the original sender and subject, when
// the body quotes them. It reports whether it changed anything.
func Unforward(in extract.Input) (extract.Input, bool) {
	from, subject, found := Forwarded(in.Text)
	if !found {
		return in, false
	}
	in.From = from
	if subject != "" {
		in.Subject = subject
	}
	return in, true
}
