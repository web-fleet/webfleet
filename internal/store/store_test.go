package store

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFreshOpenRestartAndPragmas(t *testing.T) {
	ctx := context.Background()
	dir := filepath.Join(t.TempDir(), "data")
	s, err := OpenContext(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	var version, foreignKeys, busyTimeout int
	var journalMode string
	if err := s.DB.QueryRowContext(ctx, `PRAGMA user_version`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRowContext(ctx, `PRAGMA foreign_keys`).Scan(&foreignKeys); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRowContext(ctx, `PRAGMA busy_timeout`).Scan(&busyTimeout); err != nil {
		t.Fatal(err)
	}
	if err := s.DB.QueryRowContext(ctx, `PRAGMA journal_mode`).Scan(&journalMode); err != nil {
		t.Fatal(err)
	}
	if version != schemaVersion || foreignKeys != 1 || busyTimeout != 5000 || strings.ToLower(journalMode) != "wal" {
		t.Fatalf("version=%d foreign_keys=%d busy_timeout=%d journal_mode=%q", version, foreignKeys, busyTimeout, journalMode)
	}
	if _, err := s.DB.ExecContext(ctx, `INSERT INTO app_events(kind,message,created_at) VALUES('restart-probe','persisted',?)`, Now()); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}

	s, err = OpenContext(ctx, dir)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var count int
	if err := s.DB.QueryRowContext(ctx, `SELECT count(*) FROM app_events WHERE kind='restart-probe'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("restart probe count=%d", count)
	}
	if err := s.DB.QueryRowContext(ctx, `SELECT count(*) FROM _webfleet_schema_migrations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != schemaVersion {
		t.Fatalf("migration history rows=%d want=%d", count, schemaVersion)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("data directory permissions=%o want=700", info.Mode().Perm())
	}
}

func TestMigrationsAreIdempotent(t *testing.T) {
	d := t.TempDir()
	s, err := Open(d)
	if err != nil {
		t.Fatal(err)
	}
	v, err := s.SchemaVersion()
	if err != nil || v != schemaVersion {
		t.Fatalf("version=%d err=%v", v, err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(d)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	v, err = s.SchemaVersion()
	if err != nil || v != schemaVersion {
		t.Fatalf("second version=%d err=%v", v, err)
	}
}

func TestRefusesFutureSchema(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "webfleet.db")
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?_pragma=foreign_keys(1)")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`PRAGMA user_version = 999`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = Open(dir)
	if err == nil || !strings.Contains(err.Error(), "999") {
		t.Fatalf("unexpected future-schema error: %v", err)
	}
}

func TestFailedMigrationRollsBack(t *testing.T) {
	ctx := context.Background()
	s, err := OpenContext(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	broken := migration{version: schemaVersion + 1, name: "broken migration", stmts: []string{
		`CREATE TABLE should_not_exist(id INTEGER);`,
		`THIS IS NOT SQL`,
	}}
	if err := s.apply(ctx, broken); err == nil {
		t.Fatal("broken migration succeeded")
	}
	var count int
	if err := s.DB.QueryRow(`SELECT count(*) FROM sqlite_master WHERE type='table' AND name='should_not_exist'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("failed migration left table behind: count=%d", count)
	}
	if err := s.DB.QueryRow(`SELECT count(*) FROM _webfleet_schema_migrations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != schemaVersion {
		t.Fatalf("failed migration changed history: count=%d", count)
	}
}

func TestMigrationHistoryMismatchFailsClosed(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB.Exec(`UPDATE _webfleet_schema_migrations SET name='tampered' WHERE version=3`); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = Open(dir)
	if err == nil || !strings.Contains(err.Error(), "unexpected name") {
		t.Fatalf("tampered migration history accepted: %v", err)
	}
}

func TestBackupRestoreRoundTrip(t *testing.T) {
	dir := t.TempDir()
	st, e := Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = st.DB.Exec(`INSERT INTO app_events(kind,message,created_at) VALUES('probe','yes',?)`, Now()); e != nil {
		t.Fatal(e)
	}
	backup := filepath.Join(t.TempDir(), "backup.db")
	if e = st.Backup(backup); e != nil {
		t.Fatal(e)
	}
	st.Close()
	if e = Restore(dir, backup); e != nil {
		t.Fatal(e)
	}
	st, e = Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	var n int
	if e = st.DB.QueryRow(`SELECT count(*) FROM app_events WHERE kind='probe'`).Scan(&n); e != nil || n != 1 {
		t.Fatalf("probe=%d err=%v", n, e)
	}
}
