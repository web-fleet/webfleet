package analytics

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/web-fleet/webfleet/internal/sqlite"
	"github.com/web-fleet/webfleet/internal/store"
)

func TestPropertyAndOrigin(t *testing.T) {
	st, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	sqlite.Exec(st.DB, `INSERT INTO sites(organization_id,name,primary_url,created_at,updated_at) VALUES(1,'x','https://example.com/a',?,?)`, store.Now(), store.Now())
	s := New(st)
	p, e := s.Enable(1)
	if e != nil || p.AllowedOrigin != "https://example.com" {
		t.Fatalf("%+v %v", p, e)
	}
	if e = s.Ingest(Event{Key: p.PublicKey, Path: "/"}, "https://evil.test", "1.2.3.4", "x"); e == nil {
		t.Fatal("bad origin accepted")
	}
	if e = s.Ingest(Event{Key: p.PublicKey, Path: "/"}, "https://example.com", "1.2.3.4", "x"); e != nil {
		t.Fatal(e)
	}
}

func TestRollupsAndBotFilter(t *testing.T) {
	st, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	sqlite.Exec(st.DB, `INSERT INTO sites(organization_id,name,primary_url,created_at,updated_at) VALUES(1,'x','https://example.com',?,?)`, store.Now(), store.Now())
	s := New(st)
	p, _ := s.Enable(1)
	_ = s.Ingest(Event{Key: p.PublicKey, Path: "/a"}, "https://example.com", "1", "Mozilla")
	_ = s.Ingest(Event{Key: p.PublicKey, Path: "/a"}, "https://example.com", "1", "Mozilla")
	_ = s.Ingest(Event{Key: p.PublicKey, Path: "/bot"}, "https://example.com", "2", "Googlebot")
	x, e := s.Summary(1, 7)
	if e != nil || x.Pageviews != 2 || x.Visitors != 1 {
		t.Fatalf("%+v %v", x, e)
	}
}

func TestOriginRequiredUnlessServerSideMode(t *testing.T) {
	st, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	sqlite.Exec(st.DB, `INSERT INTO sites(organization_id,name,primary_url,created_at,updated_at) VALUES(1,'x','https://example.com',?,?)`, store.Now(), store.Now())
	browser := New(st)
	p, e := browser.Enable(1)
	if e != nil {
		t.Fatal(e)
	}
	// Browser-tracker contract: an absent Origin is rejected.
	if e = browser.Ingest(Event{Key: p.PublicKey, Path: "/"}, "", "1.2.3.4", "Mozilla"); e == nil {
		t.Fatal("absent origin accepted by browser contract")
	}
	// A spoofed origin is rejected too (Origin is not authentication, but it
	// must match the property for the tracker contract).
	if e = browser.Ingest(Event{Key: p.PublicKey, Path: "/"}, "https://evil.test", "1.2.3.4", "Mozilla"); e == nil {
		t.Fatal("mismatched origin accepted")
	}
	// Deliberate server-side mode allows empty origin.
	server := NewWithOptions(st, Options{AllowNoOrigin: true})
	if e = server.Ingest(Event{Key: p.PublicKey, Path: "/"}, "", "1.2.3.4", "Mozilla"); e != nil {
		t.Fatalf("server-side empty origin rejected: %v", e)
	}
}

func TestUnknownPropertyTrafficRateLimitedByIP(t *testing.T) {
	st, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	s := New(st)
	// Attacker floods unknown property keys from one address; the limiter keys
	// on the client address, so arbitrary keys cannot grow memory without bound.
	rejected := 0
	for i := 0; i < 70; i++ {
		err := s.Ingest(Event{Key: "wf_unknown_" + fmt.Sprintf("%d", i), Path: "/"}, "", "198.51.100.9", "Mozilla")
		if err != nil && strings.Contains(err.Error(), "rate limited") {
			rejected++
		}
	}
	if rejected == 0 {
		t.Fatal("unknown-property traffic was not rate limited")
	}
	// A different client address is not throttled together with the first.
	if err := s.Ingest(Event{Key: "wf_unknown_x", Path: "/"}, "", "203.0.113.5", "Mozilla"); err == nil || strings.Contains(err.Error(), "rate limited") {
		t.Fatalf("different client address throttled: %v", err)
	}
}

func TestValidPropertyRateLimitedPerIPAndProperty(t *testing.T) {
	st, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	sqlite.Exec(st.DB, `INSERT INTO sites(organization_id,name,primary_url,created_at,updated_at) VALUES(1,'x','https://example.com',?,?)`, store.Now(), store.Now())
	s := New(st)
	p, e := s.Enable(1)
	if e != nil {
		t.Fatal(e)
	}
	// Fill the per-address/property bucket directly, then assert Ingest rejects.
	key := "ip:198.51.100.7:pid:" + fmt.Sprintf("%d", p.ID)
	for i := 0; i < 300; i++ {
		s.validLim.Allow(key)
	}
	if e = s.Ingest(Event{Key: p.PublicKey, Path: "/"}, "https://example.com", "198.51.100.7", "Mozilla"); e == nil || !strings.Contains(e.Error(), "rate limited") {
		t.Fatalf("valid traffic not rate limited: %v", e)
	}
	// A different property for the same address is independent.
	sqlite.Exec(st.DB, `INSERT INTO sites(organization_id,name,primary_url,created_at,updated_at) VALUES(1,'y','https://other.test',?,?)`, store.Now(), store.Now())
	p2, e := s.Enable(2)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.Ingest(Event{Key: p2.PublicKey, Path: "/"}, "https://other.test", "198.51.100.7", "Mozilla"); e != nil {
		t.Fatalf("independent property throttled: %v", e)
	}
}

func TestKindAndPayloadValidation(t *testing.T) {
	st, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	sqlite.Exec(st.DB, `INSERT INTO sites(organization_id,name,primary_url,created_at,updated_at) VALUES(1,'x','https://example.com',?,?)`, store.Now(), store.Now())
	s := New(st)
	p, e := s.Enable(1)
	if e != nil {
		t.Fatal(e)
	}
	for _, bad := range []Event{
		{Key: p.PublicKey, Kind: "bad\nkind", Path: "/"},
		{Key: p.PublicKey, Kind: strings.Repeat("k", 65), Path: "/"},
		{Key: p.PublicKey, Path: "/", Payload: "not-json"},
	} {
		if e := s.Ingest(bad, "https://example.com", "1.2.3.4", "Mozilla"); e == nil {
			t.Fatalf("invalid event accepted: %+v", bad)
		}
	}
	if e := s.Ingest(Event{Key: p.PublicKey, Kind: "download", Path: "/d", Payload: `{"pkg":"x"}`}, "https://example.com", "1.2.3.4", "Mozilla"); e != nil {
		t.Fatalf("valid custom event rejected: %v", e)
	}
}

func TestLimiterBoundedKeysAndReclaim(t *testing.T) {
	l := newLimiter(time.Minute, 10, 3)
	if !l.Allow("a") || !l.Allow("b") || !l.Allow("c") {
		t.Fatal("keys denied below capacity")
	}
	if l.Allow("d") {
		t.Fatal("key admitted beyond maxKeys")
	}
	l2 := newLimiter(30*time.Millisecond, 10, 3)
	_ = l2.Allow("a")
	_ = l2.Allow("b")
	_ = l2.Allow("c")
	time.Sleep(50 * time.Millisecond)
	if !l2.Allow("d") {
		t.Fatal("expired buckets not reclaimed; limiter locked at capacity")
	}
}
