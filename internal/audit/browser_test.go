package audit

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/web-fleet/webfleet/internal/netguard"
	"github.com/web-fleet/webfleet/internal/sqlite"
	"github.com/web-fleet/webfleet/internal/store"
)

func mustAddr(s string) netip.Addr { return netip.MustParseAddr(s) }

// testSandbox returns "strict" by default; environments that cannot sandbox
// Chromium (for example GitHub Actions runners with user namespaces disabled)
// opt into the documented "allow-no-sandbox" mode via the CI-only
// WEBFLEET_AUDIT_TEST_ALLOW_NO_SANDBOX variable. The production default is
// never changed; TestBrowserArgsSandboxPosture still proves strict never
// passes --no-sandbox.
func testSandbox() string {
	if os.Getenv("WEBFLEET_AUDIT_TEST_ALLOW_NO_SANDBOX") != "" {
		return "allow-no-sandbox"
	}
	return "strict"
}

func newTestGuard(allowPrivate bool, r netguard.Resolver) netguard.Guard {
	g := netguard.New()
	if r != nil {
		g.Resolver = r
	}
	g.AllowPrivate = allowPrivate
	return g
}

func newFixtureServer(hits *atomic.Int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, "<html lang=\"en\"><head><title>Guarded Fixture</title><meta name=\"description\" content=\"probe\"></head><body><img src=\"/x.png\" alt=\"a\"><h1>hi</h1></body></html>")
	}))
}

func findBrowser(t *testing.T) string {
	t.Helper()
	for _, x := range []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable"} {
		if p, e := exec.LookPath(x); e == nil {
			return p
		}
	}
	t.Skip("no browser runtime installed")
	return ""
}

func TestBrowserArgsSandboxPosture(t *testing.T) {
	strict := browserArgs("strict", "127.0.0.1:9", "https://example.com/")
	if contains(strict, "--no-sandbox") {
		t.Fatal("strict sandbox must not pass --no-sandbox")
	}
	for _, want := range []string{
		"--headless=new", "--disable-quic",
		"--proxy-server=http://127.0.0.1:9",
		"--proxy-bypass-list=<-loopback>",
		"--dump-dom", "https://example.com/",
	} {
		if !contains(strict, want) {
			t.Fatalf("strict args missing %q: %v", want, strict)
		}
	}
	optin := browserArgs("allow-no-sandbox", "127.0.0.1:9", "https://example.com/")
	if !contains(optin, "--no-sandbox") {
		t.Fatal("allow-no-sandbox must pass --no-sandbox")
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func TestLimitedBufferBoundsOutput(t *testing.T) {
	var b limitedBuffer
	b.limit = 8
	if _, err := b.Write([]byte("0123456789abcdef")); err != nil {
		t.Fatal(err)
	}
	if b.String() != "01234567" {
		t.Fatalf("buf = %q", b.String())
	}
	// Subsequent writes are consumed and discarded without error so a hostile
	// page cannot block the child on a full pipe.
	if _, err := b.Write(make([]byte, 4096)); err != nil {
		t.Fatal(err)
	}
	if len(b.buf) != 8 {
		t.Fatalf("buffer grew past limit: %d", len(b.buf))
	}
}

// TestBrowserTrafficFlowsThroughGuardedProxy is the central acceptance test:
// the target hostname audit.test resolves ONLY through the guarded proxy's
// injected resolver. If Chromium bypassed the proxy and resolved it itself, it
// would fail (NXDOMAIN) and never reach the fixture server. The browser must
// therefore have routed its navigation through the Go-controlled proxy.
func TestBrowserTrafficFlowsThroughGuardedProxy(t *testing.T) {
	bin := findBrowser(t)
	var hits atomic.Int32
	srv := newFixtureServer(&hits)
	defer srv.Close()

	runner := BrowserRunner{
		Binary:  bin,
		Sandbox: testSandbox(),
		// audit.test resolves only through this injected resolver, so a proxy
		// bypass would leave Chromium unable to reach the fixture at all.
		guard: newTestGuard(true, mapResolver{"audit.test": {mustAddr("127.0.0.1")}}),
		// Headless Chromium under -race on a loaded CI runner is far slower than
		// the production defaultAuditTimeout, so this acceptance test uses a
		// generous explicit timeout; the production audit bound stays 45s.
		Timeout: 120 * time.Second,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Second)
	defer cancel()
	_, port, _ := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	target := "http://audit.test:" + port + "/"
	res, err := runner.Run(ctx, target)
	if err != nil {
		t.Fatalf("browser audit failed: %v", err)
	}
	if hits.Load() == 0 {
		t.Fatal("fixture server was never reached: browser bypassed the guarded proxy")
	}
	if res.Status != "complete" {
		t.Fatalf("status = %s, want complete (%v)", res.Status, res.Error)
	}
	if res.Discoverability != 100 {
		t.Fatalf("fixture has a title; discoverability = %d, want 100", res.Discoverability)
	}
}

// TestBrowserCannotReachLoopbackDirectly proves the default production guard:
// with AllowPrivate=false and no injected resolver, a loopback-only target is
// refused before the browser even launches.
func TestBrowserCannotReachLoopbackDirectly(t *testing.T) {
	bin := findBrowser(t)
	var hits atomic.Int32
	srv := newFixtureServer(&hits)
	defer srv.Close()
	runner := BrowserRunner{Binary: bin, Sandbox: testSandbox(), guard: newTestGuard(false, nil)}
	_, err := runner.Run(context.Background(), "http://127.0.0.1/")
	if err == nil {
		t.Fatal("loopback target accepted by guarded browser runner")
	}
	if !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestBrowserTimeoutKillsProcess proves a hung target cannot hold an audit
// beyond the configured timeout, and that cancellation is not silently
// accepted as a successful audit.
func TestBrowserTimeoutKillsProcess(t *testing.T) {
	bin := findBrowser(t)
	// The fixture accepts the connection and never responds, so the browser
	// would hang without the timeout.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c // hold the connection open without responding
		}
	}()
	port := strings.Split(ln.Addr().String(), ":")[1]
	runner := BrowserRunner{
		Binary:  bin,
		Sandbox: testSandbox(),
		Timeout: 3 * time.Second,
		guard:   newTestGuard(true, mapResolver{"hang.test": {mustAddr("127.0.0.1")}}),
	}
	start := time.Now()
	_, err = runner.Run(context.Background(), "http://hang.test:"+port+"/")
	if err == nil {
		t.Fatal("hung audit reported success")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("unexpected error: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 12*time.Second {
		t.Fatalf("timeout did not bound execution: %v", elapsed)
	}
}

// TestAuditIsManualAndHistoryOptIn proves audits only exist when explicitly
// run, and history stays off unless enabled.
func TestAuditIsManualAndHistoryOptIn(t *testing.T) {
	st, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	if e = sqlite.Exec(st.DB, `INSERT INTO sites(organization_id,name,primary_url,created_at,updated_at) VALUES(1,'x','https://example.com',?,?)`, store.Now(), store.Now()); e != nil {
		t.Fatal(e)
	}
	s := NewWithRunner(st, fake{})
	// Nothing runs automatically on creation.
	h, _ := s.History(1)
	if len(h) != 0 {
		t.Fatal("audit ran without an explicit request")
	}
	// Explicit run appears once; a second run replaces the first while history
	// is off, and accumulates once enabled.
	s.Run(context.Background(), 1)
	s.Run(context.Background(), 1)
	h, _ = s.History(1)
	if len(h) != 1 {
		t.Fatalf("history default: %d", len(h))
	}
	if on, _ := s.HistoryEnabled(1); on {
		t.Fatal("history must default to opt-in/off")
	}
	s.SetHistory(1, true)
	s.Run(context.Background(), 1)
	h, _ = s.History(1)
	if len(h) != 2 {
		t.Fatalf("history enabled: %d", len(h))
	}
}

// blockingRunner blocks until released, so tests can observe the audit
// concurrency semaphore.
type blockingRunner struct {
	ch      chan struct{}
	started *int32
}

func (b blockingRunner) Run(ctx context.Context, u string) (Result, error) {
	atomic.AddInt32(b.started, 1)
	select {
	case <-b.ch:
		return Result{Status: "complete"}, nil
	case <-ctx.Done():
		return Result{}, ctx.Err()
	}
}

func TestAuditConcurrencyIsBounded(t *testing.T) {
	st, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	if e = sqlite.Exec(st.DB, `INSERT INTO sites(organization_id,name,primary_url,created_at,updated_at) VALUES(1,'x','https://example.com',?,?)`, store.Now(), store.Now()); e != nil {
		t.Fatal(e)
	}
	var started int32
	release := make(chan struct{})
	s := NewWithRunner(st, blockingRunner{ch: release, started: &started})
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = s.Run(context.Background(), 1)
		}()
	}
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(&started) < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if atomic.LoadInt32(&started) != 2 {
		t.Fatalf("expected exactly 2 concurrent audits, got %d", started)
	}
	time.Sleep(100 * time.Millisecond)
	if atomic.LoadInt32(&started) > 2 {
		t.Fatalf("third audit entered before a slot freed: %d", started)
	}
	close(release)
	wg.Wait()
	if atomic.LoadInt32(&started) != 3 {
		t.Fatalf("all audits must complete after release: %d", started)
	}
}
