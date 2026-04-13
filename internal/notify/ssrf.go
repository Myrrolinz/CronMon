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
//  3. Connects directly to each validated IP in order — bypassing a second DNS
//     lookup at dial time — to prevent TOCTOU DNS-rebinding attacks.  The
//     loop handles multi-IP DNS responses (e.g. dual-stack A + AAAA records)
//     gracefully: if the first IP is unreachable the dialer tries the next.
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

		// Reject if ANY resolved address is in a private/reserved range.
		// Checking all IPs before dialing prevents a mixed A/AAAA response
		// from being used to smuggle a private address through validation.
		for _, ipStr := range ips {
			ip := net.ParseIP(ipStr)
			if ip == nil {
				continue // defensive; LookupHost should always return valid IPs
			}
			if netutil.IsPrivateIP(ip) {
				return nil, fmt.Errorf("ssrf: %q resolves to private or reserved IP %s", host, ipStr)
			}
		}

		// All resolved IPs are safe.  Try each in order; return on the first
		// successful dial.  Bypassing DNS at dial time prevents a rebinding
		// attack where the DNS response changes between our check and the
		// OS-level connect syscall.
		d := &net.Dialer{}
		var lastErr error
		for _, ipStr := range ips {
			if ctx.Err() != nil {
				break // context already cancelled; no point trying further
			}
			conn, dialErr := d.DialContext(ctx, network, net.JoinHostPort(ipStr, port))
			if dialErr == nil {
				return conn, nil
			}
			lastErr = dialErr
		}
		return nil, fmt.Errorf("ssrf: dial %q: %w", host, lastErr)
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
