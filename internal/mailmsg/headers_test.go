package mailmsg

import "testing"

func TestForwarded(t *testing.T) {
	cases := []struct{ name, body, from, subject string }{
		{"Gmail", "see below\r\n\r\n---------- Forwarded message ---------\r\nFrom: Northwind Bank <otp@northwind.example>\r\nDate: Mon, 2 Nov 2026 at 10:00\r\nSubject: Your code\r\nTo: <me@mailbox.example>\r\n\r\nYour code is 482913",
			"otp@northwind.example", "Your code"},
		{"Apple Mail", "Begin forwarded message:\n\n> From: Northwind <otp@northwind.example>\n> Subject: Sign in\n> Date: 2 November 2026\n\n482913",
			"otp@northwind.example", "Sign in"},
		{"Outlook", "________________________________\nFrom: Northwind Bank <otp@northwind.example>\nSent: Monday, November 2, 2026 10:00 AM\nTo: Me <me@mailbox.example>\nSubject: Your code\n\n482913",
			"otp@northwind.example", "Your code"},
		{"Outlook, address in brackets", "-----Original Message-----\nFrom: Northwind [mailto:otp@northwind.example]\nSubject: Code\n\n482913",
			"otp@northwind.example", "Code"},
	}
	for _, c := range cases {
		from, subject, found := Forwarded(c.body)
		if !found || Address(from) != c.from || subject != c.subject {
			t.Errorf("%s: from %q subject %q found %v", c.name, from, subject, found)
		}
	}
	if _, _, found := Forwarded("From: someone@example.com wrote to say hello"); found {
		t.Error("an ordinary message was taken for a forwarded one")
	}
}

func TestMentions(t *testing.T) {
	for value, want := range map[string]bool{
		"<Sam@Mailbox.example>":                      true,
		"sam+caf_=sam=codes.example@mailbox.example": true,
		"samantha@mailbox.example":                   false,
		"sam@other.example":                          false,
	} {
		if got := Mentions(value, "sam@mailbox.example"); got != want {
			t.Errorf("Mentions(%q) = %v, want %v", value, got, want)
		}
	}
}

func TestHeadersKeepOrder(t *testing.T) {
	h := Headers([]byte("Received: by a.example;\r\n\tMon\r\nAuthentication-Results: a.example;\r\n dkim=pass\r\nFrom: x@y.example\r\n\r\nReceived: not a header\r\n"))
	if len(h) != 3 || h[0].Value != "by a.example; Mon" || !h[1].Is("authentication-results") || h[2].Name != "From" {
		t.Errorf("headers = %+v", h)
	}
}
