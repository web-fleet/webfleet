package analytics

import (
	"github.com/web-fleet/webfleet/internal/sqlite"
	"github.com/web-fleet/webfleet/internal/store"
	"testing"
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
