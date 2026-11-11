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
