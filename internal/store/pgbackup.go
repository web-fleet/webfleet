package store

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// PostgreSQL backup/restore uses the provider-native pg_dump/pg_restore tools
// rather than copying database files. The password is passed via PGPASSWORD
// (never on the command line) so credentials are not exposed in process
// listings; missing tools are detected cleanly.

const pgToolTimeout = 10 * time.Minute

type pgEndpoint struct {
	host, port, user, pass, db string
	sslmode                    string
}

func parsePGDSN(dsn string) (pgEndpoint, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return pgEndpoint{}, fmt.Errorf("parse PostgreSQL DSN: %w", err)
	}
	ep := pgEndpoint{
		host: u.Hostname(),
		port: u.Port(),
		user: u.User.Username(),
		db:   strings.TrimPrefix(u.Path, "/"),
	}
	if ep.port == "" {
		ep.port = "5432"
	}
	if ep.user == "" || ep.db == "" {
		return pgEndpoint{}, fmt.Errorf("PostgreSQL DSN must include user and database")
	}
	if p, ok := u.User.Password(); ok {
		ep.pass = p
	}
	if v := u.Query().Get("sslmode"); v != "" {
		ep.sslmode = v
	}
	return ep, nil
}

// PostgresBackup writes a custom-format pg_dump of the configured database to
// dest. The destination file is written through a temporary file and secured to
// 0600.
func PostgresBackup(ctx context.Context, dsn, dest string) error {
	ep, err := parsePGDSN(dsn)
	if err != nil {
		return err
	}
	if _, err := exec.LookPath("pg_dump"); err != nil {
		return fmt.Errorf("pg_dump not found: PostgreSQL backup requires the PostgreSQL client tools")
	}
	abs, err := filepath.Abs(dest)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return err
	}
	tmp := abs + ".tmp"
	defer os.Remove(tmp)
	args := []string{"-h", ep.host, "-p", ep.port, "-U", ep.user, "-d", ep.db, "-F", "c", "-f", tmp, "--no-owner", "--no-privileges"}
	cmd := exec.CommandContext(ctx, "pg_dump", args...)
	cmd.Env = pgToolEnv(ep)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pg_dump failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	if err := os.Chmod(tmp, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, abs); err != nil {
		return err
	}
	return nil
}

// PostgresRestore applies a custom-format pg_dump to the configured database.
// The restore is destructive by contract (the operator requested it).
func PostgresRestore(ctx context.Context, dsn, src string) error {
	ep, err := parsePGDSN(dsn)
	if err != nil {
		return err
	}
	if _, err := exec.LookPath("pg_restore"); err != nil {
		return fmt.Errorf("pg_restore not found: PostgreSQL restore requires the PostgreSQL client tools")
	}
	args := []string{"-h", ep.host, "-p", ep.port, "-U", ep.user, "-d", ep.db, "--clean", "--if-exists", "--no-owner", "--no-privileges", src}
	cmd := exec.CommandContext(ctx, "pg_restore", args...)
	cmd.Env = pgToolEnv(ep)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("pg_restore failed: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func pgToolEnv(ep pgEndpoint) []string {
	env := os.Environ()
	env = append(env, "PGPASSWORD="+ep.pass)
	if ep.sslmode != "" {
		env = append(env, "PGSSLMODE="+ep.sslmode)
	}
	return env
}

// Provider returns the active storage provider for a configured deployment.
func Provider(databaseURL string) string {
	if strings.TrimSpace(databaseURL) != "" {
		return "postgres"
	}
	return "sqlite"
}
