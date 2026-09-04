package analytics

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/webfleet-cv/webfleet/internal/geo"
	"github.com/webfleet-cv/webfleet/internal/sqlite"
	"github.com/webfleet-cv/webfleet/internal/store"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Property struct {
	ID            int64  `json:"id"`
	SiteID        int64  `json:"site_id"`
	PublicKey     string `json:"public_key"`
	Enabled       bool   `json:"enabled"`
	AllowedOrigin string `json:"allowed_origin"`
	CreatedAt     string `json:"created_at"`
}
type Service struct {
	st            *store.Store
	salt          string
	allowNoOrigin bool
	validLim      *limiter
	badLim        *limiter
	nowFn         func() time.Time
	gdb           *geo.DB
}

// Options configures the analytics service. AllowNoOrigin enables a deliberate
// server-side ingestion mode (empty Origin accepted); the default is the
// browser-tracker contract where Origin must match the property.
type Options struct {
	AllowNoOrigin bool
}

func New(st *store.Store) *Service { return NewWithOptions(st, Options{}) }
func NewWithOptions(st *store.Store, o Options) *Service {
	r, _ := sqlite.Query(st.DB, `SELECT value FROM app_settings WHERE key='analytics_visitor_salt'`)
	salt := ""
	if len(r) > 0 {
		salt = r[0]["value"].Text
	}
	if salt == "" {
		salt = randomKey(24)
		_ = sqlite.Exec(st.DB, `INSERT INTO app_settings(key,value,updated_at) VALUES('analytics_visitor_salt',?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=excluded.updated_at`, salt, store.Now())
	}
	return &Service{st: st, salt: salt, allowNoOrigin: o.AllowNoOrigin, validLim: newLimiter(time.Minute, 300, 20000), badLim: newLimiter(time.Minute, 60, 20000), nowFn: func() time.Time { return time.Now().UTC() }}
}
func randomKey(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
func (s *Service) Enable(siteID int64) (Property, error) {
	r, e := sqlite.Query(s.st.DB, `SELECT primary_url FROM sites WHERE id=?`, siteID)
	if e != nil || len(r) == 0 {
		return Property{}, errors.New("site not found")
	}
	u, e := url.Parse(r[0]["primary_url"].Text)
	if e != nil {
		return Property{}, e
	}
	origin := u.Scheme + "://" + u.Host
	key := randomKey(18)
	rows, e := sqlite.Query(s.st.DB, `INSERT INTO analytics_properties(site_id,public_key,enabled,allowed_origin,created_at) VALUES(?,?,?,?,?) ON CONFLICT(site_id) DO UPDATE SET enabled=1,allowed_origin=excluded.allowed_origin RETURNING id,public_key,created_at`, siteID, key, true, origin, store.Now())
	if e != nil {
		return Property{}, e
	}
	return Property{ID: rows[0]["id"].Int64, SiteID: siteID, PublicKey: rows[0]["public_key"].Text, Enabled: true, AllowedOrigin: origin, CreatedAt: rows[0]["created_at"].Text}, nil
}
func (s *Service) Property(siteID int64) (*Property, error) {
	r, e := sqlite.Query(s.st.DB, `SELECT id,site_id,public_key,enabled,allowed_origin,created_at FROM analytics_properties WHERE site_id=?`, siteID)
	if e != nil {
		return nil, e
	}
	if len(r) == 0 {
		return nil, nil
	}
	x := r[0]
	return &Property{ID: x["id"].Int64, SiteID: x["site_id"].Int64, PublicKey: x["public_key"].Text, Enabled: x["enabled"].Int64 != 0, AllowedOrigin: x["allowed_origin"].Text, CreatedAt: x["created_at"].Text}, nil
}

type Event struct {
	Key      string `json:"key"`
	Kind     string `json:"kind"`
	Path     string `json:"path"`
	Referrer string `json:"referrer"`
	Payload  string `json:"payload"`
}

func (s *Service) Ingest(ev Event, origin, ip, ua string) error {
	r, e := sqlite.Query(s.st.DB, `SELECT id,enabled,allowed_origin FROM analytics_properties WHERE public_key=?`, ev.Key)
	if e != nil || len(r) == 0 {
		// Unknown-property traffic is abuse-controlled per client address so
		// attacker-supplied keys cannot grow the limiter without bound.
		if !s.badLim.Allow("ip:" + ip) {
			return errors.New("analytics rate limited")
		}
		return errors.New("unknown analytics property")
	}
	x := r[0]
	if x["enabled"].Int64 == 0 {
		return errors.New("analytics disabled")
	}
	// Origin is not authentication (a non-browser client can forge it), but the
	// browser-tracker contract requires it to match the property origin.
	if origin == "" && !s.allowNoOrigin {
		return errors.New("analytics origin required")
	}
	if origin != "" && origin != x["allowed_origin"].Text {
		return errors.New("origin not allowed")
	}
	if ev.Kind == "" {
		ev.Kind = "pageview"
	}
	if len(ev.Kind) > 64 || hasControlChars(ev.Kind) {
		return errors.New("invalid analytics event kind")
	}
	if ev.Path == "" {
		ev.Path = "/"
	}
	if len(ev.Path) > 2048 || len(ev.Referrer) > 2048 || len(ev.Payload) > 8192 {
		return errors.New("analytics event too large")
	}
	if ev.Payload != "" && !json.Valid([]byte(ev.Payload)) {
		return errors.New("analytics payload must be valid JSON")
	}
	// Valid-property traffic is rate limited per client address and property,
	// keeping legitimate tracker volume practical while bounding memory.
	pid := x["id"].Int64
	if !s.validLim.Allow("ip:" + ip + ":pid:" + strconv.FormatInt(pid, 10)) {
		return errors.New("analytics rate limited")
	}
	now := time.Now().UTC()
	day := now.Format("2006-01-02")
	bucket := weeklyBucket(now)
	// Anonymous visitor identity: HMAC-SHA256 keyed by the per-instance secret
	// over (fixed 7-day analytics bucket, normalized source IP). The identity is
	// stable within the reporting bucket so a multi-day COUNT(DISTINCT) is a
	// true unique-visitor estimate for that window, and it rotates at the weekly
	// boundary so it is never a permanent tracking identity. The raw IP is never
	// persisted and the user agent is deliberately not part of the identity.
	mac := hmac.New(sha256.New, []byte(s.salt))
	mac.Write([]byte(bucket + "|" + normalizedIP(ip)))
	visitor := hex.EncodeToString(mac.Sum(nil)[:12])
	class := clientClass(ua)
	if class == "bot" {
		return nil
	}
	// Country is resolved at ingestion time from the source IP and stored as a
	// coarse code (e.g. AU). The IP itself is discarded immediately.
	country := ""
	if s.gdb != nil {
		country = s.gdb.Lookup(ip)
	}
	if e := sqlite.Exec(s.st.DB, `INSERT INTO analytics_events(property_id,kind,path,referrer,visitor_key,user_agent_class,payload_json,country,occurred_at) VALUES(?,?,?,?,?,?,?,?,?)`, pid, ev.Kind, ev.Path, ev.Referrer, visitor, class, ev.Payload, country, store.Now()); e != nil {
		return e
	}
	if ev.Kind == "pageview" {
		_ = sqlite.Exec(s.st.DB, `INSERT INTO analytics_daily(property_id,day,pageviews,visitors) VALUES(?,?,1,0) ON CONFLICT(property_id,day) DO UPDATE SET pageviews=analytics_daily.pageviews+1`, pid, day)
		before, _ := sqlite.Query(s.st.DB, `SELECT 1 FROM analytics_daily_visitors WHERE property_id=? AND day=? AND visitor_key=?`, pid, day, visitor)
		if len(before) == 0 {
			_ = sqlite.Exec(s.st.DB, `INSERT INTO analytics_daily_visitors(property_id,day,visitor_key) VALUES(?,?,?)`, pid, day, visitor)
			_ = sqlite.Exec(s.st.DB, `UPDATE analytics_daily SET visitors=visitors+1 WHERE property_id=? AND day=?`, pid, day)
		}
	}
	return nil
}

func hasControlChars(s string) bool {
	for _, c := range s {
		if c < 0x20 || c == 0x7f {
			return true
		}
	}
	return false
}

// limiter is a bounded-memory fixed-window rate limiter shared by the analytics
// ingress paths. It is intentionally in-memory: raw client addresses are never
// persisted.
type limiter struct {
	mu      sync.Mutex
	window  time.Duration
	limit   int
	maxKeys int
	buckets map[string]*limiterBucket
}

type limiterBucket struct {
	count int
	reset time.Time
}

func newLimiter(window time.Duration, limit, maxKeys int) *limiter {
	return &limiter{window: window, limit: limit, maxKeys: maxKeys, buckets: map[string]*limiterBucket{}}
}

func (l *limiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	b, ok := l.buckets[key]
	if !ok {
		for k, old := range l.buckets {
			if now.After(old.reset) {
				delete(l.buckets, k)
			}
		}
		if len(l.buckets) >= l.maxKeys {
			return false
		}
		b = &limiterBucket{reset: now.Add(l.window)}
		l.buckets[key] = b
	}
	if now.After(b.reset) {
		b.count = 0
		b.reset = now.Add(l.window)
	}
	if b.count >= l.limit {
		return false
	}
	b.count++
	return true
}

// normalizedIP canonicalizes a source IP string so the same address always
// produces the same anonymous visitor identifier regardless of notation.
// weeklyBucket returns a stable 7-day analytics bucket (aligned to a fixed
// UTC epoch) used as the reporting-period pseudonym input. The bucket is
// documented in the UI: the anonymous visitor identity is stable within it and
// rotates at the weekly boundary, so a "last 7 days" report counts distinct
// identities in its window (which may span a bucket boundary; a visitor
// crossing the boundary is counted once per bucket - a documented approximation
// of a 7-day unique-visitor estimate).
var weeklyEpoch = time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

func weeklyBucket(t time.Time) string {
	return fmt.Sprintf("W%07d", int(t.UTC().Sub(weeklyEpoch)/(7*24*time.Hour)))
}

func normalizedIP(ip string) string {
	if a, e := netip.ParseAddr(ip); e == nil {
		return a.String()
	}
	if ap, e := netip.ParseAddrPort(ip); e == nil {
		return ap.Addr().String()
	}
	return strings.TrimSpace(ip)
}

func RemoteIP(remote string) string {
	if h, _, e := net.SplitHostPort(remote); e == nil {
		return h
	}
	return strings.Trim(remote, "[]")
}
func clientClass(ua string) string {
	l := strings.ToLower(ua)
	switch {
	case strings.Contains(l, "bot") || strings.Contains(l, "crawler") || strings.Contains(l, "spider"):
		return "bot"
	case strings.Contains(l, "mobile"):
		return "mobile"
	default:
		return "desktop"
	}
}

type Daily struct {
	Day       string `json:"day"`
	Pageviews int64  `json:"pageviews"`
	Visitors  int64  `json:"visitors"`
}
type Summary struct {
	Pageviews  int64            `json:"pageviews"`
	Visitors   int64            `json:"visitors"`
	Daily      []Daily          `json:"daily"`
	TopPages   []map[string]any `json:"top_pages"`
	TopSources []map[string]any `json:"top_sources"`
}

func (s *Service) Summary(siteID int64, days int) (Summary, error) {
	if days < 1 {
		days = 7
	}
	if days > 365 {
		days = 365
	}
	p, e := s.Property(siteID)
	if e != nil || p == nil {
		return Summary{}, e
	}
	since := s.nowFn().AddDate(0, 0, -days+1).Format("2006-01-02")
	rows, e := sqlite.Query(s.st.DB, `SELECT day,pageviews,visitors FROM analytics_daily WHERE property_id=? AND day>=? ORDER BY day`, p.ID, since)
	if e != nil {
		return Summary{}, e
	}
	out := Summary{Daily: []Daily{}, TopPages: []map[string]any{}, TopSources: []map[string]any{}}
	// Headline totals are derived from the event table over the reporting
	// window, using the same weekly-bucket pseudonym as the country breakdown
	// so all three metrics share compatible visitor semantics.
	if r, e := sqlite.Query(s.st.DB, `SELECT COUNT(DISTINCT visitor_key) n FROM analytics_events WHERE property_id=? AND kind='pageview' AND occurred_at>=?`, p.ID, since); e == nil && len(r) > 0 {
		out.Visitors = r[0]["n"].Int64
	}
	if r, e := sqlite.Query(s.st.DB, `SELECT COUNT(*) n FROM analytics_events WHERE property_id=? AND kind='pageview' AND occurred_at>=?`, p.ID, since); e == nil && len(r) > 0 {
		out.Pageviews = r[0]["n"].Int64
	}
	for _, r := range rows {
		out.Daily = append(out.Daily, Daily{r["day"].Text, r["pageviews"].Int64, r["visitors"].Int64})
	}
	pages, _ := sqlite.Query(s.st.DB, `SELECT path,COUNT(*) n FROM analytics_events WHERE property_id=? AND kind='pageview' AND occurred_at>=? GROUP BY path ORDER BY n DESC LIMIT 10`, p.ID, since)
	for _, r := range pages {
		out.TopPages = append(out.TopPages, map[string]any{"path": r["path"].Text, "count": r["n"].Int64})
	}
	src, _ := sqlite.Query(s.st.DB, `SELECT referrer,COUNT(*) n FROM analytics_events WHERE property_id=? AND kind='pageview' AND occurred_at>=? AND referrer<>'' GROUP BY referrer ORDER BY n DESC LIMIT 10`, p.ID, since)
	for _, r := range src {
		out.TopSources = append(out.TopSources, map[string]any{"source": r["referrer"].Text, "count": r["n"].Int64})
	}
	return out, nil
}

type FleetSummary struct {
	Pageviews          int64 `json:"pageviews"`
	Visitors           int64 `json:"visitors"`
	SitesWithAnalytics int64 `json:"sites_with_analytics"`
}

func (s *Service) Fleet(orgID int64, days int) (FleetSummary, error) {
	if days < 1 {
		days = 1
	}
	since := s.nowFn().AddDate(0, 0, -days+1).Format("2006-01-02")
	// Headline fleet totals are derived from the event table over the requested
	// window with the same weekly-bucket pseudonym as the site Summary, so a
	// multi-day fleet visitor total is a true unique-visitor estimate. Because
	// visitor_key is instance-wide (IP-derived), the same anonymous identity
	// observed on two different properties counts once as one fleet visitor.
	r, e := sqlite.Query(s.st.DB, `SELECT (SELECT COUNT(*) FROM analytics_events ev JOIN analytics_properties ap ON ap.id=ev.property_id JOIN sites s ON s.id=ap.site_id WHERE s.organization_id=? AND ev.kind='pageview' AND ev.occurred_at>=?) pageviews,(SELECT COUNT(DISTINCT ev.visitor_key) FROM analytics_events ev JOIN analytics_properties ap ON ap.id=ev.property_id JOIN sites s ON s.id=ap.site_id WHERE s.organization_id=? AND ev.kind='pageview' AND ev.occurred_at>=?) visitors,(SELECT COUNT(*) FROM analytics_properties ap JOIN sites s ON s.id=ap.site_id WHERE ap.enabled=1 AND s.organization_id=?) sites`, orgID, since, orgID, since, orgID)
	if e != nil {
		return FleetSummary{}, e
	}
	x := r[0]
	return FleetSummary{x["pageviews"].Int64, x["visitors"].Int64, x["sites"].Int64}, nil
}

type Goal struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	EventKind string `json:"event_kind"`
	PathMatch string `json:"path_match"`
	CreatedAt string `json:"created_at"`
}

func (s *Service) CreateGoal(siteID int64, name, kind, path string) (Goal, error) {
	name = strings.TrimSpace(name)
	kind = strings.TrimSpace(kind)
	if name == "" || kind == "" {
		return Goal{}, errors.New("goal name and event kind are required")
	}
	p, e := s.Property(siteID)
	if e != nil || p == nil {
		return Goal{}, errors.New("analytics is not enabled")
	}
	r, e := sqlite.Query(s.st.DB, `INSERT INTO analytics_goals(property_id,name,event_kind,path_match,created_at) VALUES(?,?,?,?,?) RETURNING id,created_at`, p.ID, name, kind, path, store.Now())
	if e != nil {
		return Goal{}, e
	}
	return Goal{r[0]["id"].Int64, name, kind, path, r[0]["created_at"].Text}, nil
}
func (s *Service) Goals(siteID int64) ([]map[string]any, error) {
	p, e := s.Property(siteID)
	if e != nil || p == nil {
		return []map[string]any{}, e
	}
	r, e := sqlite.Query(s.st.DB, `SELECT g.id,g.name,g.event_kind,g.path_match,g.created_at,COUNT(e.id) conversions FROM analytics_goals g LEFT JOIN analytics_events e ON e.property_id=g.property_id AND e.kind=g.event_kind AND (g.path_match='' OR e.path=g.path_match) WHERE g.property_id=? GROUP BY g.id ORDER BY g.id`, p.ID)
	if e != nil {
		return nil, e
	}
	out := []map[string]any{}
	for _, x := range r {
		out = append(out, map[string]any{"id": x["id"].Int64, "name": x["name"].Text, "event_kind": x["event_kind"].Text, "path_match": x["path_match"].Text, "conversions": x["conversions"].Int64})
	}
	return out, nil
}

const Tracker = `(()=>{const s=document.currentScript,k=s&&s.dataset.webfleet;if(!k)return;const u=s.src.replace(/\/wf\.js.*/,"/api/analytics/event"),send=(kind,payload={})=>{const e={key:k,kind,path:location.pathname,referrer:document.referrer,payload:JSON.stringify(payload)};try{navigator.sendBeacon(u,new Blob([JSON.stringify(e)],{type:"text/plain;charset=UTF-8"}))}catch(_){}};window.webfleet={track:send};send("pageview");})();`

// Disable stops the property from accepting/recording new tracker events.
// Historical analytics and the property configuration are preserved.
// SetGeo installs the loaded local country database (nil = unavailable).
func (s *Service) SetGeo(db *geo.DB) { s.gdb = db }

func (s *Service) Disable(siteID int64) error {
	p, e := s.Property(siteID)
	if e != nil || p == nil {
		return errors.New("analytics not enabled for this site")
	}
	return sqlite.Exec(s.st.DB, `UPDATE analytics_properties SET enabled=0 WHERE id=?`, p.ID)
}

// TrackerSnippet returns the installation snippet for a property. The script
// is served by Web Fleet itself (origin) and carries the property key via the
// data-webfleet attribute, matching the /wf.js tracker contract.
func (s *Service) TrackerSnippet(origin, key string) string {
	return `<script defer src="` + strings.TrimRight(origin, "/") + `/wf.js" data-webfleet="` + key + `"></script>`
}

type PageViews struct {
	Page     int   `json:"page"`
	PageSize int   `json:"page_size"`
	Total    int64 `json:"total"`
	Pages    int   `json:"pages"`
	Rows     []struct {
		Path      string `json:"path"`
		Pageviews int64  `json:"pageviews"`
	} `json:"rows"`
}

// Pages returns page-view totals by normalized pathname (the tracker already
// sends location.pathname, so query strings do not fragment counts), descending,
// with server-side pagination over the same interval as the headline summary.
func (s *Service) Pages(siteID int64, days, page, pageSize int) (PageViews, error) {
	if days < 1 {
		days = 7
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 50 {
		pageSize = 10
	}
	p, e := s.Property(siteID)
	if e != nil || p == nil {
		return PageViews{}, e
	}
	since := s.nowFn().AddDate(0, 0, -days+1).Format("2006-01-02")
	var total int64
	if r, qe := sqlite.Query(s.st.DB, `SELECT COUNT(DISTINCT path) n FROM analytics_events WHERE property_id=? AND kind='pageview' AND occurred_at>=?`, p.ID, since); qe == nil && len(r) > 0 {
		total = r[0]["n"].Int64
	}
	out := PageViews{Page: page, PageSize: pageSize, Total: total, Pages: int((total + int64(pageSize) - 1) / int64(pageSize)), Rows: []struct {
		Path      string `json:"path"`
		Pageviews int64  `json:"pageviews"`
	}{}}
	rows, qe := sqlite.Query(s.st.DB, `SELECT path,COUNT(*) n FROM analytics_events WHERE property_id=? AND kind='pageview' AND occurred_at>=? AND path<>'' GROUP BY path ORDER BY n DESC,path LIMIT ? OFFSET ?`, p.ID, since, pageSize, (page-1)*pageSize)
	if qe != nil {
		return out, qe
	}
	for _, r := range rows {
		out.Rows = append(out.Rows, struct {
			Path      string `json:"path"`
			Pageviews int64  `json:"pageviews"`
		}{r["path"].Text, r["n"].Int64})
	}
	return out, nil
}

type Countries struct {
	Available bool   `json:"available"`
	Updated   string `json:"updated"`
	Ranges    int    `json:"ranges"`
	Page      int    `json:"page"`
	PageSize  int    `json:"page_size"`
	Total     int64  `json:"total"`
	Pages     int    `json:"pages"`
	Rows      []struct {
		Country  string `json:"country"`
		Visitors int64  `json:"visitors"`
	} `json:"rows"`
}

// Countries returns unique anonymous visitors by country (resolved at
// ingestion time from the source IP; the raw IP is never persisted), descending,
// paginated server-side over the same interval as the headline summary.
func (s *Service) Countries(siteID int64, days, page, pageSize int) (Countries, error) {
	if days < 1 {
		days = 7
	}
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 50 {
		pageSize = 10
	}
	p, e := s.Property(siteID)
	if e != nil || p == nil {
		return Countries{}, e
	}
	since := s.nowFn().AddDate(0, 0, -days+1).Format("2006-01-02")
	var total int64
	if r, qe := sqlite.Query(s.st.DB, `SELECT COUNT(DISTINCT country) n FROM analytics_events WHERE property_id=? AND kind='pageview' AND occurred_at>=? AND country<>''`, p.ID, since); qe == nil && len(r) > 0 {
		total = r[0]["n"].Int64
	}
	out := Countries{Available: s.gdb != nil, Page: page, PageSize: pageSize, Total: total, Pages: int((total + int64(pageSize) - 1) / int64(pageSize)), Rows: []struct {
		Country  string `json:"country"`
		Visitors int64  `json:"visitors"`
	}{}}
	rows, qe := sqlite.Query(s.st.DB, `SELECT country,COUNT(DISTINCT visitor_key) n FROM analytics_events WHERE property_id=? AND kind='pageview' AND occurred_at>=? AND country<>'' GROUP BY country ORDER BY n DESC,country LIMIT ? OFFSET ?`, p.ID, since, pageSize, (page-1)*pageSize)
	if qe != nil {
		return out, qe
	}
	for _, r := range rows {
		out.Rows = append(out.Rows, struct {
			Country  string `json:"country"`
			Visitors int64  `json:"visitors"`
		}{r["country"].Text, r["n"].Int64})
	}
	return out, nil
}
