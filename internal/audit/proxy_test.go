package audit

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/webfleet-cv/webfleet/internal/netguard"
)

type mapResolver map[string][]netip.Addr

func (m mapResolver) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	if ips, ok := m[host]; ok {
		return ips, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: host}
}

// flipResolver returns a public address on the first lookup of a host and a
// different address on every later lookup, simulating a DNS rebinding that
// flips between validation and connection.
type flipResolver struct {
	mu     sync.Mutex
	host   string
	first  netip.Addr
	second netip.Addr
	calls  int
}

func (f *flipResolver) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if host == f.host && f.calls == 1 {
		return []netip.Addr{f.first}, nil
	}
	return []netip.Addr{f.second}, nil
}

func proxyWith(allowPrivate bool, r netguard.Resolver) *GuardedProxy {
	g := netguard.New()
	if r != nil {
		g.Resolver = r
	}
	g.AllowPrivate = allowPrivate
	p, err := NewGuardedProxy(g)
	if err != nil {
		panic(err)
	}
	return p
}

// connectReq sends a CONNECT request to the proxy and returns the response.
func connectReq(t *testing.T, p *GuardedProxy, hostport string) (*http.Response, net.Conn) {
	t.Helper()
	c, err := net.Dial("tcp", p.Addr())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(c, "CONNECT "+hostport+" HTTP/1.1\r\nHost: "+hostport+"\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	return resp, c
}

// rawRequest sends a raw absolute-form request to the proxy (as a browser
// configured for a forward proxy would) and returns the response.
func rawRequest(t *testing.T, p *GuardedProxy, requestLine string) (int, string) {
	t.Helper()
	c, err := net.Dial("tcp", p.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if _, err := io.WriteString(c, requestLine+"\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(c), nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestProxyBlocksPrivateDestinations(t *testing.T) {
	p := proxyWith(false, nil)
	defer p.Close()
	for _, h := range []string{
		"127.0.0.1:443",
		"127.0.0.1:80",
		"10.0.0.1:443",
		"172.16.0.1:443",
		"192.168.1.1:443",
		"169.254.169.254:80",
		"192.0.2.1:443",
		"198.51.100.9:443",
		"203.0.113.7:443",
		"[::1]:443",
		"[fc00::1]:443",
		"[fe80::1]:443",
		"[2001:db8::1]:443",
		"[::ffff:127.0.0.1]:443",
	} {
		resp, c := connectReq(t, p, h)
		c.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("CONNECT %s = %d, want 403", h, resp.StatusCode)
		}
	}
}

func TestProxyAllowsPublicConnectThroughTunnel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok from tunnel"))
	}))
	defer srv.Close()
	hostport := strings.TrimPrefix(srv.URL, "http://")
	host := strings.Split(hostport, ":")[0]
	p := proxyWith(true, mapResolver{host: {netip.MustParseAddr("127.0.0.1")}})
	defer p.Close()
	resp, c := connectReq(t, p, hostport)
	if resp.StatusCode != http.StatusOK {
		c.Close()
		t.Fatalf("CONNECT allowed target = %d, want 200", resp.StatusCode)
	}
	io.WriteString(c, "GET / HTTP/1.0\r\n\r\n")
	body, _ := io.ReadAll(c)
	c.Close()
	if !strings.Contains(string(body), "ok from tunnel") {
		t.Fatalf("tunnel body = %q", body)
	}
}

func TestProxyPlainHTTPValidatesAndForwards(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("plain ok"))
	}))
	defer srv.Close()
	hostport := strings.TrimPrefix(srv.URL, "http://")
	host := strings.Split(hostport, ":")[0]
	p := proxyWith(true, mapResolver{host: {netip.MustParseAddr("127.0.0.1")}})
	defer p.Close()
	status, body := rawRequest(t, p, "GET http://"+hostport+"/ HTTP/1.1\r\nHost: "+hostport)
	if status != http.StatusOK || !strings.Contains(body, "plain ok") {
		t.Fatalf("plain proxy response %d %q", status, body)
	}
}

func TestProxyPlainHTTPBlocksPrivate(t *testing.T) {
	p := proxyWith(false, nil)
	defer p.Close()
	status, _ := rawRequest(t, p, "GET http://127.0.0.1:8080/ HTTP/1.1\r\nHost: 127.0.0.1:8080")
	if status != http.StatusForbidden {
		t.Fatalf("plain private = %d, want 403", status)
	}
}

func TestProxyRejectsNonHTTPAndOriginForm(t *testing.T) {
	p := proxyWith(true, nil)
	defer p.Close()
	// origin-form (not proxy absolute-form) must be rejected with 400.
	if status, _ := rawRequest(t, p, "GET / HTTP/1.1\r\nHost: example.com"); status != http.StatusBadRequest {
		t.Fatalf("origin-form = %d, want 400", status)
	}
	// Non-http scheme must be rejected with 403.
	if status, _ := rawRequest(t, p, "GET ftp://example.com/x HTTP/1.1\r\nHost: example.com"); status != http.StatusForbidden {
		t.Fatalf("ftp scheme = %d, want 403", status)
	}
}

// TestProxyDNSRebindingBlockedAtDial proves the boundary that Chromium cannot
// bypass: even if the guard's first resolution reports a public address, the
// guarded dial re-resolves and rejects the private address.
func TestProxyDNSRebindingBlockedAtDial(t *testing.T) {
	flip := &flipResolver{host: "rebind.test", first: netip.MustParseAddr("8.8.8.8"), second: netip.MustParseAddr("127.0.0.1")}
	p := proxyWith(false, flip)
	defer p.Close()
	resp, c := connectReq(t, p, "rebind.test:443")
	c.Close()
	if resp.StatusCode == http.StatusOK {
		t.Fatal("rebinding connection established to a private dial address")
	}
}

// TestProxyRedirectHopMustBeRevalidated simulates Chromium following a 302:
// the proxy returns the redirect to the client without following it, and a
// follow-up request to the private redirect target is refused.
func TestProxyRedirectHopMustBeRevalidated(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:1/private", http.StatusFound)
	}))
	defer upstream.Close()
	hostport := strings.TrimPrefix(upstream.URL, "http://")
	host := strings.Split(hostport, ":")[0]

	allow := proxyWith(true, mapResolver{host: {netip.MustParseAddr("127.0.0.1")}})
	defer allow.Close()
	status, _ := rawRequest(t, allow, "GET http://"+hostport+"/ HTTP/1.1\r\nHost: "+hostport)
	if status != http.StatusFound {
		t.Fatalf("redirect hop = %d, want 302 (proxy must not follow redirects)", status)
	}

	// The private redirect target, reached via a fresh CONNECT, is refused.
	deny := proxyWith(false, nil)
	defer deny.Close()
	resp2, c2 := connectReq(t, deny, "127.0.0.1:1")
	c2.Close()
	if resp2.StatusCode != http.StatusForbidden {
		t.Fatalf("redirect target CONNECT = %d, want 403", resp2.StatusCode)
	}
}

func TestProxyRejectsIPv4MappedAndAlternatePrivateForms(t *testing.T) {
	p := proxyWith(false, nil)
	defer p.Close()
	for _, h := range []string{
		"[::ffff:10.0.0.1]:443",
		"[::ffff:169.254.169.254]:80",
		"[::ffff:192.168.0.1]:443",
	} {
		resp, c := connectReq(t, p, h)
		c.Close()
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("mapped CONNECT %s = %d, want 403", h, resp.StatusCode)
		}
	}
}

// TestProxyCloseTerminatesActiveTunnel proves an established tunnel does not
// survive GuardedProxy.Close: the proxy tracks accepted connections and closes
// them as part of deterministic teardown.
func TestProxyCloseTerminatesActiveTunnel(t *testing.T) {
	upstream, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer upstream.Close()
	go func() {
		for {
			c, err := upstream.Accept()
			if err != nil {
				return
			}
			_ = c // hold the tunnel open
		}
	}()
	port := strings.Split(upstream.Addr().String(), ":")[1]
	p := proxyWith(true, mapResolver{"tunnel.test": {netip.MustParseAddr("127.0.0.1")}})
	resp, c := connectReq(t, p, "tunnel.test:"+port)
	if resp.StatusCode != http.StatusOK {
		c.Close()
		t.Fatalf("tunnel setup = %d, want 200", resp.StatusCode)
	}
	// The tunnel is established; Close must tear it down.
	if err := p.Close(); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Fatal("active tunnel survived proxy Close")
	}
	c.Close()
}
