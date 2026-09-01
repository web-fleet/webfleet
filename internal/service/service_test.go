package service

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnitHardening(t *testing.T) {
	u := Unit("/var/lib/webfleet", "127.0.0.1:8090")
	for _, x := range []string{"NoNewPrivileges=true", "ProtectSystem=strict", "ReadWritePaths=/var/lib/webfleet", "ExecStart=/usr/local/bin/webfleet"} {
		if !strings.Contains(u, x) {
			t.Fatal(x)
		}
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
