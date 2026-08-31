package tlshealth

import (
	"context"
	"github.com/web-fleet/webfleet/internal/sites"
	"github.com/web-fleet/webfleet/internal/store"
	"testing"
)

func TestHTTPSScopeIsExplicit(t *testing.T) {
	st, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	site, e := sites.New(st).Create("Plain", "http://example.com", 0)
	if e != nil {
		t.Fatal(e)
	}
	obs, e := New(st).InspectSite(context.Background(), site.ID)
	if e != nil {
		t.Fatal(e)
	}
	if obs.ErrorClass != "not_https" || obs.Valid {
		t.Fatalf("obs=%+v", obs)
	}
	latest, e := New(st).Latest(site.ID)
	if e != nil || latest.ID == 0 {
		t.Fatalf("latest=%+v err=%v", latest, e)
	}
}
