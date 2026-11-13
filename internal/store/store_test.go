package store

import "testing"

func TestNames(t *testing.T) {
	for _, name := range []string{"admin", "postmaster", "info", "billing", "no-reply", "login", "dmarc", "www", "codemail"} {
		if ValidAlias(name) {
			t.Errorf("%s can be claimed as an address", name)
		}
		if !ValidUsername(name) {
			t.Errorf("%s cannot be a username, although a username is never an address", name)
		}
	}
	for _, name := range []string{"shop", "sam", "quiet-fern-4821", "a"} {
		if !ValidAlias(name) || !ValidUsername(name) {
			t.Errorf("%s refused", name)
		}
	}
	for _, name := range []string{"", "Sam", "sam smith", "-sam", "sam-", "sam@x", "sam.smith", "this-name-is-far-too-long-to-be-allowed-here"} {
		if ValidAlias(name) || ValidUsername(name) {
			t.Errorf("%q accepted", name)
		}
	}
}
