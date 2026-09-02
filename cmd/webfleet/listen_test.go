package main

import (
	"flag"
	"os"
	"testing"
)

// listenerFlags parses raw argv through the same flag definitions the CLI uses
// so "provided but empty" (--host "") is distinguishable from "not provided".
func listenerFlags(args ...string) (h, p, l string, hs, ps, ls bool) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.String("host", "", "")
	fs.String("port", "", "")
	fs.String("listen", "", "")
	_ = fs.Parse(args)
	return fs.Lookup("host").Value.String(),
		fs.Lookup("port").Value.String(),
		fs.Lookup("listen").Value.String(),
		flagProvided(fs, "host"),
		flagProvided(fs, "port"),
		flagProvided(fs, "listen")
}

func TestResolveListenerDefaults(t *testing.T) {
	os.Unsetenv("WEBFLEET_HOST")
	os.Unsetenv("WEBFLEET_PORT")
	os.Unsetenv("WEBFLEET_LISTEN")
	h, p, l, hs, ps, ls := listenerFlags()
	addr, err := resolveListener(h, p, l, hs, ps, ls)
	if err != nil {
		t.Fatal(err)
	}
	if addr != "127.0.0.1:7336" {
		t.Fatalf("default listener = %q want 127.0.0.1:7336", addr)
	}
}

func TestResolveListenerEnvOnly(t *testing.T) {
	os.Unsetenv("WEBFLEET_LISTEN")
	t.Setenv("WEBFLEET_HOST", "0.0.0.0")
	t.Setenv("WEBFLEET_PORT", "7402")
	h, p, l, hs, ps, ls := listenerFlags()
	addr, err := resolveListener(h, p, l, hs, ps, ls)
	if err != nil {
		t.Fatal(err)
	}
	if addr != "0.0.0.0:7402" {
		t.Fatalf("env listener = %q want 0.0.0.0:7402", addr)
	}
}

func TestResolveListenerCLIOnly(t *testing.T) {
	os.Unsetenv("WEBFLEET_LISTEN")
	t.Setenv("WEBFLEET_HOST", "192.0.2.1")
	t.Setenv("WEBFLEET_PORT", "9999")
	h, p, l, hs, ps, ls := listenerFlags("--host", "127.0.0.1", "--port", "7402")
	addr, err := resolveListener(h, p, l, hs, ps, ls)
	if err != nil {
		t.Fatal(err)
	}
	if addr != "127.0.0.1:7402" {
		t.Fatalf("cli listener = %q want 127.0.0.1:7402", addr)
	}
}

func TestResolveListenerCLIOverridesEnv(t *testing.T) {
	os.Unsetenv("WEBFLEET_LISTEN")
	t.Setenv("WEBFLEET_HOST", "0.0.0.0")
	t.Setenv("WEBFLEET_PORT", "9000")
	h, p, l, hs, ps, ls := listenerFlags("--host", "127.0.0.1", "--port", "7402")
	addr, err := resolveListener(h, p, l, hs, ps, ls)
	if err != nil {
		t.Fatal(err)
	}
	if addr != "127.0.0.1:7402" {
		t.Fatalf("cli should override env: %q", addr)
	}
}

func TestResolveListenerInvalidPorts(t *testing.T) {
	os.Unsetenv("WEBFLEET_LISTEN")
	for _, p := range []string{"abc", "0", "-5", "65536", "70000", "7 4 0 2", "7402x"} {
		h, pp, l, hs, ps, ls := listenerFlags("--host", "127.0.0.1", "--port", p)
		if _, err := resolveListener(h, pp, l, hs, ps, ls); err == nil {
			t.Fatalf("invalid --port %q accepted", p)
		}
	}
	for _, p := range []string{"abc", "0", "-5", "65536", "70000"} {
		t.Setenv("WEBFLEET_HOST", "127.0.0.1")
		t.Setenv("WEBFLEET_PORT", p)
		h, pp, l, hs, ps, ls := listenerFlags()
		if _, err := resolveListener(h, pp, l, hs, ps, ls); err == nil {
			t.Fatalf("invalid WEBFLEET_PORT %q accepted", p)
		}
	}
}

func TestResolveListenerEmptyEnvFails(t *testing.T) {
	os.Unsetenv("WEBFLEET_LISTEN")
	t.Setenv("WEBFLEET_HOST", "127.0.0.1")
	t.Setenv("WEBFLEET_PORT", "")
	h, p, l, hs, ps, ls := listenerFlags()
	if _, err := resolveListener(h, p, l, hs, ps, ls); err == nil {
		t.Fatal("empty WEBFLEET_PORT accepted")
	}
	t.Setenv("WEBFLEET_HOST", "")
	t.Setenv("WEBFLEET_PORT", "7336")
	if _, err := resolveListener(h, p, l, hs, ps, ls); err == nil {
		t.Fatal("empty WEBFLEET_HOST accepted")
	}
}

func TestResolveListenerEmptyCLIValuesFail(t *testing.T) {
	os.Unsetenv("WEBFLEET_LISTEN")
	t.Setenv("WEBFLEET_HOST", "127.0.0.1")
	t.Setenv("WEBFLEET_PORT", "7336")
	h, p, l, hs, ps, ls := listenerFlags("--host", "", "--port", "7336")
	if _, err := resolveListener(h, p, l, hs, ps, ls); err == nil {
		t.Fatal("empty --host accepted")
	}
	t.Setenv("WEBFLEET_HOST", "127.0.0.1")
	t.Setenv("WEBFLEET_PORT", "7336")
	h, p, l, hs, ps, ls = listenerFlags("--host", "127.0.0.1", "--port", "")
	if _, err := resolveListener(h, p, l, hs, ps, ls); err == nil {
		t.Fatal("empty --port accepted")
	}
}

func TestResolveListenerIPv6(t *testing.T) {
	os.Unsetenv("WEBFLEET_LISTEN")
	t.Setenv("WEBFLEET_HOST", "::1")
	t.Setenv("WEBFLEET_PORT", "7336")
	h, p, l, hs, ps, ls := listenerFlags()
	addr, err := resolveListener(h, p, l, hs, ps, ls)
	if err != nil {
		t.Fatal(err)
	}
	if addr != "[::1]:7336" {
		t.Fatalf("IPv6 listener = %q want [::1]:7336", addr)
	}
}

func TestResolveListenerLegacyListen(t *testing.T) {
	os.Unsetenv("WEBFLEET_LISTEN")
	t.Setenv("WEBFLEET_HOST", "0.0.0.0")
	t.Setenv("WEBFLEET_PORT", "9000")
	h, p, l, hs, ps, ls := listenerFlags("--listen", "127.0.0.1:8080")
	addr, err := resolveListener(h, p, l, hs, ps, ls)
	if err != nil {
		t.Fatal(err)
	}
	if addr != "127.0.0.1:8080" {
		t.Fatalf("legacy --listen = %q", addr)
	}
	// WEBFLEET_LISTEN environment is honored as the legacy single-address form.
	os.Unsetenv("WEBFLEET_HOST")
	os.Unsetenv("WEBFLEET_PORT")
	t.Setenv("WEBFLEET_LISTEN", "127.0.0.1:9000")
	h, p, l, hs, ps, ls = listenerFlags()
	addr, err = resolveListener(h, p, l, hs, ps, ls)
	if err != nil {
		t.Fatal(err)
	}
	if addr != "127.0.0.1:9000" {
		t.Fatalf("WEBFLEET_LISTEN = %q", addr)
	}
	// Legacy form combined with --host/--port must fail.
	os.Unsetenv("WEBFLEET_LISTEN")
	h2, p2, l2, hs2, ps2, ls2 := listenerFlags("--host", "127.0.0.1", "--port", "7402", "--listen", "127.0.0.1:8080")
	if _, err := resolveListener(h2, p2, l2, hs2, ps2, ls2); err == nil {
		t.Fatal("--listen combined with --host/--port accepted")
	}
}

func TestResolveListenerTrimsWhitespace(t *testing.T) {
	os.Unsetenv("WEBFLEET_LISTEN")
	h, p, l, hs, ps, ls := listenerFlags("--host", "  127.0.0.1  ", "--port", "  7402  ")
	host, port, err := resolveHostPort(h, p, hs, ps)
	if err != nil {
		t.Fatal(err)
	}
	if host != "127.0.0.1" || port != "7402" {
		t.Fatalf("cli trimmed host/port = %q/%q want 127.0.0.1/7402", host, port)
	}
	addr, err := resolveListener(h, p, l, hs, ps, ls)
	if err != nil {
		t.Fatal(err)
	}
	if addr != "127.0.0.1:7402" {
		t.Fatalf("cli listener = %q want 127.0.0.1:7402", addr)
	}
	t.Setenv("WEBFLEET_HOST", "  0.0.0.0  ")
	t.Setenv("WEBFLEET_PORT", "  7403  ")
	h, p, l, hs, ps, ls = listenerFlags()
	host, port, err = resolveHostPort(h, p, hs, ps)
	if err != nil {
		t.Fatal(err)
	}
	if host != "0.0.0.0" || port != "7403" {
		t.Fatalf("env trimmed host/port = %q/%q want 0.0.0.0/7403", host, port)
	}
	addr, err = resolveListener(h, p, l, hs, ps, ls)
	if err != nil {
		t.Fatal(err)
	}
	if addr != "0.0.0.0:7403" {
		t.Fatalf("env listener = %q want 0.0.0.0:7403", addr)
	}
}

func TestResolveListenerWhitespaceOnlyFails(t *testing.T) {
	os.Unsetenv("WEBFLEET_LISTEN")
	h, p, l, hs, ps, ls := listenerFlags("--host", "   ", "--port", "   ")
	if _, err := resolveListener(h, p, l, hs, ps, ls); err == nil {
		t.Fatal("whitespace-only --host/--port accepted")
	}
	t.Setenv("WEBFLEET_HOST", "   ")
	t.Setenv("WEBFLEET_PORT", "   ")
	h, p, l, hs, ps, ls = listenerFlags()
	if _, err := resolveListener(h, p, l, hs, ps, ls); err == nil {
		t.Fatal("whitespace-only WEBFLEET_HOST/WEBFLEET_PORT accepted")
	}
	t.Setenv("WEBFLEET_HOST", "   ")
	t.Setenv("WEBFLEET_PORT", "7402")
	h, p, l, hs, ps, ls = listenerFlags()
	if _, err := resolveListener(h, p, l, hs, ps, ls); err == nil {
		t.Fatal("whitespace-only host accepted with valid port")
	}
}

func TestValidatePort(t *testing.T) {
	for _, ok := range []string{"1", "7336", "65535"} {
		if err := validatePort(ok); err != nil {
			t.Fatalf("valid port %q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"", "0", "-1", "65536", "7336.5", "x"} {
		if err := validatePort(bad); err == nil {
			t.Fatalf("invalid port %q accepted", bad)
		}
	}
}

func TestResolveListenerExplicitPortOverridesLegacyEnv(t *testing.T) {
	os.Unsetenv("WEBFLEET_HOST")
	os.Unsetenv("WEBFLEET_PORT")
	t.Setenv("WEBFLEET_LISTEN", "127.0.0.1:8080")
	h, p, l, hs, ps, ls := listenerFlags("--port", "7402")
	addr, err := resolveListener(h, p, l, hs, ps, ls)
	if err != nil {
		t.Fatal(err)
	}
	if addr != "127.0.0.1:7402" {
		t.Fatalf("explicit --port must override WEBFLEET_LISTEN: %q", addr)
	}
}

func TestResolveListenerExplicitHostOverridesLegacyEnv(t *testing.T) {
	os.Unsetenv("WEBFLEET_PORT")
	t.Setenv("WEBFLEET_LISTEN", "127.0.0.1:8080")
	t.Setenv("WEBFLEET_HOST", "0.0.0.0")
	h, p, l, hs, ps, ls := listenerFlags("--host", "10.0.0.1", "--port", "7402")
	addr, err := resolveListener(h, p, l, hs, ps, ls)
	if err != nil {
		t.Fatal(err)
	}
	if addr != "10.0.0.1:7402" {
		t.Fatalf("explicit --host/--port must override WEBFLEET_LISTEN: %q", addr)
	}
}

func TestResolveListenerExplicitListenOverridesHostPortEnv(t *testing.T) {
	t.Setenv("WEBFLEET_HOST", "0.0.0.0")
	t.Setenv("WEBFLEET_PORT", "9000")
	h, p, l, hs, ps, ls := listenerFlags("--listen", "127.0.0.1:8080")
	addr, err := resolveListener(h, p, l, hs, ps, ls)
	if err != nil {
		t.Fatal(err)
	}
	if addr != "127.0.0.1:8080" {
		t.Fatalf("explicit --listen must override host/port env: %q", addr)
	}
}

func TestResolveListenerEnvConflict(t *testing.T) {
	// Only environment variables involved: legacy WEBFLEET_LISTEN conflicts with
	// WEBFLEET_HOST or WEBFLEET_PORT and must fail rather than silently pick one.
	t.Setenv("WEBFLEET_LISTEN", "127.0.0.1:8080")
	t.Setenv("WEBFLEET_HOST", "0.0.0.0")
	h, p, l, hs, ps, ls := listenerFlags()
	if _, err := resolveListener(h, p, l, hs, ps, ls); err == nil {
		t.Fatal("WEBFLEET_LISTEN + WEBFLEET_HOST conflict accepted")
	}
	os.Unsetenv("WEBFLEET_HOST")
	t.Setenv("WEBFLEET_PORT", "9000")
	if _, err := resolveListener(h, p, l, hs, ps, ls); err == nil {
		t.Fatal("WEBFLEET_LISTEN + WEBFLEET_PORT conflict accepted")
	}
}

func TestListenerOverrideSelected(t *testing.T) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.String("host", "", "")
	fs.String("port", "", "")
	fs.String("listen", "", "")
	_ = fs.Parse(nil)
	os.Unsetenv("WEBFLEET_HOST")
	os.Unsetenv("WEBFLEET_PORT")
	os.Unsetenv("WEBFLEET_LISTEN")
	if listenerOverrideSelected(fs) {
		t.Fatal("bare invocation must not override the durable listener")
	}
	// WEBFLEET_LISTEN only: not an explicit new-form override.
	t.Setenv("WEBFLEET_LISTEN", "127.0.0.1:8080")
	if listenerOverrideSelected(fs) {
		t.Fatal("legacy WEBFLEET_LISTEN must not override the durable listener")
	}
	// WEBFLEET_HOST / WEBFLEET_PORT environment: override.
	os.Unsetenv("WEBFLEET_LISTEN")
	t.Setenv("WEBFLEET_HOST", "0.0.0.0")
	if !listenerOverrideSelected(fs) {
		t.Fatal("WEBFLEET_HOST must override the durable listener")
	}
	os.Unsetenv("WEBFLEET_HOST")
	t.Setenv("WEBFLEET_PORT", "7402")
	if !listenerOverrideSelected(fs) {
		t.Fatal("WEBFLEET_PORT must override the durable listener")
	}
	// Explicit CLI --host/--port: override.
	os.Unsetenv("WEBFLEET_PORT")
	_ = fs.Set("host", "127.0.0.1")
	_ = fs.Set("port", "7402")
	if !listenerOverrideSelected(fs) {
		t.Fatal("explicit --host/--port must override the durable listener")
	}
}
