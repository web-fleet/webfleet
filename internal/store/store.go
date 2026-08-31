package store

import (
	"fmt"
	"github.com/web-fleet/webfleet/internal/sqlite"
	"path/filepath"
	"time"
)

type Store struct{ DB *sqlite.DB }

const schemaVersion = 1

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
