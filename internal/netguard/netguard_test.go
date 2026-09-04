package netguard

import (
	"context"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

type mapResolver map[string][]netip.Addr

func (m mapResolver) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	if ips, ok := m[host]; ok {
		return ips, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: host}
}

type emptyResolver struct{}

func (emptyResolver) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	return []netip.Addr{}, nil
}

// rebindResolver returns an all-public answer on the first lookup (validation)
// and a mixed answer on later lookups (dial), simulating a DNS rebinding that
// flips after the validation lookup.
type rebindResolver struct {
	mu            sync.Mutex
	host          string
	first, second []netip.Addr
	calls         int
}

func (r *rebindResolver) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	if host == r.host && r.calls == 1 {
		return r.first, nil
	}
	return r.second, nil
}

func TestDialContextFailsClosedOnMixedAnswers(t *testing.T) {
	pub := netip.MustParseAddr("8.8.8.8")
	pub6 := netip.MustParseAddr("2001:4860:4860::8888")
	cases := []struct {
		name string
		ips  []netip.Addr
	}{
		{"public-then-private", []netip.Addr{pub, netip.MustParseAddr("127.0.0.1")}},
		{"private-then-public", []netip.Addr{netip.MustParseAddr("127.0.0.1"), pub}},
		{"public-v6-private-v4", []netip.Addr{pub6, netip.MustParseAddr("10.0.0.1")}},
		{"mapped-private-with-public", []netip.Addr{pub, netip.MustParseAddr("::ffff:169.254.169.254")}},
		{"unique-local-v6-with-public", []netip.Addr{pub, netip.MustParseAddr("fd00::1")}},
		{"link-local-v6-with-public", []netip.Addr{pub, netip.MustParseAddr("fe80::1")}},
		{"documentation-v4-with-public", []netip.Addr{pub, netip.MustParseAddr("192.0.2.1")}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := Guard{Resolver: mapResolver{"x": tc.ips}}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			start := time.Now()
			conn, err := g.DialContext(ctx, "tcp", "x:443")
			if err == nil {
				conn.Close()
				t.Fatal("dial accepted a mixed public/private answer set")
			}
			if !strings.Contains(err.Error(), "blocked") {
				t.Fatalf("error = %v, want blocked", err)
			}
			// The whole set is validated before any address is dialed, so a
			// public address that would otherwise be reachable is never
			// connected when the set also contains a blocked address.
			if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
				t.Fatalf("blocked decision took %v; an address in the mixed set was dialed first", elapsed)
			}
		})
	}
}

func TestDialContextRejectsEmptyAnswer(t *testing.T) {
	g := Guard{Resolver: emptyResolver{}}
	if _, err := g.DialContext(context.Background(), "tcp", "x:443"); err == nil {
		t.Fatal("empty resolution accepted")
	}
}

func TestDialContextRejectsRebindingToMixedAnswer(t *testing.T) {
	r := &rebindResolver{
		host:   "rebind.test",
		first:  []netip.Addr{netip.MustParseAddr("8.8.8.8")},
		second: []netip.Addr{netip.MustParseAddr("8.8.8.8"), netip.MustParseAddr("127.0.0.1")},
	}
	g := Guard{Resolver: r}
	if err := g.ValidateHost(context.Background(), "rebind.test"); err != nil {
		t.Fatalf("validation should pass on the all-public answer: %v", err)
	}
	conn, err := g.DialContext(context.Background(), "tcp", "rebind.test:443")
	if err == nil {
		conn.Close()
		t.Fatal("rebinding to a mixed dial-time answer accepted")
	}
	if !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("error = %v, want blocked", err)
	}
}

func TestDialContextFallsBackToNextAllowedAddress(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accept := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		accept <- c
	}()
	port := ln.Addr().(*net.TCPAddr).Port
	// ::1 on this port has no listener (refused immediately) while 127.0.0.1
	// does, so the dialer must fall back across allowed addresses after the
	// complete set passes validation.
	g := Guard{
		Resolver:     mapResolver{"x": []netip.Addr{netip.MustParseAddr("::1"), netip.MustParseAddr("127.0.0.1")}},
		AllowPrivate: true,
	}
	conn, err := g.DialContext(context.Background(), "tcp", "x:"+strconv.Itoa(port))
	if err != nil {
		t.Fatalf("multi-address fallback failed: %v", err)
	}
	conn.Close()
	select {
	case <-accept:
	case <-time.After(2 * time.Second):
		t.Fatal("fallback connection never reached the listener")
	}
}
