package fetch

import (
	"errors"
	"net"
	"syscall"
	"time"
)

var errPrivate = errors.New("that address is on a private network, which this server does not connect to")

var cgnat = &net.IPNet{IP: net.IPv4(100, 64, 0, 0), Mask: net.CIDRMask(10, 32)}

// publicIP reports whether an address is out on the internet, as opposed
// to this machine or its local network.
func publicIP(ip net.IP) bool {
	return !(ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() || ip.IsUnspecified() || cgnat.Contains(ip))
}

// dialer returns a dialer for connecting to a mail server a user named.
// Unless private addresses are allowed, it refuses them at the moment of
// connecting, after the name has been resolved, so that a hosted server
// cannot be pointed at its own network.
func dialer(allowPrivate bool) *net.Dialer {
	d := &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 60 * time.Second}
	if !allowPrivate {
		d.Control = func(_, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			if ip := net.ParseIP(host); ip == nil || !publicIP(ip) {
				return errPrivate
			}
			return nil
		}
	}
	return d
}
