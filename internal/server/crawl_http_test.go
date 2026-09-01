package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/web-fleet/webfleet/internal/crawler"
	"github.com/web-fleet/webfleet/internal/netguard"
)

type allowAllResolver struct{}

func (allowAllResolver) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
}

// crawlFixture serves a small multi-page site with a sitemap, plus an optional
// slow page used to hold a crawl in the running state.
func crawlFixture(t *testing.T, slow bool) *httptest.Server {
	t.Helper()
	var base string
	mux := http.NewServeMux()
	mux.HandleFunc("/robots.txt", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "User-agent: *\nSitemap: %s/sitemap.xml\n", base)
	})
	mux.HandleFunc("/sitemap.xml", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprintf(w, "<urlset><url><loc>%s/sm1</loc></url><url><loc>%s/sm2</loc></url></urlset>", base, base)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, `<a href="/a">a</a><a href="/b">b</a>`)
	})
	mux.HandleFunc("/a", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "a")
	})
	mux.HandleFunc("/b", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "b")
	})
	mux.HandleFunc("/sm1", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, "sm1")
	})
	mux.HandleFunc("/sm2", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		if slow {
			time.Sleep(2 * time.Second)
		}
		fmt.Fprint(w, "sm2")
	})
	srv := httptest.NewServer(mux)
	base = srv.URL
	return srv
}

func TestCrawlHTTPLifecycle(t *testing.T) {
	fixture := crawlFixture(t, false)
	defer fixture.Close()
	s, st := newRBACServer(t)
	s.crawler = crawler.NewForTests(st, netguard.Guard{Resolver: allowAllResolver{}, AllowPrivate: true})
	createUser(t, st, "admin@example.com", "secret7", "admin")
	c := loginAs(t, s, "admin@example.com", "secret7")
	siteID := createSiteViaAPI(t, s, c, "site", fixture.URL)

	// Start a crawl: it must return 202 and run in the background.
	rr := doReq(t, s, c, "POST", fmt.Sprintf("/api/sites/%d/crawl", siteID), "")
	if rr.Code != 202 {
		t.Fatalf("crawl start = %d, want 202 (%s)", rr.Code, rr.Body.String())
	}
	// Poll the crawl state until it reaches a terminal state.
	terminal := false
	for i := 0; i < 60; i++ {
		time.Sleep(100 * time.Millisecond)
		g := doReq(t, s, c, "GET", fmt.Sprintf("/api/sites/%d/crawl", siteID), "")
		var out struct {
			Crawl struct {
				Run crawler.Run `json:"run"`
			} `json:"crawl"`
		}
		if err := json.Unmarshal(g.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode crawl state: %v", err)
		}
		if out.Crawl.Run.Status != "running" {
			if out.Crawl.Run.Status != "complete" {
				t.Fatalf("crawl status=%q want complete (%+v)", out.Crawl.Run.Status, out.Crawl.Run)
			}
			if out.Crawl.Run.PagesCrawled == 0 {
				t.Fatalf("crawl completed with 0 pages: %+v", out.Crawl.Run)
			}
			if out.Crawl.Run.PagesDiscovered < out.Crawl.Run.PagesCrawled {
				t.Fatalf("discovered(%d) < crawled(%d)", out.Crawl.Run.PagesDiscovered, out.Crawl.Run.PagesCrawled)
			}
			if out.Crawl.Run.SitemapURLsDiscovered < 2 {
				t.Fatalf("sitemap_urls_discovered=%d want >=2", out.Crawl.Run.SitemapURLsDiscovered)
			}
			terminal = true
			break
		}
	}
	if !terminal {
		t.Fatal("crawl never reached a terminal state")
	}
}

func TestCrawlRejectsConcurrentRun(t *testing.T) {
	fixture := crawlFixture(t, true) // slow page holds the crawl running
	defer fixture.Close()
	s, st := newRBACServer(t)
	s.crawler = crawler.NewForTests(st, netguard.Guard{Resolver: allowAllResolver{}, AllowPrivate: true})
	createUser(t, st, "admin@example.com", "secret7", "admin")
	c := loginAs(t, s, "admin@example.com", "secret7")
	siteID := createSiteViaAPI(t, s, c, "site", fixture.URL)

	first := doReq(t, s, c, "POST", fmt.Sprintf("/api/sites/%d/crawl", siteID), "")
	if first.Code != 202 {
		t.Fatalf("first crawl = %d", first.Code)
	}
	// A second POST while the first is running must be rejected.
	second := doReq(t, s, c, "POST", fmt.Sprintf("/api/sites/%d/crawl", siteID), "")
	if second.Code != 409 {
		t.Fatalf("concurrent crawl = %d, want 409 (%s)", second.Code, second.Body.String())
	}
	// The slow crawl eventually completes; the next crawl is accepted again.
	done := false
	for i := 0; i < 60 && !done; i++ {
		time.Sleep(200 * time.Millisecond)
		g := doReq(t, s, c, "GET", fmt.Sprintf("/api/sites/%d/crawl", siteID), "")
		if strings.Contains(g.Body.String(), `"status":"complete"`) || strings.Contains(g.Body.String(), `"status":"error"`) {
			done = true
		}
	}
	third := doReq(t, s, c, "POST", fmt.Sprintf("/api/sites/%d/crawl", siteID), "")
	if third.Code != 202 {
		t.Fatalf("crawl after completion = %d, want 202", third.Code)
	}
}
