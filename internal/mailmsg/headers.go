package mailmsg

import (
	"bytes"
	"net/mail"
	"regexp"
	"strings"
)

// Field is one header field, in the order it appears in the message.
type Field struct {
	Name  string // as written, e.g. "Received"
	Value string // unfolded, without the trailing line break
}

// Headers returns a message's header fields from the top down. Each mail
// server adds its fields above those already there, so the order says
// who wrote what.
func Headers(raw []byte) []Field {
	head := raw
	for _, sep := range []string{"\r\n\r\n", "\n\n"} {
		if i := bytes.Index(raw, []byte(sep)); i >= 0 && i < len(head) {
			head = raw[:i]
		}
	}
	var out []Field
	for _, line := range strings.Split(strings.ReplaceAll(string(head), "\r\n", "\n"), "\n") {
		if line == "" {
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			if len(out) > 0 {
				out[len(out)-1].Value += " " + strings.TrimSpace(line)
			}
			continue
		}
		name, value, found := strings.Cut(line, ":")
		if !found {
			continue
		}
		out = append(out, Field{Name: strings.TrimSpace(name), Value: strings.TrimSpace(value)})
	}
	return out
}

// Is reports whether the field has the given name, ignoring case.
func (f Field) Is(name string) bool { return strings.EqualFold(f.Name, name) }

// Address returns the bare, lower-case address in a header value such as
// `"Sam" <Sam@Example.com>`, or "" if there is none.
func Address(value string) string {
	if a, err := mail.ParseAddress(value); err == nil {
		return strings.ToLower(a.Address)
	}
	return ""
}

// Mentions reports whether a header value or envelope address names the
// mailbox addr. It also recognises the forms forwarding services derive
// from an address: Gmail's "name+caf_=target=host@gmail.com" and plus
// tags in general.
func Mentions(value, addr string) bool {
	value, addr = strings.ToLower(value), strings.ToLower(addr)
	if addr == "" {
		return false
	}
	if strings.Contains(value, addr) {
		return true
	}
	local, domain, found := strings.Cut(addr, "@")
	if !found {
		return false
	}
	for _, m := range plusTagged.FindAllStringSubmatch(value, -1) {
		if m[1] == local && m[2] == domain {
			return true
		}
	}
	return false
}

var plusTagged = regexp.MustCompile(`([a-z0-9._-]+)\+[^@\s<>]*@([a-z0-9.-]+)`)

// The lines mail programs put above a message forwarded by hand.
var (
	forwardMark = regexp.MustCompile(`(?im)^[ \t>]*(-{2,}\s*(forwarded message|original message)\s*-{2,}|begin forwarded message:|_{10,})[ \t\r]*$`)
	quotedFrom  = regexp.MustCompile(`(?im)^[ \t>*]*from:\*?[ \t]*(.+?)[ \t\r]*$`)
	quotedSubj  = regexp.MustCompile(`(?im)^[ \t>*]*subject:\*?[ \t]*(.+?)[ \t\r]*$`)
)

// Forwarded looks in a message body for the header block of a message
// forwarded by hand ("---------- Forwarded message ---------", "Begin
// forwarded message:", Outlook's line of underscores) and returns the
// original sender and subject.
func Forwarded(text string) (from, subject string, found bool) {
	loc := forwardMark.FindStringIndex(text)
	if loc == nil {
		return "", "", false
	}
	// The quoted header block follows within a few lines.
	block := text[loc[1]:]
	if len(block) > 1200 {
		block = block[:1200]
	}
	m := quotedFrom.FindStringSubmatch(block)
	if m == nil {
		return "", "", false
	}
	from = strings.NewReplacer("[mailto:", "<", "]", ">", "*", "").Replace(m[1])
	if Address(from) == "" {
		// "Name <a@b>" survives; anything unparseable is kept as a name only
		// if it at least contains an address.
		if a := bareAddress.FindString(from); a != "" {
			from = a
		} else {
			return "", "", false
		}
	}
	if s := quotedSubj.FindStringSubmatch(block); s != nil {
		subject = s[1]
	}
	return from, subject, true
}

var bareAddress = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
