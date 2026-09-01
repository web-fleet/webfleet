package requestmeta

import (
	"crypto/tls"
	"net/http/httptest"
	"net/netip"
	"testing"
)

func p(s string) netip.Prefix { return netip.MustParsePrefix(s) }

func TestSchemeHonorsTLSDirect(t *testing.T) {
	c := Config{}
	r := httptest.NewRequest("GET", "http://example.com/", nil)
	if c.Scheme(r) != "http" {
		t.Fatalf("plain = %q", c.Scheme(r))
	}
	r = httptest.NewRequest("GET", "https://example.com/", nil)
	r.TLS = &tls.ConnectionState{}
	if c.Scheme(r) != "https" {
		t.Fatalf("tls = %q", c.Scheme(r))
	}
}

func TestSchemeTrustedProxyForwardedProto(t *testing.T) {
	c := Config{Trusted: []netip.Prefix{p("127.0.0.1/32")}}
	r := httptest.NewRequest("GET", "http://example.com/", nil)
	r.RemoteAddr = "127.0.0.1:9999"
	r.Header.Set("X-Forwarded-Proto", "https")
	if c.Scheme(r) != "https" {
		t.Fatalf("trusted proxy proto = %q", c.Scheme(r))
	}
	// Only http/https are honored; anything else stays http.
	r.Header.Set("X-Forwarded-Proto", "weird")
	if c.Scheme(r) != "http" {
		t.Fatalf("bogus proto honored: %q", c.Scheme(r))
	}
}

func TestSchemeIgnoresUntrustedSpoof(t *testing.T) {
	c := Config{Trusted: []netip.Prefix{p("10.0.0.0/8")}}
	r := httptest.NewRequest("GET", "http://example.com/", nil)
	r.RemoteAddr = "192.0.2.5:9999" // untrusted direct peer
	r.Header.Set("X-Forwarded-Proto", "https")
	if c.Scheme(r) != "http" {
		t.Fatalf("untrusted spoofed proto honored: %q", c.Scheme(r))
	}
	// A spoofed X-Forwarded-For must not change the client identity either.
	r.Header.Set("X-Forwarded-For", "203.0.113.9")
	if got := c.ClientIP(r); got != "192.0.2.5" {
		t.Fatalf("untrusted client ip = %q", got)
	}
}

func TestClientIPTrustedProxyRightmostUntrusted(t *testing.T) {
	c := Config{Trusted: []netip.Prefix{p("127.0.0.1/32"), p("10.0.0.0/8")}}
	r := httptest.NewRequest("GET", "http://example.com/", nil)
	r.RemoteAddr = "127.0.0.1:9999"
	// Attacker-influenced leftmost value is ignored; the real client is the
	// rightmost non-trusted entry added by the trusted proxy.
	r.Header.Set("X-Forwarded-For", "203.0.113.9, 10.1.2.3, 127.0.0.1")
	if got := c.ClientIP(r); got != "203.0.113.9" {
		t.Fatalf("client ip = %q", got)
	}
	// All-trusted chain falls back to the peer.
	r.Header.Set("X-Forwarded-For", "10.1.2.3")
	if got := c.ClientIP(r); got != "127.0.0.1" {
		t.Fatalf("all-trusted client ip = %q", got)
	}
	// Empty chain falls back to the peer.
	r.Header.Del("X-Forwarded-For")
	if got := c.ClientIP(r); got != "127.0.0.1" {
		t.Fatalf("empty client ip = %q", got)
	}
}

func TestClientIPUntrustedIsPeer(t *testing.T) {
	c := Config{}
	r := httptest.NewRequest("GET", "http://example.com/", nil)
	r.RemoteAddr = "192.0.2.7:4444"
	r.Header.Set("X-Forwarded-For", "203.0.113.99")
	if got := c.ClientIP(r); got != "192.0.2.7" {
		t.Fatalf("untrusted peer client ip = %q", got)
	}
}