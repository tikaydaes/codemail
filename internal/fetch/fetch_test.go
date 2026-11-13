package fetch

import (
	"context"
	"errors"
	"net"
	"testing"
)

func TestPrivateAddressesRefused(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if _, err := dialer(false).DialContext(context.Background(), "tcp", ln.Addr().String()); !errors.Is(err, errPrivate) {
		t.Errorf("connecting to this machine: %v, want a refusal", err)
	}
	c, err := dialer(true).DialContext(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("with private addresses allowed: %v", err)
	}
	c.Close()
	for ip, want := range map[string]bool{"8.8.8.8": true, "10.1.2.3": false, "192.168.1.1": false, "100.100.100.100": false,
		"169.254.169.254": false, "::1": false, "fd00::1": false, "2001:4860:4860::8888": true, "::ffff:127.0.0.1": false} {
		if got := publicIP(net.ParseIP(ip)); got != want {
			t.Errorf("publicIP(%s) = %v, want %v", ip, got, want)
		}
	}
}

func TestSealer(t *testing.T) {
	s, err := LoadSealer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sealed, _ := s.Seal("app password")
	if got, err := s.Open(sealed); err != nil || got != "app password" {
		t.Errorf("open = %q, %v", got, err)
	}
	other, _ := LoadSealer(t.TempDir())
	if _, err := other.Open(sealed); err == nil {
		t.Error("another server's key opened the password")
	}
}
