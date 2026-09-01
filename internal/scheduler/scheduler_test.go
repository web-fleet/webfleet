package scheduler

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/web-fleet/webfleet/internal/monitor"
	"github.com/web-fleet/webfleet/internal/sites"
	"github.com/web-fleet/webfleet/internal/store"
)

type countingChecker struct {
	mu    sync.Mutex
	calls map[int64]int
}

func (c *countingChecker) CheckSite(ctx context.Context, id int64) (monitor.Result, error) {
	c.mu.Lock()
	c.calls[id]++
	c.mu.Unlock()
	return monitor.Result{SiteID: id, OK: true}, nil
}

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// TestTwoWorkersCheckEachSiteOnce proves the claim/lease boundary: two
// independent worker-capable scheduler instances against the same database
// must not both perform the same due work in the same interval.
func TestTwoWorkersCheckEachSiteOnce(t *testing.T) {
	st, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	site, e := sites.New(st).Create(1, "Example", "https://127.0.0.1:1/", 0)
	if e != nil {
		t.Fatal(e)
	}
	checker := &countingChecker{calls: map[int64]int{}}
	w1 := New(st, checker, nil, nil, nil, time.Minute, 6*time.Hour, 2, discardLog())
	w2 := New(st, checker, nil, nil, nil, time.Minute, 6*time.Hour, 2, discardLog())
	if w1.owner == w2.owner {
		t.Fatal("workers share a lease owner identity")
	}
	ctx := context.Background()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); w1.RunOnce(ctx) }()
	go func() { defer wg.Done(); w2.RunOnce(ctx) }()
	wg.Wait()
	if n := checker.calls[site.ID]; n != 1 {
		t.Fatalf("site checked %d times by two workers in one interval, want 1", n)
	}
}

// TestLeaseExpiryAllowsRecoveryAtSchedulerLevel proves that after a worker's
// lease expires (crash), a second worker can take over the same site.
func TestLeaseExpiryAllowsRecoveryAtSchedulerLevel(t *testing.T) {
	st, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	site, e := sites.New(st).Create(1, "Example", "https://127.0.0.1:1/", 0)
	if e != nil {
		t.Fatal(e)
	}
	ctx := context.Background()
	// First worker acquires and holds a short lease, then "crashes".
	if ok, _ := st.AcquireLease(ctx, "check", site.ID, "worker-a", 30*time.Millisecond); !ok {
		t.Fatal("initial lease failed")
	}
	time.Sleep(60 * time.Millisecond)
	checker := &countingChecker{calls: map[int64]int{}}
	w2 := New(st, checker, nil, nil, nil, time.Minute, 6*time.Hour, 2, discardLog())
	w2.RunOnce(ctx)
	if n := checker.calls[site.ID]; n != 1 {
		t.Fatalf("recovery worker did not claim expired lease: calls=%d", n)
	}
}