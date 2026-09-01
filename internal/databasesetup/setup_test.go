package databasesetup

import (
	"github.com/web-fleet/webfleet/internal/config"
	"github.com/web-fleet/webfleet/internal/store"
	"testing"
)

func TestPersistedPostgresReturnsToAdminCreation(t *testing.T) {
	dir := t.TempDir()
	st, e := store.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	if e = config.SaveDatabaseChoice(dir, config.DatabaseChoice{Provider: "postgres", URL: "postgres://configured"}); e != nil {
		t.Fatal(e)
	}
	x, e := StateFor(st, dir)
	if e != nil {
		t.Fatal(e)
	}
	if x.Selectable || x.Provider != "postgres" {
		t.Fatalf("%+v", x)
	}
}
