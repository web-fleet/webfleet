package service

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeRunner records systemctl/journalctl invocations and returns scripted
// outputs/exit codes so the lifecycle can be tested without a real manager.
type fakeRunner struct {
	script map[string]fakeResult // key "verb args..." -> result
	log    []string
	calls  map[string]int // per-key call count for sequence scripting
	seq    map[string][]fakeResult
}
type fakeResult struct {
	out  string
	code int
	err  error
}

func (f *fakeRunner) Run(name string, args ...string) (string, int, error) {
	key := name + " " + strings.Join(args, " ")
	f.log = append(f.log, key)
	if f.calls == nil {
		f.calls = map[string]int{}
	}
	n := f.calls[key]
	f.calls[key] = n + 1
	if seq, ok := f.seq[key]; ok && n < len(seq) {
		r := seq[n]
		return r.out, r.code, r.err
	}
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
	oldHealth := healthWindow
	oldPriorRead := readPriorStateAtRecovery
	UnitPath = filepath.Join(dir, "webfleet.service")
	BinaryPath = filepath.Join(dir, "webfleet")
	os.WriteFile(BinaryPath, []byte("#!/bin/sh\nexit 0\n"), 0o755)
	isRoot = func() bool { return true }
	ensureAccount = func() error { return nil }
	chownData = func(string) error { return nil }
	mkdirData = func(string, os.FileMode) error { return nil }
	r := &fakeRunner{script: map[string]fakeResult{}, seq: map[string][]fakeResult{}}
	defaultRunner = r
	t.Cleanup(func() {
		UnitPath, BinaryPath = oldUnit, oldBin
		isRoot, ensureAccount, chownData = oldRoot, oldAccount, oldChown
		mkdirData = func(path string, mode os.FileMode) error { return os.MkdirAll(path, mode) }
		healthWindow = oldHealth
		healthCheckFunc = func(url string) error { return healthCheckReal(url) }
		readPriorStateAtRecovery = oldPriorRead
		defaultRunner = oldRunner
	})
	return r
}

// prepareUpdate sets up the fake state (is-active), the prior-state marker, and
// a healthy health-check stub so update/rollback proceed on their normal path.
func prepareUpdate(r *fakeRunner, active string, healthOK bool) {
	r.script["systemctl is-active webfleet.service"] = fakeResult{out: active, code: 0}
	old := healthCheckFunc
	healthCheckFunc = func(url string) error {
		if !healthOK {
			return fmt.Errorf("health failed")
		}
		return nil
	}
	_ = old
	_ = os.WriteFile(BinaryPath+".prior-active", []byte(active), 0o600)
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

func setState(r *fakeRunner, enabled, active string) {
	r.script["systemctl is-enabled webfleet.service"] = fakeResult{out: enabled, code: 0}
	r.script["systemctl is-active webfleet.service"] = fakeResult{out: active, code: 0}
}

func writeForeignUnit(t *testing.T) {
	t.Helper()
	os.WriteFile(UnitPath, []byte("[Unit]\nDescription=admin\n[Service]\nExecStart=/usr/bin/thing\n[Install]\nWantedBy=multi-user.target\n"), 0o644)
}

func TestUpdateAndRollbackRefuseForeignUnit(t *testing.T) {
	r := setupService(t)
	writeForeignUnit(t)
	before, _ := os.ReadFile(BinaryPath)
	exe := filepath.Join(t.TempDir(), "wf2")
	os.WriteFile(exe, []byte("#!/bin/sh\n# v2\nexit 0\n"), 0o755)
	if e := Update(exe, fakeSHA(exe)); e == nil {
		t.Fatal("update mutated a foreign unit")
	}
	if e := Rollback(); e == nil {
		t.Fatal("rollback mutated a foreign unit")
	}
	after, _ := os.ReadFile(BinaryPath)
	if !bytes.Equal(before, after) {
		t.Fatal("update/rollback mutated the binary of a foreign unit")
	}
	for _, call := range r.log {
		if strings.Contains(call, "restart webfleet.service") {
			t.Fatalf("foreign unit triggered a restart: %s", call)
		}
	}
}

func TestReinstallChangedBinaryRestarts(t *testing.T) {
	r := setupService(t)
	installManagedUnit(t)
	setState(r, "enabled", "active")
	// A different executable than the installed one -> restart must occur.
	exe := filepath.Join(t.TempDir(), "wf2")
	os.WriteFile(exe, []byte("#!/bin/sh\n# different binary\nexit 0\n"), 0o755)
	r.script["systemctl daemon-reload"] = fakeResult{}
	r.script["systemctl enable webfleet.service"] = fakeResult{}
	r.script["systemctl restart webfleet.service"] = fakeResult{}
	if e := Install(exe, "/var/lib/webfleet", "127.0.0.1:8090"); e != nil {
		t.Fatal(e)
	}
	if !contains(r.log, "systemctl restart webfleet.service") {
		t.Fatal("changed binary did not trigger a restart")
	}
}

func TestReinstallSameBinaryIsNoOp(t *testing.T) {
	r := setupService(t)
	installManagedUnit(t)
	setState(r, "enabled", "active")
	// Byte-identical executable + identical unit + enabled + active -> no-op.
	exe := filepath.Join(t.TempDir(), "wf2")
	os.WriteFile(exe, mustRead(t, BinaryPath), 0o755)
	if e := Install(exe, "/var/lib/webfleet", "127.0.0.1:8090"); e != nil {
		t.Fatal(e)
	}
	for _, call := range r.log {
		if strings.HasPrefix(call, "systemctl daemon-reload") {
			t.Fatal("identical reinstall performed a daemon-reload")
		}
	}
}

func TestReinstallRestoresExactNegativeState(t *testing.T) {
	r := setupService(t)
	installManagedUnit(t)
	// Prior state: disabled + stopped.
	setState(r, "disabled", "inactive")
	exe := filepath.Join(t.TempDir(), "wf2")
	os.WriteFile(exe, []byte("#!/bin/sh\n# new\nexit 0\n"), 0o755)
	// Changed unit (different listen) -> forward enable then restart fails.
	r.script["systemctl daemon-reload"] = fakeResult{}
	r.script["systemctl enable webfleet.service"] = fakeResult{}
	r.script["systemctl restart webfleet.service"] = fakeResult{out: "activation failed", code: 1}
	if e := Install(exe, "/var/lib/webfleet", "127.0.0.1:9090"); e == nil {
		t.Fatal("failed reinstall returned nil")
	}
	// Rollback must neutralize, restore binary+unit, and explicitly re-apply the
	// negative states (disable + stop), not only the positive ones.
	disableCalls, stopCalls := 0, 0
	for _, call := range r.log {
		if call == "systemctl disable webfleet.service" {
			disableCalls++
		}
		if call == "systemctl stop webfleet.service" {
			stopCalls++
		}
	}
	if disableCalls < 2 {
		t.Fatalf("negative enabled state not re-applied during rollback (disable calls=%d)", disableCalls)
	}
	if stopCalls < 1 {
		t.Fatalf("negative active state not re-applied during rollback (stop calls=%d)", stopCalls)
	}
	// The prior unit must be restored.
	b, _ := os.ReadFile(UnitPath)
	if !strings.Contains(string(b), "127.0.0.1:8090") {
		t.Fatal("prior unit not restored after failed reinstall")
	}
}

func TestUninstallSurfacesStopDisableFailure(t *testing.T) {
	r := setupService(t)
	installManagedUnit(t)
	r.script["systemctl disable --now webfleet.service"] = fakeResult{out: "cannot stop", code: 1}
	if e := Uninstall(); e == nil {
		t.Fatal("uninstall ignored a failed disable --now")
	}
	if _, e := os.Stat(UnitPath); os.IsNotExist(e) {
		t.Fatal("uninstall removed the unit despite the failed stop/disable")
	}
}

func TestMalformedManagedUnitClassified(t *testing.T) {
	r := setupService(t)
	// Marker present but a required directive missing -> classified invalid.
	os.WriteFile(UnitPath, []byte("# Managed by webfleet. Do not edit manually.\n[Unit]\n[Service]\n[Install]\n"), 0o644)
	if e := lifecycle("start"); e == nil {
		t.Fatal("malformed managed unit accepted for start")
	}
	if e := Status(io.Discard); e == nil {
		t.Fatal("malformed managed unit accepted for status")
	}
	_ = r
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	b, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	return b
}

func fakeSHA(path string) string {
	h, _ := fileSHA256(path)
	return h
}

func TestUpdatePreservesActiveState(t *testing.T) {
	r := setupService(t)
	installManagedUnit(t)
	exe := filepath.Join(t.TempDir(), "wf2")
	os.WriteFile(exe, []byte("#!/bin/sh\n# v2\nexit 0\n"), 0o755)
	// Active service -> update restarts it.
	setState(r, "enabled", "active")
	prepareUpdate(r, "active", true)
	r.script["systemctl restart webfleet.service"] = fakeResult{}
	if e := Update(exe, fakeSHA(exe)); e != nil {
		t.Fatal(e)
	}
	if !contains(r.log, "systemctl restart webfleet.service") {
		t.Fatal("active update did not restart")
	}
	// Stopped service -> update installs the binary and leaves it stopped.
	r.log = nil
	setState(r, "enabled", "inactive")
	prepareUpdate(r, "inactive", true)
	if e := Update(exe, fakeSHA(exe)); e != nil {
		t.Fatal(e)
	}
	for _, call := range r.log {
		if strings.Contains(call, "restart webfleet.service") || strings.Contains(call, "start webfleet.service") {
			t.Fatalf("stopped update started the service: %s", call)
		}
	}
}

func TestUpdateFailedActivationRestoresOldBinaryAndActive(t *testing.T) {
	r := setupService(t)
	installManagedUnit(t)
	oldBin := mustRead(t, BinaryPath)
	setState(r, "enabled", "active")
	prepareUpdate(r, "active", true)
	exe := filepath.Join(t.TempDir(), "wf2")
	os.WriteFile(exe, []byte("#!/bin/sh\n# v2\nexit 0\n"), 0o755)
	r.script["systemctl restart webfleet.service"] = fakeResult{out: "activation failed", code: 1}
	if e := Update(exe, fakeSHA(exe)); e == nil {
		t.Fatal("failed activation update returned nil")
	}
	now, _ := os.ReadFile(BinaryPath)
	if !bytes.Equal(now, oldBin) {
		t.Fatal("failed activation did not restore the old binary")
	}
	// The restore path restarted the old service.
	if !contains(r.log, "systemctl restart webfleet.service") {
		t.Fatal("restored active service was not restarted")
	}
}

func TestRollbackRestoresStoppedStateWithoutStarting(t *testing.T) {
	r := setupService(t)
	installManagedUnit(t)
	oldBin := mustRead(t, BinaryPath)
	// Prior stopped service: update keeps it stopped (no start).
	setState(r, "enabled", "inactive")
	prepareUpdate(r, "inactive", true)
	exe := filepath.Join(t.TempDir(), "wf2")
	os.WriteFile(exe, []byte("#!/bin/sh\n# v2\nexit 0\n"), 0o755)
	if e := Update(exe, fakeSHA(exe)); e != nil {
		t.Fatal(e)
	}
	// Rollback must restore the old binary without starting the service.
	r.log = nil
	if e := Rollback(); e != nil {
		t.Fatal(e)
	}
	now, _ := os.ReadFile(BinaryPath)
	if !bytes.Equal(now, oldBin) {
		t.Fatal("rollback did not restore the old binary")
	}
	for _, call := range r.log {
		if strings.Contains(call, "restart webfleet.service") || strings.Contains(call, "start webfleet.service") {
			t.Fatalf("rollback of a stopped service started it: %s", call)
		}
	}
}

func TestRollbackRestoresRunningState(t *testing.T) {
	r := setupService(t)
	installManagedUnit(t)
	// Prior active service: update restarts (active), rollback restarts too.
	setState(r, "enabled", "active")
	prepareUpdate(r, "active", true)
	exe := filepath.Join(t.TempDir(), "wf2")
	os.WriteFile(exe, []byte("#!/bin/sh\n# v2\nexit 0\n"), 0o755)
	r.script["systemctl restart webfleet.service"] = fakeResult{}
	if e := Update(exe, fakeSHA(exe)); e != nil {
		t.Fatal(e)
	}
	r.log = nil
	if e := Rollback(); e != nil {
		t.Fatal(e)
	}
	if !contains(r.log, "systemctl restart webfleet.service") {
		t.Fatal("rollback of an active service did not restart it")
	}
	// After a successful rollback the metadata is consumed.
	if _, e := os.Stat(BinaryPath + ".rollback"); !os.IsNotExist(e) {
		t.Fatal("rollback metadata not consumed after successful rollback")
	}
	// Enable/disable state is never touched by update or rollback.
	for _, call := range r.log {
		if strings.HasPrefix(call, "systemctl enable ") || strings.HasPrefix(call, "systemctl disable ") {
			t.Fatalf("update/rollback changed enablement: %s", call)
		}
	}
}

func TestUpdateVerifiesHealthNotJustRestartExit(t *testing.T) {
	r := setupService(t)
	installManagedUnit(t)
	oldBin := mustRead(t, BinaryPath)
	exe := filepath.Join(t.TempDir(), "wf2")
	os.WriteFile(exe, []byte("#!/bin/sh\n# v2\nexit 0\n"), 0o755)
	// restart exits 0 but the service never becomes active/healthy -> update
	// must fail and restore the old binary.
	setState(r, "enabled", "active")
	prepareUpdate(r, "active", false) // health never OK
	healthWindow = 1 * time.Second
	r.script["systemctl restart webfleet.service"] = fakeResult{}
	r.script["systemctl stop webfleet.service"] = fakeResult{}
	if e := Update(exe, fakeSHA(exe)); e == nil {
		t.Fatal("update succeeded although the new binary never became healthy")
	}
	now, _ := os.ReadFile(BinaryPath)
	if !bytes.Equal(now, oldBin) {
		t.Fatal("unhealthy update did not restore the old binary")
	}
}

func TestUpdateActiveStateQueryFailureAborts(t *testing.T) {
	r := setupService(t)
	installManagedUnit(t)
	oldBin := mustRead(t, BinaryPath)
	exe := filepath.Join(t.TempDir(), "wf2")
	os.WriteFile(exe, []byte("#!/bin/sh\n# v2\nexit 0\n"), 0o755)
	// Failure to query the current active state -> abort before binary mutation.
	r.script["systemctl is-active webfleet.service"] = fakeResult{out: "", code: 1, err: fmt.Errorf("systemctl is-active failed")}
	if e := Update(exe, fakeSHA(exe)); e == nil {
		t.Fatal("update proceeded when the active state could not be determined")
	}
	now, _ := os.ReadFile(BinaryPath)
	if !bytes.Equal(now, oldBin) {
		t.Fatal("state-query failure still mutated the binary")
	}
}

func TestUpdatePriorStateMarkerWriteFailureAborts(t *testing.T) {
	r := setupService(t)
	installManagedUnit(t)
	oldBin := mustRead(t, BinaryPath)
	exe := filepath.Join(t.TempDir(), "wf2")
	os.WriteFile(exe, []byte("#!/bin/sh\n# v2\nexit 0\n"), 0o755)
	setState(r, "enabled", "inactive")
	r.script["systemctl is-active webfleet.service"] = fakeResult{out: "inactive", code: 0}
	// Make the marker write fail by creating a directory at that path.
	os.MkdirAll(BinaryPath+".prior-active", 0o700)
	if e := Update(exe, fakeSHA(exe)); e == nil {
		t.Fatal("update proceeded when the rollback marker could not be written")
	}
	now, _ := os.ReadFile(BinaryPath)
	if !bytes.Equal(now, oldBin) {
		t.Fatal("marker-write failure still mutated the binary")
	}
}

func TestRollbackFailClosedWithoutMarker(t *testing.T) {
	r := setupService(t)
	installManagedUnit(t)
	// A rollback binary exists but no prior-state marker -> rollback must
	// refuse rather than defaulting to active.
	os.WriteFile(BinaryPath+".rollback", []byte("#!/bin/sh\nexit 0\n"), 0o755)
	os.Remove(BinaryPath + ".prior-active")
	if e := Rollback(); e == nil {
		t.Fatal("rollback defaulted to active without a prior-state marker")
	}
	_ = r
}

// TestEndToEndActiveUpdateThenRollback proves a successful update of an active
// service retains rollback metadata and a later manual Rollback restores the
// old binary and the active state - with NO test-side metadata fabrication.
func TestEndToEndActiveUpdateThenRollback(t *testing.T) {
	r := setupService(t)
	installManagedUnit(t)
	oldBin := mustRead(t, BinaryPath)
	setState(r, "enabled", "active")
	prepareUpdate(r, "active", true)
	exe := filepath.Join(t.TempDir(), "wf2")
	os.WriteFile(exe, []byte("#!/bin/sh\n# v2\nexit 0\n"), 0o755)
	r.script["systemctl restart webfleet.service"] = fakeResult{}
	if e := Update(exe, fakeSHA(exe)); e != nil {
		t.Fatal(e)
	}
	// Metadata must survive a successful active update.
	if _, e := os.Stat(BinaryPath + ".rollback"); e != nil {
		t.Fatal("rollback binary missing after successful update")
	}
	if _, e := os.Stat(BinaryPath + ".prior-active"); e != nil {
		t.Fatal("prior-active marker missing after successful update")
	}
	now, _ := os.ReadFile(BinaryPath)
	if bytes.Equal(now, oldBin) {
		t.Fatal("update did not replace the binary")
	}
	// Manual rollback restores the old binary and the active state.
	r.log = nil
	r.script["systemctl restart webfleet.service"] = fakeResult{}
	if e := Rollback(); e != nil {
		t.Fatal(e)
	}
	back, _ := os.ReadFile(BinaryPath)
	if !bytes.Equal(back, oldBin) {
		t.Fatal("rollback did not restore the old binary")
	}
	if !contains(r.log, "systemctl restart webfleet.service") {
		t.Fatal("rollback of an active service did not restart it")
	}
}

// TestEndToEndStoppedUpdateThenRollback proves a successful update of a stopped
// service retains metadata and a later Rollback restores the old binary while
// leaving the service stopped.
func TestEndToEndStoppedUpdateThenRollback(t *testing.T) {
	r := setupService(t)
	installManagedUnit(t)
	oldBin := mustRead(t, BinaryPath)
	setState(r, "enabled", "inactive")
	prepareUpdate(r, "inactive", true)
	exe := filepath.Join(t.TempDir(), "wf2")
	os.WriteFile(exe, []byte("#!/bin/sh\n# v2\nexit 0\n"), 0o755)
	if e := Update(exe, fakeSHA(exe)); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(BinaryPath + ".prior-active"); e != nil {
		t.Fatal("prior-active marker missing after stopped update")
	}
	r.log = nil
	if e := Rollback(); e != nil {
		t.Fatal(e)
	}
	back, _ := os.ReadFile(BinaryPath)
	if !bytes.Equal(back, oldBin) {
		t.Fatal("rollback did not restore the old binary")
	}
	for _, call := range r.log {
		if strings.Contains(call, "restart webfleet.service") || strings.Contains(call, "start webfleet.service") {
			t.Fatalf("rollback of a stopped service started it: %s", call)
		}
	}
}

// TestFailedUpdateRecoverySurfacesFailures proves recovery is verified and a
// failed restoration step is surfaced rather than silently claimed.
func TestFailedUpdateRecoverySurfacesFailures(t *testing.T) {
	// Recovery restart failure after an unhealthy new version is surfaced.
	{
		r := setupService(t)
		installManagedUnit(t)
		setState(r, "enabled", "active")
		prepareUpdate(r, "active", false) // health fails -> recovery path
		healthWindow = 1 * time.Second
		exe := filepath.Join(t.TempDir(), "wf2")
		os.WriteFile(exe, []byte("#!/bin/sh\n# v2\nexit 0\n"), 0o755)
		r.script["systemctl restart webfleet.service"] = fakeResult{}
		r.script["systemctl stop webfleet.service"] = fakeResult{}
		// Recovery restart (the 2nd restart call) fails.
		r.seq["systemctl restart webfleet.service"] = []fakeResult{{}, {out: "restore failed", code: 1}}
		uerr := Update(exe, fakeSHA(exe))
		if uerr == nil {
			t.Fatal("update succeeded despite a failed recovery restart")
		}
		if !strings.Contains(uerr.Error(), "recovery") {
			t.Fatalf("recovery restart failure not surfaced: %v", uerr)
		}
	}
	// Recovery stop failure is surfaced.
	{
		r := setupService(t)
		installManagedUnit(t)
		setState(r, "enabled", "active")
		prepareUpdate(r, "active", false)
		healthWindow = 1 * time.Second
		exe := filepath.Join(t.TempDir(), "wf2")
		os.WriteFile(exe, []byte("#!/bin/sh\n# v2\nexit 0\n"), 0o755)
		r.script["systemctl restart webfleet.service"] = fakeResult{}
		r.script["systemctl stop webfleet.service"] = fakeResult{out: "cannot stop", code: 1}
		uerr := Update(exe, fakeSHA(exe))
		if uerr == nil {
			t.Fatal("update succeeded despite a failed recovery stop")
		}
		if !strings.Contains(uerr.Error(), "recovery") {
			t.Fatalf("recovery stop failure not surfaced: %v", uerr)
		}
	}
}

// TestInitialRestartFailureSurfacesRecoveryFailure proves the initial-restart
// failure branch also surfaces a recovery failure (not just the health branch).
func TestInitialRestartFailureSurfacesRecoveryFailure(t *testing.T) {
	r := setupService(t)
	installManagedUnit(t)
	setState(r, "enabled", "active")
	prepareUpdate(r, "active", true)
	exe := filepath.Join(t.TempDir(), "wf2")
	os.WriteFile(exe, []byte("#!/bin/sh\n# v2\nexit 0\n"), 0o755)
	// Initial restart of the new binary fails (code 1), then recovery's own
	// restart also fails.
	r.seq["systemctl restart webfleet.service"] = []fakeResult{
		{out: "new binary failed to start", code: 1},
		{out: "recovery restart failed", code: 1},
	}
	r.script["systemctl stop webfleet.service"] = fakeResult{}
	uerr := Update(exe, fakeSHA(exe))
	if uerr == nil {
		t.Fatal("update succeeded despite initial restart failure")
	}
	if !strings.Contains(uerr.Error(), "restart after update") {
		t.Fatalf("original restart failure missing: %v", uerr)
	}
	if !strings.Contains(uerr.Error(), "recovery") {
		t.Fatalf("recovery failure not surfaced: %v", uerr)
	}
}

// TestInitialRestartFailureRecoverySucceedsCleansMetadata proves when the
// initial restart fails but recovery succeeds, the old binary is restored,
// health is verified, and both rollback artifacts are consumed.
func TestInitialRestartFailureRecoverySucceedsCleansMetadata(t *testing.T) {
	r := setupService(t)
	installManagedUnit(t)
	oldBin := mustRead(t, BinaryPath)
	setState(r, "enabled", "active")
	prepareUpdate(r, "active", true)
	healthWindow = 1 * time.Second
	exe := filepath.Join(t.TempDir(), "wf2")
	os.WriteFile(exe, []byte("#!/bin/sh\n# v2\nexit 0\n"), 0o755)
	r.seq["systemctl restart webfleet.service"] = []fakeResult{
		{out: "new binary failed to start", code: 1}, // initial
		{}, // recovery restart succeeds
	}
	r.script["systemctl stop webfleet.service"] = fakeResult{}
	uerr := Update(exe, fakeSHA(exe))
	if uerr == nil {
		t.Fatal("update should report the initial restart failure")
	}
	back, _ := os.ReadFile(BinaryPath)
	if !bytes.Equal(back, oldBin) {
		t.Fatal("recovery did not restore the old binary")
	}
	// Verified recovery consumed both artifacts.
	if _, e := os.Stat(BinaryPath + ".rollback"); !os.IsNotExist(e) {
		t.Fatal("stale rollback binary left after verified recovery")
	}
	if _, e := os.Stat(BinaryPath + ".prior-active"); !os.IsNotExist(e) {
		t.Fatal("stale prior-active marker left after verified recovery")
	}
}

// TestRecoveryFailsClosedWithoutMarker proves recovery does not guess to active
// when the prior-state marker is missing/invalid.
func TestRecoveryFailsClosedWithoutMarker(t *testing.T) {
	// The seam removes/corrupts the marker AFTER Update() has captured and
	// persisted it but BEFORE recovery reads it, so this genuinely exercises the
	// "marker missing at recovery time" fail-closed contract.
	run := func(desc string, mutate func()) {
		r := setupService(t)
		installManagedUnit(t)
		setState(r, "enabled", "active")
		prepareUpdate(r, "active", false)
		healthWindow = 1 * time.Second
		exe := filepath.Join(t.TempDir(), "wf2")
		os.WriteFile(exe, []byte("#!/bin/sh\n# v2\nexit 0\n"), 0o755)
		r.script["systemctl restart webfleet.service"] = fakeResult{}
		// Record the log position at the moment the marker is found missing, so
		// we can assert NO recovery activation (stop/restart) is issued AFTER the
		// fail-closed decision - independent of the update's own legitimate restart.
		markerRead := 0
		readPriorStateAtRecovery = func() (string, error) {
			markerRead = len(r.log)
			mutate()
			b, err := os.ReadFile(BinaryPath + ".prior-active")
			if err != nil {
				return "", err
			}
			return strings.TrimSpace(string(b)), nil
		}
		uerr := Update(exe, fakeSHA(exe))
		if uerr == nil {
			t.Fatalf("[%s] update succeeded despite a missing recovery marker", desc)
		}
		// A genuine fail-closed guard: recovery must refuse to guess and report
		// that the prior-state marker is missing/invalid - not merely surface
		// some unrelated "recovery" error.
		if !strings.Contains(uerr.Error(), "prior-state marker") {
			t.Fatalf("[%s] recovery did not fail closed on the marker (got: %v)", desc, uerr)
		}
		// Fail closed means recovery must NOT proceed into a guessed-active
		// restart: no stop/restart may be issued after the marker failure.
		for _, call := range r.log[markerRead:] {
			if strings.HasPrefix(call, "systemctl stop ") || strings.HasPrefix(call, "systemctl restart ") {
				t.Fatalf("[%s] recovery proceeded to activate after the marker failure: %s", desc, call)
			}
		}
	}
	run("missing", func() { os.Remove(BinaryPath + ".prior-active") })
	run("invalid", func() { os.WriteFile(BinaryPath+".prior-active", []byte("bogus"), 0o600) })
}
