package scheduler

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/webfleet-cv/webfleet/internal/sites"
	"github.com/webfleet-cv/webfleet/internal/sqlite"
	"github.com/webfleet-cv/webfleet/internal/store"
)

// runClaimMatrix is the provider-neutral scheduler claim/lease adversarial
// contract. It runs against both SQLite and real PostgreSQL so transaction
// semantics at this boundary are proven on each provider, not assumed.
func runClaimMatrix(t *testing.T, st *store.Store) {
	ctx := context.Background()
	now := time.Now().UTC()

	// Claim exclusion: one owner at a time while unexpired.
	_, ok, err := st.ClaimDue(ctx, "check", 101, "worker-a", now, now.Add(time.Minute))
	if err != nil || !ok {
		t.Fatalf("claim a: %v %v", ok, err)
	}
	if _, ok, _ := st.ClaimDue(ctx, "check", 101, "worker-b", now, now.Add(time.Minute)); ok {
		t.Fatal("second owner claimed the same unit")
	}
	// A different unit is independent.
	if _, ok, _ := st.ClaimDue(ctx, "check", 102, "worker-b", now, now.Add(time.Minute)); !ok {
		t.Fatal("independent unit not claimable")
	}

	// Renewal prevents takeover by a live owner.
	gen, ok, _ := st.ClaimDue(ctx, "crawl", 101, "worker-a", now, now.Add(time.Minute))
	if !ok {
		t.Fatal("crawl claim failed")
	}
	if ok, _ := st.RenewClaim(ctx, "crawl", 101, "worker-a", gen, now.Add(2*time.Minute)); !ok {
		t.Fatal("renewal failed")
	}
	if _, ok, _ := st.ClaimDue(ctx, "crawl", 101, "worker-b", now.Add(30*time.Second), now.Add(time.Minute)); ok {
		t.Fatal("live claim stolen despite renewal")
	}

	// Expiry recovery with fencing: a stale owner cannot complete the new
	// owner's slot.
	genA, ok, _ := st.ClaimDue(ctx, "check", 103, "worker-a", now, now.Add(time.Minute))
	if !ok {
		t.Fatal("check claim 3 failed")
	}
	if _, ok, _ := st.ClaimDue(ctx, "check", 103, "worker-b", now.Add(2*time.Minute), now.Add(3*time.Minute)); !ok {
		t.Fatal("takeover after expiry failed")
	}
	if err := st.CompleteClaim(ctx, "check", 103, "worker-a", genA, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	rows, e := sqlite.Query(st.DB, `SELECT owner,generation FROM scheduler_claims WHERE claim_kind='check' AND site_id=103`)
	if e != nil || len(rows) != 1 || rows[0]["owner"].Text != "worker-b" {
		t.Fatalf("stale owner corrupted the claim: %v %v", rows, e)
	}

	// Completion advances next_due_at; not claimable before it passes.
	genB, ok, _ := st.ClaimDue(ctx, "check", 104, "worker-a", now, now.Add(time.Minute))
	if !ok {
		t.Fatal("check claim 4 failed")
	}
	nextDue := now.Add(30 * time.Second)
	if err := st.CompleteClaim(ctx, "check", 104, "worker-a", genB, nextDue); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.ClaimDue(ctx, "check", 104, "worker-b", now.Add(10*time.Second), now.Add(time.Minute)); ok {
		t.Fatal("claimed before next_due_at")
	}
	if _, ok, _ := st.ClaimDue(ctx, "check", 104, "worker-b", nextDue.Add(time.Second), nextDue.Add(time.Minute)); !ok {
		t.Fatal("not claimable at next_due_at")
	}

	// Check and crawl kinds are independent.
	if _, ok, _ := st.ClaimDue(ctx, "check", 105, "worker-a", now, now.Add(time.Minute)); !ok {
		t.Fatal("check 5 claim failed")
	}
	if _, ok, _ := st.ClaimDue(ctx, "crawl", 105, "worker-a", now, now.Add(time.Minute)); !ok {
		t.Fatal("crawl 5 claim failed (kinds not independent)")
	}

	// Two scheduler instances against the same database perform one execution
	// per due slot, and a crashed worker's expired claim is reclaimed.
	site, err := sites.New(st).Create(1, "Example", "https://127.0.0.1:1/", 0)
	if err != nil {
		t.Fatal(err)
	}
	checker := &countingChecker{calls: map[int64]int{}}
	w1 := New(st, checker, nil, nil, nil, time.Minute, 6*time.Hour, 2, discardLog())
	w2 := New(st, checker, nil, nil, nil, time.Minute, 6*time.Hour, 2, discardLog())
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); w1.RunOnce(ctx) }()
	go func() { defer wg.Done(); w2.RunOnce(ctx) }()
	wg.Wait()
	if n := checker.calls[site.ID]; n != 1 {
		t.Fatalf("site executed %d times across two workers, want 1", n)
	}
	// Crash recovery: a stale owner's claim with an expired lease is reclaimed.
	site2, err := sites.New(st).Create(1, "Recovery", "https://127.0.0.1:1/", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.ClaimDue(ctx, "check", site2.ID, "worker-a", now.Add(-10*time.Minute), now.Add(-5*time.Minute)); !ok {
		t.Fatal("seed stale claim failed")
	}
	checker2 := &countingChecker{calls: map[int64]int{}}
	w3 := New(st, checker2, nil, nil, nil, time.Minute, 6*time.Hour, 2, discardLog())
	w3.RunOnce(ctx)
	if n := checker2.calls[site2.ID]; n != 1 {
		t.Fatalf("recovery worker did not reclaim expired slot: %d", n)
	}
}

func TestClaimMatrixSQLite(t *testing.T) {
	st, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	runClaimMatrix(t, st)
}

func TestClaimMatrixPostgres(t *testing.T) {
	base := os.Getenv("WEBFLEET_TEST_POSTGRES_URL")
	if base == "" {
		t.Skip("WEBFLEET_TEST_POSTGRES_URL not set")
	}
	u, e := url.Parse(base)
	if e != nil {
		t.Fatal(e)
	}
	name := fmt.Sprintf("wf_claim_%d", time.Now().UnixNano())
	admin, e := store.OpenPostgres(context.Background(), base)
	if e != nil {
		t.Fatal(e)
	}
	if _, e := admin.DB.ExecContext(context.Background(), "CREATE DATABASE "+name); e != nil {
		admin.Close()
		t.Fatal(e)
	}
	admin.Close()
	u.Path = "/" + name
	st, e := store.OpenPostgres(context.Background(), u.String())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		st.Close()
		admin, e := store.OpenPostgres(context.Background(), base)
		if e == nil {
			_, _ = admin.DB.ExecContext(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
			admin.Close()
		}
	})
	runClaimMatrix(t, st)
}
