package rbac

import "testing"

func TestPermissions(t *testing.T) {
	if !Can("operator", "site.update") || Can("viewer", "site.update") || !Can("viewer", "site.read") {
		t.Fatal("permission matrix")
	}
}
