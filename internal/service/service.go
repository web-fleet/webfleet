package service

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

const UnitPath = "/etc/systemd/system/webfleet.service"
const BinaryPath = "/usr/local/bin/webfleet"

// DefaultDataDir is the canonical data directory the installed service owns
// and runs from. It must be independent of the CLI default (./data) because
// `service install` is run as root with no runtime config and the unit it
// writes embeds the path.
const DefaultDataDir = "/var/lib/webfleet"

// ServiceAccount is the dedicated unprivileged account the unit runs as. It is
// created idempotently by Install so a clean machine needs no hidden manual
// prerequisites.
const ServiceUser = "webfleet"
const ServiceGroup = "webfleet"

func Unit(dataDir, listen string) string {
	if dataDir == "" {
		dataDir = DefaultDataDir
	}
	if listen == "" {
		listen = "127.0.0.1:8090"
	}
	return `[Unit]
Description=Web Fleet website monitoring
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=` + ServiceUser + `
Group=` + ServiceGroup + `
Environment=WEBFLEET_DATA_DIR=` + dataDir + `
Environment=WEBFLEET_LISTEN=` + listen + `
ExecStart=` + BinaryPath + `
Restart=on-failure
RestartSec=3
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=` + dataDir + `

[Install]
WantedBy=multi-user.target
`
}
func requireLinux() error {
	if runtime.GOOS != "linux" {
		return errors.New("service management is supported on Linux only")
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
func Install(exe, dataDir, listen string) error {
	if e := requireLinux(); e != nil {
		return e
	}
	if os.Geteuid() != 0 {
		return errors.New("service install requires root")
	}
	if e := ensureServiceAccount(); e != nil {
		return e
	}
	if e := os.MkdirAll(dataDir, 0o700); e != nil {
		return e
	}
	_ = os.Chmod(dataDir, 0o700)
	if e := os.Chown(dataDir, 0, 0); e != nil {
		return e
	}
	if e := chownService(dataDir); e != nil {
		return e
	}
	if _, e := exec.LookPath("systemctl"); e != nil {
		return errors.New("systemctl not found")
	}
	// Preserve any existing binary so activation failure can roll back cleanly.
	hadBinary := false
	if _, e := os.Stat(BinaryPath); e == nil {
		hadBinary = true
		if e := copyFile(BinaryPath, BinaryPath+".preinstall", 0o755); e != nil {
			return e
		}
	}
	installOK := false
	defer func() {
		// Partial failure must not leave a state that looks successfully
		// installed: restore the previous binary and remove the unit.
		if !installOK {
			_ = exec.Command("systemctl", "disable", "webfleet.service").Run()
			_ = os.Remove(UnitPath)
			if hadBinary {
				_ = copyFile(BinaryPath+".preinstall", BinaryPath, 0o755)
			} else {
				_ = os.Remove(BinaryPath)
			}
			_ = os.Remove(BinaryPath + ".preinstall")
		}
	}()
	if e := copyFile(exe, BinaryPath, 0o755); e != nil {
		return e
	}
	if e := os.WriteFile(UnitPath, []byte(Unit(dataDir, listen)), 0o644); e != nil {
		return e
	}
	for _, a := range [][]string{{"daemon-reload"}, {"enable", "--now", "webfleet.service"}} {
		if out, e := exec.Command("systemctl", a...).CombinedOutput(); e != nil {
			return fmt.Errorf("systemctl %s: %s: %w (installation rolled back)", strings.Join(a, " "), strings.TrimSpace(string(out)), e)
		}
	}
	installOK = true
	_ = os.Remove(BinaryPath + ".preinstall")
	return nil
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
func Uninstall() error {
	if e := requireLinux(); e != nil {
		return e
	}
	if os.Geteuid() != 0 {
		return errors.New("service uninstall requires root")
	}
	_ = exec.Command("systemctl", "disable", "--now", "webfleet.service").Run()
	_ = os.Remove(UnitPath)
	_ = exec.Command("systemctl", "daemon-reload").Run()
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
	if os.Geteuid() != 0 {
		return errors.New("update requires root")
	}
	if e := Verify(artifact, want); e != nil {
		return e
	}
	if _, e := os.Stat(BinaryPath); e == nil {
		if e := copyFile(BinaryPath, BinaryPath+".rollback", 0o755); e != nil {
			return e
		}
	}
	if e := copyFile(artifact, BinaryPath, 0o755); e != nil {
		return e
	}
	if out, e := exec.Command("systemctl", "restart", "webfleet.service").CombinedOutput(); e != nil {
		_ = Rollback()
		return fmt.Errorf("restart after update: %s: %w", strings.TrimSpace(string(out)), e)
	}
	return nil
}
func Rollback() error {
	if e := requireLinux(); e != nil {
		return e
	}
	if os.Geteuid() != 0 {
		return errors.New("rollback requires root")
	}
	if _, e := os.Stat(BinaryPath + ".rollback"); e != nil {
		return errors.New("no rollback binary available")
	}
	cur := BinaryPath + ".failed"
	_ = os.Remove(cur)
	if e := os.Rename(BinaryPath, cur); e != nil {
		return e
	}
	if e := os.Rename(BinaryPath+".rollback", BinaryPath); e != nil {
		_ = os.Rename(cur, BinaryPath)
		return e
	}
	return exec.Command("systemctl", "restart", "webfleet.service").Run()
}
func Executable() string { p, _ := os.Executable(); p, _ = filepath.Abs(p); return p }
