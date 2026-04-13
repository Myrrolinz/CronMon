// Package netutil provides shared network utility helpers.
package netutil

import (
	"fmt"
	"net"
)

// PrivateRanges is the list of IP network blocks that are considered private,
// loopback, or link-local.  Exported so callers can reference it directly if
// needed; prefer calling IsPrivateIP instead.
//
// IPv4-mapped IPv6 addresses (e.g. ::ffff:192.168.1.1) are handled by the
// To4() normalisation inside IsPrivateIP and do not need their own entry here.
// Adding ::ffff:0:0/96 separately would cause net.IPNet.Contains to silently
// degrade it to 0.0.0.0/0, matching every IPv4 address.
var PrivateRanges = func() []*net.IPNet {
	cidrs := []string{
		"10.0.0.0/8",     // RFC 1918
		"172.16.0.0/12",  // RFC 1918
		"192.168.0.0/16", // RFC 1918
		"127.0.0.0/8",    // loopback (IPv4)
		"169.254.0.0/16", // link-local (IPv4)
		"::1/128",        // loopback (IPv6)
		"fe80::/10",      // link-local (IPv6)
		"fc00::/7",       // unique local (IPv6)
	}
	nets := make([]*net.IPNet, 0, len(cidrs))
	for _, cidr := range cidrs {
		_, ipnet, err := net.ParseCIDR(cidr)
		if err != nil {
			// Hard-coded strings must always parse; a failure here is a
			// programming error, not a runtime condition.
			panic(fmt.Sprintf("netutil: invalid private CIDR %q: %v", cidr, err))
		}
		nets = append(nets, ipnet)
	}
	return nets
}()

// IsPrivateIP reports whether ip falls within any private, loopback, or
// link-local range.  IPv4-mapped IPv6 addresses (::ffff:x.x.x.x) are
// normalized to their IPv4 form via To4() before the range checks run, so the
// IPv4 private ranges cover them automatically.
func IsPrivateIP(ip net.IP) bool {
	// Normalise IPv4-in-IPv6 to its IPv4 form so that the IPv4 private-range
	// checks apply correctly.
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	for _, r := range PrivateRanges {
		if r.Contains(ip) {
			return true
		}
	}
	return false
}
