package store

import (
	"fmt"
	"github.com/web-fleet/webfleet/internal/sqlite"
	"path/filepath"
	"time"
)

type Store struct{ DB *sqlite.DB }

const schemaVersion = 3

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
