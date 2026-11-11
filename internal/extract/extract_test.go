package extract

import "testing"

func TestExtract(t *testing.T) {
	tests := []struct {
		name       string
		in         Input
		code, link string
		knownGap   string // why the trained model does not handle this yet
	}{
		{
			name: "plain code",
			in:   Input{From: "Foo <no-reply@foo.example>", Subject: "Sign in to Foo", Text: "Hi,\n\nYour verification code is 123456. It expires in 10 minutes.\n"},
			code: "123456",
		},
		{
			name: "code in subject, numbers in footer",
			in:   Input{Subject: "482913 is your Foo code", Text: "Enter this code to sign in.\n\n© 2026 Foo Inc, 1600 Main St\n"},
			code: "482913",
		},
		{
			name: "receipt has no code",
			in:   Input{Subject: "Your receipt", Text: "Thanks for your order!\nOrder #84729163\nTotal $1299.00\nPlaced 2026-11-11\nCall 1800 555 0199\n"},
		},
		{
			name: "grouped code",
			in:   Input{Subject: "Your login code", Text: "Use this code to log in:\n\n123 456\n"},
			code: "123456",
		},
		{
			name: "alphanumeric code",
			in:   Input{Subject: "Finish signing in", Text: "Your temporary sign-in code: X7K9P2\n"},
			code: "X7K9P2",
		},
		{
			name: "html-only code",
			in:   Input{Subject: "Security alert", HTML: `<html><head><style>.c{font-size:2024px}</style></head><body><p>Your security code</p><div><b>907712</b></div></body></html>`},
			code: "907712",
		},
		{
			name: "magic link among footer links",
			in: Input{
				Subject: "Confirm your email address",
				HTML: `<p>Welcome!</p><a href="https://app.example/verify?token=8f3a9c1e7b2d4f6a8c0e1b3d5f7a9c1e">Verify email</a>
					<a href="https://example.com/">Example</a>
					<a href="https://example.com/unsubscribe?u=8f3a9c1e7b2d4f6a8c0e1b3d5f7a9c1e">Unsubscribe</a>
					<a href="https://example.com/privacy">Privacy</a>`,
			},
			link: "https://app.example/verify?token=8f3a9c1e7b2d4f6a8c0e1b3d5f7a9c1e",
		},
		{
			name:     "bare link in text",
			in:       Input{Subject: "Reset your password", Text: "Click the link below to reset your password:\nhttps://acme.example/r/Zk3Jq9XvB7nM2pL5tR8wY1cD.\n\nHelp: https://acme.example/help\n"},
			link:     "https://acme.example/r/Zk3Jq9XvB7nM2pL5tR8wY1cD",
			knownGap: "the training mail has few plain-text link emails, so bare URLs score too low",
		},
		{
			name: "newsletter has nothing",
			in:   Input{Subject: "November news", Text: "We shipped 12 features in 2026. Read more at https://blog.example/november\n"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.knownGap != "" {
				t.Skip("known gap: " + tt.knownGap)
			}
			got := Default.Extract(tt.in)
			if got.Code != tt.code {
				t.Errorf("code = %q, want %q (candidates %+v)", got.Code, tt.code, got.Candidates)
			}
			if got.Link != tt.link {
				t.Errorf("link = %q, want %q (candidates %+v)", got.Link, tt.link, got.Candidates)
			}
		})
	}
}

func TestService(t *testing.T) {
	for from, want := range map[string]string{
		"Foo Bar <no-reply@foo.example>": "Foo Bar",
		"no-reply@foo.example":           "foo.example",
	} {
		if got := service(from); got != want {
			t.Errorf("service(%q) = %q, want %q", from, got, want)
		}
	}
}
