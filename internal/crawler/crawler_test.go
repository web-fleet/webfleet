package crawler

import (
	"context"
	"fmt"
	"github.com/web-fleet/webfleet/internal/netguard"
	"github.com/web-fleet/webfleet/internal/sites"
	"github.com/web-fleet/webfleet/internal/store"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
)

type resolver struct{ ip netip.Addr }

func (r resolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return []netip.Addr{r.ip}, nil
}
func TestCrawlRespectsRobotsAndFindsBrokenLinks(t *testing.T) {
	var base string
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "User-agent: *\nDisallow: /private\nSitemap: "+base+"/sitemap.xml\n")
	})
	mux.HandleFunc("/sitemap.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, "<urlset><url><loc>"+base+"/extra</loc></url></urlset>")
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<a href="/ok">ok</a><a href="/missing">missing</a><a href="/private">private</a>`)
	})
	mux.HandleFunc("/ok", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "ok")
	})
	mux.HandleFunc("/extra", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "extra")
	})
	mux.HandleFunc("/missing", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	mux.HandleFunc("/private", func(w http.ResponseWriter, r *http.Request) { t.Error("robots-disallowed page fetched") })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	base = srv.URL
	u, _ := url.Parse(base)
	st, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	site, e := sites.New(st).Create("x", base, 0)
	if e != nil {
		t.Fatal(e)
	}
	g := netguard.Guard{Resolver: resolver{netip.MustParseAddr(u.Hostname())}, AllowPrivate: true}
	d, e := NewForTests(st, g).CrawlSite(context.Background(), site.ID)
	if e != nil {
		t.Fatal(e)
	}
	if !d.Run.RobotsFound || !d.Run.SitemapFound || d.Run.BrokenInternal < 1 {
		t.Fatalf("run=%+v", d.Run)
	}
	for _, p := range d.Pages {
		if strings.Contains(p.URL, "/private") {
			t.Fatal("private page crawled")
		}
	}
}
