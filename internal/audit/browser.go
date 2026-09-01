package audit

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/web-fleet/webfleet/internal/netguard"
)

const (
	defaultAuditTimeout = 45 * time.Second
	maxDOMOutput        = 16 << 20
	maxStderrOutput     = 1 << 20
	auditConcurrency    = 2
)

// BrowserRunner evaluates a page with a real browser. The browser is never
// allowed to resolve or dial the target itself: every connection it makes
// flows through an in-process GuardedProxy that applies the same public-network
// guard as monitoring and crawling. The target URL is also validated before
// launch as a fast-fail and defense-in-depth check.
type BrowserRunner struct {
	Binary    string
	Sandbox   string // "strict" (default) or "allow-no-sandbox"
	Timeout   time.Duration
	MaxOutput int64
	guard     netguard.Guard
}

func (b BrowserRunner) normalized() BrowserRunner {
	if b.Timeout <= 0 {
		b.Timeout = defaultAuditTimeout
	}
	if b.MaxOutput <= 0 {
		b.MaxOutput = maxDOMOutput
	}
	if b.Sandbox == "" {
		b.Sandbox = "strict"
	}
	if b.guard.Resolver == nil {
		b.guard = netguard.New()
	}
	return b
}

func (b BrowserRunner) Run(ctx context.Context, raw string) (Result, error) {
	b = b.normalized()
	target, err := url.Parse(raw)
	if err != nil {
		return Result{}, fmt.Errorf("audit target: %w", err)
	}
	if err := b.guard.ValidateURL(ctx, target); err != nil {
		return Result{}, fmt.Errorf("audit target blocked: %w", err)
	}
	bin := b.Binary
	if bin == "" {
		for _, x := range []string{"chromium", "chromium-browser", "google-chrome", "google-chrome-stable"} {
			if p, e := exec.LookPath(x); e == nil {
				bin = p
				break
			}
		}
	}
	if bin == "" {
		return Result{}, errors.New("browser audit runtime is not installed")
	}
	proxy, err := NewGuardedProxy(b.guard)
	if err != nil {
		return Result{}, fmt.Errorf("start audit proxy: %w", err)
	}
	defer proxy.Close()

	start := time.Now()
	ctx, cancel := context.WithTimeout(ctx, b.Timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, browserArgs(b.Sandbox, proxy.Addr(), raw)...)
	commandContextCmd(cmd)
	cmd.Cancel = func() error { return killTree(cmd.Process) }
	cmd.WaitDelay = 3 * time.Second
	var stdout limitedBuffer
	stdout.limit = b.MaxOutput
	var stderr limitedBuffer
	stderr.limit = maxStderrOutput
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("browser audit: %w", err)
	}
	waitErr := cmd.Wait()
	d := time.Since(start).Milliseconds()
	if waitErr != nil {
		if ctx.Err() != nil {
			return Result{}, fmt.Errorf("browser audit timed out after %s", b.Timeout)
		}
		return Result{}, fmt.Errorf("browser audit: %w", waitErr)
	}
	return scoreRendered(strings.ToLower(stdout.String()), d, raw), nil
}

// browserArgs builds the Chromium command line. The proxy flags force every
// request, including loopback, through the guarded proxy; --disable-quic stops
// Chromium from attempting a UDP path that would bypass the proxy; the
// background-networking flags reduce Chromium's out-of-band connectivity probes
// (which would otherwise reach public services through the guarded proxy).
// --no-sandbox is only added when the operator explicitly opted in via
// WEBFLEET_AUDIT_SANDBOX.
func browserArgs(sandbox, proxyAddr, target string) []string {
	args := []string{
		"--headless=new",
		"--disable-gpu",
		"--disable-dev-shm-usage",
		"--disable-quic",
		"--disable-background-networking",
		"--disable-component-update",
		"--disable-sync",
		"--no-first-run",
		"--no-default-browser-check",
		"--proxy-server=http://" + proxyAddr,
		"--proxy-bypass-list=<-loopback>",
		"--dump-dom",
	}
	if sandbox == "allow-no-sandbox" {
		args = append(args, "--no-sandbox")
	}
	return append(args, target)
}

func scoreRendered(html string, durationMS int64, u string) Result {
	find := []string{}
	a, bp, disc := 100, 100, 100
	if !strings.Contains(html, "<title") {
		disc -= 20
		find = append(find, "Missing document title")
	}
	if !strings.Contains(html, "name=\"description\"") && !strings.Contains(html, "name='description'") {
		disc -= 15
		find = append(find, "Missing meta description")
	}
	if strings.Contains(html, "<img") && !strings.Contains(html, " alt=") {
		a -= 15
		find = append(find, "Images may be missing alt text")
	}
	if !strings.Contains(html, "<html lang=") {
		a -= 10
		find = append(find, "Missing document language")
	}
	if strings.Contains(html, "http://") {
		bp -= 15
		find = append(find, "Rendered document contains insecure HTTP references")
	}
	perf := 100 - int(durationMS/50)
	if perf < 20 {
		perf = 20
	}
	if perf > 100 {
		perf = 100
	}
	return Result{Status: "complete", Performance: perf, Accessibility: a, BestPractices: bp, Discoverability: disc, Findings: find, DurationMS: durationMS, URL: u}
}

// limitedBuffer captures a bounded amount of child output so a hostile page
// cannot exhaust memory by making the browser emit unbounded text. Writes
// beyond the limit are consumed and discarded so the child never blocks.
type limitedBuffer struct {
	mu    sync.Mutex
	buf   []byte
	limit int64
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if int64(len(b.buf)) >= b.limit {
		return len(p), nil
	}
	room := int(b.limit - int64(len(b.buf)))
	if len(p) > room {
		b.buf = append(b.buf, p[:room]...)
		return len(p), nil
	}
	b.buf = append(b.buf, p...)
	return len(p), nil
}

func (b *limitedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}