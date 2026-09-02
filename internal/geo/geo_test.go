package geo

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

const fixtureCSV = `start_ip,end_ip,country_code,country_name
203.0.113.0,203.0.113.255,AU,Australia
198.51.100.0,198.51.100.255,US,United States
2001:db8::,2001:db8::ffff:ffff,DE,Germany
`

func TestLoadCSVAndLookup(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "db.csv")
	os.WriteFile(p, []byte(fixtureCSV), 0o600)
	db, e := LoadCSV(p)
	if e != nil {
		t.Fatal(e)
	}
	if db.Lookup("203.0.113.7") != "AU" || db.Lookup("203.0.113.255") != "AU" || db.Lookup("198.51.100.1") != "US" {
		t.Fatalf("IPv4 lookups wrong")
	}
	if db.Lookup("2001:db8::1") != "DE" {
		t.Fatalf("IPv6 lookup wrong")
	}
	if db.Lookup("10.0.0.1") != "" || db.Lookup("2001:db9::1") != "" {
		t.Fatal("unmapped address resolved to a country")
	}
	if db.Ranges() != 3 {
		t.Fatalf("ranges=%d want 3", db.Ranges())
	}
}

func TestManagerInstallAtomicAndFailureRetainsPrevious(t *testing.T) {
	valid := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, fixtureCSV) }))
	defer valid.Close()
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "not a csv") }))
	defer broken.Close()

	dir := t.TempDir()
	m := NewManager(dir, valid.URL)
	if m.LoadExisting() {
		t.Fatal("no database should be installed initially")
	}
	if e := m.Install(context.Background(), false); e != nil {
		t.Fatal(e)
	}
	if m.DB() == nil || m.DB().Lookup("203.0.113.1") != "AU" {
		t.Fatal("install did not load the database")
	}
	if m.LastUpdated() == "" {
		t.Fatal("last-updated not recorded")
	}
	first := m.dbPath()
	// A failed update (corrupt payload) must keep the previous working database.
	m2 := NewManager(dir, broken.URL)
	m2.LoadExisting()
	if m2.DB() == nil {
		t.Fatal("existing database not loaded by second manager")
	}
	if e := m2.Install(context.Background(), true); e == nil {
		t.Fatal("corrupt update unexpectedly succeeded")
	}
	if m2.DB() == nil || m2.DB().Lookup("203.0.113.1") != "AU" {
		t.Fatal("corrupt update replaced the previous working database")
	}
	if _, e := os.Stat(first); e != nil {
		t.Fatal("active database file missing after failed update")
	}
}

func TestManagerGzipPayload(t *testing.T) {
	var gz bytes.Buffer
	zw := gzip.NewWriter(&gz)
	zw.Write([]byte(fixtureCSV))
	zw.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/gzip")
		w.Write(gz.Bytes())
	}))
	defer srv.Close()
	m := NewManager(t.TempDir(), srv.URL)
	if e := m.Install(context.Background(), false); e != nil {
		t.Fatal(e)
	}
	if m.DB() == nil || m.DB().Lookup("2001:db8::2") != "DE" {
		t.Fatal("gzip payload not loaded")
	}
}
