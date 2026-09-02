// Package geo provides local, offline country resolution for analytics
// ingestion. Visitor IPs are never sent to a third-party geolocation service;
// the raw IP is discarded immediately after the coarse country code is derived.
//
// Database source: the DB-IP Lite country dataset (https://db-ip.com), licensed
// CC BY 4.0 (attribution required). It is freely redistributable and requires
// no registration, so Web Fleet manages the whole lifecycle: it downloads the
// CSV into its data directory, loads it in memory, refreshes it periodically,
// and performs lookups locally.
//
// Supported file format: the DB-IP Lite country CSV
//
//	ip_start,ip_end,country
//	1.0.0.0,1.0.0.255,AU
//	2001:db8::,2001:db8::ffff:ffff,DE
//
// A variant with a fourth "country_name" column and a network/CIDR variant
// (a single "network" column) are also accepted. Plain and gzip-compressed
// (.csv.gz) downloads are supported; downloaded payloads are decompressed
// before being persisted as plain .csv so they reload after a restart.
package geo

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/csv"
	"fmt"
	"io"
	"net/http"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

type rangeEntry struct {
	start, end netip.Addr
	country    string
}

// DB is a loaded, immutable country database indexed by sorted address ranges.
type DB struct {
	v4, v6 []rangeEntry
}

// LoadCSV reads a DB-IP Lite country CSV (plain or gzip) and builds the index.
func LoadCSV(path string) (*DB, error) {
	f, e := os.Open(path)
	if e != nil {
		return nil, e
	}
	defer f.Close()
	var r io.Reader = f
	if isGzip(path) {
		gz, e := gzip.NewReader(f)
		if e != nil {
			return nil, fmt.Errorf("read gzip %s: %w", path, e)
		}
		defer gz.Close()
		r = gz
	}
	return ParseCSV(r)
}

// isGzip reports whether a payload carries gzip magic bytes.
func isGzipBytes(b []byte) bool { return len(b) > 2 && b[0] == 0x1f && b[1] == 0x8b }

// decompressPayload returns the plain CSV bytes for a downloaded payload,
// transparently decompressing gzip. The plain bytes are what gets persisted.
func decompressPayload(body []byte) ([]byte, error) {
	if !isGzipBytes(body) {
		return body, nil
	}
	gz, e := gzip.NewReader(bytes.NewReader(body))
	if e != nil {
		return nil, fmt.Errorf("geoip gzip: %w", e)
	}
	defer gz.Close()
	plain, e := io.ReadAll(gz)
	if e != nil {
		return nil, fmt.Errorf("geoip gzip: %w", e)
	}
	return plain, nil
}

// parsePayload parses a downloaded payload, transparently decompressing gzip.
func parsePayload(body []byte) (*DB, error) {
	plain, e := decompressPayload(body)
	if e != nil {
		return nil, e
	}
	return ParseCSV(bytes.NewReader(plain))
}

// ParseCSV builds a DB from a DB-IP Lite country CSV stream.
func ParseCSV(r io.Reader) (*DB, error) {
	cr := csv.NewReader(r)
	cr.FieldsPerRecord = -1
	db := &DB{}
	first := true
	for {
		row, e := cr.Read()
		if e == io.EOF {
			break
		}
		if e != nil {
			return nil, fmt.Errorf("geoip csv: %w", e)
		}
		if first {
			first = false
			if len(row) > 0 && (strings.EqualFold(row[0], "start_ip") || strings.EqualFold(row[0], "network")) {
				continue
			}
		}
		start, end, country := "", "", ""
		if len(row) >= 3 {
			if strings.Contains(row[0], "/") {
				// network/CIDR variant
				p, pe := netip.ParsePrefix(row[0])
				if pe != nil {
					return nil, fmt.Errorf("geoip csv: bad network %q", row[0])
				}
				p = p.Masked()
				start = p.Addr().String()
				end = lastAddr(p).String()
			} else {
				start, end = row[0], row[1]
			}
			country = row[2]
		}
		if start == "" || end == "" || len(country) != 2 {
			continue
		}
		sa, e1 := netip.ParseAddr(start)
		ea, e2 := netip.ParseAddr(end)
		if e1 != nil || e2 != nil {
			continue
		}
		if sa.Is4() {
			db.v4 = append(db.v4, rangeEntry{sa, ea, country})
		} else {
			db.v6 = append(db.v6, rangeEntry{sa, ea, country})
		}
	}
	if len(db.v4)+len(db.v6) == 0 {
		return nil, fmt.Errorf("geoip csv: no valid country ranges")
	}
	sort.Slice(db.v4, func(i, j int) bool { return db.v4[i].start.Less(db.v4[j].start) })
	sort.Slice(db.v6, func(i, j int) bool { return db.v6[i].start.Less(db.v6[j].start) })
	return db, nil
}

func isGzip(path string) bool {
	return strings.HasSuffix(strings.ToLower(path), ".gz")
}

// Lookup returns the two-letter country code for an IP, or "" when unknown.
func (d *DB) Lookup(ip string) string {
	a, e := netip.ParseAddr(ip)
	if e != nil {
		return ""
	}
	var list []rangeEntry
	if a.Is4() {
		list = d.v4
	} else {
		list = d.v6
	}
	i := sort.Search(len(list), func(i int) bool { return list[i].start.Compare(a) > 0 })
	if i == 0 {
		return ""
	}
	ent := list[i-1]
	if a.Compare(ent.end) <= 0 {
		return ent.country
	}
	return ""
}

// Ranges returns the number of loaded prefixes, used as a load indicator.
func (d *DB) Ranges() int { return len(d.v4) + len(d.v6) }

func lastAddr(p netip.Prefix) netip.Addr {
	a := p.Addr()
	b := a.AsSlice()
	hostBits := a.BitLen() - p.Bits()
	for i := len(b) - 1; i >= 0 && hostBits > 0; i-- {
		n := hostBits
		if n > 8 {
			n = 8
		}
		b[i] |= uint8(0xff) >> (8 - n)
		hostBits -= n
	}
	if a.Is4() {
		return netip.AddrFrom4([4]byte{b[0], b[1], b[2], b[3]})
	}
	var v16 [16]byte
	copy(v16[:], b)
	return netip.AddrFrom16(v16)
}

// Manager owns the local country-database lifecycle. Install/refresh are atomic:
// the new file is downloaded to a temp path, validated, then swapped over the
// previous database; a failed download/validation keeps the previous database.
type Manager struct {
	mu       sync.Mutex
	dataDir  string
	url      string
	db       *DB
	updated  string
	interval time.Duration
	now      func() time.Time
}

// refreshInterval is the stale-data policy: DB-IP Lite Country is updated
// monthly, so a database younger than this is used without a network request
// and an older one is refreshed in the background (keeping the previous
// database active on failure).
const refreshInterval = 30 * 24 * time.Hour

// NewManager creates a manager storing the database under <dataDir>/geoip.
func NewManager(dataDir, url string) *Manager {
	return &Manager{dataDir: dataDir, url: url, interval: refreshInterval, now: time.Now}
}

func (m *Manager) dir() string { return filepath.Join(m.dataDir, "geoip") }
func (m *Manager) dbPath() string {
	return filepath.Join(m.dir(), "dbip-country-lite.csv")
}
func (m *Manager) updatedPath() string { return filepath.Join(m.dir(), ".updated") }

// DB returns the currently loaded database (nil when none is installed).
func (m *Manager) DB() *DB {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.db
}

// NeedsRefresh reports whether the installed database is absent or older than
// the refresh interval, so automatic updates refresh stale databases rather
// than only installing missing ones.
func (m *Manager) NeedsRefresh() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.db == nil {
		return true
	}
	if m.updated == "" {
		return true
	}
	t, e := time.Parse(time.RFC3339, m.updated)
	if e != nil {
		return true
	}
	return m.now().UTC().Sub(t) >= m.interval
}

// LastUpdated returns the RFC3339 install time of the active database, or "".
func (m *Manager) LastUpdated() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.updated
}

// LoadExisting loads whatever database is already on disk without any network
// activity (used at startup). Returns true when a database is active.
func (m *Manager) LoadExisting() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if b, e := os.ReadFile(m.updatedPath()); e == nil {
		m.updated = strings.TrimSpace(string(b))
	}
	db, e := LoadCSV(m.dbPath())
	if e != nil {
		m.db = nil
		return false
	}
	m.db = db
	return true
}

// EnsureCurrent implements the automatic lifecycle: a missing database is
// installed, a fresh one is left alone (no network), and a stale one is
// refreshed in place - atomically, keeping the previous database active on
// failure. This is the operation the server startup auto-update path uses.
func (m *Manager) EnsureCurrent(ctx context.Context) error {
	if !m.NeedsRefresh() {
		return nil
	}
	return m.fetch(ctx)
}

// Update forces an immediate refresh regardless of database age (the manual
// "Update database" action). A failed refresh keeps the previous database and
// its last-updated timestamp.
func (m *Manager) Update(ctx context.Context) error {
	return m.fetch(ctx)
}

func (m *Manager) fetch(ctx context.Context) error {
	if m.url == "" {
		return fmt.Errorf("no GeoIP database URL configured")
	}
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, m.url, nil)
	if e != nil {
		return e
	}
	resp, e := http.DefaultClient.Do(req)
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("geoip download %d", resp.StatusCode)
	}
	body, e := io.ReadAll(io.LimitReader(resp.Body, 256<<20))
	if e != nil {
		return e
	}
	// Decompress (if needed) and validate before swapping so a corrupted
	// download never replaces a working database; the persisted file is always
	// plain CSV named .csv so it reloads after a restart.
	plain, e := decompressPayload(body)
	if e != nil {
		return e
	}
	db, e := ParseCSV(bytes.NewReader(plain))
	if e != nil {
		return fmt.Errorf("geoip payload invalid: %w", e)
	}
	if e := os.MkdirAll(m.dir(), 0o700); e != nil {
		return e
	}
	tmp := m.dbPath() + ".tmp"
	if e := os.WriteFile(tmp, plain, 0o600); e != nil {
		return e
	}
	now := m.now().UTC().Format(time.RFC3339)
	if e := os.Rename(tmp, m.dbPath()); e != nil {
		_ = os.Remove(tmp)
		return e
	}
	_ = os.WriteFile(m.updatedPath(), []byte(now), 0o600)
	m.mu.Lock()
	m.db = db
	m.updated = now
	m.mu.Unlock()
	return nil
}
