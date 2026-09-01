package deployments

import (
	"encoding/json"
	"errors"
	"github.com/web-fleet/webfleet/internal/sqlite"
	"github.com/web-fleet/webfleet/internal/store"
	"strings"
	"time"
)

type Event struct {
	ID          int64          `json:"id"`
	SiteID      int64          `json:"site_id"`
	Provider    string         `json:"provider"`
	ExternalID  string         `json:"external_id"`
	Revision    string         `json:"revision"`
	Environment string         `json:"environment"`
	Status      string         `json:"status"`
	URL         string         `json:"url"`
	Metadata    map[string]any `json:"metadata"`
	DeployedAt  string         `json:"deployed_at"`
	ReceivedAt  string         `json:"received_at"`
}
type Service struct{ st *store.Store }

func New(st *store.Store) *Service { return &Service{st: st} }
func (s *Service) Record(e Event) (Event, error) {
	e.Provider = strings.TrimSpace(strings.ToLower(e.Provider))
	if e.Provider == "" || e.SiteID < 1 {
		return e, errors.New("provider and site are required")
	}
	if e.Environment == "" {
		e.Environment = "production"
	}
	if e.Status == "" {
		e.Status = "deployed"
	}
	if e.DeployedAt == "" {
		e.DeployedAt = store.Now()
	} else if _, x := time.Parse(time.RFC3339, e.DeployedAt); x != nil {
		if _, x = time.Parse(time.RFC3339Nano, e.DeployedAt); x != nil {
			return e, errors.New("invalid deployed_at")
		}
	}
	e.ReceivedAt = store.Now()
	m, _ := json.Marshal(e.Metadata)
	r, x := sqlite.Query(s.st.DB, `INSERT INTO deployment_events(site_id,provider,external_id,revision,environment,status,url,metadata_json,deployed_at,received_at) VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(site_id,provider,external_id) WHERE external_id <> '' DO UPDATE SET revision=excluded.revision,status=excluded.status,url=excluded.url,metadata_json=excluded.metadata_json,deployed_at=excluded.deployed_at RETURNING id`, e.SiteID, e.Provider, e.ExternalID, e.Revision, e.Environment, e.Status, e.URL, string(m), e.DeployedAt, e.ReceivedAt)
	if x != nil {
		return e, x
	}
	e.ID = r[0]["id"].Int64
	return e, nil
}
func (s *Service) History(siteID int64) ([]Event, error) {
	r, e := sqlite.Query(s.st.DB, `SELECT * FROM deployment_events WHERE site_id=? ORDER BY deployed_at DESC LIMIT 100`, siteID)
	if e != nil {
		return nil, e
	}
	out := []Event{}
	for _, x := range r {
		v := Event{ID: x["id"].Int64, SiteID: siteID, Provider: x["provider"].Text, ExternalID: x["external_id"].Text, Revision: x["revision"].Text, Environment: x["environment"].Text, Status: x["status"].Text, URL: x["url"].Text, DeployedAt: x["deployed_at"].Text, ReceivedAt: x["received_at"].Text}
		_ = json.Unmarshal([]byte(x["metadata_json"].Text), &v.Metadata)
		out = append(out, v)
	}
	return out, nil
}

type Correlation struct {
	Deployment              Event `json:"deployment"`
	ChecksAfter             int64 `json:"checks_after"`
	FailuresAfter           int64 `json:"failures_after"`
	MedianLatencyAfter      int64 `json:"median_latency_after_ms"`
	AnalyticsPageviewsAfter int64 `json:"analytics_pageviews_after"`
}

func (s *Service) Correlate(siteID int64) (*Correlation, error) {
	h, e := s.History(siteID)
	if e != nil || len(h) == 0 {
		return nil, e
	}
	d := h[0]
	r, e := sqlite.Query(s.st.DB, `SELECT COUNT(*) n,SUM(CASE WHEN ok=0 THEN 1 ELSE 0 END) failures,COALESCE(AVG(latency_ms),0) latency FROM check_results WHERE site_id=? AND checked_at>=?`, siteID, d.DeployedAt)
	if e != nil {
		return nil, e
	}
	x := r[0]
	a, _ := sqlite.Query(s.st.DB, `SELECT COUNT(*) n FROM analytics_events ae JOIN analytics_properties ap ON ap.id=ae.property_id WHERE ap.site_id=? AND ae.occurred_at>=? AND ae.kind='pageview'`, siteID, d.DeployedAt)
	views := int64(0)
	if len(a) > 0 {
		views = a[0]["n"].Int64
	}
	return &Correlation{d, x["n"].Int64, x["failures"].Int64, x["latency"].Int64, views}, nil
}
