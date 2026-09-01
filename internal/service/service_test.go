package service

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestUnitHardening(t *testing.T) {
	u := Unit("/var/lib/webfleet", "127.0.0.1:8090")
	for _, x := range []string{"NoNewPrivileges=true", "ProtectSystem=strict", "ReadWritePaths=/var/lib/webfleet", "ExecStart=/usr/local/bin/webfleet", "User=" + ServiceUser, "Group=" + ServiceGroup} {
		if !strings.Contains(u, x) {
			t.Fatal(x)
		}
	}
}

// TestInstallRequiresRootAndLinux guards the lifecycle boundary: without root a
// clean machine cannot be installed, and the function must fail before doing
// anything rather than half-installing.
func TestInstallRequiresRootAndLinux(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("linux-only")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root; root install path is exercised on a real host")
	}
	if e := Install("", t.TempDir(), "127.0.0.1:0"); e == nil {
		t.Fatal("install succeeded without root")
	}
}
func TestVerify(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x")
	os.WriteFile(p, []byte("abc"), 0600)
	h := sha256.Sum256([]byte("abc"))
	if e := Verify(p, hex.EncodeToString(h[:])); e != nil {
		t.Fatal(e)
	}
	if e := Verify(p, "deadbeef"); e == nil {
		t.Fatal("bad checksum accepted")
	}
}
