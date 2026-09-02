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
	"time"
)

// fixtureCSV uses the real DB-IP Lite three-column format: ip_start,ip_end,country.
const fixtureCSV = `ip_start,ip_end,country
203.0.113.0,203.0.113.255,AU
198.51.100.0,198.51.100.255,US
2001:db8::,2001:db8::ffff:ffff,DE
`

// fixtureCSV4 is a compatibility variant with a fourth country_name column.
const fixtureCSV4 = `start_ip,end_ip,country_code,country_name
203.0.113.0,203.0.113.255,AU,Australia
`

func gzBytes(b []byte) []byte {
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	_, _ = zw.Write(b)
	_ = zw.Close()
	return buf.Bytes()
}

func TestLoadCSVRealFormatAndVariant(t *testing.T) {
	for _, tc := range []struct {
		name string
		csv  string
	}{{"real 3-column", fixtureCSV}, {"4-column variant", fixtureCSV4}} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			p := filepath.Join(dir, "db.csv")
			os.WriteFile(p, []byte(tc.csv), 0o600)
			db, e := LoadCSV(p)
			if e != nil {
				t.Fatal(e)
			}
			if db.Lookup("203.0.113.7") != "AU" {
				t.Fatal("IPv4 lookup wrong")
			}
			if tc.name == "real 3-column" && (db.Lookup("2001:db8::1") != "DE" || db.Lookup("198.51.100.1") != "US") {
				t.Fatal("IPv6/US lookup wrong")
			}
			if db.Lookup("10.0.0.1") != "" {
				t.Fatal("unmapped address resolved")
			}
			if db.Ranges() < 1 {
				t.Fatal("no ranges loaded")
			}
		})
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
	if _, e := os.Stat(filepath.Join(dir, "geoip", "dbip-country-lite.csv")); e != nil {
		t.Fatal("active database file missing after failed update")
	}
}

// TestGzipDatabaseSurvivesRestart proves a gzip download is decompressed before
// being persisted, so a fresh manager on the same data directory reloads it
// without any network request.
func TestGzipDatabaseSurvivesRestart(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/gzip")
		w.Write(gzBytes([]byte(fixtureCSV)))
	}))
	defer srv.Close()
	dir := t.TempDir()
	m := NewManager(dir, srv.URL)
	if e := m.Install(context.Background(), false); e != nil {
		t.Fatal(e)
	}
	// Persisted file must be plain CSV named .csv (decompressed before writing).
	raw, e := os.ReadFile(filepath.Join(dir, "geoip", "dbip-country-lite.csv"))
	if e != nil {
		t.Fatal(e)
	}
	if bytes.Contains(raw, []byte{0x1f, 0x8b}) {
		t.Fatal("gzip bytes persisted under a .csv name; restart reload would fail")
	}
	// Fresh manager / "process restart": load from disk with no network.
	brokenURL := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Fatal("network request during LoadExisting") }))
	defer brokenURL.Close()
	m2 := NewManager(dir, brokenURL.URL)
	if !m2.LoadExisting() {
		t.Fatal("gzip-installed database did not survive restart reload")
	}
	if m2.DB().Lookup("203.0.113.1") != "AU" || m2.DB().Lookup("2001:db8::2") != "DE" {
		t.Fatal("IPv4/IPv6 lookup failed after restart reload")
	}
	if m2.LastUpdated() == "" {
		t.Fatal("last-updated did not survive restart")
	}
}

// TestManagerStalePolicy uses a controllable clock to prove the auto-update
// contract: missing -> eligible, fresh -> not, stale -> eligible; a failed
// refresh retains the previous database and a successful one advances it.
func TestManagerStalePolicy(t *testing.T) {
	valid := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, fixtureCSV) }))
	defer valid.Close()
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "broken") }))
	defer broken.Close()

	dir := t.TempDir()
	clock := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	m := NewManager(dir, valid.URL)
	m.now = func() time.Time { return clock }
	// Missing database is refresh-eligible.
	if !m.NeedsRefresh() {
		t.Fatal("missing database should be refresh-eligible")
	}
	if e := m.Install(context.Background(), false); e != nil {
		t.Fatal(e)
	}
	if m.LastUpdated() != clock.Format(time.RFC3339) {
		t.Fatalf("last-updated=%q want clock time", m.LastUpdated())
	}
	// Fresh database within the interval is not refreshed.
	if m.NeedsRefresh() {
		t.Fatal("fresh database should not be refresh-eligible")
	}
	// Advance past the 30-day interval: now stale.
	clock = clock.Add(31 * 24 * time.Hour)
	if !m.NeedsRefresh() {
		t.Fatal("stale database should be refresh-eligible")
	}
	// Failed refresh retains the previous database and its timestamp.
	mf := NewManager(dir, broken.URL)
	mf.now = func() time.Time { return clock }
	mf.LoadExisting()
	before := mf.LastUpdated()
	if e := mf.Install(context.Background(), true); e == nil {
		t.Fatal("failed refresh unexpectedly succeeded")
	}
	if mf.DB() == nil || mf.DB().Lookup("203.0.113.1") != "AU" {
		t.Fatal("failed refresh dropped the previous database")
	}
	if mf.LastUpdated() != before {
		t.Fatal("failed refresh advanced last-updated")
	}
	// Successful refresh activates the new database and advances the timestamp.
	clock = clock.Add(time.Hour)
	ms := NewManager(dir, valid.URL)
	ms.now = func() time.Time { return clock }
	ms.LoadExisting()
	if e := ms.Install(context.Background(), true); e != nil {
		t.Fatal(e)
	}
	if ms.LastUpdated() != clock.Format(time.RFC3339) {
		t.Fatalf("successful refresh did not advance last-updated: %q", ms.LastUpdated())
	}
	if ms.DB() == nil || ms.DB().Lookup("198.51.100.9") != "US" {
		t.Fatal("refreshed database not active")
	}
}
