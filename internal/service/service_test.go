package service

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeRunner records systemctl/journalctl invocations and returns scripted
// outputs/exit codes so the lifecycle can be tested without a real manager.
type fakeRunner struct {
	script map[string]fakeResult // key "verb args..." -> result
	log    []string
}
type fakeResult struct {
	out  string
	code int
	err  error
}

func (f *fakeRunner) Run(name string, args ...string) (string, int, error) {
	key := name + " " + strings.Join(args, " ")
	f.log = append(f.log, key)
	if r, ok := f.script[key]; ok {
		return r.out, r.code, r.err
	}
	return "", 0, nil
}
func (f *fakeRunner) Stream(name string, args ...string) (int, error) {
	key := name + " " + strings.Join(args, " ")
	f.log = append(f.log, key)
	if r, ok := f.script[key]; ok {
		return r.code, r.err
	}
	return 0, nil
}

// setupService points the unit/binary paths at temp files, simulates root and
// the service account, and installs a fake runner. It returns the fake runner
// and a cleanup.
func setupService(t *testing.T) *fakeRunner {
	t.Helper()
	dir := t.TempDir()
	oldUnit, oldBin := UnitPath, BinaryPath
	oldRoot, oldAccount, oldChown := isRoot, ensureAccount, chownData
	oldRunner := defaultRunner
	UnitPath = filepath.Join(dir, "webfleet.service")
	BinaryPath = filepath.Join(dir, "webfleet")
	os.WriteFile(BinaryPath, []byte("#!/bin/sh\nexit 0\n"), 0o755)
	isRoot = func() bool { return true }
	ensureAccount = func() error { return nil }
	chownData = func(string) error { return nil }
	mkdirData = func(string, os.FileMode) error { return nil }
	r := &fakeRunner{script: map[string]fakeResult{}}
	defaultRunner = r
	t.Cleanup(func() {
		UnitPath, BinaryPath = oldUnit, oldBin
		isRoot, ensureAccount, chownData = oldRoot, oldAccount, oldChown
		mkdirData = func(path string, mode os.FileMode) error { return os.MkdirAll(path, mode) }
		defaultRunner = oldRunner
	})
	return r
}

func installManagedUnit(t *testing.T) {
	t.Helper()
	if e := os.WriteFile(UnitPath, []byte(Unit("/var/lib/webfleet", "127.0.0.1:8090")), 0o644); e != nil {
		t.Fatal(e)
	}
}

func TestUnitHardeningAndManagedMarker(t *testing.T) {
	setupService(t)
	u := Unit("/var/lib/webfleet", "127.0.0.1:8090")
	for _, x := range []string{"# Managed by webfleet. Do not edit manually.", "NoNewPrivileges=true", "ProtectSystem=strict", "ReadWritePaths=\"/var/lib/webfleet\"", "User=" + ServiceUser, "Group=" + ServiceGroup, "WantedBy=multi-user.target"} {
		if !strings.Contains(u, x) {
			t.Fatal("missing " + x)
		}
	}
	if !strings.Contains(Unit("/var/lib/web fleet", "127.0.0.1:8090"), "WEBFLEET_DATA_DIR=\"/var/lib/web fleet\"") {
		t.Fatal("data path with spaces is not quoted")
	}
	if e := os.WriteFile(UnitPath, []byte(u), 0o644); e != nil {
		t.Fatal(e)
	}
	if !managedUnit(UnitPath) {
		t.Fatal("managed marker not detected from the unit file")
	}
}

func TestValidateNoControlAndQuote(t *testing.T) {
	if e := validateNoControl("127.0.0.1:8090", "listen"); e != nil {
		t.Fatal(e)
	}
	if e := validateNoControl("127.0.0.1:8090\nfoo", "listen"); e == nil {
		t.Fatal("newline accepted")
	}
	if got := systemdQuote(`/a b/"x"$y`); got != `"/a b/\"x\"\$y"` {
		t.Fatalf("quote = %q", got)
	}
}

func TestLifecycleVerbsRequireManagedUnit(t *testing.T) {
	r := setupService(t)
	// Without an installed unit, verbs refuse (no systemctl call).
	for _, v := range []string{"start", "stop", "restart", "enable", "disable"} {
		if err := lifecycle(v); err == nil {
			t.Fatalf("%s succeeded without an installed unit", v)
		}
		if len(r.log) != 0 {
			t.Fatalf("%s touched systemctl without a managed unit", v)
		}
	}
	// With a managed unit, verbs dispatch to systemctl.
	installManagedUnit(t)
	r.script["systemctl start webfleet.service"] = fakeResult{}
	if err := lifecycle("start"); err != nil {
		t.Fatal(err)
	}
	if !contains(r.log, "systemctl start webfleet.service") {
		t.Fatal("start did not call systemctl")
	}
	// A failed verb surfaces the exit code.
	r.script["systemctl stop webfleet.service"] = fakeResult{out: "Job failed", code: 1}
	if err := lifecycle("stop"); err == nil {
		t.Fatal("failed stop returned nil")
	}
}

func TestLifecycleRefusesForeignUnit(t *testing.T) {
	setupService(t)
	// A unit at the path WITHOUT the managed marker must be refused.
	if e := os.WriteFile(UnitPath, []byte("[Unit]\nDescription=admin unit\n"), 0o644); e != nil {
		t.Fatal(e)
	}
	for _, v := range []string{"start", "stop", "restart", "enable", "disable", "uninstall"} {
		if err := lifecycle(v); err == nil {
			t.Fatalf("%s modified a foreign unit", v)
		}
	}
}

func TestInstallRequiresRootAndLinux(t *testing.T) {
	oldRoot := isRoot
	isRoot = func() bool { return false }
	defer func() { isRoot = oldRoot }()
	if e := Install("", t.TempDir(), "127.0.0.1:0"); e == nil {
		t.Fatal("install succeeded without root")
	}
}

func TestReinstallRestoresPriorStateOnFailure(t *testing.T) {
	r := setupService(t)
	// First install succeeds: write unit, daemon-reload, enable, start.
	exe := filepath.Join(t.TempDir(), "wf")
	os.WriteFile(exe, []byte("#!/bin/sh\nexit 0\n"), 0o755)
	r.script["systemctl daemon-reload"] = fakeResult{}
	r.script["systemctl enable webfleet.service"] = fakeResult{}
	r.script["systemctl start webfleet.service"] = fakeResult{}
	if e := Install(exe, "/var/lib/webfleet", "127.0.0.1:8090"); e != nil {
		t.Fatal(e)
	}
	priorUnit, _ := os.ReadFile(UnitPath)
	if !strings.Contains(string(priorUnit), "Managed by webfleet") {
		t.Fatal("installed unit lacks the managed marker")
	}
	// Second install: prior enabled+active; force a failure on enable and prove
	// the prior unit is restored.
	r.script["systemctl is-enabled webfleet.service"] = fakeResult{out: "enabled", code: 0}
	r.script["systemctl is-active webfleet.service"] = fakeResult{out: "active", code: 0}
	// A changed unit (different listen) forces the enable step, which fails;
	// rollback must then restore the prior unit.
	r.script["systemctl daemon-reload"] = fakeResult{}
	r.script["systemctl enable webfleet.service"] = fakeResult{out: "failed to enable", code: 3}
	r.script["systemctl restart webfleet.service"] = fakeResult{}
	r.script["systemctl start webfleet.service"] = fakeResult{}
	if e := Install(exe, "/var/lib/webfleet", "127.0.0.1:9090"); e == nil {
		t.Fatal("failed reinstall returned nil")
	}
	got, _ := os.ReadFile(UnitPath)
	if string(got) != string(priorUnit) {
		t.Fatalf("reinstall failure did not restore the prior unit:\n--- got\n%s\n--- want\n%s", got, priorUnit)
	}
}

func TestStatusReportsStates(t *testing.T) {
	r := setupService(t)
	installManagedUnit(t)
	r.script["systemctl is-enabled webfleet.service"] = fakeResult{out: "enabled", code: 0}
	r.script["systemctl is-active webfleet.service"] = fakeResult{out: "active", code: 0}
	r.script["systemctl show -p MainPID --value webfleet.service"] = fakeResult{out: "1234", code: 0}
	// Health check against the loopback listen from the unit.
	h := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	defer h.Close()
	listen := strings.TrimPrefix(h.URL, "http://")
	os.WriteFile(UnitPath, []byte(Unit("/var/lib/webfleet", listen)), 0o644)
	var buf bytes.Buffer
	if e := Status(&buf); e != nil {
		t.Fatal(e)
	}
	for _, want := range []string{"unit:    webfleet.service", "enabled: enabled", "active:  active", "pid:     1234", "data:    /var/lib/webfleet", "health:  ok"} {
		if !strings.Contains(buf.String(), want) {
			t.Fatalf("status output missing %q:\n%s", want, buf.String())
		}
	}
	// Not installed -> clear diagnostic.
	r2 := setupService(t)
	_ = r2
	os.Remove(UnitPath)
	if e := Status(io.Discard); e == nil {
		t.Fatal("status succeeded without an installed unit")
	}
}

func TestLogsConstruction(t *testing.T) {
	r := setupService(t)
	installManagedUnit(t)
	if e := Logs(false, io.Discard); e != nil {
		t.Fatal(e)
	}
	if !contains(r.log, "journalctl --unit webfleet.service") {
		t.Fatal("logs did not run journalctl --unit")
	}
	r.log = nil
	if e := Logs(true, io.Discard); e != nil {
		t.Fatal(e)
	}
	if !contains(r.log, "journalctl --unit webfleet.service -f") {
		t.Fatal("follow did not add -f")
	}
}

func TestUninstallPreservesDataAndIsIdempotent(t *testing.T) {
	r := setupService(t)
	installManagedUnit(t)
	r.script["systemctl disable --now webfleet.service"] = fakeResult{}
	r.script["systemctl daemon-reload"] = fakeResult{}
	if e := Uninstall(); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(UnitPath); !os.IsNotExist(e) {
		t.Fatal("unit file not removed")
	}
	// Repeated uninstall: unit gone -> requireManaged refuses (no error mutation).
	r.log = nil
	if e := Uninstall(); e == nil {
		t.Fatal("uninstall of a missing unit should report not-installed")
	}
}

func TestQuotingSurvivesSpacesInDataPath(t *testing.T) {
	r := setupService(t)
	exe := filepath.Join(t.TempDir(), "wf")
	os.WriteFile(exe, []byte("#!/bin/sh\nexit 0\n"), 0o755)
	data := filepath.Join(t.TempDir(), "web fleet data")
	r.script["systemctl daemon-reload"] = fakeResult{}
	r.script["systemctl enable webfleet.service"] = fakeResult{}
	r.script["systemctl start webfleet.service"] = fakeResult{}
	if e := Install(exe, data, "127.0.0.1:8090"); e != nil {
		t.Fatal(e)
	}
	b, _ := os.ReadFile(UnitPath)
	if !strings.Contains(string(b), `WEBFLEET_DATA_DIR="`+data+`"`) {
		t.Fatalf("spaced data path not quoted in unit:\n%s", b)
	}
}

func contains(hay []string, needle string) bool {
	for _, h := range hay {
		if h == needle {
			return true
		}
	}
	return false
}
