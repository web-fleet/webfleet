package service

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const UnitPath = "/etc/systemd/system/webfleet.service"
const BinaryPath = "/usr/local/bin/webfleet"

func Unit(dataDir, listen string) string {
	if dataDir == "" {
		dataDir = "/var/lib/webfleet"
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
User=webfleet
Group=webfleet
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
	if e := os.MkdirAll(dataDir, 0o700); e != nil {
		return e
	}
	_ = os.Chmod(dataDir, 0o700)
	if _, e := exec.LookPath("systemctl"); e != nil {
		return errors.New("systemctl not found")
	}
	if e := copyFile(exe, BinaryPath, 0o755); e != nil {
		return e
	}
	if e := os.WriteFile(UnitPath, []byte(Unit(dataDir, listen)), 0o644); e != nil {
		return e
	}
	for _, a := range [][]string{{"daemon-reload"}, {"enable", "--now", "webfleet.service"}} {
		if out, e := exec.Command("systemctl", a...).CombinedOutput(); e != nil {
			return fmt.Errorf("systemctl %s: %s: %w", strings.Join(a, " "), strings.TrimSpace(string(out)), e)
		}
	}
	return nil
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
