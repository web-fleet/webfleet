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

func TestPostgresRestartStateSurvivesReload(t *testing.T) {
	dir := t.TempDir()
	st, e := store.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	if e = config.SaveDatabaseChoice(dir, config.DatabaseChoice{Provider: "postgres", URL: "postgres://configured"}); e != nil {
		t.Fatal(e)
	}
	// Before restart the process still runs SQLite, so the admin form must not
	// appear and the restart-required state must be reported.
	before, e := StateFor(st, dir)
	if e != nil {
		t.Fatal(e)
	}
	if !before.RestartRequired || before.Selectable {
		t.Fatalf("pre-restart state = %+v, want restart_required and not selectable", before)
	}
	// After restart the process runs PostgreSQL: the admin form may appear but
	// the provider remains locked.
	st.DB.Dialect = "postgres"
	after, e := StateFor(st, dir)
	if e != nil {
		t.Fatal(e)
	}
	if after.RestartRequired || after.Selectable || after.Provider != "postgres" {
		t.Fatalf("post-restart state = %+v, want admin creation on locked postgres", after)
	}
}

func TestSQLiteDefaultIsSelectableAndEnvironmentPostgresIsNot(t *testing.T) {
	dir := t.TempDir()
	st, e := store.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	// No choice file: SQLite default, running SQLite, chooser available.
	def, e := StateFor(st, dir)
	if e != nil {
		t.Fatal(e)
	}
	if !def.Selectable || def.Provider != "sqlite" || def.RestartRequired {
		t.Fatalf("sqlite default state = %+v", def)
	}
	// Provisioned via environment: the process runs PostgreSQL even though no
	// choice file exists, so the browser chooser must not offer SQLite.
	st.DB.Dialect = "postgres"
	env, e := StateFor(st, dir)
	if e != nil {
		t.Fatal(e)
	}
	if env.Selectable || env.RestartRequired {
		t.Fatalf("env-postgres state = %+v, want locked and no restart notice", env)
	}
}
