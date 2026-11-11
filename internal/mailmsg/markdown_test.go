package mailmsg

import (
	"strings"
	"testing"

	"github.com/tikaydaes/codemail/internal/extract"
)

func TestMarkdownFromHTML(t *testing.T) {
	md := Markdown(extract.Input{HTML: `<html><head><title>T</title><style>p{color:red}</style></head><body>
		<div style="display:none;max-height:0">Preview text&#8204;&#8204;</div>
		<table><tr><td><img src="https://x.example/logo.png" alt="Acme logo"><!-- c -->
		<a href="https://x.example/"><img src="https://x.example/b.png" alt="Open Acme"></a>
		<a href="https://twitter.example/acme"><img src="https://x.example/t.png"></a></td></tr>
		<tr><td><h1>Confirm your email</h1><p>Hi <b>Kaity</b>, click below.</p>
		<a href="https://app.example/verify?t=1">Verify email</a>
		<ul><li>One</li><li>Two</li></ul>
		<img src="https://x.example/pixel.gif" width="1" height="1">
		<script>alert(1)</script></td></tr></table></body></html>`})
	for _, want := range []string{"# Confirm your email", "**Kaity**", "[Verify email](https://app.example/verify?t=1)", "- One", "[Open Acme](https://x.example/)"} {
		if !strings.Contains(md, want) {
			t.Errorf("missing %q in:\n%s", want, md)
		}
	}
	for _, bad := range []string{"Preview text", "alert", "color:red", "pixel", "<", "|", "Acme logo", "twitter", "[]"} {
		if strings.Contains(md, bad) {
			t.Errorf("unexpected %q in:\n%s", bad, md)
		}
	}
}

func TestMarkdownFromText(t *testing.T) {
	md := Markdown(extract.Input{Text: "Hi *there*,\r\n1. Use code 123456\r\n- not_a_list\r\n\r\nReset: https://a.example/r?x=a_b_c.\r\n"})
	want := "Hi \\*there\\*,  \n1\\. Use code 123456  \n\\-not\\_a\\_list\n\nReset: <https://a.example/r?x=a_b_c>."
	want = strings.Replace(want, `\-not`, `\- not`, 1)
	if md != want {
		t.Errorf("got:\n%q\nwant:\n%q", md, want)
	}
}
