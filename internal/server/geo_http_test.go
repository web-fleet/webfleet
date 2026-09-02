package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/web-fleet/webfleet/internal/config"
	"github.com/web-fleet/webfleet/internal/geo"
	"github.com/web-fleet/webfleet/internal/store"
)

const geoFixtureCSV = `ip_start,ip_end,country
203.0.113.0,203.0.113.255,AU
2001:db8::,2001:db8::ffff:ffff,DE
`

func seedGeo(dir, url string, stale bool) (string, error) {
	m := geo.NewManager(dir, url)
	if e := m.EnsureCurrent(context.Background()); e != nil {
		return "", e
	}
	if stale {
		upd := filepath.Join(dir, "geoip", ".updated")
		t := time.Now().UTC().AddDate(0, 0, -31).Format(time.RFC3339)
		if e := os.WriteFile(upd, []byte(t), 0o600); e != nil {
			return "", e
		}
		return t, nil
	}
	return m.LastUpdated(), nil
}

func geoServer(t *testing.T, dir, url string, auto bool, counting *atomic.Int64) *Server {
	t.Helper()
	st, e := store.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { st.Close() })
	cfg := config.Config{DataDir: dir, GeoIPURL: url, GeoIPAutoUpdate: auto}
	return New(cfg, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestServerGeoAutoUpdateRefreshesStaleDatabase(t *testing.T) {
	var reqs atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs.Add(1)
		io.WriteString(w, geoFixtureCSV)
	}))
	defer upstream.Close()
	dir := t.TempDir()
	seeded, e := seedGeo(dir, upstream.URL, true)
	if e != nil {
		t.Fatal(e)
	}
	reqs.Store(0)
	s := geoServer(t, dir, upstream.URL, true, &reqs)
	// The production startup path must refresh the stale database: it issues a
	// download request and activates the new database with an advanced timestamp
	// that differs from the exact seeded value.
	done := false
	for i := 0; i < 50 && !done; i++ {
		time.Sleep(100 * time.Millisecond)
		if reqs.Load() > 0 && s.geo.DB() != nil && s.geo.LastUpdated() != seeded {
			done = true
		}
	}
	if !done {
		t.Fatalf("stale database was not auto-refreshed (requests=%d last_updated=%q)", reqs.Load(), s.geo.LastUpdated())
	}
	if s.geo.DB() == nil || s.geo.DB().Lookup("203.0.113.7") != "AU" {
		t.Fatal("refreshed database not active")
	}
}

func TestServerGeoAutoUpdateFreshIsNoNetwork(t *testing.T) {
	var reqs atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs.Add(1)
		io.WriteString(w, geoFixtureCSV)
	}))
	defer upstream.Close()
	dir := t.TempDir()
	if _, e := seedGeo(dir, upstream.URL, false); e != nil {
		t.Fatal(e)
	}
	reqs.Store(0)
	geoServer(t, dir, upstream.URL, true, &reqs)
	time.Sleep(500 * time.Millisecond)
	if reqs.Load() != 0 {
		t.Fatalf("fresh database triggered %d network requests", reqs.Load())
	}
}

func TestServerGeoAutoUpdateDisabledIsNoNetwork(t *testing.T) {
	var reqs atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs.Add(1)
		io.WriteString(w, geoFixtureCSV)
	}))
	defer upstream.Close()
	dir := t.TempDir()
	if _, e := seedGeo(dir, upstream.URL, true); e != nil {
		t.Fatal(e)
	}
	reqs.Store(0)
	s := geoServer(t, dir, upstream.URL, false, &reqs)
	time.Sleep(500 * time.Millisecond)
	if reqs.Load() != 0 {
		t.Fatalf("auto-update disabled still made %d requests", reqs.Load())
	}
	if s.geo.DB() == nil {
		t.Fatal("existing database not loaded with auto-update disabled")
	}
}

func TestServerGeoStaleRefreshFailureRetainsPrevious(t *testing.T) {
	valid := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, geoFixtureCSV) }))
	defer valid.Close()
	var reqs atomic.Int64
	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqs.Add(1)
		io.WriteString(w, "not a csv")
	}))
	defer broken.Close()
	dir := t.TempDir()
	seeded, e := seedGeo(dir, valid.URL, true)
	if e != nil {
		t.Fatal(e)
	}
	reqs.Store(0)
	s := geoServer(t, dir, broken.URL, true, &reqs)
	// The refresh attempt must be made, then fail and retain the old database
	// with its exact seeded last-updated timestamp.
	sawAttempt := false
	for i := 0; i < 50 && !sawAttempt; i++ {
		time.Sleep(100 * time.Millisecond)
		sawAttempt = reqs.Load() > 0
	}
	if !sawAttempt {
		t.Fatal("stale refresh attempt was never made")
	}
	if s.geo.DB() == nil || s.geo.DB().Lookup("203.0.113.7") != "AU" {
		t.Fatal("failed refresh dropped the previous database")
	}
	if s.geo.LastUpdated() != seeded {
		t.Fatalf("failed refresh changed last-updated to %q (want exact seeded %q)", s.geo.LastUpdated(), seeded)
	}
}
