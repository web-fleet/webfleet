package main

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/web-fleet/webfleet/internal/service"
)

// withStub dispatch replaces the lifecycle seam so runService can be exercised
// end-to-end without root or systemd, recording the parsed command.
func withStub(t *testing.T, stub func(serviceCommand) (string, error)) func(serviceCommand) (string, error) {
	t.Helper()
	old := execServiceCommand
	execServiceCommand = stub
	t.Cleanup(func() { execServiceCommand = old })
	return old
}

func TestRunServiceInstallParsesFlags(t *testing.T) {
	cases := []struct {
		name       string
		args       []string
		wantData   string
		wantListen string
	}{
		{"defaults", []string{"install"}, service.DefaultDataDir, "127.0.0.1:8090"},
		{"data", []string{"install", "--data", "/srv/webfleet"}, "/srv/webfleet", "127.0.0.1:8090"},
		{"data-with-spaces", []string{"install", "--data", "/srv/web fleet"}, "/srv/web fleet", "127.0.0.1:8090"},
		{"data-equals", []string{"install", "--data=/srv/webfleet"}, "/srv/webfleet", "127.0.0.1:8090"},
		{"listen", []string{"install", "--listen", "127.0.0.1:9000"}, service.DefaultDataDir, "127.0.0.1:9000"},
		{"both", []string{"install", "--data", "/srv/webfleet", "--listen", "127.0.0.1:9000"}, "/srv/webfleet", "127.0.0.1:9000"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var got serviceCommand
			withStub(t, func(c serviceCommand) (string, error) {
				got = c
				return "ok", nil
			})
			var out, errOut bytes.Buffer
			if code := runServiceIO(&out, &errOut, tc.args, "127.0.0.1:8090"); code != 0 {
				t.Fatalf("exit %d, stderr: %s", code, errOut.String())
			}
			if got.verb != "install" || got.data != tc.wantData || got.listen != tc.wantListen {
				t.Fatalf("parsed = %+v, want data=%q listen=%q", got, tc.wantData, tc.wantListen)
			}
		})
	}
}

func TestRunServiceInstallUsageErrors(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{"missing-data-value", []string{"install", "--data"}},
		{"missing-listen-value", []string{"install", "--listen"}},
		{"unknown-flag", []string{"install", "--bogus", "x"}},
		{"unexpected-positional", []string{"install", "/srv/webfleet"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withStub(t, func(serviceCommand) (string, error) { return "ok", nil })
			var out, errOut bytes.Buffer
			if code := runServiceIO(&out, &errOut, tc.args, "127.0.0.1:8090"); code != 2 {
				t.Fatalf("exit %d, want 2; stdout=%q stderr=%q", code, out.String(), errOut.String())
			}
			if errOut.Len() == 0 {
				t.Fatal("usage error printed no diagnostic")
			}
		})
	}
}

func TestRunServiceLogs(t *testing.T) {
	for _, follow := range []bool{false, true} {
		args := []string{"logs"}
		if follow {
			args = append(args, "--follow")
		}
		var got serviceCommand
		withStub(t, func(c serviceCommand) (string, error) {
			got = c
			return "journal", nil
		})
		var out, errOut bytes.Buffer
		if code := runServiceIO(&out, &errOut, args, "127.0.0.1:8090"); code != 0 {
			t.Fatalf("exit %d, stderr: %s", code, errOut.String())
		}
		if got.verb != "logs" || got.follow != follow {
			t.Fatalf("logs parsed = %+v, want follow=%v", got, follow)
		}
		if !strings.Contains(out.String(), "journal") {
			t.Fatalf("logs output not written to stdout: %q", out.String())
		}
	}
	// logs rejects extra positionals.
	withStub(t, func(c serviceCommand) (string, error) {
		return "", nil
	})
	var out, errOut bytes.Buffer
	if code := runServiceIO(&out, &errOut, []string{"logs", "--follow", "extra"}, "127.0.0.1:8090"); code != 2 {
		t.Fatalf("logs with positional exit %d, want 2", code)
	}
}

func TestRunServiceUpdate(t *testing.T) {
	var got serviceCommand
	withStub(t, func(c serviceCommand) (string, error) {
		got = c
		return serviceSuccessMessage(c), nil
	})
	var out, errOut bytes.Buffer
	if code := runServiceIO(&out, &errOut, []string{"update", "/tmp/art", "abc123"}, "127.0.0.1:8090"); code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, errOut.String())
	}
	if got.verb != "update" || got.artifact != "/tmp/art" || got.sha != "abc123" {
		t.Fatalf("update parsed = %+v", got)
	}
	if !strings.Contains(out.String(), "webfleet.service updated.") {
		t.Fatalf("update success message missing: %q", out.String())
	}

	for _, bad := range [][]string{
		{"update", "/tmp/art"},
		{"update", "/tmp/art", "abc", "extra"},
		{"update", "--data", "/tmp/art", "abc"},
	} {
		withStub(t, func(serviceCommand) (string, error) { return "", nil })
		var b1, b2 bytes.Buffer
		if code := runServiceIO(&b1, &b2, bad, "127.0.0.1:8090"); code != 2 {
			t.Fatalf("update %v exit %d, want 2", bad, code)
		}
	}
}

// TestServiceSuccessMessagesAreStateNeutral proves the real success-message
// semantics do not contradict the state-preserving lifecycle: install/update/
// rollback must not claim the service is active or was restarted, and uninstall
// must not hard-code a data directory that may differ from the managed one.
func TestServiceSuccessMessagesAreStateNeutral(t *testing.T) {
	verbs := []string{"install", "update", "rollback", "uninstall"}
	for _, verb := range verbs {
		msg := strings.ToLower(serviceSuccessMessage(serviceCommand{verb: verb}))
		if msg == "" {
			t.Fatalf("%s has no success message", verb)
		}
		for _, banned := range []string{"active", "restarted", "/var/lib/webfleet"} {
			if strings.Contains(msg, banned) {
				t.Fatalf("success message for %s overstates state: %q contains %q", verb, msg, banned)
			}
		}
	}
	// The verbs whose effect IS a state change may state it.
	for _, verb := range []string{"start", "stop", "restart", "enable", "disable"} {
		if serviceSuccessMessage(serviceCommand{verb: verb}) == "" {
			t.Fatalf("%s has no success message", verb)
		}
	}
}

func TestRunServiceNoArgsDefaultsToStatus(t *testing.T) {
	var got serviceCommand
	withStub(t, func(c serviceCommand) (string, error) {
		got = c
		return "status-body", nil
	})
	var out, errOut bytes.Buffer
	if code := runServiceIO(&out, &errOut, nil, "127.0.0.1:8090"); code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, errOut.String())
	}
	if got.verb != "status" {
		t.Fatalf("default verb = %q, want status", got.verb)
	}
}

func TestRunServiceSimpleVerbs(t *testing.T) {
	for _, verb := range []string{"start", "stop", "restart", "enable", "disable", "rollback", "uninstall"} {
		var got serviceCommand
		withStub(t, func(c serviceCommand) (string, error) {
			got = c
			return "ok", nil
		})
		var out, errOut bytes.Buffer
		if code := runServiceIO(&out, &errOut, []string{verb}, "127.0.0.1:8090"); code != 0 {
			t.Fatalf("%s exit %d, stderr: %s", verb, code, errOut.String())
		}
		if got.verb != verb {
			t.Fatalf("verb = %q, want %q", got.verb, verb)
		}
		// Positionals and flags are refused.
		withStub(t, func(serviceCommand) (string, error) { return "", nil })
		var b1, b2 bytes.Buffer
		if code := runServiceIO(&b1, &b2, []string{verb, "extra"}, "127.0.0.1:8090"); code != 2 {
			t.Fatalf("%s with positional exit %d, want 2", verb, code)
		}
	}
}

func TestRunServiceOperationalFailure(t *testing.T) {
	withStub(t, func(serviceCommand) (string, error) { return "", errors.New("boom") })
	var out, errOut bytes.Buffer
	if code := runServiceIO(&out, &errOut, []string{"start"}, "127.0.0.1:8090"); code != 1 {
		t.Fatalf("exit %d, want 1", code)
	}
	if !strings.Contains(errOut.String(), "boom") {
		t.Fatalf("operational failure not surfaced: %q", errOut.String())
	}
}

func TestRunServiceUnknownCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := runServiceIO(&out, &errOut, []string{"frobnicate"}, "127.0.0.1:8090"); code != 2 {
		t.Fatalf("exit %d, want 2", code)
	}
	if !strings.Contains(errOut.String(), "frobnicate") {
		t.Fatalf("unknown command diagnostic missing: %q", errOut.String())
	}
}

func TestRunServiceOutputWriters(t *testing.T) {
	// runService (the os.Stdout/os.Stderr wrapper) still compiles and routes to
	// the injected writers through runServiceIO; verify the wiring here.
	var got serviceCommand
	withStub(t, func(c serviceCommand) (string, error) {
		got = c
		return "success-line", nil
	})
	var out, errOut bytes.Buffer
	code := runServiceIO(&out, &errOut, []string{"install", "--listen", "127.0.0.1:9001"}, "127.0.0.1:8090")
	if code != 0 {
		t.Fatalf("exit %d", code)
	}
	if got.listen != "127.0.0.1:9001" {
		t.Fatalf("listen = %q", got.listen)
	}
	if !strings.Contains(out.String(), "success-line") {
		t.Fatalf("stdout missing success message: %q", out.String())
	}
	if errOut.Len() != 0 {
		t.Fatalf("unexpected stderr: %q", errOut.String())
	}
	_ = io.Discard
}
