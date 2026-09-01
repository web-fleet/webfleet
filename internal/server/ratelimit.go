package server

import (
	"sync"
	"time"
)

// rateLimiter is a fixed-window per-key limiter with bounded memory. It is a
// deliberately simple abuse control for a self-hosted application, keyed by
// the resolved client address, not a distributed rate limiter.
type rateLimiter struct {
	mu      sync.Mutex
	window  time.Duration
	limit   int
	maxKeys int
	buckets map[string]*rateBucket
}

type rateBucket struct {
	count int
	reset time.Time
}

func newRateLimiter(window time.Duration, limit, maxKeys int) *rateLimiter {
	return &rateLimiter{window: window, limit: limit, maxKeys: maxKeys, buckets: map[string]*rateBucket{}}
}

// Allow reports whether the key may proceed. Expired buckets are evicted on
// each call; when the bucket map would exceed maxKeys the request is denied so
// attacker-controlled keys cannot grow memory without bound.
func (l *rateLimiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	b, ok := l.buckets[key]
	if !ok {
		for k, old := range l.buckets {
			if now.After(old.reset) {
				delete(l.buckets, k)
			}
		}
		if len(l.buckets) >= l.maxKeys {
			return false
		}
		b = &rateBucket{reset: now.Add(l.window)}
		l.buckets[key] = b
	}
	if now.After(b.reset) {
		b.count = 0
		b.reset = now.Add(l.window)
	}
	if b.count >= l.limit {
		return false
	}
	b.count++
	return true
}