package server_test

import (
	"encoding/json"
	"testing"

	"github.com/web-fleet/webfleet/internal/crawler"
	"github.com/web-fleet/webfleet/internal/dnsobs"
	"github.com/web-fleet/webfleet/internal/fleet"
	"github.com/web-fleet/webfleet/internal/incidents"
	"github.com/web-fleet/webfleet/internal/monitor"
	"github.com/web-fleet/webfleet/internal/sites"
)

func keys(t *testing.T, v any) map[string]json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]json.RawMessage
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func requireKeys(t *testing.T, got map[string]json.RawMessage, want ...string) {
	t.Helper()
	for _, k := range want {
		if _, ok := got[k]; !ok {
			t.Fatalf("missing JSON key %q in %v", k, got)
		}
	}
}

func TestPublicJSONFieldContracts(t *testing.T) {
	requireKeys(t, keys(t, sites.List{}), "sites", "page", "page_size", "total", "pages")
	requireKeys(t, keys(t, fleet.Summary{}), "total", "healthy", "degraded", "warning", "down", "unknown", "attention")
	requireKeys(t, keys(t, incidents.Incident{}), "id", "site_id", "state", "opened_at", "closed_at", "acknowledged_at", "summary")
	requireKeys(t, keys(t, monitor.Result{}), "id", "site_id", "monitor_id")
	requireKeys(t, keys(t, dnsobs.Observation{}), "id", "site_id", "a", "aaaa", "cname", "status", "error", "checked_at")
	requireKeys(t, keys(t, crawler.Run{}), "id", "site_id", "status", "pages_crawled", "internal_links", "external_links", "broken_internal", "broken_external", "new_broken", "robots_found", "sitemap_found", "error", "started_at", "finished_at")
	requireKeys(t, keys(t, crawler.Page{}), "url", "status_code", "depth", "error")
	requireKeys(t, keys(t, crawler.Link{}), "from_url", "to_url", "kind", "status_code", "broken", "error")
}

func TestCrawlRunJSONFieldContracts(t *testing.T) {
	requireKeys(t, keys(t, crawler.Run{}),
		"id", "site_id", "status",
		"pages_crawled", "pages_discovered", "page_limit", "limit_reached",
		"sitemap_urls_discovered", "current_url",
		"internal_links", "external_links", "broken_internal", "broken_external", "new_broken",
		"robots_found", "sitemap_found", "error", "started_at", "finished_at")
}
