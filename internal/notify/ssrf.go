package notify

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"

	"github.com/myrrolinz/cronmon/internal/netutil"
)

// resolveFunc is a pluggable DNS resolver used by the SSRF-safe dialer.
// Swapping it out in tests allows injection of controlled results without
// making real DNS queries or modifying /etc/hosts.
type resolveFunc func(host string) ([]string, error)

// defaultResolve is the production resolver backed by the system DNS.
func defaultResolve(host string) ([]string, error) {
	return net.LookupHost(host)
}

// makeSSRFSafeDialContext returns a DialContext function that:
//  1. Resolves the target hostname using the supplied resolve function.
//  2. Rejects the connection if any resolved IP is in a private/reserved range.
//  3. Connects directly to the first validated IP — bypassing a second DNS
//     lookup at dial time — to prevent TOCTOU DNS-rebinding attacks.
func makeSSRFSafeDialContext(resolve resolveFunc) func(ctx context.Context, network, addr string) (net.Conn, error) {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, fmt.Errorf("ssrf: split host:port %q: %w", addr, err)
		}

		// Resolve — if addr already contains an IP literal, LookupHost returns
		// it unchanged.
		ips, err := resolve(host)
		if err != nil {
			return nil, fmt.Errorf("ssrf: resolve %q: %w", host, err)
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("ssrf: no addresses for %q", host)
		}

		// Reject if any resolved address is in a private/reserved range.
		for _, ipStr := range ips {
			ip := net.ParseIP(ipStr)
			if ip == nil {
				continue // defensive; LookupHost should always return valid IPs
			}
			if netutil.IsPrivateIP(ip) {
				return nil, fmt.Errorf("ssrf: %q resolves to private or reserved IP %s", host, ipStr)
			}
		}

		// Connect directly to the first validated IP.  Bypassing DNS for the
		// actual dial prevents a rebinding attack where the DNS response changes
		// between our check and the OS-level connect syscall.
		d := &net.Dialer{}
		return d.DialContext(ctx, network, net.JoinHostPort(ips[0], port))
	}
}

// newSSRFSafeClient returns an *http.Client whose transport uses
// makeSSRFSafeDialContext with the provided resolver and request timeout.
func newSSRFSafeClient(resolve resolveFunc, timeout time.Duration) *http.Client {
	return &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			DialContext: makeSSRFSafeDialContext(resolve),
		},
	}
}
