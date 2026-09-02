package service

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"
)

var UnitPath = "/etc/systemd/system/webfleet.service"
var BinaryPath = "/usr/local/bin/webfleet"

// DefaultDataDir is the canonical data directory the installed service owns
// and runs from. It must be independent of the CLI default (./data) because
// `service install` is run as root with no runtime config and the unit it
// writes embeds the path.
const DefaultDataDir = "/var/lib/webfleet"

// DefaultListen is the canonical loopback listen address embedded in the unit.
const DefaultListen = "127.0.0.1:8090"

// ServiceAccount is the dedicated unprivileged account the unit runs as. It is
// created idempotently by Install so a clean machine needs no hidden manual
// prerequisites.
const ServiceUser = "webfleet"
const ServiceGroup = "webfleet"

// unitMarker marks unit files written by `webfleet service`. Service operations
// refuse to act on a unit that does not carry it, so the CLI can never modify
// an unrelated system service that happens to share the unit name.
const unitMarker = "# Managed by webfleet. Do not edit manually."

// Runner abstracts systemctl/journalctl so the CLI is testable without a real
// systemd manager. Run returns captured combined output, the exit code (0 on
// success, -1 when the command could not launch) and a launch error only.
type Runner interface {
	Run(name string, args ...string) (string, int, error)
	Stream(name string, args ...string) (int, error)
}

type execRunner struct{}

func (execRunner) Run(name string, args ...string) (string, int, error) {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err == nil {
		return string(out), 0, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return string(out), ee.ExitCode(), nil
	}
	return string(out), -1, err
}

func (execRunner) Stream(name string, args ...string) (int, error) {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	if err == nil {
		return 0, nil
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode(), nil
	}
	return -1, err
}

var defaultRunner Runner = execRunner{}

// setRunner replaces the systemctl runner (test seam; nil restores the default).
func setRunner(r Runner) { defaultRunner = r }

func systemctl(args ...string) (string, int, error) { return defaultRunner.Run("systemctl", args...) }

func bounded(s string) string {
	const max = 2000
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}

// systemctlSuccess runs a systemctl operation that must exit zero; launch
// failures and nonzero exits are both errors.
func systemctlSuccess(args ...string) error {
	out, code, err := systemctl(args...)
	if err != nil {
		return fmt.Errorf("cannot run systemctl %s: %w", strings.Join(args, " "), err)
	}
	if code != 0 {
		return fmt.Errorf("systemctl %s exited %d: %s", strings.Join(args, " "), code, bounded(strings.TrimSpace(out)))
	}
	return nil
}

// unitStateWord runs a state verb (is-enabled/is-active), tolerating a nonzero
// exit for legitimate negative answers and returning the trimmed word.
func unitStateWord(verb string) (string, error) {
	out, code, err := systemctl(verb, "webfleet.service")
	if err != nil {
		return "", fmt.Errorf("cannot run systemctl %s: %w", verb, err)
	}
	if code == 0 {
		return strings.TrimSpace(out), nil
	}
	return strings.TrimSpace(out), nil
}

// systemdQuote escapes a value for a systemd ExecStart/Environment/ReadWritePaths
// directive so paths containing spaces or systemd-special characters survive.
func systemdQuote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '%':
			b.WriteString("%%")
		case '"', '\\', '$', '`':
			b.WriteByte('\\')
			b.WriteByte(c)
		default:
			b.WriteByte(c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// validateNoControl rejects CR, LF, NUL and other control characters so no
// user-supplied value can inject directives into a systemd unit.
func validateNoControl(v, what string) error {
	for i := 0; i < len(v); i++ {
		if v[i] < 0x20 || v[i] == 0x7f {
			return fmt.Errorf("%s %q contains a control character", what, v)
		}
	}
	return nil
}

// fileSHA256 returns the hex SHA-256 of a file (used to detect a changed
// executable during reinstall).
func fileSHA256(path string) (string, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return "", e
	}
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:]), nil
}

// validateManagedUnit performs the structural ownership/integrity check: the
// unit must carry the webfleet marker AND contain the required Web Fleet
// directives, so a stale or malformed managed unit is classified rather than
// treated as healthy.
func validateManagedUnit(body []byte) error {
	t := string(body)
	if !strings.Contains(t, unitMarker) {
		return errors.New("not a webfleet-managed unit")
	}
	for _, want := range []string{"[Unit]", "[Service]", "[Install]", "Description=Web Fleet", "ExecStart=" + BinaryPath, "User=" + ServiceUser, "Environment=WEBFLEET_DATA_DIR", "WantedBy=multi-user.target"} {
		if !strings.Contains(t, want) {
			return fmt.Errorf("malformed managed unit: missing %q", want)
		}
	}
	return nil
}

// managedUnit reports whether the unit file carries the webfleet managed marker.
func managedUnit(path string) bool {
	b, e := os.ReadFile(path)
	if e != nil {
		return false
	}
	return strings.Contains(string(b), unitMarker)
}

// requireManaged refuses to operate on a unit that is not installed or not
// owned by webfleet, so the CLI never touches an unrelated system service.
func requireManaged(verb string) error {
	b, e := os.ReadFile(UnitPath)
	if errors.Is(e, os.ErrNotExist) {
		return fmt.Errorf("refusing to %s webfleet.service: unit is not installed (run `webfleet service install`)", verb)
	}
	if e != nil {
		return fmt.Errorf("refusing to %s webfleet.service: %w", verb, e)
	}
	if ve := validateManagedUnit(b); ve != nil {
		return fmt.Errorf("refusing to %s webfleet.service: %v", verb, ve)
	}
	return nil
}

func requireLinux() error {
	if runtime.GOOS != "linux" {
		return errors.New("service management is supported on Linux only")
	}
	return nil
}

var isRoot = func() bool { return os.Geteuid() == 0 }
var ensureAccount = func() error { return ensureServiceAccount() }
var chownData = func(path string) error { return chownService(path) }
var mkdirData = func(path string, mode os.FileMode) error { return os.MkdirAll(path, mode) }

func requireRoot(verb string) error {
	if !isRoot() {
		return fmt.Errorf("%s requires root (sudo webfleet service %s)", verb, verb)
	}
	return nil
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, e := os.Open(src)
	if e != nil {
		return e
	}
	defer in.Close()
	tmp := dst + ".new"
	out, e := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if e != nil {
		return e
	}
	if _, e = io.Copy(out, in); e != nil {
		out.Close()
		return e
	}
	if e = out.Sync(); e != nil {
		out.Close()
		return e
	}
	if e = out.Close(); e != nil {
		return e
	}
	return os.Rename(tmp, dst)
}

// unitBody renders the systemd directives (no managed marker).
func unitBody(dataDir, listen string) string {
	if dataDir == "" {
		dataDir = DefaultDataDir
	}
	if listen == "" {
		listen = DefaultListen
	}
	return `[Unit]
Description=Web Fleet website monitoring
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=` + ServiceUser + `
Group=` + ServiceGroup + `
Environment=WEBFLEET_DATA_DIR=` + systemdQuote(dataDir) + `
Environment=WEBFLEET_LISTEN=` + systemdQuote(listen) + `
ExecStart=` + BinaryPath + `
Restart=on-failure
RestartSec=3
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=` + systemdQuote(dataDir) + `

[Install]
WantedBy=multi-user.target
`
}

// Unit returns the full managed unit content for the given data dir and listen
// address.
func Unit(dataDir, listen string) string {
	return unitMarker + "\n" + unitBody(dataDir, listen)
}

// unitEnv reads an Environment=WEBFLEET_* value from a unit body.
func unitEnv(body, key string) string {
	prefix := "Environment=" + key + "="
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.Trim(strings.TrimPrefix(line, prefix), `"`)
		}
	}
	return ""
}

// Install installs (or idempotently reinstalls) the webfleet systemd unit: it
// creates the service account and data directory, copies the current binary,
// writes the managed unit, daemon-reloads, enables and starts/restarts the
// service. A partial failure restores the prior unit, enablement, active state
// and binary. A byte-identical unit that is already enabled and active is a
// no-op.
func Install(exe, dataDir, listen string) error {
	if e := requireLinux(); e != nil {
		return e
	}
	if !isRoot() {
		return errors.New("service install requires root")
	}
	for _, v := range []struct{ val, name string }{{dataDir, "data dir"}, {listen, "listen"}} {
		if e := validateNoControl(v.val, v.name); e != nil {
			return e
		}
	}
	if dataDir == "" {
		dataDir = DefaultDataDir
	}
	if listen == "" {
		listen = DefaultListen
	}
	if _, e := exec.LookPath("systemctl"); e != nil {
		return errors.New("systemctl not found; is systemd installed?")
	}
	if e := ensureAccount(); e != nil {
		return e
	}
	if e := mkdirData(dataDir, 0o700); e != nil {
		return e
	}
	_ = os.Chmod(dataDir, 0o700)
	_ = os.Chown(dataDir, 0, 0)
	if e := chownData(dataDir); e != nil {
		return e
	}
	unit := Unit(dataDir, listen)
	priorUnit, hadUnit := []byte(nil), false
	if b, e := os.ReadFile(UnitPath); e == nil {
		hadUnit = true
		priorUnit = b
		if ve := validateManagedUnit(b); ve != nil {
			return fmt.Errorf("refusing to reinstall webfleet.service: %v", ve)
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	// Snapshot the prior enablement/active states so rollback can reproduce the
	// exact previous operational state, positive and negative alike.
	priorEnabled, priorActive := "", ""
	if hadUnit {
		priorEnabled, _ = unitStateWord("is-enabled")
		priorActive, _ = unitStateWord("is-active")
	}
	// Detect a changed executable: reinstall of a newer binary must restart the
	// service even when the unit text is unchanged.
	incomingDigest, err := fileSHA256(exe)
	if err != nil {
		return fmt.Errorf("read incoming executable: %w", err)
	}
	priorBinaryDigest, hadBinary := "", false
	if d, e := fileSHA256(BinaryPath); e == nil {
		hadBinary = true
		priorBinaryDigest = d
	}
	binaryChanged := !hadBinary || incomingDigest != priorBinaryDigest
	// Genuine no-op: identical unit bytes, identical executable, already enabled
	// and active - nothing to rewrite, reload or restart.
	if hadUnit && string(priorUnit) == unit && !binaryChanged && priorEnabled == "enabled" && priorActive == "active" {
		return nil
	}
	// Preserve the prior binary so activation failure can roll back cleanly.
	if hadBinary {
		if e := copyFile(BinaryPath, BinaryPath+".preinstall", 0o755); e != nil {
			return e
		}
	}
	installOK := false
	restore := func() string {
		var errs []string
		// 1) Neutralize any attempted activation before restoring the binary so a
		// previously running service is never restarted with the failing binary.
		_ = systemctlSuccess("stop", "webfleet.service")
		_ = systemctlSuccess("disable", "webfleet.service")
		// 2) Restore the executable before the unit and before activation.
		if hadBinary {
			if e := copyFile(BinaryPath+".preinstall", BinaryPath, 0o755); e != nil {
				errs = append(errs, fmt.Sprintf("restore binary: %v", e))
			}
		} else {
			_ = os.Remove(BinaryPath)
		}
		_ = os.Remove(BinaryPath + ".preinstall")
		// 3) Restore the unit.
		if hadUnit {
			if e := os.WriteFile(UnitPath, priorUnit, 0o644); e != nil {
				errs = append(errs, fmt.Sprintf("restore unit: %v", e))
			}
		} else {
			_ = os.Remove(UnitPath)
		}
		if e := systemctlSuccess("daemon-reload"); e != nil {
			errs = append(errs, fmt.Sprintf("reload systemd: %v", e))
		}
		// 4) Restore the exact prior enablement and active states, negative
		// states included.
		if hadUnit {
			if priorEnabled == "enabled" {
				if e := systemctlSuccess("enable", "webfleet.service"); e != nil {
					errs = append(errs, fmt.Sprintf("re-enable: %v", e))
				}
			} else if priorEnabled != "" && priorEnabled != "disabled" {
				errs = append(errs, fmt.Sprintf("prior enablement %q cannot be restored", priorEnabled))
			} else {
				_ = systemctlSuccess("disable", "webfleet.service")
			}
			if priorActive == "active" {
				if e := systemctlSuccess("start", "webfleet.service"); e != nil {
					errs = append(errs, fmt.Sprintf("restart prior service: %v", e))
				}
			} else if priorActive != "" && priorActive != "inactive" && priorActive != "dead" && priorActive != "failed" {
				errs = append(errs, fmt.Sprintf("prior active state %q cannot be restored", priorActive))
			} else {
				// Service stays stopped (already neutralized above).
				_ = systemctlSuccess("stop", "webfleet.service")
			}
		}
		if len(errs) == 0 {
			return ""
		}
		return "; rollback incomplete: " + strings.Join(errs, "; ")
	}
	defer func() {
		if !installOK {
			_ = restore()
		}
	}()
	if e := copyFile(exe, BinaryPath, 0o755); e != nil {
		return e
	}
	if e := os.WriteFile(UnitPath, []byte(unit), 0o644); e != nil {
		return e
	}
	unitChanged := !hadUnit || string(priorUnit) != unit
	changed := unitChanged || binaryChanged
	steps := [][]string{{"daemon-reload"}}
	if changed {
		steps = append(steps, []string{"enable", "webfleet.service"}, []string{"restart", "webfleet.service"})
	} else {
		if priorEnabled != "enabled" {
			steps = append(steps, []string{"enable", "webfleet.service"})
		}
		if priorActive != "active" {
			steps = append(steps, []string{"start", "webfleet.service"})
		}
	}
	for _, a := range steps {
		if out, code, err := systemctl(a...); err != nil || code != 0 {
			return fmt.Errorf("systemctl %s: %s: %w (installation rolled back)", strings.Join(a, " "), bounded(strings.TrimSpace(out)), errorIfNil(err, code, a))
		}
	}
	installOK = true
	_ = os.Remove(BinaryPath + ".preinstall")
	return nil
}

func errorIfNil(err error, code int, args []string) error {
	if err != nil {
		return err
	}
	return fmt.Errorf("exited %d", code)
}

// ensureServiceAccount creates the webfleet group and user idempotently so a
// clean machine needs no manually created prerequisites. The service account is
// a system account with no login shell and no home directory.
func ensureServiceAccount() error {
	if out, e := exec.Command("groupadd", "--system", ServiceGroup).CombinedOutput(); e != nil {
		if !strings.Contains(string(out), "exists") {
			return fmt.Errorf("groupadd: %s", strings.TrimSpace(string(out)))
		}
	}
	if out, e := exec.Command("useradd", "--system", "--no-create-home", "--shell", "/usr/sbin/nologin", "--gid", ServiceGroup, ServiceUser).CombinedOutput(); e != nil {
		if !strings.Contains(string(out), "exists") {
			return fmt.Errorf("useradd: %s", strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// chownService transfers the data directory to the service account so the
// unprivileged service can open its database. The parent chain is left
// root-owned; only the service data directory is handed over.
func chownService(path string) error {
	uid, gid, e := lookupServiceIDs()
	if e != nil {
		return e
	}
	return os.Chown(path, uid, gid)
}

func lookupServiceIDs() (int, int, error) {
	g, e := user.LookupGroup(ServiceGroup)
	if e != nil {
		return 0, 0, fmt.Errorf("service group not found: %w", e)
	}
	gid, _ := strconv.Atoi(g.Gid)
	u, e := user.Lookup(ServiceUser)
	if e != nil {
		return 0, 0, fmt.Errorf("service user not found: %w", e)
	}
	uid, _ := strconv.Atoi(u.Uid)
	return uid, gid, nil
}

// Start starts the service.
func Start() error {
	return lifecycle("start")
}

// Stop stops the service.
func Stop() error {
	return lifecycle("stop")
}

// Restart restarts the service.
func Restart() error {
	return lifecycle("restart")
}

// Enable enables the service at boot.
func Enable() error {
	return lifecycle("enable")
}

// Disable disables the service at boot (without stopping it).
func Disable() error {
	return lifecycle("disable")
}

func lifecycle(verb string) error {
	if e := requireLinux(); e != nil {
		return e
	}
	if e := requireRoot(verb); e != nil {
		return e
	}
	if e := requireManaged(verb); e != nil {
		return e
	}
	if err := systemctlSuccess(verb, "webfleet.service"); err != nil {
		return err
	}
	return nil
}

// Uninstall stops and disables the service, removes the unit and reloads
// systemd. The data directory and installed binary are deliberately preserved.
func Uninstall() error {
	if e := requireLinux(); e != nil {
		return e
	}
	if e := requireRoot("uninstall"); e != nil {
		return e
	}
	if e := requireManaged("uninstall"); e != nil {
		return e
	}
	// Surface a failed stop/disable rather than pretending uninstall succeeded.
	if e := systemctlSuccess("disable", "--now", "webfleet.service"); e != nil {
		return fmt.Errorf("uninstall: %w", e)
	}
	if e := os.Remove(UnitPath); e != nil && !errors.Is(e, os.ErrNotExist) {
		return e
	}
	return systemctlSuccess("daemon-reload")
}

// Status reports the resolved service state, pid, data/listen configuration and
// a live health check. It is read-only and does not require root.
func Status(out io.Writer) error {
	if e := requireLinux(); e != nil {
		return e
	}
	body, e := os.ReadFile(UnitPath)
	if errors.Is(e, os.ErrNotExist) {
		return fmt.Errorf("webfleet.service is not installed (run `webfleet service install`)")
	}
	if e != nil {
		return fmt.Errorf("cannot read %s: %w", UnitPath, e)
	}
	if ve := validateManagedUnit(body); ve != nil {
		return fmt.Errorf("webfleet.service unit at %s is not valid: %v", UnitPath, ve)
	}
	enabled, _ := unitStateWord("is-enabled")
	active, _ := unitStateWord("is-active")
	pid, _, _ := systemctl("show", "-p", "MainPID", "--value", "webfleet.service")
	dataDir := unitEnv(string(body), "WEBFLEET_DATA_DIR")
	listen := unitEnv(string(body), "WEBFLEET_LISTEN")
	if dataDir == "" {
		dataDir = DefaultDataDir
	}
	if listen == "" {
		listen = DefaultListen
	}
	fmt.Fprintf(out, "unit:    webfleet.service\n")
	fmt.Fprintf(out, "file:    %s\n", UnitPath)
	fmt.Fprintf(out, "enabled: %s\n", strings.TrimSpace(enabled))
	fmt.Fprintf(out, "active:  %s\n", strings.TrimSpace(active))
	fmt.Fprintf(out, "pid:     %s\n", strings.TrimSpace(pid))
	fmt.Fprintf(out, "user:    %s\n", ServiceUser)
	fmt.Fprintf(out, "data:    %s\n", dataDir)
	fmt.Fprintf(out, "listen:  %s\n", listen)
	if strings.TrimSpace(active) != "active" {
		fmt.Fprintln(out, "health:  not running")
		return fmt.Errorf("webfleet.service is %q; expected active", strings.TrimSpace(active))
	}
	if err := healthCheck("http://" + listen + "/healthz"); err != nil {
		fmt.Fprintf(out, "health:  unreachable (%v)\n", err)
		return fmt.Errorf("service is active but its health check failed: %v", err)
	}
	fmt.Fprintln(out, "health:  ok")
	return nil
}

func healthCheck(url string) error {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("healthz returned %d", resp.StatusCode)
	}
	return nil
}

// Logs streams the service journal. follow=false prints the current journal;
// follow=true tails live output.
func Logs(follow bool, out io.Writer) error {
	if e := requireLinux(); e != nil {
		return e
	}
	if e := requireManaged("view logs for"); e != nil {
		return e
	}
	args := []string{"--unit", "webfleet.service"}
	if follow {
		args = append(args, "-f")
		code, err := defaultRunner.Stream("journalctl", args...)
		if err != nil {
			return err
		}
		if code != 0 {
			return fmt.Errorf("journalctl exited with status %d", code)
		}
		return nil
	}
	o, code, err := defaultRunner.Run("journalctl", args...)
	if err != nil {
		return fmt.Errorf("cannot run journalctl: %w", err)
	}
	if code != 0 {
		return fmt.Errorf("journalctl exited %d: %s", code, bounded(strings.TrimSpace(o)))
	}
	fmt.Fprint(out, o)
	return nil
}

func Verify(path, want string) error {
	b, e := os.ReadFile(path)
	if e != nil {
		return e
	}
	h := sha256.Sum256(b)
	got := hex.EncodeToString(h[:])
	if !strings.EqualFold(got, strings.TrimSpace(want)) {
		return fmt.Errorf("checksum mismatch: got %s", got)
	}
	return nil
}

func Update(artifact, want string) error {
	if e := requireLinux(); e != nil {
		return e
	}
	if !isRoot() {
		return errors.New("update requires root")
	}
	if e := requireManaged("update"); e != nil {
		return e
	}
	if e := Verify(artifact, want); e != nil {
		return e
	}
	// Snapshot the prior active state so the update preserves the operator's
	// operational state: a deliberately stopped service stays stopped.
	priorActive, _ := unitStateWord("is-active")
	wasActive := priorActive == "active"
	if _, e := os.Stat(BinaryPath); e == nil {
		if e := copyFile(BinaryPath, BinaryPath+".rollback", 0o755); e != nil {
			return e
		}
		_ = os.WriteFile(BinaryPath+".prior-active", []byte(priorActive), 0o600)
	}
	if e := copyFile(artifact, BinaryPath, 0o755); e != nil {
		return e
	}
	if !wasActive {
		// Preserve the stopped state: install the new binary, do not start it.
		return nil
	}
	if out, code, err := systemctl("restart", "webfleet.service"); err != nil || code != 0 {
		// Restore the old binary and the prior active state on a failed activation.
		_ = systemctlSuccess("stop", "webfleet.service")
		if e := copyFile(BinaryPath+".rollback", BinaryPath, 0o755); e == nil {
			_ = systemctlSuccess("restart", "webfleet.service")
		}
		_ = os.Remove(BinaryPath + ".prior-active")
		return fmt.Errorf("restart after update: %s: %w", bounded(strings.TrimSpace(out)), errorIfNil(err, code, nil))
	}
	return nil
}

func Rollback() error {
	if e := requireLinux(); e != nil {
		return e
	}
	if !isRoot() {
		return errors.New("rollback requires root")
	}
	if e := requireManaged("rollback"); e != nil {
		return e
	}
	if _, e := os.Stat(BinaryPath + ".rollback"); e != nil {
		return errors.New("no rollback binary available")
	}
	// Restore the operational state that existed before the update: read the
	// persisted prior active state and only start the service if it was running.
	priorActive := "active"
	if b, e := os.ReadFile(BinaryPath + ".prior-active"); e == nil {
		priorActive = strings.TrimSpace(string(b))
	}
	wasActive := priorActive == "active"
	cur := BinaryPath + ".failed"
	_ = os.Remove(cur)
	if e := os.Rename(BinaryPath, cur); e != nil {
		return e
	}
	if e := os.Rename(BinaryPath+".rollback", BinaryPath); e != nil {
		_ = os.Rename(cur, BinaryPath)
		return e
	}
	_ = os.Remove(BinaryPath + ".prior-active")
	if !wasActive {
		// Preserve the stopped state: restore the old binary without starting it.
		return nil
	}
	return systemctlSuccess("restart", "webfleet.service")
}
func Executable() string { p, _ := os.Executable(); p, _ = filepath.Abs(p); return p }
