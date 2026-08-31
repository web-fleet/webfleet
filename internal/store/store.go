package store

import (
	"fmt"
	"github.com/web-fleet/webfleet/internal/sqlite"
	"path/filepath"
	"time"
)

type Store struct{ DB *sqlite.DB }

const schemaVersion = 10

func Open(dataDir string) (*Store, error) {
	db, err := sqlite.Open(filepath.Join(dataDir, "webfleet.db"))
	if err != nil {
		return nil, err
	}
	s := &Store{DB: db}
	if err = s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}
func (s *Store) Close() error { return s.DB.Close() }
func (s *Store) migrate() error {
	if err := s.DB.Exec(`CREATE TABLE IF NOT EXISTS schema_meta(version INTEGER NOT NULL);`); err != nil {
		return err
	}
	rows, err := s.DB.Query(`SELECT version FROM schema_meta LIMIT 1`)
	if err != nil {
		return err
	}
	if len(rows) == 0 {
		if err = s.DB.Exec(`INSERT INTO schema_meta(version) VALUES(0)`); err != nil {
			return err
		}
		rows = []sqlite.Row{{"version": {Int64: 0}}}
	}
	v := int(rows[0]["version"].Int64)
	if v > schemaVersion {
		return fmt.Errorf("database schema %d is newer than supported %d", v, schemaVersion)
	}
	if v < 1 {
		stmts := []string{
			`CREATE TABLE organizations(id INTEGER PRIMARY KEY, name TEXT NOT NULL, created_at TEXT NOT NULL);`,
			`INSERT INTO organizations(name,created_at) VALUES('My Web Fleet', datetime('now'));`,
			`CREATE TABLE app_events(id INTEGER PRIMARY KEY, kind TEXT NOT NULL, message TEXT NOT NULL, created_at TEXT NOT NULL);`,
		}
		for _, q := range stmts {
			if err = s.DB.Exec(q); err != nil {
				return err
			}
		}
		if err = s.DB.Exec(`UPDATE schema_meta SET version=1`); err != nil {
			return err
		}
		v = 1
	}
	if v < 2 {
		stmts := []string{
			`CREATE TABLE users(id INTEGER PRIMARY KEY, email TEXT NOT NULL UNIQUE, password_hash TEXT NOT NULL, role TEXT NOT NULL, created_at TEXT NOT NULL);`,
			`CREATE TABLE sessions(id INTEGER PRIMARY KEY, token_hash BLOB NOT NULL UNIQUE, user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE, csrf_token TEXT NOT NULL, expires_at TEXT NOT NULL, created_at TEXT NOT NULL);`,
			`CREATE TABLE audit_events(id INTEGER PRIMARY KEY, kind TEXT NOT NULL, detail TEXT NOT NULL DEFAULT '', created_at TEXT NOT NULL);`,
		}
		for _, q := range stmts {
			if err = s.DB.Exec(q); err != nil {
				return err
			}
		}
		if err = s.DB.Exec(`UPDATE schema_meta SET version=2`); err != nil {
			return err
		}
	}

	if v < 3 {
		stmts := []string{
			`CREATE TABLE groups(id INTEGER PRIMARY KEY, organization_id INTEGER NOT NULL DEFAULT 1 REFERENCES organizations(id), name TEXT NOT NULL, created_at TEXT NOT NULL, UNIQUE(organization_id,name));`,
			`CREATE TABLE sites(id INTEGER PRIMARY KEY, organization_id INTEGER NOT NULL REFERENCES organizations(id), name TEXT NOT NULL, primary_url TEXT NOT NULL, enabled INTEGER NOT NULL DEFAULT 1, group_id INTEGER REFERENCES groups(id) ON DELETE SET NULL, archived_at TEXT, created_at TEXT NOT NULL, updated_at TEXT NOT NULL);`,
			`CREATE INDEX sites_name_idx ON sites(name);`,
			`CREATE INDEX sites_group_idx ON sites(group_id);`,
		}
		for _, q := range stmts {
			if err = s.DB.Exec(q); err != nil {
				return err
			}
		}
		if err = s.DB.Exec(`UPDATE schema_meta SET version=3`); err != nil {
			return err
		}
	}

	if v < 4 {
		stmts := []string{
			`CREATE TABLE monitors(id INTEGER PRIMARY KEY, site_id INTEGER NOT NULL REFERENCES sites(id) ON DELETE CASCADE, kind TEXT NOT NULL DEFAULT 'http', timeout_ms INTEGER NOT NULL DEFAULT 10000, expected_min INTEGER NOT NULL DEFAULT 200, expected_max INTEGER NOT NULL DEFAULT 399, created_at TEXT NOT NULL);`,
			`CREATE UNIQUE INDEX monitors_site_kind_idx ON monitors(site_id,kind);`,
			`INSERT INTO monitors(site_id,kind,timeout_ms,expected_min,expected_max,created_at) SELECT id,'http',10000,200,399,datetime('now') FROM sites;`,
			`CREATE TABLE check_results(id INTEGER PRIMARY KEY, site_id INTEGER NOT NULL REFERENCES sites(id) ON DELETE CASCADE, monitor_id INTEGER NOT NULL REFERENCES monitors(id) ON DELETE CASCADE, ok INTEGER NOT NULL, status_code INTEGER NOT NULL DEFAULT 0, latency_ms INTEGER NOT NULL DEFAULT 0, final_url TEXT NOT NULL DEFAULT '', error_class TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '', checked_at TEXT NOT NULL);`,
			`CREATE INDEX check_results_site_time_idx ON check_results(site_id,checked_at DESC);`,
		}
		for _, q := range stmts {
			if err = s.DB.Exec(q); err != nil {
				return err
			}
		}
		if err = s.DB.Exec(`UPDATE schema_meta SET version=4`); err != nil {
			return err
		}
	}

	if v < 5 {
		stmts := []string{
			`CREATE TABLE site_health(site_id INTEGER PRIMARY KEY REFERENCES sites(id) ON DELETE CASCADE, state TEXT NOT NULL DEFAULT 'unknown', consecutive_failures INTEGER NOT NULL DEFAULT 0, last_check_id INTEGER REFERENCES check_results(id) ON DELETE SET NULL, last_change_at TEXT NOT NULL, last_success_at TEXT, last_failure_at TEXT);`,
			`INSERT INTO site_health(site_id,state,last_change_at) SELECT id,'unknown',datetime('now') FROM sites;`,
		}
		for _, q := range stmts {
			if err = s.DB.Exec(q); err != nil {
				return err
			}
		}
		if err = s.DB.Exec(`UPDATE schema_meta SET version=5`); err != nil {
			return err
		}
	}

	if v < 6 {
		stmts := []string{
			`CREATE TABLE incidents(id INTEGER PRIMARY KEY, site_id INTEGER NOT NULL REFERENCES sites(id) ON DELETE CASCADE, state TEXT NOT NULL, summary TEXT NOT NULL, opened_at TEXT NOT NULL, closed_at TEXT, acknowledged_at TEXT);`,
			`CREATE INDEX incidents_site_time_idx ON incidents(site_id,opened_at DESC);`,
			`CREATE TABLE alert_policies(id INTEGER PRIMARY KEY, organization_id INTEGER NOT NULL DEFAULT 1 REFERENCES organizations(id), name TEXT NOT NULL, transport TEXT NOT NULL, enabled INTEGER NOT NULL DEFAULT 1, created_at TEXT NOT NULL);`,
			`INSERT INTO alert_policies(organization_id,name,transport,enabled,created_at) VALUES(1,'Dashboard alerts','in_app',1,datetime('now'));`,
			`CREATE TABLE alert_deliveries(id INTEGER PRIMARY KEY, incident_id INTEGER NOT NULL REFERENCES incidents(id) ON DELETE CASCADE, site_id INTEGER NOT NULL REFERENCES sites(id) ON DELETE CASCADE, transport TEXT NOT NULL, kind TEXT NOT NULL, status TEXT NOT NULL, created_at TEXT NOT NULL);`,
		}
		for _, q := range stmts {
			if err = s.DB.Exec(q); err != nil {
				return err
			}
		}
		if err = s.DB.Exec(`UPDATE schema_meta SET version=6`); err != nil {
			return err
		}
	}

	if v < 7 {
		if err = s.DB.Exec(`CREATE TABLE tls_observations(id INTEGER PRIMARY KEY, site_id INTEGER NOT NULL REFERENCES sites(id) ON DELETE CASCADE, valid INTEGER NOT NULL, hostname_valid INTEGER NOT NULL, issuer TEXT NOT NULL DEFAULT '', subject TEXT NOT NULL DEFAULT '', serial TEXT NOT NULL DEFAULT '', not_before TEXT NOT NULL DEFAULT '', not_after TEXT NOT NULL DEFAULT '', days_remaining INTEGER NOT NULL DEFAULT 0, error_class TEXT NOT NULL DEFAULT '', error TEXT NOT NULL DEFAULT '', checked_at TEXT NOT NULL);`); err != nil {
			return err
		}
		if err = s.DB.Exec(`CREATE INDEX tls_observations_site_idx ON tls_observations(site_id,id DESC);`); err != nil {
			return err
		}
		if err = s.DB.Exec(`UPDATE schema_meta SET version=7`); err != nil {
			return err
		}
	}

	if v < 8 {
		if err = s.DB.Exec(`CREATE TABLE dns_observations(id INTEGER PRIMARY KEY, site_id INTEGER NOT NULL REFERENCES sites(id) ON DELETE CASCADE, a_records TEXT NOT NULL DEFAULT '', aaaa_records TEXT NOT NULL DEFAULT '', cname TEXT NOT NULL DEFAULT '', status TEXT NOT NULL, changed INTEGER NOT NULL DEFAULT 0, error TEXT NOT NULL DEFAULT '', checked_at TEXT NOT NULL);`); err != nil {
			return err
		}
		if err = s.DB.Exec(`CREATE INDEX dns_observations_site_idx ON dns_observations(site_id,id DESC);`); err != nil {
			return err
		}
		if err = s.DB.Exec(`UPDATE schema_meta SET version=8`); err != nil {
			return err
		}
	}

	if v < 9 {
		stmts := []string{
			`CREATE TABLE header_expectations(id INTEGER PRIMARY KEY, site_id INTEGER NOT NULL REFERENCES sites(id) ON DELETE CASCADE, name TEXT NOT NULL, required INTEGER NOT NULL DEFAULT 1, UNIQUE(site_id,name));`,
			`INSERT INTO header_expectations(site_id,name,required) SELECT id,'Content-Security-Policy',1 FROM sites;`,
			`INSERT INTO header_expectations(site_id,name,required) SELECT id,'Strict-Transport-Security',1 FROM sites;`,
			`INSERT INTO header_expectations(site_id,name,required) SELECT id,'X-Content-Type-Options',1 FROM sites;`,
			`INSERT INTO header_expectations(site_id,name,required) SELECT id,'Referrer-Policy',1 FROM sites;`,
			`CREATE TABLE http_observations(id INTEGER PRIMARY KEY, site_id INTEGER NOT NULL REFERENCES sites(id) ON DELETE CASCADE, check_id INTEGER NOT NULL REFERENCES check_results(id) ON DELETE CASCADE, redirect_chain TEXT NOT NULL DEFAULT '[]', headers_json TEXT NOT NULL DEFAULT '{}', missing_headers TEXT NOT NULL DEFAULT '', changed INTEGER NOT NULL DEFAULT 0, observed_at TEXT NOT NULL);`,
			`CREATE INDEX http_observations_site_idx ON http_observations(site_id,id DESC);`,
		}
		for _, q := range stmts {
			if err = s.DB.Exec(q); err != nil {
				return err
			}
		}
		if err = s.DB.Exec(`UPDATE schema_meta SET version=9`); err != nil {
			return err
		}
	}

	if v < 10 {
		stmts := []string{
			`CREATE TABLE crawl_runs(id INTEGER PRIMARY KEY, site_id INTEGER NOT NULL REFERENCES sites(id) ON DELETE CASCADE, status TEXT NOT NULL, pages_crawled INTEGER NOT NULL DEFAULT 0, internal_links INTEGER NOT NULL DEFAULT 0, external_links INTEGER NOT NULL DEFAULT 0, broken_internal INTEGER NOT NULL DEFAULT 0, broken_external INTEGER NOT NULL DEFAULT 0, new_broken INTEGER NOT NULL DEFAULT 0, robots_found INTEGER NOT NULL DEFAULT 0, sitemap_found INTEGER NOT NULL DEFAULT 0, error TEXT NOT NULL DEFAULT '', started_at TEXT NOT NULL, finished_at TEXT);`,
			`CREATE INDEX crawl_runs_site_idx ON crawl_runs(site_id,id DESC);`,
			`CREATE TABLE crawl_pages(id INTEGER PRIMARY KEY, run_id INTEGER NOT NULL REFERENCES crawl_runs(id) ON DELETE CASCADE, site_id INTEGER NOT NULL REFERENCES sites(id) ON DELETE CASCADE, url TEXT NOT NULL, status_code INTEGER NOT NULL DEFAULT 0, depth INTEGER NOT NULL DEFAULT 0, error TEXT NOT NULL DEFAULT '');`,
			`CREATE INDEX crawl_pages_run_idx ON crawl_pages(run_id);`,
			`CREATE TABLE crawl_links(id INTEGER PRIMARY KEY, run_id INTEGER NOT NULL REFERENCES crawl_runs(id) ON DELETE CASCADE, site_id INTEGER NOT NULL REFERENCES sites(id) ON DELETE CASCADE, from_url TEXT NOT NULL, to_url TEXT NOT NULL, kind TEXT NOT NULL, status_code INTEGER NOT NULL DEFAULT 0, broken INTEGER NOT NULL DEFAULT 0, error TEXT NOT NULL DEFAULT '');`,
			`CREATE INDEX crawl_links_run_idx ON crawl_links(run_id);`,
		}
		for _, q := range stmts {
			if err = s.DB.Exec(q); err != nil {
				return err
			}
		}
		if err = s.DB.Exec(`UPDATE schema_meta SET version=10`); err != nil {
			return err
		}
	}
	return nil
}
func (s *Store) SchemaVersion() (int, error) {
	r, e := s.DB.Query(`SELECT version FROM schema_meta LIMIT 1`)
	if e != nil || len(r) == 0 {
		return 0, e
	}
	return int(r[0]["version"].Int64), nil
}
func Now() string { return time.Now().UTC().Format(time.RFC3339Nano) }
