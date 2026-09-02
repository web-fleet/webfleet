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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
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
	if !strings.Contains(label, "Test and use PostgreSQL") {
		t.Fatalf("postgres action label = %q (want Test and use PostgreSQL)", label)
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

// countingAuthServer starts the application through the production init path
// (config.Load -> store.Open -> New) and wraps its handler so every POST
// /api/setup and /api/login is counted, to prove a single UI submit issues a
// single request.
func countingAuthServer(t *testing.T, dataDir string) (string, *atomic.Int64, *atomic.Int64) {
	t.Helper()
	t.Setenv("WEBFLEET_DATABASE_URL", "")
	t.Setenv("WEBFLEET_DATA_DIR", dataDir)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open(cfg.DataDir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	s := New(cfg, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	inner := s.Handler()
	var setups, logins atomic.Int64
	wrap := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			switch r.URL.Path {
			case "/api/setup":
				setups.Add(1)
			case "/api/login":
				logins.Add(1)
			}
		}
		inner.ServeHTTP(w, r)
	})
	srv := httptest.NewServer(wrap)
	t.Cleanup(srv.Close)
	return srv.URL, &setups, &logins
}

// TestA11ySingleSubmitAuthProvesOneRequest guards against duplicate auth-form
// submit handlers: one Create administrator click must produce exactly one
// POST /api/setup, and one Sign in click exactly one POST /api/login.
func TestA11ySingleSubmitAuthProvesOneRequest(t *testing.T) {
	ctx := a11yContext(t)
	srv, setups, logins := countingAuthServer(t, t.TempDir())

	if err := chromedp.Run(ctx, chromedp.Navigate(srv)); err != nil {
		t.Fatal(err)
	}
	if !a11yPoll(t, ctx, `document.getElementById('auth-title').textContent === 'Choose your database'`) {
		t.Fatal("database stage did not render")
	}
	if err := chromedp.Run(ctx, chromedp.Click(`#db-action`)); err != nil {
		t.Fatal(err)
	}
	if !a11yPoll(t, ctx, `document.getElementById('auth-form').dataset.mode === 'setup' && !document.getElementById('auth-form').hidden`) {
		t.Fatal("administrator form did not appear in setup mode")
	}
	if err := chromedp.Run(ctx,
		chromedp.SendKeys(`#email`, "admin@example.com"),
		chromedp.SendKeys(`#password`, "secret7"),
		chromedp.Click(`#auth-submit`),
	); err != nil {
		t.Fatal(err)
	}
	if !a11yPoll(t, ctx, `document.getElementById('auth-stage').hidden === true`) {
		t.Fatal("administrator creation did not reach the dashboard")
	}
	if got := setups.Load(); got != 1 {
		t.Fatalf("one Create administrator click issued %d POST /api/setup (want 1)", got)
	}

	// Log out and sign back in with a single submit.
	if err := a11yEvalTrue(ctx, `document.getElementById('logout').click()`); err != nil {
		t.Fatal(err)
	}
	if !a11yPoll(t, ctx, `document.getElementById('auth-form').dataset.mode === 'login' && !document.getElementById('auth-form').hidden`) {
		t.Fatal("login form did not appear after logout")
	}
	if err := chromedp.Run(ctx,
		chromedp.SendKeys(`#email`, "admin@example.com"),
		chromedp.SendKeys(`#password`, "secret7"),
		chromedp.Click(`#auth-submit`),
	); err != nil {
		t.Fatal(err)
	}
	if !a11yPoll(t, ctx, `document.getElementById('auth-stage').hidden === true`) {
		t.Fatal("login did not reach the dashboard")
	}
	if got := logins.Load(); got != 1 {
		t.Fatalf("one Sign in click issued %d POST /api/login (want 1)", got)
	}
}

// renderedVisible reports whether an element is actually painted: computed
// display/visibility are not 'none'/'hidden' and it has nonzero painted size.
// This is the acceptance criterion - the DOM `hidden` property alone is not,
// because author CSS display rules can override it.
func renderedVisible(ctx context.Context, id string) bool {
	var v bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(function(){var el=document.getElementById('`+id+`');if(!el)return false;var s=getComputedStyle(el);var r=el.getBoundingClientRect();return s.display!=='none'&&s.visibility!=='hidden'&&r.width>0&&r.height>0})()`, &v)); err != nil {
		return false
	}
	return v
}

// assertFirstRunPanels asserts the real rendered visibility of every first-run
// panel (chooser, restart notice, auth form, boot error, dashboard, auth
// stage, postgres sub-panel), proving the panels are mutually exclusive on
// screen - not merely in their hidden properties.
func assertFirstRunPanels(t *testing.T, ctx context.Context, chooser, restart, auth, bootError, dashboard bool, wantMode string) {
	t.Helper()
	r := map[string]bool{
		"database-stage": renderedVisible(ctx, "database-stage"),
		"restart-stage":  renderedVisible(ctx, "restart-stage"),
		"auth-form":      renderedVisible(ctx, "auth-form"),
		"boot-error":     renderedVisible(ctx, "boot-error"),
		"dashboard":      renderedVisible(ctx, "dashboard"),
		"auth-stage":     renderedVisible(ctx, "auth-stage"),
		"postgres-setup": renderedVisible(ctx, "postgres-setup"),
	}
	if r["database-stage"] && r["auth-form"] {
		t.Fatal("rendered impossible combination: database chooser and auth form")
	}
	if r["restart-stage"] && r["auth-form"] {
		t.Fatal("rendered impossible combination: restart notice and auth form")
	}
	if r["database-stage"] && r["restart-stage"] {
		t.Fatal("rendered impossible combination: database chooser and restart notice")
	}
	if r["auth-form"] && r["dashboard"] {
		t.Fatal("rendered impossible combination: auth form and dashboard")
	}
	if r["dashboard"] && r["auth-stage"] {
		t.Fatal("rendered impossible combination: dashboard and auth stage")
	}
	if r["boot-error"] && (r["database-stage"] || r["restart-stage"] || r["auth-form"] || r["dashboard"]) {
		t.Fatal("rendered impossible combination: boot error alongside another panel")
	}
	if r["database-stage"] != chooser {
		t.Fatalf("database-stage rendered=%v want=%v", r["database-stage"], chooser)
	}
	if r["restart-stage"] != restart {
		t.Fatalf("restart-stage rendered=%v want=%v", r["restart-stage"], restart)
	}
	if r["auth-form"] != auth {
		t.Fatalf("auth-form rendered=%v want=%v", r["auth-form"], auth)
	}
	if r["boot-error"] != bootError {
		t.Fatalf("boot-error rendered=%v want=%v", r["boot-error"], bootError)
	}
	if r["dashboard"] != dashboard {
		t.Fatalf("dashboard rendered=%v want=%v", r["dashboard"], dashboard)
	}
	if wantMode != "" && auth {
		var mode string
		if err := chromedp.Run(ctx, chromedp.Evaluate(`(document.getElementById('auth-form').dataset.mode||'')`, &mode)); err != nil {
			t.Fatal(err)
		}
		if mode != wantMode {
			t.Fatalf("auth form mode=%q want=%q", mode, wantMode)
		}
	}
	// The auth stage shell must render exactly when one of its panels does.
	childRendered := r["database-stage"] || r["restart-stage"] || r["auth-form"] || r["boot-error"]
	if childRendered != r["auth-stage"] {
		t.Fatalf("auth-stage rendered=%v but child panels rendered=%v (shell must track its content)", r["auth-stage"], childRendered)
	}
}

// TestA11yFirstRunStateMatrixSQLite walks the full SQLite first-run state
// matrix with mutual-exclusivity assertions at every step.
func TestA11yFirstRunStateMatrixSQLite(t *testing.T) {
	ctx := a11yContext(t)
	srv, _ := startRealServer(t, t.TempDir(), "")
	if err := chromedp.Run(ctx, chromedp.Navigate(srv)); err != nil {
		t.Fatal(err)
	}
	// STATE A: fresh, database not chosen -> chooser only.
	if !a11yPoll(t, ctx, `document.getElementById('auth-title').textContent === 'Choose your database'`) {
		t.Fatal("database stage did not render")
	}
	assertFirstRunPanels(t, ctx, true, false, false, false, false, "")
	// STATE B: SQLite chosen -> create-administrator form in setup mode.
	if err := chromedp.Run(ctx, chromedp.Click(`#db-action`)); err != nil {
		t.Fatal(err)
	}
	if !a11yPoll(t, ctx, `document.getElementById('auth-form').dataset.mode === 'setup' && !document.getElementById('auth-form').hidden`) {
		t.Fatal("administrator form did not appear after SQLite choice")
	}
	assertFirstRunPanels(t, ctx, false, false, true, false, false, "setup")
	// Create administrator -> automatic dashboard transition.
	if err := chromedp.Run(ctx,
		chromedp.SendKeys(`#email`, "admin@example.com"),
		chromedp.SendKeys(`#password`, "secret7"),
		chromedp.Click(`#auth-submit`),
	); err != nil {
		t.Fatal(err)
	}
	if !a11yPoll(t, ctx, `document.getElementById('auth-stage').hidden === true`) {
		t.Fatal("administrator creation did not reach the dashboard")
	}
	// STATE F: established installation -> login form only after logout.
	if err := a11yEvalTrue(ctx, `document.getElementById('logout').click()`); err != nil {
		t.Fatal(err)
	}
	if !a11yPoll(t, ctx, `document.getElementById('auth-form').dataset.mode === 'login' && !document.getElementById('auth-form').hidden`) {
		t.Fatal("login form did not appear after logout")
	}
	assertFirstRunPanels(t, ctx, false, false, true, false, false, "login")
}

// TestA11yFirstRunStateMatrixPostgres walks the PostgreSQL first-run matrix:
// configuring a URL keeps the auth form hidden, a valid choice commits to the
// restart-required stage, and after restarting onto PostgreSQL the
// create-administrator form appears in setup mode and succeeds.
func TestA11yFirstRunStateMatrixPostgres(t *testing.T) {
	ctx := a11yContext(t)
	dsn := openFreshPGForServer(t)
	sqliteData := t.TempDir()
	srv, stop := startRealServer(t, sqliteData, "")
	if err := chromedp.Run(ctx, chromedp.Navigate(srv)); err != nil {
		t.Fatal(err)
	}
	// STATE A: fresh -> chooser only.
	if !a11yPoll(t, ctx, `document.getElementById('auth-title').textContent === 'Choose your database'`) {
		t.Fatal("database stage did not render")
	}
	assertFirstRunPanels(t, ctx, true, false, false, false, false, "")
	// STATE C: configure PostgreSQL URL; auth stays hidden while entering it.
	if err := chromedp.Run(ctx, chromedp.Click(`input[name="database"][value="postgres"]`)); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, chromedp.WaitVisible(`#postgres-setup`)); err != nil {
		t.Fatal(err)
	}
	assertFirstRunPanels(t, ctx, true, false, false, false, false, "")
	if err := chromedp.Run(ctx, chromedp.SetValue(`#postgres-url`, dsn, chromedp.ByID)); err != nil {
		t.Fatal(err)
	}
	assertFirstRunPanels(t, ctx, true, false, false, false, false, "")
	// STATE D: commit -> restart-required only.
	if err := chromedp.Run(ctx, chromedp.Click(`#db-action`)); err != nil {
		t.Fatal(err)
	}
	if !a11yPoll(t, ctx, `document.getElementById('restart-stage').hidden === false`) {
		t.Fatal("restart-required stage did not appear after postgres choice")
	}
	assertFirstRunPanels(t, ctx, false, true, false, false, false, "")
	// STATE E: restart onto PostgreSQL -> create-administrator form (setup).
	stop()
	srv2, _ := startRealServer(t, "", dsn)
	if err := chromedp.Run(ctx, chromedp.Navigate(srv2)); err != nil {
		t.Fatal(err)
	}
	if !a11yPoll(t, ctx, `document.getElementById('auth-form').dataset.mode === 'setup' && !document.getElementById('auth-form').hidden`) {
		t.Fatal("create-administrator form did not appear after postgres restart")
	}
	assertFirstRunPanels(t, ctx, false, false, true, false, false, "setup")
	if err := chromedp.Run(ctx,
		chromedp.SendKeys(`#email`, "admin@example.com"),
		chromedp.SendKeys(`#password`, "secret7"),
		chromedp.Click(`#auth-submit`),
	); err != nil {
		t.Fatal(err)
	}
	if !a11yPoll(t, ctx, `document.getElementById('auth-stage').hidden === true`) {
		t.Fatal("postgres administrator creation did not reach the dashboard")
	}
}

// TestA11yPendingRestartHidesAuthEvenWithAdmin is the regression for the
// owner's "invalid credentials" finding: a committed PostgreSQL choice that
// still requires a restart must hide ALL auth/setup actions against the old
// running database, even when an administrator already exists there.
func TestA11yPendingRestartHidesAuthEvenWithAdmin(t *testing.T) {
	ctx := a11yContext(t)
	dsn := openFreshPGForServer(t)
	dataDir := t.TempDir()
	srv, _ := startRealServer(t, dataDir, "")
	// Create an administrator on the running SQLite database through the UI.
	completeSQLiteFirstRun(t, ctx, srv)
	// Commit a PostgreSQL choice without restarting: a pending transition.
	if err := config.SaveDatabaseChoice(dataDir, config.DatabaseChoice{Provider: "postgres", URL: dsn}); err != nil {
		t.Fatal(err)
	}
	// Reload: even with a valid session, boot must show ONLY the restart
	// notice - never the dashboard, login or admin form against the old DB.
	if err := chromedp.Run(ctx, chromedp.Navigate(srv)); err != nil {
		t.Fatal(err)
	}
	if !a11yPoll(t, ctx, `document.getElementById('restart-stage').hidden === false`) {
		t.Fatal("pending postgres transition did not show the restart notice")
	}
	assertFirstRunPanels(t, ctx, false, true, false, false, false, "")
	if !a11yPoll(t, ctx, `document.getElementById('auth-stage').hidden === false`) {
		t.Fatal("dashboard was not hidden during the pending transition")
	}
}

// assertAtMostOnePanelRendered fails if more than one of the first-run panels
// is painted in the current browser frame, and that the auth stage shell
// tracks its content. This is the exact-screenshot regression: the owner saw
// Loading + Retry + restart-required + auth fields all at once.
func assertAtMostOnePanelRendered(t *testing.T, ctx context.Context) {
	t.Helper()
	ids := []string{"database-stage", "restart-stage", "auth-form", "boot-error", "dashboard"}
	rendered := 0
	for _, id := range ids {
		if renderedVisible(ctx, id) {
			rendered++
		}
	}
	if rendered > 1 {
		t.Fatalf("browser frame rendered %d first-run panels at once (owner screenshot state)", rendered)
	}
	// Loading must never coexist with a rendered panel or boot error.
	var title string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.getElementById('auth-title').textContent`, &title)); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(title, "Loading") && rendered > 0 {
		t.Fatalf("Loading title coexists with %d rendered panels", rendered)
	}
	authStage := renderedVisible(ctx, "auth-stage")
	if authStage != (renderedVisible(ctx, "database-stage") || renderedVisible(ctx, "restart-stage") || renderedVisible(ctx, "auth-form") || renderedVisible(ctx, "boot-error")) {
		t.Fatal("auth-stage shell rendered state does not match its panels")
	}
}

// TestA11yFirstRunRenderedVisibility walks the first-run states and asserts,
// on real rendered visibility, that no browser frame ever paints more than one
// first-run panel and that Loading is gone in every terminal state.
func TestA11yFirstRunRenderedVisibility(t *testing.T) {
	ctx := a11yContext(t)
	srv, _ := startRealServer(t, t.TempDir(), "")
	if err := chromedp.Run(ctx, chromedp.Navigate(srv)); err != nil {
		t.Fatal(err)
	}
	if !a11yPoll(t, ctx, `document.getElementById('auth-title').textContent === 'Choose your database'`) {
		t.Fatal("database stage did not render")
	}
	assertAtMostOnePanelRendered(t, ctx)
	assertFirstRunPanels(t, ctx, true, false, false, false, false, "")
	// SQLite -> admin form; still a single rendered panel, no Loading.
	if err := chromedp.Run(ctx, chromedp.Click(`#db-action`)); err != nil {
		t.Fatal(err)
	}
	if !a11yPoll(t, ctx, `document.getElementById('auth-form').dataset.mode === 'setup' && !document.getElementById('auth-form').hidden`) {
		t.Fatal("administrator form did not appear")
	}
	assertAtMostOnePanelRendered(t, ctx)
	assertFirstRunPanels(t, ctx, false, false, true, false, false, "setup")
	// Create administrator -> dashboard is the single rendered panel.
	if err := chromedp.Run(ctx,
		chromedp.SendKeys(`#email`, "admin@example.com"),
		chromedp.SendKeys(`#password`, "secret7"),
		chromedp.Click(`#auth-submit`),
	); err != nil {
		t.Fatal(err)
	}
	if !a11yPoll(t, ctx, `document.getElementById('auth-stage').hidden === true`) {
		t.Fatal("dashboard did not appear")
	}
	assertAtMostOnePanelRendered(t, ctx)
	assertFirstRunPanels(t, ctx, false, false, false, false, true, "")
}

// TestA11yInitialLoadShowsOnlyLoading proves the pre-boot shell renders nothing
// but the Loading text: no chooser, restart, auth, boot-error or dashboard is
// painted before boot resolves.
func TestA11yInitialLoadShowsOnlyLoading(t *testing.T) {
	ctx := a11yContext(t)
	dataDir := t.TempDir()
	t.Setenv("WEBFLEET_DATABASE_URL", "")
	t.Setenv("WEBFLEET_DATA_DIR", dataDir)
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
	// Delay /api/setup/database (boot's first await) to hold the initial shell.
	wrapped := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/setup/database" {
			time.Sleep(2500 * time.Millisecond)
		}
		s.Handler().ServeHTTP(w, r)
	}))
	defer wrapped.Close()
	if err := chromedp.Run(ctx, chromedp.Navigate(wrapped.URL)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	var title string
	_ = chromedp.Run(ctx, chromedp.Evaluate(`document.getElementById('auth-title').textContent`, &title))
	if !strings.Contains(title, "Loading") {
		t.Fatalf("pre-boot shell title = %q (want Loading)", title)
	}
	for _, id := range []string{"database-stage", "restart-stage", "auth-form", "boot-error", "dashboard"} {
		if renderedVisible(ctx, id) {
			t.Fatalf("pre-boot shell rendered %s (must be visually absent)", id)
		}
	}
	// Once boot resolves the database chooser becomes the single rendered panel.
	if !a11yPoll(t, ctx, `document.getElementById('auth-title').textContent === 'Choose your database'`) {
		t.Fatal("database stage did not render after boot")
	}
	assertAtMostOnePanelRendered(t, ctx)
}

// TestA11yDeleteDialogLayout proves the destructive Delete row in the edit
// dialog renders the "Type <name> to confirm" instruction inline, keeps the
// confirm input and Delete button in a row with an explicit gap (right-aligned
// on desktop, wrapping below the input on narrow widths), and only enables
// Delete when the typed name matches.
func TestA11yDeleteDialogLayout(t *testing.T) {
	ctx := a11yContext(t)
	srv := a11yServer(t)
	a11ySetup(t, ctx, srv)
	siteID := a11yAddSite(t, srv)
	if err := chromedp.Run(ctx, chromedp.Evaluate(fmt.Sprintf(`location.hash="#/sites/%d"`, siteID), nil)); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, chromedp.WaitVisible(`#edit-site`)); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, chromedp.Click(`#edit-site`)); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, chromedp.WaitVisible(`#site-dialog`)); err != nil {
		t.Fatal(err)
	}
	var name string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.getElementById('delete-site-name').textContent`, &name)); err != nil {
		t.Fatal(err)
	}
	if name != "S" {
		t.Fatalf("delete target name = %q want S", name)
	}
	// "Type <name> to confirm" stays on a single line (inline sentence).
	var inline bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(function(){var s=document.getElementById('delete-site-name'),p=s.closest('.delete-prompt');return Math.abs(s.getBoundingClientRect().top-p.getBoundingClientRect().top)<4})()`, &inline)); err != nil {
		t.Fatal(err)
	}
	if !inline {
		var dbg string
		_ = chromedp.Run(ctx, chromedp.Evaluate(`(function(){var s=document.getElementById('delete-site-name'),p=document.querySelector('.delete-prompt');return 's.top='+s.getBoundingClientRect().top+' p.top='+p.getBoundingClientRect().top+' ptext='+p.textContent+' sdisp='+getComputedStyle(s).display})()`, &dbg))
		t.Fatalf("delete confirmation sentence is not inline: %s", dbg)
	}
	// Input and Delete button sit in a row with an explicit gap (button right).
	var gap bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(function(){var i=document.getElementById('delete-confirm'),b=document.getElementById('delete-site-btn');return b.getBoundingClientRect().left>i.getBoundingClientRect().right})()`, &gap)); err != nil {
		t.Fatal(err)
	}
	if !gap {
		t.Fatal("delete button is not separated from the confirmation input")
	}
	// Button stays disabled until the exact site name is typed.
	var disabled bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.getElementById('delete-site-btn').disabled`, &disabled)); err != nil {
		t.Fatal(err)
	}
	if !disabled {
		t.Fatal("delete button enabled before confirmation")
	}
	if err := chromedp.Run(ctx, chromedp.SetValue(`#delete-confirm`, "wrong", chromedp.ByID)); err != nil {
		t.Fatal(err)
	}
	if !a11yPoll(t, ctx, `document.getElementById('delete-site-btn').disabled === true`) {
		t.Fatal("delete enabled for a non-matching name")
	}
	if err := chromedp.Run(ctx, chromedp.SetValue(`#delete-confirm`, "S", chromedp.ByID)); err != nil {
		t.Fatal(err)
	}
	if !a11yPoll(t, ctx, `document.getElementById('delete-site-btn').disabled === false`) {
		t.Fatal("delete stayed disabled after the matching name was typed")
	}
	// Narrow width: the destructive row wraps so the button moves below the input.
	if err := chromedp.Run(ctx, chromedp.EmulateViewport(320, 700)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	var wrapped bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(function(){var i=document.getElementById('delete-confirm'),b=document.getElementById('delete-site-btn');return b.getBoundingClientRect().top>i.getBoundingClientRect().top})()`, &wrapped)); err != nil {
		t.Fatal(err)
	}
	if !wrapped {
		t.Fatal("delete row did not wrap below the input at 320px")
	}
}

// TestA11yDialogCloseButton proves the Add website dialog's top-right × actually
// closes it (rendered open state), returns focus to the Add website control and
// leaves the background interactive - not merely that a close control exists.
func TestA11yDialogCloseButton(t *testing.T) {
	ctx := a11yContext(t)
	srv := a11yServer(t)
	a11ySetup(t, ctx, srv)
	if !a11yPoll(t, ctx, `document.getElementById('auth-title') !== null && document.getElementById('add-site-head') !== null`) {
		t.Fatal("fleet view did not render")
	}
	if err := chromedp.Run(ctx, chromedp.Click(`#add-site-head`)); err != nil {
		t.Fatal(err)
	}
	if !a11yPoll(t, ctx, `document.getElementById('site-dialog').open === true`) {
		t.Fatal("site dialog did not open")
	}
	if err := chromedp.Run(ctx, chromedp.Click(`#site-dialog .icon-button`)); err != nil {
		t.Fatal(err)
	}
	if !a11yPoll(t, ctx, `!document.getElementById('site-dialog').open && document.getElementById('site-dialog').getBoundingClientRect().width === 0`) {
		t.Fatal("site dialog did not close via the × button")
	}
	var focusID string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.activeElement.id||''`, &focusID)); err != nil {
		t.Fatal(err)
	}
	if focusID != "add-site-head" {
		t.Fatalf("focus after × close = %q (want add-site-head)", focusID)
	}
	// Background is interactive again: reopening works.
	if err := chromedp.Run(ctx, chromedp.Click(`#add-site-head`)); err != nil {
		t.Fatal(err)
	}
	if !a11yPoll(t, ctx, `document.getElementById('site-dialog').open === true`) {
		t.Fatal("site dialog could not be reopened after close")
	}
}

// TestFaviconReferencedAndEmbedded proves the application HTML references the
// Web Fleet favicon and the embedded asset exists (three-copy sync retained).
func TestFaviconReferencedAndEmbedded(t *testing.T) {
	html, e := os.ReadFile("web/index.html")
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(html), `rel="icon" href="assets/images/web-fleet-mark.svg"`) {
		t.Fatal("embedded index.html does not reference the Web Fleet favicon")
	}
	svg, e := os.ReadFile("web/assets/images/web-fleet-mark.svg")
	if e != nil {
		t.Fatalf("embedded favicon asset missing: %v", e)
	}
	if !strings.Contains(string(svg), "Web Fleet mark") {
		t.Fatal("favicon asset is not the Web Fleet mark")
	}
}

// TestA11ySelectChevronAndAnalyticsLayout proves the global select treatment
// (custom inset chevron, appearance:none, roomy right padding) applies, and the
// Analytics empty-state button sits below its explanatory text.
func TestA11ySelectChevronAndAnalyticsLayout(t *testing.T) {
	ctx := a11yContext(t)
	srv := a11yServer(t)
	a11ySetup(t, ctx, srv)
	siteID := a11yAddSite(t, srv)
	// Sites list: the group filter select uses the corrected treatment.
	if err := chromedp.Run(ctx, chromedp.Evaluate(`location.hash='#/sites'`, nil)); err != nil {
		t.Fatal(err)
	}
	if !a11yPoll(t, ctx, `!!document.getElementById('group-filter')`) {
		t.Fatal("sites view did not render")
	}
	var appearance, padRight string
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`getComputedStyle(document.getElementById('group-filter')).appearance`, &appearance),
		chromedp.Evaluate(`getComputedStyle(document.getElementById('group-filter')).paddingRight`, &padRight),
	); err != nil {
		t.Fatal(err)
	}
	if appearance != "none" {
		t.Fatalf("group-filter appearance=%q want none (custom chevron)", appearance)
	}
	if strings.TrimSuffix(padRight, "px") == "" || parseFloat(padRight) < 24 {
		t.Fatalf("group-filter padding-right=%q want >=24px for chevron inset", padRight)
	}
	// Site detail: the Analytics empty-state stacks text above the button.
	if err := chromedp.Run(ctx, chromedp.Evaluate(fmt.Sprintf(`location.hash="#/sites/%d"`, siteID), nil)); err != nil {
		t.Fatal(err)
	}
	if !a11yPoll(t, ctx, `!!document.getElementById('enable-analytics') && !!document.querySelector('.empty-copy')`) {
		t.Fatal("analytics empty state did not render")
	}
	var below bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(`(function(){var b=document.getElementById('enable-analytics'),t=document.querySelector('.empty-copy');return b.getBoundingClientRect().top>=t.getBoundingClientRect().bottom+4})()`, &below)); err != nil {
		t.Fatal(err)
	}
	if !below {
		t.Fatal("Enable tracker is not stacked below the analytics text")
	}
}

func parseFloat(s string) float64 {
	v, _ := strconv.ParseFloat(strings.TrimSuffix(s, "px"), 64)
	return v
}

// TestA11yAnalyticsEnableInstallAndDisable drives the analytics lifecycle in
// the browser: Enable tracker opens the install-code modal with the real
// snippet, Copy/Close work, the permanent Tracking code button reopens it, and
// Disable tracker returns to the empty state (history preserved server-side).
func TestA11yAnalyticsEnableInstallAndDisable(t *testing.T) {
	ctx := a11yContext(t)
	srv := a11yServer(t)
	a11ySetup(t, ctx, srv)
	siteID := a11yAddSite(t, srv)
	if err := chromedp.Run(ctx, chromedp.Evaluate(fmt.Sprintf(`location.hash="#/sites/%d"`, siteID), nil)); err != nil {
		t.Fatal(err)
	}
	if !a11yPoll(t, ctx, `!!document.getElementById('enable-analytics')`) {
		var view string
		_ = chromedp.Run(ctx, chromedp.Evaluate(`document.getElementById('view').textContent.slice(0,200)`, &view))
		t.Fatalf("analytics empty state did not render: view=%q", view)
	}
	if err := chromedp.Run(ctx, chromedp.Click(`#enable-analytics`)); err != nil {
		t.Fatal(err)
	}
	// Install modal opens with the real tracking snippet.
	if !a11yPoll(t, ctx, `document.getElementById('tracker-dialog').open === true`) {
		t.Fatal("tracking code modal did not open after enabling")
	}
	var snippet string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.getElementById('tracker-snippet').textContent`, &snippet)); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(snippet, "/wf.js") || !strings.Contains(snippet, `data-webfleet="`) {
		t.Fatalf("tracking snippet missing the tracker contract: %q", snippet)
	}
	// Copy + Close work.
	if err := chromedp.Run(ctx, chromedp.Click(`#copy-tracker`)); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, chromedp.Click(`#close-tracker`)); err != nil {
		t.Fatal(err)
	}
	if !a11yPoll(t, ctx, `document.getElementById('tracker-dialog').open === false`) {
		t.Fatal("tracking modal did not close")
	}
	// Country section offers the actionable install path when no database is loaded.
	if !a11yPoll(t, ctx, `document.body.textContent.includes('Country database not installed.') && !!document.getElementById('geo-install')`) {
		t.Fatal("actionable country-database install state missing")
	}
	// Permanent Tracking code button reopens it later.
	if !a11yPoll(t, ctx, `!!document.getElementById('show-tracker')`) {
		t.Fatal("Tracking code button missing after enabling")
	}
	if err := chromedp.Run(ctx, chromedp.Click(`#show-tracker`)); err != nil {
		t.Fatal(err)
	}
	if !a11yPoll(t, ctx, `document.getElementById('tracker-dialog').open === true`) {
		t.Fatal("Tracking code button did not reopen the modal")
	}
	if err := chromedp.Run(ctx, chromedp.Click(`#close-tracker`)); err != nil {
		t.Fatal(err)
	}
	// Disable tracker (confirm auto-accepted) returns to the empty state.
	if err := chromedp.Run(ctx, chromedp.Evaluate(`window.confirm=()=>true`, nil)); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, chromedp.Click(`#disable-analytics`)); err != nil {
		t.Fatal(err)
	}
	if !a11yPoll(t, ctx, `!!document.getElementById('enable-analytics') && document.getElementById('tracker-dialog').open === false`) {
		t.Fatal("analytics did not return to the disabled empty state")
	}
}

// TestA11yCrawlEvidenceAndGrammar proves the site inventory renders real crawl
// failure/broken-link evidence (not just counts): one failed page uses singular
// grammar with its URL and reason, and a broken link exposes source, target and
// result - the diagnostics the product contract requires.
func TestA11yCrawlEvidenceAndGrammar(t *testing.T) {
	ctx := a11yContext(t)
	dir := t.TempDir()
	st, e := store.Open(dir)
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	scfg, _ := config.Load()
	scfg.DataDir = dir
	srv := httptest.NewServer(New(scfg, st, slog.New(slog.NewTextHandler(io.Discard, nil))).Handler())
	defer srv.Close()
	a11ySetup(t, ctx, srv)
	siteID := a11yAddSite(t, srv)
	now := store.Now()
	if _, e := st.DB.Exec(`INSERT INTO crawl_runs(site_id,status,pages_crawled,pages_failed,internal_links,external_links,broken_internal,broken_external,new_broken,robots_found,sitemap_found,pages_discovered,page_limit,limit_reached,started_at,finished_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, siteID, "complete", 2, 1, 2, 0, 1, 0, 1, 0, 0, 2, 500, 0, now, now); e != nil {
		t.Fatal(e)
	}
	var runID int64
	_ = st.DB.QueryRow(`SELECT id FROM crawl_runs WHERE site_id=? ORDER BY id DESC LIMIT 1`, siteID).Scan(&runID)
	_, _ = st.DB.Exec(`INSERT INTO crawl_pages(run_id,site_id,url,status_code,depth,error,kind,origin,ok) VALUES(?,?,'/',200,0,'','page','internal',1)`, runID, siteID)
	_, _ = st.DB.Exec(`INSERT INTO crawl_pages(run_id,site_id,url,status_code,depth,error,kind,origin,ok) VALUES(?,?,'/broken-page',500,1,'HTTP 500','page','internal',0)`, runID, siteID)
	_, _ = st.DB.Exec(`INSERT INTO crawl_links(run_id,site_id,from_url,to_url,kind,status_code,broken,error) VALUES(?,?,'/','/old-page','internal',404,1,'')`, runID, siteID)

	if err := chromedp.Run(ctx, chromedp.Evaluate(fmt.Sprintf(`location.hash="#/sites/%d"`, siteID), nil)); err != nil {
		t.Fatal(err)
	}
	if !a11yPoll(t, ctx, `document.body.textContent.includes('1 page failed')`) {
		t.Fatal("singular '1 page failed' not rendered")
	}
	if !a11yPoll(t, ctx, `document.querySelector('details[open]') !== null || (document.body.textContent.includes('/broken-page') && document.body.textContent.includes('HTTP 500'))`) {
		t.Fatal("failed-page URL/reason evidence missing")
	}
	// Broken link table exposes source, target and result.
	if !a11yPoll(t, ctx, `document.body.textContent.includes('/old-page') && document.body.textContent.includes('/') && document.body.textContent.includes('HTTP 404')`) {
		t.Fatal("broken-link source/target/result evidence missing")
	}
}
