package deployments

import (
	"context"
	"testing"

	"github.com/webfleet-cv/webfleet/internal/sites"
	"github.com/webfleet-cv/webfleet/internal/sqlite"
	"github.com/webfleet-cv/webfleet/internal/store"
)

func TestDeploymentIdempotency(t *testing.T) {
	st, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	svc := New(st)
	site, e := sites.New(st).Create(1, "x", "https://example.com", 0)
	if e != nil {
		t.Fatal(e)
	}
	// Repeated delivery of the same provider event id updates deterministically.
	a1, e := svc.Record(Event{SiteID: site.ID, Provider: "github", ExternalID: "rev-1", Revision: "abc"})
	if e != nil {
		t.Fatal(e)
	}
	a2, e := svc.Record(Event{SiteID: site.ID, Provider: "github", ExternalID: "rev-1", Revision: "def"})
	if e != nil {
		t.Fatal(e)
	}
	if a1.ID != a2.ID {
		t.Fatalf("same provider event id produced two rows: %d vs %d", a1.ID, a2.ID)
	}
	rows, _ := sqlite.Query(st.DB, `SELECT revision FROM deployment_events WHERE id=?`, a1.ID)
	if rows[0]["revision"].Text != "def" {
		t.Fatalf("dedup did not update: %+v", rows[0])
	}
	// Events without an external id must NOT all collapse on an empty string.
	empty1, e := svc.Record(Event{SiteID: site.ID, Provider: "github", Revision: "one"})
	if e != nil {
		t.Fatal(e)
	}
	empty2, e := svc.Record(Event{SiteID: site.ID, Provider: "github", Revision: "two"})
	if e != nil {
		t.Fatal(e)
	}
	if empty1.ID == empty2.ID {
		t.Fatal("empty external_id events collapsed into one row")
	}
	// Cross-site/provider events never deduplicate together.
	otherSite, e := sites.New(st).Create(1, "y", "https://other.test", 0)
	if e != nil {
		t.Fatal(e)
	}
	o1, e := svc.Record(Event{SiteID: otherSite.ID, Provider: "github", ExternalID: "rev-1", Revision: "x"})
	if e != nil {
		t.Fatal(e)
	}
	o2, e := svc.Record(Event{SiteID: site.ID, Provider: "gitlab", ExternalID: "rev-1", Revision: "x"})
	if e != nil {
		t.Fatal(e)
	}
	if o1.ID == a1.ID || o2.ID == a1.ID {
		t.Fatal("cross-site/provider deduplication occurred")
	}
	_ = context.Background()
}
