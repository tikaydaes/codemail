package mailauth

import (
	"testing"

	"github.com/tikaydaes/codemail/internal/mailmsg"
)

// Headers as a large provider leaves them: its own lines on top, then
// whatever the sender wrote.
const gmailStyle = "Delivered-To: sam@mailbox.example\r\n" +
	"Received: by 2002:a05:6402:1234:b0:500:1:2:3 with SMTP id x1csp1;\r\n\tMon, 2 Nov 2026 10:00:00 -0800 (PST)\r\n" +
	"ARC-Authentication-Results: i=1; mx.bigmail.example;\r\n\tdkim=pass header.i=@northwind.example header.s=s1;\r\n\tspf=pass smtp.mailfrom=bounce@northwind.example;\r\n\tdmarc=pass (p=REJECT) header.from=northwind.example\r\n" +
	"Received: from out.northwind.example (out.northwind.example. [192.0.2.7])\r\n\tby mx.bigmail.example with ESMTPS id abc;\r\n" +
	"Authentication-Results: mx.bigmail.example;\r\n\tdkim=pass header.i=@northwind.example header.s=s1 header.b=abcd;\r\n\tdmarc=pass (p=REJECT sp=REJECT dis=NONE) header.from=northwind.example\r\n" +
	"From: Northwind <otp@northwind.example>\r\n\r\nbody\r\n"

func TestProviderVerdict(t *testing.T) {
	cases := []struct {
		name      string
		raw       string
		relay     string
		forwarded bool
		want      string
	}{
		{"fetched, provider vouches", gmailStyle, "", false, Pass},
		{"forwarded by the provider itself", gmailStyle, "bigmail.example", true, Pass},
		{"forwarded by someone else carrying the provider's header", gmailStyle, "other.example", true, None},
		{"forwarding server unknown", gmailStyle, "", true, None},
		{"a verdict the sender wrote, with no provider line above it",
			"Authentication-Results: mx.bigmail.example; dmarc=pass header.from=northwind.example\r\nFrom: otp@northwind.example\r\n\r\nx", "", false, None},
		{"a forged verdict under a provider that records none",
			"Received: from evil.example by mx.smallhost.example with ESMTP;\r\n" +
				"Authentication-Results: mx.bigmail.example; dmarc=pass header.from=northwind.example\r\nFrom: otp@northwind.example\r\n\r\nx", "", false, None},
		{"provider says it failed",
			"Received: from evil.example by mx.bigmail.example with ESMTP;\r\n" +
				"Authentication-Results: mx.bigmail.example; spf=fail smtp.mailfrom=x@evil.example; dmarc=fail header.from=northwind.example\r\nFrom: otp@northwind.example\r\n\r\nx", "", false, Fail},
		{"a pass for some other domain does not count",
			"Received: from evil.example by mx.bigmail.example with ESMTP;\r\n" +
				"Authentication-Results: mx.bigmail.example; dkim=pass header.d=evil.example; spf=pass smtp.mailfrom=x@evil.example\r\nFrom: otp@northwind.example\r\n\r\nx", "", false, None},
		{"a provider that leaves its name out, forwarding",
			"Received: from a.prod.bigcorp.example by b.prod.bigcorp.example with HTTPS;\r\n" +
				"Authentication-Results: spf=pass (sender IP is 192.0.2.7) smtp.mailfrom=northwind.example; dkim=pass (signature was verified) header.d=northwind.example;dmarc=pass action=none header.from=northwind.example;compauth=pass reason=100\r\nFrom: otp@northwind.example\r\n\r\nx",
			"bigcorp.example", true, Pass},
		{"the same header when fetched cannot be attributed",
			"Received: from a.prod.bigcorp.example by b.prod.bigcorp.example with HTTPS;\r\n" +
				"Authentication-Results: spf=pass smtp.mailfrom=northwind.example; dmarc=pass header.from=northwind.example\r\nFrom: otp@northwind.example\r\n\r\nx",
			"", false, None},
	}
	for _, c := range cases {
		got, detail := providerVerdict(mailmsg.Headers([]byte(c.raw)), "northwind.example", c.relay, c.forwarded)
		if got != c.want {
			t.Errorf("%s: verdict %q (%s), want %q", c.name, got, detail, c.want)
		}
	}
}
