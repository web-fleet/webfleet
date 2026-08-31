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
	if c.Listen != "127.0.0.1:8090" || c.CheckInterval != 45*time.Second || c.CheckConcurrency != 12 {
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
