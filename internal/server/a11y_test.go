package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/chromedp/cdproto/network"
	"github.com/chromedp/chromedp"
	"github.com/web-fleet/webfleet/internal/config"
	"github.com/web-fleet/webfleet/internal/store"
)

// This suite drives the real embedded application in a headless browser and
// asserts deterministic keyboard/focus/announcement behavior for the primary
// workflows. The browser is pre-authenticated by injecting the session cookie
// (chromedp's fetch path is not relied upon for Set-Cookie), so the assertions
// exercise the actual dashboard and dialog code.

func a11yBrowser() string {
	for _, x := range []string{"chromium-browser", "chromium", "google-chrome", "google-chrome-stable"} {
		if p, e := exec.LookPath(x); e == nil {
			return p
		}
	}
	return ""
}

func a11yContext(t *testing.T) context.Context {
	t.Helper()
	// Browser integration tests are not race-relevant and time out under -race
	// instrumentation; skip when the race detector is active.
	if raceEnabled {
		t.Skip("browser accessibility tests skipped under -race")
	}
	bin := a11yBrowser()
	if bin == "" {
		t.Skip("no browser runtime installed")
	}
	alloc, cancelAlloc := chromedp.NewExecAllocator(context.Background(),
		chromedp.ExecPath(bin), chromedp.Headless, chromedp.DisableGPU, chromedp.NoFirstRun)
	ctx, cancelCtx := chromedp.NewContext(alloc)
	ctx, cancelTimeout := context.WithTimeout(ctx, 60*time.Second)
	t.Cleanup(func() { cancelTimeout(); cancelCtx(); cancelAlloc() })
	return ctx
}

func a11yServer(t *testing.T) *httptest.Server {
	t.Helper()
	st, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = st.Close() })
	s := New(config.Config{}, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return srv
}

// a11ySetup performs setup over HTTP and injects the session cookie into the
// browser, then navigates so boot() shows the dashboard.
func a11ySetup(t *testing.T, ctx context.Context, srv *httptest.Server) {
	t.Helper()
	resp, err := http.Post(srv.URL+"/api/setup", "application/json",
		bytes.NewBufferString(`{"email":"admin@example.com","password":"secret7"}`))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 201 {
		t.Fatalf("setup %d", resp.StatusCode)
	}
	var sessionCookie string
	for _, c := range resp.Cookies() {
		if c.Name == "webfleet_session" {
			sessionCookie = c.Value
		}
	}
	if sessionCookie == "" {
		t.Fatal("no session cookie from setup")
	}
	if err := chromedp.Run(ctx, chromedp.Navigate(srv.URL)); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, network.SetCookie("webfleet_session", sessionCookie).WithURL(srv.URL)); err != nil {
		t.Fatalf("inject cookie: %v", err)
	}
	if err := chromedp.Run(ctx, chromedp.Navigate(srv.URL)); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, chromedp.WaitVisible(`#dashboard`)); err != nil {
		t.Fatalf("dashboard did not appear with injected session: %v", err)
	}
}

func activeElementID(ctx context.Context) (string, error) {
	var id string
	err := chromedp.Run(ctx, chromedp.Evaluate(`document.activeElement && document.activeElement.id`, &id))
	return id, err
}

// TestA11yDialogInitialFocusAndReturn proves dialogs take initial focus on
// open, Escape closes them, and focus returns to the opening element.
func TestA11yDialogInitialFocusAndReturn(t *testing.T) {
	ctx := a11yContext(t)
	srv := a11yServer(t)
	a11ySetup(t, ctx, srv)
	// The fleet view renders the add-site button.
	if err := chromedp.Run(ctx, chromedp.WaitVisible(`#add-site-head`)); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, chromedp.Click(`#add-site-head`)); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, chromedp.WaitVisible(`#site-dialog`)); err != nil {
		t.Fatal(err)
	}
	id, err := activeElementID(ctx)
	if err != nil || id != "site-name" {
		t.Fatalf("dialog initial focus = %q (want site-name), err=%v", id, err)
	}
	// Escape closes the dialog; native <dialog> returns focus to the opener.
	if err := chromedp.Run(ctx, chromedp.KeyEvent("\u001b")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	var open bool
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.getElementById('site-dialog').open`, &open)); err != nil {
		t.Fatal(err)
	}
	if open {
		t.Fatal("dialog did not close on Escape")
	}
	id, err = activeElementID(ctx)
	if err != nil || id != "add-site-head" {
		t.Fatalf("focus after dialog close = %q (want add-site-head), err=%v", id, err)
	}
}

// TestA11yLoginErrorAnnounced proves failed login surfaces an announced error
// on the auth screen (role=alert).
// TestA11yFormErrorAnnounced proves the auth screen surfaces a validation
// error in a role=alert live region. Native HTML5 validation is stripped so
// the application's own error path is exercised deterministically.
func TestA11yFormErrorAnnounced(t *testing.T) {
	ctx := a11yContext(t)
	srv := a11yServer(t)
	if err := chromedp.Run(ctx,
		chromedp.Navigate(srv.URL),
		chromedp.WaitVisible(`#auth-form`),
		chromedp.Evaluate(`document.getElementById('email').removeAttribute('required');document.getElementById('password').removeAttribute('required');document.getElementById('password').removeAttribute('minlength');true`, nil),
		chromedp.SetValue(`#email`, "admin@example.com"),
		chromedp.SetValue(`#password`, "x"),
		chromedp.Click(`#auth-submit`),
	); err != nil {
		t.Fatal(err)
	}
	var errText string
	deadline := time.Now().Add(5 * time.Second)
	for {
		if err := chromedp.Run(ctx, chromedp.Evaluate(`(document.getElementById('auth-error')||{}).textContent || ''`, &errText)); err != nil {
			t.Fatal(err)
		}
		if strings.TrimSpace(errText) != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("form error not announced within timeout")
		}
		time.Sleep(50 * time.Millisecond)
	}
	var role string
	if err := chromedp.Run(ctx, chromedp.Evaluate(`document.getElementById('auth-error').getAttribute('role')`, &role)); err != nil {
		t.Fatal(err)
	}
	if role != "alert" {
		t.Fatalf("auth-error role = %q, want alert", role)
	}
}
func TestA11yMobileNavToggle(t *testing.T) {
	ctx := a11yContext(t)
	srv := a11yServer(t)
	a11ySetup(t, ctx, srv)
	if err := chromedp.Run(ctx, chromedp.EmulateViewport(375, 700)); err != nil {
		t.Fatal(err)
	}
	if err := chromedp.Run(ctx, chromedp.Click(`#mobile-nav`)); err != nil {
		t.Fatal(err)
	}
	var expanded string
	var open bool
	var navLinks int
	if err := chromedp.Run(ctx,
		chromedp.Evaluate(`document.getElementById('mobile-nav').getAttribute('aria-expanded')`, &expanded),
		chromedp.Evaluate(`document.getElementById('primary-rail').classList.contains('mobile-open')`, &open),
		chromedp.Evaluate(`document.querySelectorAll('#primary-rail nav a').length`, &navLinks),
	); err != nil {
		t.Fatal(err)
	}
	if expanded != "true" || !open || navLinks == 0 {
		t.Fatalf("mobile nav expanded=%q open=%v links=%d", expanded, open, navLinks)
	}
}

// TestA11yKeyboardTabReachesControls proves the primary controls are reachable
// by keyboard traversal from the fleet view.
func TestA11yKeyboardTabReachesControls(t *testing.T) {
	ctx := a11yContext(t)
	srv := a11yServer(t)
	a11ySetup(t, ctx, srv)
	if err := chromedp.Run(ctx, chromedp.WaitVisible(`#add-site-head`)); err != nil {
		t.Fatal(err)
	}
	// Tab forward several times; the add-site button must be reachable.
	reached := false
	for i := 0; i < 12; i++ {
		if err := chromedp.Run(ctx, chromedp.KeyEvent("\u0009")); err != nil { // TAB
			t.Fatal(err)
		}
		time.Sleep(40 * time.Millisecond)
		id, err := activeElementID(ctx)
		if err == nil && id == "add-site-head" {
			reached = true
			break
		}
	}
	if !reached {
		t.Fatal("add-site-head not reachable by keyboard tabbing")
	}
}

var _ = json.Marshal
