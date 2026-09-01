package server

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/web-fleet/webfleet/internal/rbac"
)

// knownActions is the authoritative action vocabulary. Adding a route with a
// new action here is the only way to extend the permission matrix; the test
// below rejects unknown actions.
var knownActions = map[string]bool{
	"organization.read": true, "organization.update": true, "organization.delete": true,
	"membership.update": true,
	"tokens.manage":     true,
	"webhooks.read":     true, "webhooks.manage": true,
	"maintenance.read": true, "maintenance.manage": true,
	"fleet.read":        true,
	"site.read":         true, "site.create": true, "site.update": true, "site.archive": true, "site.delete": true,
	"monitor.read": true, "monitor.run": true, "monitor.update": true,
	"audit.read": true, "audit.run": true, "audit.configure": true,
	"crawl.read": true, "crawl.run": true,
	"tls.read":   true, "tls.run": true,
	"dns.read":   true, "dns.run": true,
	"analytics.read": true, "analytics.manage": true,
	"incidents.read": true, "incidents.acknowledge": true,
	"deployments.read": true, "deployments.record": true,
	"session": true,
}

// minRoleFor returns the least-privileged role that can perform the action.
func minRoleFor(action string) string {
	if action == "session" {
		return "viewer"
	}
	for _, role := range []string{"viewer", "operator", "admin", "owner"} {
		if rbac.Can(role, action) {
			return role
		}
	}
	return ""
}

// docRoute mirrors docs/hardening/route-inventory.json entries.
type docRoute struct {
	Method     string `json:"method"`
	Path       string `json:"path"`
	Action     string `json:"action"`
	CSRF       bool   `json:"csrf"`
	SiteScoped bool   `json:"siteScoped"`
	MinRole    string `json:"minRole"`
}
type docInventory struct {
	Actions []string   `json:"actions"`
	Routes  []docRoute `json:"routes"`
}

func TestRouteContractIsCompleteAndConsistent(t *testing.T) {
	if len(routeContract) == 0 {
		t.Fatal("route contract is empty; routes() must populate it")
	}
	seen := map[string]bool{}
	for _, rt := range routeContract {
		key := rt.Method + " " + rt.Path
		if seen[key] {
			t.Fatalf("duplicate route %s", key)
		}
		seen[key] = true
		if rt.Action == "" {
			continue
		}
		if !knownActions[rt.Action] {
			t.Fatalf("route %s uses unknown action %q; extend knownActions deliberately", key, rt.Action)
		}
		// CSRF posture: every state-changing authenticated route must be
		// CSRF-protected; GET routes are read-only and exempt.
		if rt.Method != "GET" && !rt.CSRF {
			t.Fatalf("authenticated non-GET route %s must require CSRF", key)
		}
		if rt.Method == "GET" && rt.CSRF {
			t.Fatalf("GET route %s must not require CSRF", key)
		}
	}
}

func TestRouteInventoryMatchesDocumentedInventory(t *testing.T) {
	b, err := os.ReadFile("../../docs/hardening/route-inventory.json")
	if err != nil {
		t.Fatalf("read inventory: %v", err)
	}
	var doc docInventory
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatalf("parse inventory: %v", err)
	}
	for _, a := range doc.Actions {
		if a == "session" {
			continue
		}
		if !knownActions[a] {
			t.Fatalf("documented action %q is not in the shipped vocabulary", a)
		}
	}
	docByKey := map[string]docRoute{}
	for _, d := range doc.Routes {
		key := d.Method + " " + d.Path
		if _, dup := docByKey[key]; dup {
			t.Fatalf("documented route duplicated: %s", key)
		}
		docByKey[key] = d
	}
	if len(docByKey) != len(routeContract) {
		t.Fatalf("inventory has %d routes, route table has %d", len(docByKey), len(routeContract))
	}
	for _, rt := range routeContract {
		key := rt.Method + " " + rt.Path
		d, ok := docByKey[key]
		if !ok {
			t.Fatalf("route %s is registered but missing from route-inventory.json", key)
		}
		action := d.Action
		if action == "" {
			action = rt.Action
		}
		if action != rt.Action {
			t.Fatalf("route %s action mismatch: doc=%q table=%q", key, action, rt.Action)
		}
		if d.CSRF != rt.CSRF {
			t.Fatalf("route %s csrf mismatch: doc=%v table=%v", key, d.CSRF, rt.CSRF)
		}
		wantScoped := strings.HasPrefix(rt.Path, "/api/sites/{id}")
		if d.SiteScoped != wantScoped {
			t.Fatalf("route %s siteScoped mismatch: doc=%v derived=%v", key, d.SiteScoped, wantScoped)
		}
		wantMin := ""
		if rt.Action != "" {
			wantMin = minRoleFor(rt.Action)
		}
		if d.MinRole != wantMin {
			t.Fatalf("route %s minRole mismatch: doc=%q derived=%q", key, d.MinRole, wantMin)
		}
		delete(docByKey, key)
	}
	for key := range docByKey {
		t.Fatalf("route %s documented but not registered", key)
	}
}

func TestEveryAuthenticatedActionHasAMinimumRole(t *testing.T) {
	for _, rt := range routeContract {
		if rt.Action == "" {
			continue
		}
		if minRoleFor(rt.Action) == "" {
			t.Fatalf("action %q grants no role", rt.Action)
		}
	}
}