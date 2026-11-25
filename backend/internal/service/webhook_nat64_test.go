package service

import (
	"net"
	"testing"
)

// C1: SSRF filtering must see through NAT64 (64:ff9b::/96) and IPv4-mapped IPv6,
// which embed an IPv4 address in an IPv6 wrapper — otherwise a target like the
// cloud metadata endpoint reaches the internal network via an IPv6 literal that
// the plain IsPrivate/IsLoopback checks don't recognize. CGNAT (100.64/10) shared
// space is likewise off-limits.
func TestIsDisallowedIP_NAT64AndMapped(t *testing.T) {
	blocked := []string{
		"64:ff9b::a9fe:a9fe", // NAT64-wrapped 169.254.169.254 (metadata)
		"64:ff9b::7f00:1",    // NAT64-wrapped 127.0.0.1 (loopback)
		"64:ff9b::0a00:5",    // NAT64-wrapped 10.0.0.5 (private)
		"::ffff:127.0.0.1",   // IPv4-mapped loopback
		"100.64.0.1",         // CGNAT shared space
		"169.254.169.254",    // link-local metadata
	}
	for _, s := range blocked {
		if !isDisallowedIP(net.ParseIP(s)) {
			t.Errorf("expected %s to be disallowed", s)
		}
	}

	allowed := []string{
		"8.8.8.8",            // public v4
		"64:ff9b::0808:0808", // NAT64-wrapped 8.8.8.8 (public) — must NOT be over-blocked
		"2606:4700:4700::1111",
	}
	for _, s := range allowed {
		if isDisallowedIP(net.ParseIP(s)) {
			t.Errorf("expected %s to be allowed", s)
		}
	}
}
