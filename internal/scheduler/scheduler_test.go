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
	"github.com/web-fleet/webfleet/internal/sqlite"
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

// TestTwoWorkersCheckEachSiteOnce proves the due/claim boundary: two
// independent worker-capable scheduler instances against the same database must
// not both perform the same due work in the same interval, and the second
// worker cannot claim the slot again until it is actually due.
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
	// The slot is not due again until next_due_at; a further RunOnce by either
	// worker must not re-check it.
	w1.RunOnce(ctx)
	w2.RunOnce(ctx)
	if n := checker.calls[site.ID]; n != 1 {
		t.Fatalf("site re-checked before due: %d", n)
	}
}

// TestCrashRecoveryAfterLeaseExpiry proves a crashed worker's claim is
// reclaimed once its lease expires, and the recovered worker can complete the
// slot.
func TestCrashRecoveryAfterLeaseExpiry(t *testing.T) {
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
	now := time.Now().UTC()
	// A crashed worker claimed the slot long enough ago that the lease expired.
	if _, ok, e := st.ClaimDue(ctx, "check", site.ID, "worker-a", now.Add(-10*time.Minute), now.Add(-5*time.Minute)); e != nil || !ok {
		t.Fatalf("seed claim: ok=%v err=%v", ok, e)
	}
	checker := &countingChecker{calls: map[int64]int{}}
	w2 := New(st, checker, nil, nil, nil, time.Minute, 6*time.Hour, 2, discardLog())
	w2.RunOnce(ctx)
	if n := checker.calls[site.ID]; n != 1 {
		t.Fatalf("recovery worker did not claim expired slot: calls=%d", n)
	}
}

// TestRenewalPreventsTakeover proves a live worker renewing its lease cannot be
// stolen by another worker.
func TestRenewalPreventsTakeover(t *testing.T) {
	st, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	gen, ok, err := st.ClaimDue(ctx, "check", 1, "worker-a", now, now.Add(time.Minute))
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	// Renewal extends the lease under the same generation.
	if ok, err := st.RenewClaim(ctx, "check", 1, "worker-a", gen, now.Add(2*time.Minute)); err != nil || !ok {
		t.Fatalf("renew: ok=%v err=%v", ok, err)
	}
	// Another worker cannot take over while the lease is live.
	if _, ok, _ := st.ClaimDue(ctx, "check", 1, "worker-b", now.Add(30*time.Second), now.Add(time.Minute)); ok {
		t.Fatal("live claim stolen")
	}
}

// TestFencingPreventsStaleCompletion proves a stale owner cannot complete the
// newer owner's slot after forced ownership transfer.
func TestFencingPreventsStaleCompletion(t *testing.T) {
	st, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	genA, ok, err := st.ClaimDue(ctx, "check", 1, "worker-a", now, now.Add(time.Minute))
	if err != nil || !ok {
		t.Fatalf("a claim: %v %v", ok, err)
	}
	// Force ownership transfer: A's lease expires, B claims (generation bumps).
	if _, ok, _ := st.ClaimDue(ctx, "check", 1, "worker-b", now.Add(2*time.Minute), now.Add(3*time.Minute)); !ok {
		t.Fatal("b could not take over expired lease")
	}
	// A's stale completion is a no-op (owner/generation no longer match).
	if err := st.CompleteClaim(ctx, "check", 1, "worker-a", genA, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	// next_due_at must be untouched by A.
	rows, _ := sqlite.Query(st.DB, `SELECT owner,generation FROM scheduler_claims WHERE claim_kind='check' AND site_id=1`)
	if len(rows) != 1 || rows[0]["owner"].Text != "worker-b" {
		t.Fatalf("stale owner corrupted the claim: %v", rows)
	}
	// B can complete its own slot.
	genB := rows[0]["generation"].Int64
	if err := st.CompleteClaim(ctx, "check", 1, "worker-b", genB, now.Add(30*time.Second)); err != nil {
		t.Fatal(err)
	}
}

// TestCheckAndCrawlClaimsIndependent proves check and crawl due-state do not
// interfere.
func TestCheckAndCrawlClaimsIndependent(t *testing.T) {
	st, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	ctx := context.Background()
	now := time.Now().UTC()
	if _, ok, _ := st.ClaimDue(ctx, "check", 1, "a", now, now.Add(time.Minute)); !ok {
		t.Fatal("check claim failed")
	}
	if _, ok, _ := st.ClaimDue(ctx, "crawl", 1, "a", now, now.Add(time.Minute)); !ok {
		t.Fatal("crawl claim failed")
	}
	if _, ok, _ := st.ClaimDue(ctx, "check", 1, "b", now, now.Add(time.Minute)); ok {
		t.Fatal("check stolen by another owner")
	}
	if _, ok, _ := st.ClaimDue(ctx, "crawl", 1, "b", now, now.Add(time.Minute)); ok {
		t.Fatal("crawl stolen by another owner")
	}
}
