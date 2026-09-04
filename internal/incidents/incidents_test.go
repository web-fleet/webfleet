package incidents

import (
	"github.com/webfleet-cv/webfleet/internal/sites"
	"github.com/webfleet-cv/webfleet/internal/sqlite"
	"github.com/webfleet-cv/webfleet/internal/store"
	"testing"
)

func TestOneIncidentPerFailurePeriod(t *testing.T) {
	st, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	site, e := sites.New(st).Create(1, "x", "https://example.com", 0)
	if e != nil {
		t.Fatal(e)
	}
	svc := New(st)
	if e = svc.Transition(site.ID, "healthy", "warning", "2026-01-01T00:00:00Z"); e != nil {
		t.Fatal(e)
	}
	if e = svc.Transition(site.ID, "warning", "down", "2026-01-01T00:01:00Z"); e != nil {
		t.Fatal(e)
	}
	rows, _ := svc.List(site.ID)
	if len(rows) != 1 || rows[0].State != "down" {
		t.Fatalf("incidents=%+v", rows)
	}
	if e = svc.Transition(site.ID, "down", "healthy", "2026-01-01T00:02:00Z"); e != nil {
		t.Fatal(e)
	}
	rows, _ = svc.List(site.ID)
	if rows[0].ClosedAt == "" {
		t.Fatal("incident not closed")
	}
	d, e := sqlite.Query(st.DB, `SELECT kind FROM alert_deliveries WHERE incident_id=? ORDER BY id`, rows[0].ID)
	if e != nil || len(d) != 2 || d[0]["kind"].Text != "open" || d[1]["kind"].Text != "recovery" {
		t.Fatalf("deliveries=%+v err=%v", d, e)
	}
}
