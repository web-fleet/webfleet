// Package requestmeta resolves request identity that is safe to trust: the
// effective scheme and the originating client address, honoring forwarded
// headers only from explicitly configured trusted proxy prefixes. A direct,
// untrusted peer can never influence the scheme, client IP, externally
// generated URLs, OIDC redirect URIs or cookie security decisions by supplying
// X-Forwarded-* headers.
package requestmeta

import (
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// Config is the trusted-proxy boundary. Forwarded headers are honored only
// when the immediate TCP peer is one of these prefixes.
type Config struct {
	Trusted []netip.Prefix
}

func (c Config) trusted(ip netip.Addr) bool {
	for _, p := range c.Trusted {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// peerIP returns the immediate TCP peer address.
func peerIP(r *http.Request) (netip.Addr, bool) {
	if r == nil {
		return netip.Addr{}, false
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		if ip, perr := netip.ParseAddr(host); perr == nil {
			return ip, true
		}
		return netip.Addr{}, false
	}
	// Tests and some clients set RemoteAddr without a port.
	ip, err := netip.ParseAddr(r.RemoteAddr)
	if err != nil {
		return netip.Addr{}, false
	}
	return ip, true
}

// Scheme returns the effective request scheme. r.TLS is authoritative for a
// direct HTTPS connection. A trusted peer's X-Forwarded-Proto (http or https
// only) is honored. Any other combination resolves to http, so an untrusted
// client spoofing X-Forwarded-Proto cannot upgrade a plaintext request.
func (c Config) Scheme(r *http.Request) string {
	if r != nil && r.TLS != nil {
		return "https"
	}
	if ip, ok := peerIP(r); ok && c.trusted(ip) {
		switch p := r.Header.Get("X-Forwarded-Proto"); p {
		case "https", "http":
			return p
		}
	}
	return "http"
}

// Secure reports whether the effective request is HTTPS.
func (c Config) Secure(r *http.Request) bool { return c.Scheme(r) == "https" }

// ClientIP returns the originating client address. For an untrusted direct
// peer it is the peer itself. For a trusted proxy it is the rightmost entry in
// X-Forwarded-For that is not itself a trusted prefix, so a spoofed leftmost
// value cannot impersonate the client. If no usable untrusted entry exists,
// the peer is returned.
func (c Config) ClientIP(r *http.Request) string {
	peer, ok := peerIP(r)
	if !ok {
		return ""
	}
	if !c.trusted(peer) {
		return peer.String()
	}
	xf := r.Header.Get("X-Forwarded-For")
	if xf == "" {
		return peer.String()
	}
	parts := strings.Split(xf, ",")
	for i := len(parts) - 1; i >= 0; i-- {
		ip, err := netip.ParseAddr(strings.TrimSpace(parts[i]))
		if err != nil {
			continue
		}
		if !c.trusted(ip) {
			return ip.String()
		}
	}
	return peer.String()
}