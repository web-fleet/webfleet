package incidents

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/web-fleet/webfleet/internal/database"
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

// Transition applies a health state change to incidents and, in the same
// transaction, writes webhook outbox rows for enabled destinations. The
// incident state and its attributable webhook work commit atomically and never
// depend on the external webhook succeeding; a background delivery worker
// performs the HTTP afterwards.
func (s *Service) Transition(siteID int64, prev, next, at string) error {
	if next == prev || next == "unknown" {
		return nil
	}
	tx, e := s.store.DB.BeginTx(context.Background(), nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	openID := func() (int64, error) {
		r, e := sqlite.Query(tx, `SELECT id FROM incidents WHERE site_id=? AND closed_at IS NULL ORDER BY id DESC LIMIT 1`, siteID)
		if e != nil {
			return 0, e
		}
		if len(r) == 0 {
			return 0, nil
		}
		return r[0]["id"].Int64, nil
	}
	if next == "healthy" {
		id, e := openID()
		if e != nil {
			return e
		}
		if id == 0 {
			return tx.Commit()
		}
		if e = sqlite.Exec(tx, `UPDATE incidents SET state='resolved',closed_at=? WHERE id=?`, at, id); e != nil {
			return e
		}
		if e = recordDelivery(tx, id, siteID, "recovery", at); e != nil {
			return e
		}
		if e = enqueueWebhooks(tx, siteID, id, "incident.recover", "resolved", "Website recovered", at); e != nil {
			return e
		}
		return tx.Commit()
	}
	id, e := openID()
	if e != nil {
		return e
	}
	if id > 0 {
		// Escalation within an open incident: update state, do not re-notify.
		if e = sqlite.Exec(tx, `UPDATE incidents SET state=? WHERE id=?`, next, id); e != nil {
			return e
		}
		return tx.Commit()
	}
	r, e := sqlite.Query(tx, `INSERT INTO incidents(site_id,state,summary,opened_at) VALUES(?,?,?,?) RETURNING id`, siteID, next, "Website entered "+next+" state", at)
	if e != nil {
		return e
	}
	id = r[0]["id"].Int64
	if e = recordDelivery(tx, id, siteID, "open", at); e != nil {
		return e
	}
	if e = enqueueWebhooks(tx, siteID, id, "incident.open", next, "Website entered "+next+" state", at); e != nil {
		return e
	}
	return tx.Commit()
}

// recordDelivery keeps the in-app alert history in the same transaction as the
// incident state change.
func recordDelivery(tx *database.Tx, incidentID, siteID int64, kind, at string) error {
	return sqlite.Exec(tx, `INSERT INTO alert_deliveries(incident_id,site_id,transport,kind,status,created_at) VALUES(?,?,'in_app',?,'delivered',?)`, incidentID, siteID, kind, at)
}

// enqueueWebhooks writes a pending delivery row per enabled webhook belonging
// to the site's own organization, derived from the persisted site within the
// same transaction (never from an untrusted caller). This keeps an incident
// event inside its tenant boundary, so an incident for organization A can never
// queue a payload to organization B's webhooks.
func enqueueWebhooks(tx *database.Tx, siteID, incidentID int64, kind, state, summary, at string) error {
	payload, _ := json.Marshal(map[string]any{
		"site_id":     siteID,
		"incident_id": incidentID,
		"state":       state,
		"summary":     summary,
		"at":          at,
	})
	return sqlite.Exec(tx, `INSERT INTO notification_deliveries(webhook_id,event_kind,status,payload_json,created_at) SELECT w.id,?, 'pending',?,? FROM notification_webhooks w JOIN sites s ON s.id=? WHERE w.enabled=1 AND w.organization_id=s.organization_id`, kind, string(payload), at, siteID)
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
