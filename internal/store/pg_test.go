package store_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/web-fleet/webfleet/internal/analytics"
	"github.com/web-fleet/webfleet/internal/apitokens"
	"github.com/web-fleet/webfleet/internal/audit"
	"github.com/web-fleet/webfleet/internal/auth"
	"github.com/web-fleet/webfleet/internal/deployments"
	"github.com/web-fleet/webfleet/internal/incidents"
	"github.com/web-fleet/webfleet/internal/maintenance"
	"github.com/web-fleet/webfleet/internal/notifications"
	"github.com/web-fleet/webfleet/internal/sites"
	"github.com/web-fleet/webfleet/internal/sqlite"
	"github.com/web-fleet/webfleet/internal/store"
)

// openFreshPG creates a unique database on a real PostgreSQL server (configured
// via WEBFLEET_TEST_POSTGRES_URL), opens it with the application store (running
// all migrations), and returns the store plus its DSN. The database is dropped
// on cleanup.
func openFreshPG(t *testing.T) (*store.Store, string) {
	t.Helper()
	base := os.Getenv("WEBFLEET_TEST_POSTGRES_URL")
	if base == "" {
		t.Skip("WEBFLEET_TEST_POSTGRES_URL not set; real PostgreSQL parity not run")
	}
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("wf_test_%d", time.Now().UnixNano())
	admin, err := store.OpenPostgres(context.Background(), base)
	if err != nil {
		t.Fatalf("connect admin: %v", err)
	}
	if _, err := admin.DB.ExecContext(context.Background(), "CREATE DATABASE "+name); err != nil {
		admin.Close()
		t.Fatalf("create database: %v", err)
	}
	admin.Close()
	u.Path = "/" + name
	dsn := u.String()
	st, err := store.OpenPostgres(context.Background(), dsn)
	if err != nil {
		t.Fatalf("open fresh postgres: %v", err)
	}
	t.Cleanup(func() {
		st.Close()
		admin, err := store.OpenPostgres(context.Background(), base)
		if err != nil {
			t.Logf("cleanup admin connect: %v", err)
			return
		}
		defer admin.Close()
		_, _ = admin.DB.ExecContext(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	})
	return st, dsn
}

func TestPostgresFreshMigrationAndRestartIdempotent(t *testing.T) {
	st, _ := openFreshPG(t)
	v, err := st.SchemaVersion()
	if err != nil || v < 1 {
		t.Fatalf("fresh postgres schema version = %d err=%v", v, err)
	}
	rows, err := sqlite.Query(st.DB, `SELECT COUNT(*) n FROM _webfleet_schema_migrations`)
	if err != nil || rows[0]["n"].Int64 != int64(v) {
		t.Fatalf("migration history rows = %v err=%v (want %d)", rows, err, v)
	}
}

func TestPostgresRestartIsIdempotent(t *testing.T) {
	st, dsn := openFreshPG(t)
	st.Close()
	st2, err := store.OpenPostgres(context.Background(), dsn)
	if err != nil {
		t.Fatalf("reopen migrated postgres: %v", err)
	}
	defer st2.Close()
	v, err := st2.SchemaVersion()
	if err != nil || v < 1 {
		t.Fatalf("post-restart schema version = %d err=%v", v, err)
	}
}

func TestPostgresRefusesFutureSchema(t *testing.T) {
	st, dsn := openFreshPG(t)
	if _, err := st.DB.ExecContext(context.Background(), `INSERT INTO _webfleet_schema_migrations(version,name,applied_at) VALUES(?,?,?)`, 999, "future", store.Now()); err != nil {
		t.Fatal(err)
	}
	st.Close()
	if _, err := store.OpenPostgres(context.Background(), dsn); err == nil {
		t.Fatal("future schema accepted on postgres")
	}
}

// TestPostgresCoreParity exercises the normal product storage paths against a
// real PostgreSQL server and asserts the same observable results the SQLite
// suite relies on.
func TestPostgresCoreParity(t *testing.T) {
	st, _ := openFreshPG(t)
	ctx := context.Background()

	authSvc := auth.New(st)
	if err := authSvc.CreateAdmin("admin@example.com", "secret7"); err != nil {
		t.Fatal(err)
	}
	tok, sess, err := authSvc.Login("admin@example.com", "secret7")
	if err != nil || tok == "" || sess.CSRF == "" {
		t.Fatalf("login: %v", err)
	}
	if _, err := authSvc.Session(tok); err != nil {
		t.Fatalf("session: %v", err)
	}
	org, err := st.PrimaryOrgID(ctx)
	if err != nil {
		t.Fatal(err)
	}
	mem, err := sqlite.Query(st.DB, `SELECT role FROM organization_memberships WHERE organization_id=? AND user_id=?`, org, sess.UserID)
	if err != nil || len(mem) != 1 || mem[0]["role"].Text != "owner" {
		t.Fatalf("owner membership: %v %v", mem, err)
	}

	sitesSvc := sites.New(st)
	group, err := sitesSvc.CreateGroup(org, "Clients")
	if err != nil {
		t.Fatal(err)
	}
	site, err := sitesSvc.Create(org, "Example", "https://127.0.0.1:1/", group.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := sitesSvc.SetTags(org, site.ID, []string{"prod"}); err != nil {
		t.Fatal(err)
	}
	tags, err := sitesSvc.Tags(org, site.ID)
	if err != nil || len(tags) != 1 || tags[0] != "prod" {
		t.Fatalf("tags: %v %v", tags, err)
	}

	webSvc := notifications.New(st)
	if _, _, err := webSvc.Create(org, "hook", "https://8.8.8.8/hook"); err != nil {
		t.Fatal(err)
	}

	inc := incidents.New(st)
	if err := inc.Transition(site.ID, "unknown", "down", store.Now()); err != nil {
		t.Fatal(err)
	}
	if err := inc.Transition(site.ID, "down", "healthy", store.Now()); err != nil {
		t.Fatal(err)
	}
	il, err := inc.List(site.ID)
	if err != nil || len(il) != 1 || il[0].State != "resolved" {
		t.Fatalf("incidents: %v %v", il, err)
	}
	rows, err := sqlite.Query(st.DB, `SELECT event_kind,status FROM notification_deliveries`)
	if err != nil || len(rows) < 1 {
		t.Fatalf("webhook outbox: %v %v", rows, err)
	}

	auditSvc := audit.NewWithRunner(st, fakeAuditRunner{})
	if _, err := auditSvc.Run(ctx, site.ID); err != nil {
		t.Fatal(err)
	}
	if on, _ := auditSvc.HistoryEnabled(site.ID); on {
		t.Fatal("audit history must default off")
	}

	an := analytics.New(st)
	prop, err := an.Enable(site.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := an.Ingest(analytics.Event{Key: prop.PublicKey, Kind: "pageview", Path: "/"}, "https://127.0.0.1:1", "198.51.100.4", "Mozilla/5.0"); err != nil {
		t.Fatal(err)
	}
	sum, err := an.Summary(site.ID, 7)
	if err != nil || sum.Pageviews != 1 {
		t.Fatalf("analytics summary: %+v %v", sum, err)
	}

	dep := deployments.New(st)
	if _, err := dep.Record(deployments.Event{SiteID: site.ID, Provider: "github", ExternalID: "x1", Revision: "abc"}); err != nil {
		t.Fatal(err)
	}

	tokSvc := apitokens.New(st)
	created, err := tokSvc.Create(sess.UserID, org, "ci", []string{"sites:read"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, scopes, err := tokSvc.Authenticate(created.Token); err != nil || !apitokens.HasScope(scopes, "sites:read") {
		t.Fatalf("token authenticate: %v", err)
	}
	if err := tokSvc.Revoke(created.ID, sess.UserID, org); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := tokSvc.Authenticate(created.Token); err == nil {
		t.Fatal("revoked token authenticated on postgres")
	}

	maint := maintenance.New(st)
	if err := maint.Set(maintenance.Settings{CheckDays: 30, AnalyticsRawDays: 14, AuditDays: 90}); err != nil {
		t.Fatal(err)
	}
	if err := maint.Run(); err != nil {
		t.Fatal(err)
	}

	if ok, err := st.AcquireLease(ctx, "check", site.ID, "worker-a", time.Minute); err != nil || !ok {
		t.Fatalf("lease: ok=%v err=%v", ok, err)
	}
	if ok, _ := st.AcquireLease(ctx, "check", site.ID, "worker-b", time.Minute); ok {
		t.Fatal("second worker claimed the same postgres lease")
	}
}

type fakeAuditRunner struct{}

func (fakeAuditRunner) Run(ctx context.Context, u string) (audit.Result, error) {
	return audit.Result{Status: "complete", Performance: 90, Accessibility: 90, BestPractices: 90, Discoverability: 90}, nil
}

func TestPostgresBackupRestoreRehearsal(t *testing.T) {
	st, dsn := openFreshPG(t)
	ctx := context.Background()
	authSvc := auth.New(st)
	if err := authSvc.CreateAdmin("admin@example.com", "secret7"); err != nil {
		t.Fatal(err)
	}
	site, err := sites.New(st).Create(1, "Example", "https://127.0.0.1:1/", 0)
	if err != nil {
		t.Fatal(err)
	}
	backup := filepath.Join(t.TempDir(), "webfleet.pgdump")
	if err := store.PostgresBackup(ctx, dsn, backup); err != nil {
		t.Fatalf("backup: %v", err)
	}
	if fi, err := os.Stat(backup); err != nil || fi.Size() == 0 {
		t.Fatalf("backup file: %v", err)
	}
	// Destructive change: wipe the schema, then restore.
	st.Close()
	st2, err := store.OpenPostgres(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := st2.DB.ExecContext(ctx, `DROP TABLE sites CASCADE`); err != nil {
		st2.Close()
		t.Fatal(err)
	}
	st2.Close()
	if err := store.PostgresRestore(ctx, dsn, backup); err != nil {
		t.Fatalf("restore: %v", err)
	}
	st3, err := store.OpenPostgres(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer st3.Close()
	rows, err := sqlite.Query(st3.DB, `SELECT COUNT(*) n FROM sites WHERE id=?`, site.ID)
	if err != nil || rows[0]["n"].Int64 != 1 {
		t.Fatalf("restored site missing: %v %v", rows, err)
	}
}

func TestPostgresBackupRestoreBadInputFailsSafely(t *testing.T) {
	_, dsn := openFreshPG(t)
	ctx := context.Background()
	// A bogus source is not a pg_dump archive: restore must fail cleanly.
	if err := store.PostgresRestore(ctx, dsn, "/nonexistent/file"); err == nil {
		t.Fatal("restore from nonexistent file succeeded")
	}
	// A non-archive file must fail cleanly too.
	bad := filepath.Join(t.TempDir(), "not-an-archive")
	if err := os.WriteFile(bad, []byte("this is not a postgres archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := store.PostgresRestore(ctx, dsn, bad); err == nil {
		t.Fatal("restore from a non-archive succeeded")
	}
	// Backup to an unwritable path fails cleanly.
	if err := store.PostgresBackup(ctx, dsn, "/proc/nope/webfleet.pgdump"); err == nil {
		t.Fatal("backup to unwritable path succeeded")
	}
}

func TestProviderDetection(t *testing.T) {
	if store.Provider("") != "sqlite" {
		t.Fatal("empty url must be sqlite")
	}
	if store.Provider("postgres://u@h/db") != "postgres" {
		t.Fatal("postgres url must be postgres")
	}
}
