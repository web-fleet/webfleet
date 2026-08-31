package dnsobs

import (
	"context"
	"github.com/web-fleet/webfleet/internal/sites"
	"github.com/web-fleet/webfleet/internal/store"
	"net/netip"
	"testing"
)

type fake struct {
	ips   []netip.Addr
	cname string
	err   error
}

func (f *fake) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return f.ips, f.err
}
func (f *fake) LookupCNAME(context.Context, string) (string, error) { return f.cname, f.err }
func TestChangesOnlyAgainstSuccessfulObservations(t *testing.T) {
	st, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	site, e := sites.New(st).Create("x", "https://example.com", 0)
	if e != nil {
		t.Fatal(e)
	}
	r := &fake{ips: []netip.Addr{netip.MustParseAddr("93.184.216.34")}, cname: "example.com."}
	svc := NewForTests(st, r, false)
	a, e := svc.ObserveSite(context.Background(), site.ID)
	if e != nil || a.Changed {
		t.Fatalf("first=%+v err=%v", a, e)
	}
	r.ips = []netip.Addr{netip.MustParseAddr("8.8.8.8")}
	b, e := svc.ObserveSite(context.Background(), site.ID)
	if e != nil || !b.Changed {
		t.Fatalf("second=%+v err=%v", b, e)
	}
}
