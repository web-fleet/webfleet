package server

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"

	"github.com/web-fleet/webfleet/internal/config"
	"github.com/web-fleet/webfleet/internal/store"
)

// openFreshPGForServer creates a unique database on a real PostgreSQL server
// (configured via WEBFLEET_TEST_POSTGRES_URL) and returns its DSN. The database
// is dropped on cleanup.
func openFreshPGForServer(t *testing.T) string {
	t.Helper()
	base := os.Getenv("WEBFLEET_TEST_POSTGRES_URL")
	if base == "" {
		t.Skip("WEBFLEET_TEST_POSTGRES_URL not set; real PostgreSQL lifecycle not run")
	}
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	name := fmt.Sprintf("wf_srv_%d", time.Now().UnixNano())
	admin, err := store.OpenPostgres(context.Background(), base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := admin.DB.ExecContext(context.Background(), "CREATE DATABASE "+name); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	admin.Close()
	u.Path = "/" + name
	dsn := u.String()
	t.Cleanup(func() {
		if a, err := store.OpenPostgres(context.Background(), base); err == nil {
			defer a.Close()
			_, _ = a.DB.ExecContext(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		}
	})
	return dsn
}

// startRealServer starts the application through the same initialization path
// as cmd/webfleet: config.Load() -> store.Open -> New -> a real TCP listener
// (not an httptest fixture), against real on-disk/in-server storage.
func startRealServer(t *testing.T, dataDir, pgURL string) (string, func()) {
	t.Helper()
	if pgURL == "" {
		t.Setenv("WEBFLEET_DATABASE_URL", "")
		t.Setenv("WEBFLEET_DATA_DIR", dataDir)
	} else {
		t.Setenv("WEBFLEET_DATA_DIR", t.TempDir())
		t.Setenv("WEBFLEET_DATABASE_URL", pgURL)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	var st *store.Store
	if cfg.DatabaseURL != "" {
		st, err = store.OpenPostgres(context.Background(), cfg.DatabaseURL)
	} else {
		st, err = store.Open(cfg.DataDir)
	}
	if err != nil {
		t.Fatal(err)
	}
	s := New(cfg, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: s.Handler()}
	go func() { _ = srv.Serve(ln) }()
	stop := func() { _ = srv.Close(); _ = ln.Close(); _ = st.Close() }
	t.Cleanup(stop)
	return "http://" + ln.Addr().String(), stop
}

// a11yEvalTrue evaluates an expression and returns its value wrapped so
// chromedp never has to encode a bare undefined result.
func a11yEvalTrue(ctx context.Context, expr string) error {
	return chromedp.Run(ctx, chromedp.Evaluate(`(function(){`+expr+`;return true})()`, nil))
}

func completeSQLiteFirstRun(t *testing.T, ctx context.Context, srv string) {
	t.Helper()
	if err := chromedp.Run(ctx, chromedp.Navigate(srv)); err != nil {
		t.Fatal(err)
	}
	// Stage 1: database choice. Administrator fields must not be visible yet.
	if !a11yPoll(t, ctx, `document.getElementById('auth-title').textContent === 'Choose your database'`) {
		t.Fatal("database stage did not render")
	}
	var adminHidden bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.getElementById('auth-form').hidden`, &adminHidden)); err != nil {
		t.Fatal(err)
	}
	if !adminHidden {
		t.Fatal("administrator form shown before the database stage completed")
	}
	// Commit SQLite and reach Stage 2.
	if err := chromedp.Run(ctx, chromedp.Click(`#db-action`)); err != nil {
		t.Fatal(err)
	}
	if !a11yPoll(t, ctx, `document.getElementById('auth-form').dataset.mode === 'setup' && !document.getElementById('auth-form').hidden`) {
		t.Fatal("administrator form did not appear in setup mode after the SQLite decision")
	}
	// Stage 2: create the administrator; the browser must transition
	// automatically with no reload.
	if err := chromedp.Run(ctx,
		chromedp.SendKeys(`#email`, "admin@example.com"),
		chromedp.SendKeys(`#password`, "secret7"),
		chromedp.Click(`#auth-submit`),
	); err != nil {
		t.Fatal(err)
	}
	if !a11yPoll(t, ctx, `document.getElementById('auth-stage').hidden === true`) {
		t.Fatal("administrator creation did not transition to the dashboard")
	}
	if !a11yPoll(t, ctx, `!!document.getElementById('add-site-head')`) {
		t.Fatal("dashboard fleet view did not render after setup")
	}
}

// restartAndReauth navigates to a restarted server, proves the persisted
// session auto-authenticates the returning browser, then logs out and logs
// back in through the real UI with a clean browser (cookies cleared) to prove
// credentials survive the restart.
func restartAndReauth(t *testing.T, ctx context.Context, srv string) {
	t.Helper()
	if err := chromedp.Run(ctx, chromedp.Navigate(srv)); err != nil {
		t.Fatal(err)
	}
	// A valid session persisted across the restart, so the returning browser is
	// auto-authenticated without touching the form.
	if !a11yPoll(t, ctx, `!!document.getElementById('add-site-head')`) {
		t.Fatal("session did not survive the restart")
	}
	// Log out, clear the session cookie, then log back in through the UI.
	if err := a11yEvalTrue(ctx, `document.getElementById('logout').click()`); err != nil {
		t.Fatal(err)
	}
	if !a11yPoll(t, ctx, `document.getElementById('auth-form').dataset.mode === 'login' && !document.getElementById('auth-form').hidden`) {
		t.Fatal("login form did not appear after logout on the restarted server")
	}
	if err := chromedp.Run(ctx, network.ClearBrowserCookies()); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, chromedp.Navigate(srv)); err != nil {
		t.Fatal(err)
	}
	if !a11yPoll(t, ctx, `document.getElementById('auth-form').dataset.mode === 'login' && !document.getElementById('auth-form').hidden`) {
		t.Fatal("login form did not appear on the restarted server with a clean browser")
	}
	if err := chromedp.Run(ctx,
		chromedp.SendKeys(`#email`, "admin@example.com"),
		chromedp.SendKeys(`#password`, "secret7"),
		chromedp.Click(`#auth-submit`),
	); err != nil {
		t.Fatal(err)
	}
	if !a11yPoll(t, ctx, `document.getElementById('auth-stage').hidden === true`) {
		t.Fatal("login on the restarted server did not reach the dashboard")
	}
	if !a11yPoll(t, ctx, `!!document.getElementById('add-site-head')`) {
		t.Fatal("dashboard did not render after post-restart login")
	}
}

// TestA11yFirstRunSQLiteLifecycle drives the production server init path with
// real on-disk storage through the complete first-run/restart cycle: clean
// SQLite, database stage -> administrator stage, administrator creation with an
// automatic authenticated transition, logout, login, process restart, login.
func TestA11yFirstRunSQLiteLifecycle(t *testing.T) {
	ctx := a11yContext(t)
	dataDir := t.TempDir()
	srv, stop := startRealServer(t, dataDir, "")
	completeSQLiteFirstRun(t, ctx, srv)

	// Logout through the browser control; the login form appears.
	if err := a11yEvalTrue(ctx, `document.getElementById('logout').click()`); err != nil {
		t.Fatal(err)
	}
	if !a11yPoll(t, ctx, `document.getElementById('auth-stage').hidden === false`) {
		t.Fatal("auth stage did not reappear after logout")
	}
	var mode string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.getElementById('auth-form').dataset.mode`, &mode)); err != nil {
		t.Fatal(err)
	}
	if mode != "login" {
		t.Fatalf("post-logout auth form mode = %q (want login)", mode)
	}
	if err := chromedp.Run(ctx,
		chromedp.SendKeys(`#email`, "admin@example.com"),
		chromedp.SendKeys(`#password`, "secret7"),
		chromedp.Click(`#auth-submit`),
	); err != nil {
		t.Fatal(err)
	}
	if !a11yPoll(t, ctx, `document.getElementById('auth-stage').hidden === true`) {
		t.Fatal("login after logout did not reach the dashboard")
	}

	// Restart the process on the same data directory, then log in again.
	stop()
	srv2, _ := startRealServer(t, dataDir, "")
	restartAndReauth(t, ctx, srv2)
}

// TestA11yFirstRunPostgresLifecycle runs the same lifecycle against a
// real PostgreSQL database provisioned via WEBFLEET_DATABASE_URL: the database
// stage is skipped (the provider is fixed) and the administrator form appears
// directly; then dashboard, logout/login, restart, login.
func TestA11yFirstRunPostgresLifecycle(t *testing.T) {
	ctx := a11yContext(t)
	dsn := openFreshPGForServer(t)
	srv, stop := startRealServer(t, "", dsn)

	if err := chromedp.Run(ctx, chromedp.Navigate(srv)); err != nil {
		t.Fatal(err)
	}
	if !a11yPoll(t, ctx, `document.getElementById('auth-form').dataset.mode === 'setup' && !document.getElementById('auth-form').hidden`) {
		t.Fatal("env-provisioned postgres did not show the administrator form in setup mode")
	}
	if err := chromedp.Run(ctx,
		chromedp.SendKeys(`#email`, "admin@example.com"),
		chromedp.SendKeys(`#password`, "secret7"),
		chromedp.Click(`#auth-submit`),
	); err != nil {
		t.Fatal(err)
	}
	if !a11yPoll(t, ctx, `document.getElementById('auth-stage').hidden === true`) {
		t.Fatal("administrator creation did not transition to the dashboard on postgres")
	}
	if !a11yPoll(t, ctx, `!!document.getElementById('add-site-head')`) {
		t.Fatal("dashboard did not render after postgres setup")
	}
	// logout -> login
	if err := a11yEvalTrue(ctx, `document.getElementById('logout').click()`); err != nil {
		t.Fatal(err)
	}
	if !a11yPoll(t, ctx, `document.getElementById('auth-stage').hidden === false`) {
		t.Fatal("auth stage did not reappear after logout on postgres")
	}
	if err := chromedp.Run(ctx,
		chromedp.SendKeys(`#email`, "admin@example.com"),
		chromedp.SendKeys(`#password`, "secret7"),
		chromedp.Click(`#auth-submit`),
	); err != nil {
		t.Fatal(err)
	}
	if !a11yPoll(t, ctx, `document.getElementById('auth-stage').hidden === true`) {
		t.Fatal("postgres login after logout did not reach the dashboard")
	}
	// restart the process on the same database, then log in again
	stop()
	srv2, _ := startRealServer(t, "", dsn)
	restartAndReauth(t, ctx, srv2)
}

// TestA11yPostgresChooserFlow proves the Stage-1 PostgreSQL chooser on a
// SQLite-running server: selecting PostgreSQL reveals the URL and a Test
// connection button; an unreachable URL yields an inline error; a valid URL
// commits the choice and shows the restart-required stage.
func TestA11yPostgresChooserFlow(t *testing.T) {
	ctx := a11yContext(t)
	dsn := openFreshPGForServer(t)
	srv, _ := startRealServer(t, t.TempDir(), "")

	if err := chromedp.Run(ctx, chromedp.Navigate(srv)); err != nil {
		t.Fatal(err)
	}
	if !a11yPoll(t, ctx, `document.getElementById('auth-title').textContent === 'Choose your database'`) {
		t.Fatal("database stage did not render")
	}
	if err := chromedp.Run(ctx, chromedp.Click(`input[name="database"][value="postgres"]`)); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, chromedp.WaitVisible(`#postgres-setup`)); err != nil {
		t.Fatal(err)
	}
	var label string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.getElementById('db-action').textContent`, &label)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(label, "Test connection") {
		t.Fatalf("postgres action label = %q (want Test connection)", label)
	}
	// An unreachable URL must surface an inline error and re-enable the button.
	if err := chromedp.Run(ctx,
		chromedp.SetValue(`#postgres-url`, "postgres://nobody:wrong@127.0.0.1:1/nowhere", chromedp.ByID),
		chromedp.Click(`#db-action`),
	); err != nil {
		t.Fatal(err)
	}
	if !a11yPoll(t, ctx, `(document.getElementById('db-error')||{}).textContent.length > 0`) {
		t.Fatal("postgres connection failure did not surface an inline error")
	}
	var disabled bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.getElementById('db-action').disabled`, &disabled)); err != nil {
		t.Fatal(err)
	}
	if disabled {
		t.Fatal("test connection button stayed disabled after a failed attempt")
	}
	// A valid URL commits the choice and shows the restart-required stage.
	if err := chromedp.Run(ctx,
		chromedp.SetValue(`#postgres-url`, dsn, chromedp.ByID),
		chromedp.Click(`#db-action`),
	); err != nil {
		t.Fatal(err)
	}
	if !a11yPoll(t, ctx, `document.getElementById('restart-stage').hidden === false`) {
		var dberr, disabled, urlv, action string
		_ = chromedp.Run(ctx, chromedp.Evaluate(`(document.getElementById('db-error')||{}).textContent||''`, &dberr))
		_ = chromedp.Run(ctx, chromedp.Evaluate(`document.getElementById('db-action').disabled`, &disabled))
		_ = chromedp.Run(ctx, chromedp.Evaluate(`document.getElementById('postgres-url').value`, &urlv))
		_ = chromedp.Run(ctx, chromedp.Evaluate(`document.getElementById('db-action').textContent`, &action))
		t.Fatalf("valid postgres choice did not show the restart stage (db-error=%q disabled=%v urlLen=%d action=%q)", dberr, disabled, len(urlv), action)
	}
}

// TestA11yBootErrorState proves a failed boot request cannot leave the shell on
// the eternal "Loading..." state: boot() must render an actionable boot error
// with Retry, and Retry must recover once the request succeeds.
func TestA11yBootErrorState(t *testing.T) {
	ctx := a11yContext(t)
	dataDir := t.TempDir()
	t.Setenv("WEBFLEET_DATA_DIR", dataDir)
	t.Setenv("WEBFLEET_DATABASE_URL", "")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	s := New(cfg, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	// Fail the first /api/setup/status call, then behave normally so Retry can
	// recover. /api/session delegates to the real handler (401 on a fresh
	// install), which is what makes boot() reach setup/status.
	var failOnce sync.Once
	wrapped := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/setup/status" {
			ok := true
			failOnce.Do(func() {
				ok = false
				w.WriteHeader(500)
				_ = json.NewEncoder(w).Encode(map[string]string{"error": "injected boot failure"})
			})
			if !ok {
				return
			}
		}
		s.Handler().ServeHTTP(w, r)
	}))
	defer wrapped.Close()

	if err := chromedp.Run(ctx, chromedp.Navigate(wrapped.URL)); err != nil {
		t.Fatal(err)
	}
	if !a11yPoll(t, ctx, `document.getElementById('boot-error').hidden === false`) {
		t.Fatal("boot error state not shown after /api/setup/status failure")
	}
	var title string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.getElementById('auth-title').textContent`, &title)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(title, "Could not load") {
		t.Fatalf("boot error title = %q (want 'Could not load ...')", title)
	}
	if err := chromedp.Run(ctx, chromedp.WaitVisible(`#boot-retry`)); err != nil {
		t.Fatal(err)
	}
	// Retry recovers: the next setup/status succeeds and the database stage
	// appears instead of an eternal loading screen.
	if err := chromedp.Run(ctx, chromedp.Click(`#boot-retry`)); err != nil {
		t.Fatal(err)
	}
	if !a11yPoll(t, ctx, `document.getElementById('database-stage').hidden === false`) {
		t.Fatal("Retry did not recover to the database stage")
	}
}
