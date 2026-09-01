package config

import (
	"os"
	"testing"
	"time"
)

func TestLoadDefaultsAndOverrides(t *testing.T) {
	t.Setenv("WEBFLEET_DATA_DIR", t.TempDir())
	t.Setenv("WEBFLEET_CHECK_INTERVAL", "45s")
	t.Setenv("WEBFLEET_CHECK_CONCURRENCY", "12")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != "127.0.0.1:8090" || c.CheckInterval != 45*time.Second || c.CheckConcurrency != 12 || c.AuditSandbox != "strict" {
		t.Fatalf("unexpected config: %+v", c)
	}
}
func TestLoadRejectsBadConcurrency(t *testing.T) {
	t.Setenv("WEBFLEET_DATA_DIR", t.TempDir())
	t.Setenv("WEBFLEET_CHECK_CONCURRENCY", "0")
	if _, err := Load(); err == nil {
		t.Fatal("expected error")
	}
	os.Unsetenv("WEBFLEET_CHECK_CONCURRENCY")
}
func TestLoadAuditSandboxOverride(t *testing.T) {
	t.Setenv("WEBFLEET_DATA_DIR", t.TempDir())
	t.Setenv("WEBFLEET_AUDIT_SANDBOX", "allow-no-sandbox")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.AuditSandbox != "allow-no-sandbox" {
		t.Fatalf("audit sandbox = %q", c.AuditSandbox)
	}
	t.Setenv("WEBFLEET_AUDIT_SANDBOX", "bogus")
	if _, err := Load(); err == nil {
		t.Fatal("bogus audit sandbox accepted")
	}
}

func TestParsePrefixList(t *testing.T) {
	got, err := ParsePrefixList("127.0.0.1, 10.0.0.0/8, ::1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("prefixes = %v", got)
	}
	if got[0].String() != "127.0.0.1/32" || got[1].String() != "10.0.0.0/8" || got[2].String() != "::1/128" {
		t.Fatalf("prefixes = %v", got)
	}
	if _, err := ParsePrefixList("not-an-ip"); err == nil {
		t.Fatal("invalid prefix accepted")
	}
}

func TestLoadTrustedProxies(t *testing.T) {
	t.Setenv("WEBFLEET_DATA_DIR", t.TempDir())
	t.Setenv("WEBFLEET_TRUSTED_PROXIES", "127.0.0.1, 192.168.1.0/24")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(c.TrustedProxies) != 2 {
		t.Fatalf("trusted proxies = %v", c.TrustedProxies)
	}
	t.Setenv("WEBFLEET_TRUSTED_PROXIES", "bogus")
	if _, err := Load(); err == nil {
		t.Fatal("bogus trusted proxy accepted")
	}
}

func TestLoadPublicURL(t *testing.T) {
	t.Setenv("WEBFLEET_DATA_DIR", t.TempDir())
	t.Setenv("WEBFLEET_PUBLIC_URL", "https://webfleet.example.com/")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.PublicURL != "https://webfleet.example.com" {
		t.Fatalf("public url = %q", c.PublicURL)
	}
	for _, bad := range []string{"not-a-url", "ftp://x", "https://x/path", "https://user:pass@x"} {
		t.Setenv("WEBFLEET_PUBLIC_URL", bad)
		if _, err := Load(); err == nil {
			t.Fatalf("invalid public URL accepted: %q", bad)
		}
	}
}
