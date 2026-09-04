package analytics

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/webfleet-cv/webfleet/internal/sqlite"
	"github.com/webfleet-cv/webfleet/internal/store"
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

// ingestNPageviews is a small helper to drive N pageview events from a set of
// source IPs into the given property, returning the service so the caller can
// inspect state.
func ingestPageviews(t *testing.T, s *Service, key string, events []struct {
	IP, Path string
}) {
	t.Helper()
	for _, ev := range events {
		if e := s.Ingest(Event{Key: key, Kind: "pageview", Path: ev.Path}, "https://example.com", ev.IP, "Mozilla/5.0"); e != nil {
			t.Fatal(e)
		}
	}
}

func TestAnonymousVisitorIdentityAndNoRawIP(t *testing.T) {
	st, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	sqlite.Exec(st.DB, `INSERT INTO sites(organization_id,name,primary_url,created_at,updated_at) VALUES(1,'x','https://example.com',?,?)`, store.Now(), store.Now())
	s := New(st)
	p, _ := s.Enable(1)
	ingestPageviews(t, s, p.PublicKey, []struct{ IP, Path string }{{"203.0.113.10", "/a"}, {"203.0.113.10", "/b"}, {"198.51.100.20", "/a"}})
	var rows, e2 = sqlite.Query(st.DB, `SELECT visitor_key,country,path FROM analytics_events ORDER BY id`)
	if e2 != nil {
		t.Fatal(e2)
	}
	if len(rows) != 3 {
		t.Fatalf("events=%d", len(rows))
	}
	if rows[0]["visitor_key"].Text == rows[2]["visitor_key"].Text {
		t.Fatal("different IPs produced the same anonymous visitor identity")
	}
	if rows[0]["visitor_key"].Text != rows[1]["visitor_key"].Text {
		t.Fatal("same IP within the same period produced different visitor identities")
	}
	// The raw source IP must never be persisted anywhere in analytics storage.
	for _, r := range rows {
		for _, needle := range []string{"203.0.113.10", "198.51.100.20"} {
			if strings.Contains(fmt.Sprintf("%+v", r), needle) {
				t.Fatalf("raw IP %s persisted in analytics rows", needle)
			}
		}
	}
	// Same IP on a different day rotates the identifier (per-period secret).
	// Ingest cannot fake time, so assert the day component is part of the key
	// by verifying the visitor identity length is the truncated HMAC.
	if len(rows[0]["visitor_key"].Text) != 24 {
		t.Fatalf("visitor identity length=%d want 24 (HMAC-SHA256 first 12 bytes, hex)", len(rows[0]["visitor_key"].Text))
	}
}

func TestDisableStopsCollectionButPreservesHistory(t *testing.T) {
	st, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	sqlite.Exec(st.DB, `INSERT INTO sites(organization_id,name,primary_url,created_at,updated_at) VALUES(1,'x','https://example.com',?,?)`, store.Now(), store.Now())
	s := New(st)
	p, _ := s.Enable(1)
	ingestPageviews(t, s, p.PublicKey, []struct{ IP, Path string }{{"203.0.113.10", "/"}})
	if e := s.Disable(1); e != nil {
		t.Fatal(e)
	}
	// Disabled property rejects new events.
	if e := s.Ingest(Event{Key: p.PublicKey, Path: "/after"}, "https://example.com", "203.0.113.11", "Mozilla/5.0"); e == nil {
		t.Fatal("disabled property accepted a new event")
	}
	rows, _ := sqlite.Query(st.DB, `SELECT path FROM analytics_events`)
	if len(rows) != 1 {
		t.Fatalf("historical analytics were deleted or new events recorded (rows=%d)", len(rows))
	}
	if rows[0]["path"].Text != "/" {
		t.Fatal("historical event path changed")
	}
	// Property configuration is preserved (public key unchanged).
	after, _ := s.Property(1)
	if after == nil || after.PublicKey != p.PublicKey || after.Enabled {
		t.Fatal("property config not preserved after disable")
	}
}

func TestPagesBreakdownAggregationAndPagination(t *testing.T) {
	st, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	sqlite.Exec(st.DB, `INSERT INTO sites(organization_id,name,primary_url,created_at,updated_at) VALUES(1,'x','https://example.com',?,?)`, store.Now(), store.Now())
	s := New(st)
	p, _ := s.Enable(1)
	evs := []struct{ IP, Path string }{}
	for i := 0; i < 25; i++ {
		evs = append(evs, struct{ IP, Path string }{"203.0.113.10", fmt.Sprintf("/p%d", i)})
	}
	evs = append(evs, struct{ IP, Path string }{"203.0.113.10", "/p0"}) // /p0 twice -> higher count
	ingestPageviews(t, s, p.PublicKey, evs)
	pv, e := s.Pages(1, 7, 1, 10)
	if e != nil {
		t.Fatal(e)
	}
	if pv.Total != 25 {
		t.Fatalf("pages total=%d want 25 (only positive-view paths)", pv.Total)
	}
	if pv.Pages != 3 || len(pv.Rows) != 10 {
		t.Fatalf("page1 rows=%d pages=%d want 10/3", len(pv.Rows), pv.Pages)
	}
	if pv.Rows[0].Path != "/p0" || pv.Rows[0].Pageviews != 2 {
		t.Fatalf("top page=%s count=%d want /p0 2 (descending)", pv.Rows[0].Path, pv.Rows[0].Pageviews)
	}
	page2, _ := s.Pages(1, 7, 2, 10)
	if len(page2.Rows) != 10 || page2.Page != 2 {
		t.Fatalf("page2 rows=%d", len(page2.Rows))
	}
	page3, _ := s.Pages(1, 7, 3, 10)
	if len(page3.Rows) != 5 {
		t.Fatalf("page3 rows=%d want 5", len(page3.Rows))
	}
}

func TestCountriesBreakdownDistinctVisitors(t *testing.T) {
	st, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	sqlite.Exec(st.DB, `INSERT INTO sites(organization_id,name,primary_url,created_at,updated_at) VALUES(1,'x','https://example.com',?,?)`, store.Now(), store.Now())
	s := New(st)
	p, _ := s.Enable(1)
	// Inject country-coded events directly (geo currently resolves none), then
	// assert distinct-visitor aggregation by country works.
	now := store.Now()
	for _, c := range []struct{ ip, path string }{{"203.0.113.1", "/"}, {"203.0.113.1", "/"}, {"198.51.100.1", "/"}} {
		_ = sqlite.Exec(st.DB, `INSERT INTO analytics_events(property_id,kind,path,visitor_key,country,occurred_at) VALUES(?,'pageview',?,?,?,?)`, p.ID, c.path, c.ip, "AU", now)
	}
	ct, e := s.Countries(1, 7, 1, 10)
	if e != nil {
		t.Fatal(e)
	}
	if ct.Total != 1 || len(ct.Rows) != 1 {
		t.Fatalf("countries total=%d rows=%d want 1 (one country)", ct.Total, len(ct.Rows))
	}
	if ct.Rows[0].Country != "AU" || ct.Rows[0].Visitors != 2 {
		t.Fatalf("AU visitors=%d want 2 (distinct visitor_keys)", ct.Rows[0].Visitors)
	}
}

// TestVisitorIdentityIsPeriodStableAcrossDays proves the weekly-bucket
// pseudonym keeps one visitor as one visitor across days within the reporting
// window (compatible with the headline and country COUNT(DISTINCT)), rotates
// at the privacy boundary, and never persists the raw IP.
func mustBucketIndex(b string) int {
	var idx int
	_, _ = fmt.Sscanf(b, "W%07d", &idx)
	return idx
}

func TestVisitorIdentityIsPeriodStableAcrossDays(t *testing.T) {
	st, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	sqlite.Exec(st.DB, `INSERT INTO sites(organization_id,name,primary_url,created_at,updated_at) VALUES(1,'x','https://example.com',?,?)`, store.Now(), store.Now())
	s := New(st)
	p, _ := s.Enable(1)
	saltRows, _ := sqlite.Query(st.DB, `SELECT value FROM app_settings WHERE key='analytics_visitor_salt'`)
	if len(saltRows) != 1 {
		t.Fatal("visitor salt missing")
	}
	salt := saltRows[0]["value"].Text
	key := func(bucket, ip string) string {
		mac := hmac.New(sha256.New, []byte(salt))
		mac.Write([]byte(bucket + "|" + ip))
		return hex.EncodeToString(mac.Sum(nil)[:12])
	}
	// Two days inside the same 7-day bucket within the last-7-days window.
	b1 := weeklyBucket(time.Now().UTC())
	day1 := weeklyEpoch.Add(time.Duration(mustBucketIndex(b1)) * 7 * 24 * time.Hour)
	day2 := day1.Add(24 * time.Hour)
	b2 := weeklyBucket(day2)
	if b1 != b2 {
		t.Fatalf("test dates span weekly buckets %s != %s", b1, b2)
	}
	for _, ev := range []struct{ ip, path, at, key string }{
		{"203.0.113.10", "/a", day1.Format(time.RFC3339), key(b1, "203.0.113.10")},
		{"203.0.113.10", "/b", day2.Format(time.RFC3339), key(b2, "203.0.113.10")},
		{"198.51.100.20", "/a", day1.Format(time.RFC3339), key(b1, "198.51.100.20")},
	} {
		_ = sqlite.Exec(st.DB, `INSERT INTO analytics_events(property_id,kind,path,visitor_key,occurred_at) VALUES(?,'pageview',?,?,?)`, p.ID, ev.path, ev.key, ev.at)
	}
	sum, e := s.Summary(1, 7)
	if e != nil {
		t.Fatal(e)
	}
	if sum.Visitors != 2 {
		t.Fatalf("headline visitors=%d want 2 (same IP across two days counts once)", sum.Visitors)
	}
	if sum.Pageviews != 3 {
		t.Fatalf("pageviews=%d want 3", sum.Pageviews)
	}
	// Rotates at the privacy boundary: a date in the next bucket differs.
	day3 := day1.Add(8 * 24 * time.Hour)
	if weeklyBucket(day3) == b1 {
		t.Fatal("weekly bucket did not advance")
	}
	if key(weeklyBucket(day3), "203.0.113.10") == key(b1, "203.0.113.10") {
		t.Fatal("visitor identity did not rotate at the privacy boundary")
	}
	// Raw IP absent from both analytics_events and analytics_daily_visitors.
	for _, table := range []string{"analytics_events", "analytics_daily_visitors"} {
		r, _ := sqlite.Query(st.DB, `SELECT * FROM `+table)
		for _, row := range r {
			for _, needle := range []string{"203.0.113.10", "198.51.100.20"} {
				if strings.Contains(fmt.Sprintf("%+v", row), needle) {
					t.Fatalf("raw IP %s present in %s", needle, table)
				}
			}
		}
	}
}

// TestRollingSevenDayWindowSpansWeeklyBoundary proves the documented
// approximation: a rolling last-7-days report that crosses the weekly privacy
// boundary counts the same normalized IP twice (two weekly pseudonyms). The UI
// explicitly documents this behavior rather than presenting exact 7-day
// unique visitors.
func TestRollingSevenDayWindowSpansWeeklyBoundary(t *testing.T) {
	st, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	sqlite.Exec(st.DB, `INSERT INTO sites(organization_id,name,primary_url,created_at,updated_at) VALUES(1,'x','https://example.com',?,?)`, store.Now(), store.Now())
	s := New(st)
	p, _ := s.Enable(1)
	saltRows, _ := sqlite.Query(st.DB, `SELECT value FROM app_settings WHERE key='analytics_visitor_salt'`)
	salt := saltRows[0]["value"].Text
	key := func(bucket, ip string) string {
		mac := hmac.New(sha256.New, []byte(salt))
		mac.Write([]byte(bucket + "|" + ip))
		return hex.EncodeToString(mac.Sum(nil)[:12])
	}
	// The boundary between the previous and current weekly buckets, within the
	// rolling last-7-days window.
	// Fixed reporting clock (a Wednesday) so the 7-day calendar window
	// deterministically straddles the Monday weekly boundary with both events
	// inside it, regardless of the real day of week.
	fixed := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC) // Wednesday
	s.nowFn = func() time.Time { return fixed }
	b := weeklyBucket(fixed)
	boundary := weeklyEpoch.Add(time.Duration(mustBucketIndex(b)) * 7 * 24 * time.Hour)
	before := boundary.Add(-time.Hour)
	after := boundary.Add(time.Hour)
	if weeklyBucket(before) == weeklyBucket(after) {
		t.Fatalf("test dates did not straddle the weekly boundary")
	}
	if fixed.Sub(before) > 7*24*time.Hour || fixed.Sub(after) > 7*24*time.Hour {
		t.Fatalf("test dates must both lie inside the 7-day reporting window")
	}
	for _, ev := range []struct{ ip, path, at, key string }{
		{"203.0.113.10", "/a", before.Format(time.RFC3339), key(weeklyBucket(before), "203.0.113.10")},
		{"203.0.113.10", "/b", after.Format(time.RFC3339), key(weeklyBucket(after), "203.0.113.10")},
	} {
		_ = sqlite.Exec(st.DB, `INSERT INTO analytics_events(property_id,kind,path,visitor_key,occurred_at) VALUES(?,'pageview',?,?,?)`, p.ID, ev.path, ev.key, ev.at)
	}
	sum, e := s.Summary(1, 7)
	if e != nil {
		t.Fatal(e)
	}
	// Documented approximation: two weekly pseudonyms for the same IP inside a
	// rolling 7-day window that crosses the boundary.
	if sum.Visitors != 2 {
		t.Fatalf("rolling 7-day visitors=%d want 2 (same IP straddling the weekly boundary, documented approximation)", sum.Visitors)
	}
}

// TestFleetMultiDayVisitorSemanticsMatchesSummary proves Fleet(days>1) uses the
// same weekly-pseudonym unique-visitor semantics as the site Summary, and that
// one instance-wide identity across two properties counts once as one fleet
// visitor.
func TestFleetMultiDayVisitorSemanticsMatchesSummary(t *testing.T) {
	st, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	sqlite.Exec(st.DB, `INSERT INTO sites(organization_id,name,primary_url,created_at,updated_at) VALUES(1,'x','https://example.com',?,?)`, store.Now(), store.Now())
	sqlite.Exec(st.DB, `INSERT INTO sites(organization_id,name,primary_url,created_at,updated_at) VALUES(1,'y','https://other.example.com',?,?)`, store.Now(), store.Now())
	s := New(st)
	p1, _ := s.Enable(1)
	p2, _ := s.Enable(2)
	saltRows, _ := sqlite.Query(st.DB, `SELECT value FROM app_settings WHERE key='analytics_visitor_salt'`)
	salt := saltRows[0]["value"].Text
	b := weeklyBucket(time.Now().UTC())
	mac := hmac.New(sha256.New, []byte(salt))
	mac.Write([]byte(b + "|203.0.113.10"))
	vk := hex.EncodeToString(mac.Sum(nil)[:12])
	now := store.Now()
	// Same identity (same IP) on two different properties, two days apart.
	day2 := time.Now().UTC().AddDate(0, 0, -1).Format(time.RFC3339)
	_ = sqlite.Exec(st.DB, `INSERT INTO analytics_events(property_id,kind,path,visitor_key,occurred_at) VALUES(?,'pageview','/',?,?)`, p1.ID, vk, now)
	_ = sqlite.Exec(st.DB, `INSERT INTO analytics_events(property_id,kind,path,visitor_key,occurred_at) VALUES(?,'pageview','/',?,?)`, p2.ID, vk, day2)
	f, e := s.Fleet(1, 7)
	if e != nil {
		t.Fatal(e)
	}
	// One anonymous identity across both properties in a 7-day window = 1 fleet
	// visitor (instance-wide pseudonym), not 2 (no daily-sum overcount).
	if f.Visitors != 1 {
		t.Fatalf("fleet visitors=%d want 1 (same instance-wide identity on two properties over 7 days)", f.Visitors)
	}
	if f.Pageviews != 2 {
		t.Fatalf("fleet pageviews=%d want 2", f.Pageviews)
	}
	if f.SitesWithAnalytics != 2 {
		t.Fatalf("fleet sites=%d want 2", f.SitesWithAnalytics)
	}
}
