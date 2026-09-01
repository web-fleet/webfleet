package incidents

import (
	"errors"
	"github.com/web-fleet/webfleet/internal/sqlite"
	"github.com/web-fleet/webfleet/internal/store"
	"strings"
)

type Service struct{ store *store.Store }
type Incident struct {
	ID             int64  `json:"id"`
	SiteID         int64  `json:"site_id"`
	State          string `json:"state"`
	OpenedAt       string `json:"opened_at"`
	ClosedAt       string `json:"closed_at"`
	AcknowledgedAt string `json:"acknowledged_at"`
	Summary        string `json:"summary"`
}

func New(st *store.Store) *Service { return &Service{store: st} }
func (s *Service) Transition(siteID int64, prev, next, at string) error {
	if next == prev {
		return nil
	}
	if next == "healthy" {
		rows, e := sqlite.Query(s.store.DB, `SELECT id FROM incidents WHERE site_id=? AND closed_at IS NULL ORDER BY id DESC LIMIT 1`, siteID)
		if e != nil {
			return e
		}
		if len(rows) == 0 {
			return nil
		}
		id := rows[0]["id"].Int64
		if e = sqlite.Exec(s.store.DB, `UPDATE incidents SET state='resolved',closed_at=? WHERE id=?`, at, id); e != nil {
			return e
		}
		return s.delivery(id, siteID, "recovery", at)
	}
	if next == "unknown" {
		return nil
	}
	rows, e := sqlite.Query(s.store.DB, `SELECT id FROM incidents WHERE site_id=? AND closed_at IS NULL ORDER BY id DESC LIMIT 1`, siteID)
	if e != nil {
		return e
	}
	if len(rows) > 0 {
		return sqlite.Exec(s.store.DB, `UPDATE incidents SET state=? WHERE id=?`, next, rows[0]["id"].Int64)
	}
	summary := "Website entered " + next + " state"
	r, e := sqlite.Query(s.store.DB, `INSERT INTO incidents(site_id,state,summary,opened_at) VALUES(?,?,?,?) RETURNING id`, siteID, next, summary, at)
	if e != nil {
		return e
	}
	return s.delivery(r[0]["id"].Int64, siteID, "open", at)
}
func (s *Service) delivery(incidentID, siteID int64, kind, at string) error {
	return sqlite.Exec(s.store.DB, `INSERT INTO alert_deliveries(incident_id,site_id,transport,kind,status,created_at) VALUES(?,?,'in_app',?,'delivered',?)`, incidentID, siteID, kind, at)
}
func (s *Service) List(siteID int64) ([]Incident, error) {
	rows, e := sqlite.Query(s.store.DB, `SELECT id,site_id,state,summary,opened_at,COALESCE(closed_at,'') closed_at,COALESCE(acknowledged_at,'') acknowledged_at FROM incidents WHERE site_id=? ORDER BY id DESC LIMIT 100`, siteID)
	if e != nil {
		return nil, e
	}
	out := make([]Incident, 0, len(rows))
	for _, r := range rows {
		out = append(out, rowIncident(r))
	}
	return out, nil
}
func (s *Service) Acknowledge(orgID, id int64, at string) error {
	rows, e := sqlite.Query(s.store.DB, `SELECT i.id FROM incidents i JOIN sites s ON s.id=i.site_id WHERE i.id=? AND s.organization_id=?`, id, orgID)
	if e != nil || len(rows) == 0 {
		return errors.New("incident not found")
	}
	return sqlite.Exec(s.store.DB, `UPDATE incidents SET acknowledged_at=? WHERE id=?`, at, id)
}
func rowIncident(r sqlite.Row) Incident {
	return Incident{ID: r["id"].Int64, SiteID: r["site_id"].Int64, State: r["state"].Text, Summary: r["summary"].Text, OpenedAt: r["opened_at"].Text, ClosedAt: r["closed_at"].Text, AcknowledgedAt: r["acknowledged_at"].Text}
}
func NormalizeState(s string) string { return strings.ToLower(strings.TrimSpace(s)) }
