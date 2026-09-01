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
	return sqlite.Exec(s.st.DB, `INSERT INTO analytics_events(property_id,kind,path,referrer,visitor_key,user_agent_class,payload_json,occurred_at) VALUES(?,?,?,?,?,?,?,?)`, x["id"].Int64, ev.Kind, ev.Path, ev.Referrer, visitor, class, ev.Payload, store.Now())
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

const Tracker = `(()=>{const s=document.currentScript,k=s&&s.dataset.webfleet;if(!k)return;const e={key:k,kind:"pageview",path:location.pathname,referrer:document.referrer,payload:"{}"};try{navigator.sendBeacon(s.src.replace(/\/wf\.js.*/,"/api/analytics/event"),new Blob([JSON.stringify(e)],{type:"application/json"}))}catch(_){}})();`
