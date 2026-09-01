package rbac

import "testing"

func TestRoleActionMatrix(t *testing.T) {
	cases := []struct {
		role, action string
		want         bool
	}{
		{"viewer", "site.read", true},
		{"viewer", "monitor.read", true},
		{"viewer", "fleet.read", true},
		{"viewer", "analytics.read", true},
		{"viewer", "audit.read", true},
		{"viewer", "site.update", false},
		{"viewer", "site.create", false},
		{"viewer", "monitor.run", false},
		{"viewer", "audit.run", false},
		{"viewer", "membership.update", false},
		{"viewer", "tokens.manage", false},
		{"viewer", "organization.update", false},

		{"operator", "site.read", true},
		{"operator", "site.create", true},
		{"operator", "site.update", true},
		{"operator", "site.archive", true},
		{"operator", "site.delete", false},
		{"operator", "monitor.run", true},
		{"operator", "monitor.update", true},
		{"operator", "audit.run", true},
		{"operator", "crawl.run", true},
		{"operator", "tls.run", true},
		{"operator", "dns.run", true},
		{"operator", "analytics.manage", true},
		{"operator", "deployments.record", true},
		{"operator", "incidents.acknowledge", true},
		{"operator", "fleet.read", true},
		{"operator", "membership.update", false},
		{"operator", "tokens.manage", false},
		{"operator", "webhooks.manage", false},
		{"operator", "maintenance.manage", false},
		{"operator", "organization.update", false},
		{"operator", "organization.read", true},

		{"admin", "organization.read", true},
		{"admin", "organization.update", true},
		{"admin", "membership.update", true},
		{"admin", "tokens.manage", true},
		{"admin", "webhooks.manage", true},
		{"admin", "maintenance.manage", true},
		{"admin", "site.delete", true},
		{"admin", "organization.delete", false},

		{"owner", "organization.delete", true},
		{"owner", "site.delete", true},
		{"owner", "anything", true},
		{"owner", "", true},

		{"", "site.read", false},
		{"superuser", "site.read", false},
	}
	for _, c := range cases {
		if got := Can(c.role, c.action); got != c.want {
			t.Errorf("Can(%q, %q) = %v, want %v", c.role, c.action, got, c.want)
		}
	}
}