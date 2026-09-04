package databasesetup

import (
	"context"
	"errors"
	"github.com/webfleet-cv/webfleet/internal/config"
	"github.com/webfleet-cv/webfleet/internal/store"
	"strings"
	"time"
)

type State struct {
	Selectable      bool   `json:"selectable"`
	Provider        string `json:"provider"`
	RestartRequired bool   `json:"restart_required"`
}

func StateFor(st *store.Store, dataDir string) (State, error) {
	var n int
	if e := st.DB.QueryRow(`SELECT COUNT(*) FROM users`).Scan(&n); e != nil {
		return State{}, e
	}
	c, e := config.LoadDatabaseChoice(dataDir)
	if e != nil {
		return State{}, e
	}
	chosen := c.Provider
	if chosen == "" {
		chosen = "sqlite"
	}
	// The running store reveals whether the operator has already restarted onto
	// the chosen provider, which is how restart-required state survives reloads.
	running := "sqlite"
	if st.Dialect() == "postgres" {
		running = "postgres"
	}
	// The reported provider reflects what the deployment actually runs on: an
	// environment-provisioned PostgreSQL deployment (no choice file) is
	// reported as postgres, not the sqlite default.
	provider := chosen
	if running == "postgres" {
		provider = "postgres"
	}
	state := State{Provider: provider}
	if n == 0 {
		switch chosen {
		case "postgres":
			// Selection is always locked for postgres. Before restart the
			// operator must see the restart-required state; after restart the
			// administrator form appears. Both cases keep the admin form hidden
			// until the process actually runs on the chosen provider.
			state.Selectable = false
		default:
			// SQLite is the default choice and needs no restart. When the
			// process is already running on PostgreSQL (provisioned by
			// environment), the chooser must not allow switching back to SQLite.
			state.Selectable = running == "sqlite"
		}
	}
	// A committed PostgreSQL choice that the running process has not yet
	// restarted onto is ALWAYS a pending database transition, independent of
	// whether an administrator already exists in the running database. The UI
	// must therefore never offer login or administrator actions against the old
	// database while the switch is pending: it shows the restart notice only.
	state.RestartRequired = chosen == "postgres" && running != "postgres"
	return state, nil
}
func Apply(ctx context.Context, st *store.Store, dataDir, provider, url string) (State, error) {
	state, e := StateFor(st, dataDir)
	if e != nil {
		return state, e
	}
	if !state.Selectable {
		return state, errors.New("database provider is locked after administrator creation")
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	switch provider {
	case "sqlite":
		if e = config.SaveDatabaseChoice(dataDir, config.DatabaseChoice{Provider: "sqlite"}); e != nil {
			return state, e
		}
		return State{Selectable: true, Provider: "sqlite"}, nil
	case "postgres":
		if strings.TrimSpace(url) == "" {
			return state, errors.New("PostgreSQL URL is required")
		}
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		pg, e := store.OpenPostgres(c, url)
		if e != nil {
			return state, e
		}
		pg.Close()
		if e = config.SaveDatabaseChoice(dataDir, config.DatabaseChoice{Provider: "postgres", URL: url}); e != nil {
			return state, e
		}
		return State{Selectable: false, Provider: "postgres", RestartRequired: true}, nil
	default:
		return state, errors.New("unsupported database provider")
	}
}
