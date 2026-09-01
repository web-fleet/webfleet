package server

import (
	"testing"
	"time"
)

func TestRateLimiterBoundedKeys(t *testing.T) {
	l := newRateLimiter(time.Minute, 10, 3)
	for _, k := range []string{"a", "b", "c"} {
		if !l.Allow(k) {
			t.Fatalf("key %q denied below capacity", k)
		}
	}
	// Distinct attacker-controlled keys cannot grow the bucket map beyond
	// maxKeys; the next new key is denied.
	if l.Allow("d") {
		t.Fatal("key admitted beyond maxKeys")
	}
	if l.Allow("e") {
		t.Fatal("key admitted beyond maxKeys")
	}
}

func TestRateLimiterReclaimsExpiredKeys(t *testing.T) {
	l := newRateLimiter(40*time.Millisecond, 10, 3)
	for _, k := range []string{"a", "b", "c"} {
		if !l.Allow(k) {
			t.Fatalf("key %q denied below capacity", k)
		}
	}
	// After the window passes, expired buckets are evicted so the limiter is
	// not permanently locked at capacity and new distinct keys are admitted.
	time.Sleep(70 * time.Millisecond)
	if !l.Allow("d") {
		t.Fatal("expired buckets were not reclaimed; limiter locked at capacity")
	}
}

func TestRateLimiterPerKeyLimit(t *testing.T) {
	l := newRateLimiter(time.Minute, 2, 100)
	if !l.Allow("a") || !l.Allow("a") {
		t.Fatal("key allowed fewer than limit times")
	}
	if l.Allow("a") {
		t.Fatal("key allowed past its limit")
	}
	// A different key is independent.
	if !l.Allow("b") {
		t.Fatal("independent key affected by another key's limit")
	}
}