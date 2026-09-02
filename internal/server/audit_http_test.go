package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/web-fleet/webfleet/internal/audit"
)

// slowAuditRunner is a test Runner that reports the audit as running for a
// while before completing, so the persisted status can be observed.
type slowAuditRunner struct{ sleep time.Duration }

func (r slowAuditRunner) Run(ctx context.Context, target string) (audit.Result, error) {
	select {
	case <-time.After(r.sleep):
	case <-ctx.Done():
		return audit.Result{Status: "failed", Error: "cancelled"}, ctx.Err()
	}
	return audit.Result{Status: "complete", Performance: 90, Accessibility: 85, BestPractices: 80, Discoverability: 95, URL: target}, nil
}

func TestAuditHTTPLifecycleShowsRunningThenComplete(t *testing.T) {
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("<html><title>x</title></html>")) }))
	defer fixture.Close()
	s, st := newRBACServer(t)
	s.audit = audit.NewWithRunner(st, slowAuditRunner{sleep: 700 * time.Millisecond})
	createUser(t, st, "admin@example.com", "secret7", "admin")
	c := loginAs(t, s, "admin@example.com", "secret7")
	siteID := createSiteViaAPI(t, s, c, "site", fixture.URL)

	rr := doReq(t, s, c, "POST", fmt.Sprintf("/api/sites/%d/audit", siteID), "")
	if rr.Code != 202 {
		t.Fatalf("audit start = %d, want 202 (%s)", rr.Code, rr.Body.String())
	}
	// The persisted running row appears (even though the runner is slow).
	sawRunning := false
	terminal := false
	for i := 0; i < 40 && !terminal; i++ {
		time.Sleep(100 * time.Millisecond)
		g := doReq(t, s, c, "GET", fmt.Sprintf("/api/sites/%d/audit", siteID), "")
		var out struct {
			Runs []audit.Result `json:"runs"`
		}
		if err := json.Unmarshal(g.Body.Bytes(), &out); err != nil {
			t.Fatalf("decode audit: %v", err)
		}
		if len(out.Runs) == 0 {
			continue
		}
		if out.Runs[0].Status == "running" {
			sawRunning = true
		}
		if out.Runs[0].Status == "complete" {
			terminal = true
			if out.Runs[0].Performance != 90 {
				t.Fatalf("complete audit scores wrong: %+v", out.Runs[0])
			}
		}
	}
	if !sawRunning {
		t.Fatal("audit was never observed in the running state")
	}
	if !terminal {
		t.Fatal("audit did not reach complete")
	}
}

func TestAuditRejectsConcurrentRun(t *testing.T) {
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("<html>x</html>")) }))
	defer fixture.Close()
	s, st := newRBACServer(t)
	s.audit = audit.NewWithRunner(st, slowAuditRunner{sleep: 900 * time.Millisecond})
	createUser(t, st, "admin@example.com", "secret7", "admin")
	c := loginAs(t, s, "admin@example.com", "secret7")
	siteID := createSiteViaAPI(t, s, c, "site", fixture.URL)

	first := doReq(t, s, c, "POST", fmt.Sprintf("/api/sites/%d/audit", siteID), "")
	if first.Code != 202 {
		t.Fatalf("first audit = %d", first.Code)
	}
	// Wait until the running row exists so the second POST reliably races it.
	seen := false
	for i := 0; i < 20 && !seen; i++ {
		time.Sleep(50 * time.Millisecond)
		g := doReq(t, s, c, "GET", fmt.Sprintf("/api/sites/%d/audit", siteID), "")
		if strings.Contains(g.Body.String(), `"status":"running"`) {
			seen = true
		}
	}
	if !seen {
		t.Fatal("running row never appeared")
	}
	second := doReq(t, s, c, "POST", fmt.Sprintf("/api/sites/%d/audit", siteID), "")
	if second.Code != 409 {
		t.Fatalf("concurrent audit = %d, want 409 (%s)", second.Code, second.Body.String())
	}
}
