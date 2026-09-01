package analytics

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"github.com/web-fleet/webfleet/internal/sqlite"
	"github.com/web-fleet/webfleet/internal/store"
	"net/url"
	"strings"
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
	st   *store.Store
	salt string
}

func New(st *store.Store) *Service { return &Service{st: st, salt: randomKey(24)} }
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
		return errors.New("unknown analytics property")
	}
	x := r[0]
	if x["enabled"].Int64 == 0 {
		return errors.New("analytics disabled")
	}
	if origin != "" && origin != x["allowed_origin"].Text {
		return errors.New("origin not allowed")
	}
	if ev.Kind == "" {
		ev.Kind = "pageview"
	}
	if ev.Path == "" {
		ev.Path = "/"
	}
	if len(ev.Path) > 2048 || len(ev.Referrer) > 2048 || len(ev.Payload) > 8192 {
		return errors.New("analytics event too large")
	}
	day := time.Now().UTC().Format("2006-01-02")
	h := sha256.Sum256([]byte(day + "|" + s.salt + "|" + ip + "|" + ua))
	visitor := hex.EncodeToString(h[:12])
	class := clientClass(ua)
	if class == "bot" {
		return nil
	}
	pid := x["id"].Int64
	if e := sqlite.Exec(s.st.DB, `INSERT INTO analytics_events(property_id,kind,path,referrer,visitor_key,user_agent_class,payload_json,occurred_at) VALUES(?,?,?,?,?,?,?,?)`, pid, ev.Kind, ev.Path, ev.Referrer, visitor, class, ev.Payload, store.Now()); e != nil {
		return e
	}
	if ev.Kind == "pageview" {
		_ = sqlite.Exec(s.st.DB, `INSERT INTO analytics_daily(property_id,day,pageviews,visitors) VALUES(?,?,1,0) ON CONFLICT(property_id,day) DO UPDATE SET pageviews=pageviews+1`, pid, day)
		before, _ := sqlite.Query(s.st.DB, `SELECT 1 FROM analytics_daily_visitors WHERE property_id=? AND day=? AND visitor_key=?`, pid, day, visitor)
		if len(before) == 0 {
			_ = sqlite.Exec(s.st.DB, `INSERT INTO analytics_daily_visitors(property_id,day,visitor_key) VALUES(?,?,?)`, pid, day, visitor)
			_ = sqlite.Exec(s.st.DB, `UPDATE analytics_daily SET visitors=visitors+1 WHERE property_id=? AND day=?`, pid, day)
		}
	}
	return nil
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
	since := time.Now().UTC().AddDate(0, 0, -days+1).Format("2006-01-02")
	rows, e := sqlite.Query(s.st.DB, `SELECT day,pageviews,visitors FROM analytics_daily WHERE property_id=? AND day>=? ORDER BY day`, p.ID, since)
	if e != nil {
		return Summary{}, e
	}
	out := Summary{Daily: []Daily{}, TopPages: []map[string]any{}, TopSources: []map[string]any{}}
	for _, r := range rows {
		d := Daily{r["day"].Text, r["pageviews"].Int64, r["visitors"].Int64}
		out.Daily = append(out.Daily, d)
		out.Pageviews += d.Pageviews
		out.Visitors += d.Visitors
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

func (s *Service) Fleet(days int) (FleetSummary, error) {
	if days < 1 {
		days = 1
	}
	since := time.Now().UTC().AddDate(0, 0, -days+1).Format("2006-01-02")
	r, e := sqlite.Query(s.st.DB, `SELECT COALESCE(SUM(d.pageviews),0) pageviews,COALESCE(SUM(d.visitors),0) visitors,(SELECT COUNT(*) FROM analytics_properties WHERE enabled=1) sites FROM analytics_daily d WHERE d.day>=?`, since)
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

const Tracker = `(()=>{const s=document.currentScript,k=s&&s.dataset.webfleet;if(!k)return;const u=s.src.replace(/\/wf\.js.*/,"/api/analytics/event"),send=(kind,payload={})=>{const e={key:k,kind,path:location.pathname,referrer:document.referrer,payload:JSON.stringify(payload)};try{navigator.sendBeacon(u,new Blob([JSON.stringify(e)],{type:"application/json"}))}catch(_){}};window.webfleet={track:send};send("pageview");})();`
