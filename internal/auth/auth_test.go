package auth

import (
	"context"
	"github.com/web-fleet/webfleet/internal/sqlite"
	"github.com/web-fleet/webfleet/internal/store"
	"sync"
	"testing"
)

func TestFirstAdminAndSessionLifecycle(t *testing.T) {
	st, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	a := New(st)
	need, e := a.NeedsSetup()
	if e != nil || !need {
		t.Fatalf("need=%v err=%v", need, e)
	}
	if e = a.CreateAdmin("admin@example.com", "secret7"); e != nil {
		t.Fatal(e)
	}
	if e = a.CreateAdmin("other@example.com", "secret7"); e == nil {
		t.Fatal("second first-admin allowed")
	}
	tok, s, e := a.Login("admin@example.com", "secret7")
	if e != nil || tok == "" || s.CSRF == "" {
		t.Fatalf("login failed: %v", e)
	}
	if _, e = a.Session(tok); e != nil {
		t.Fatal(e)
	}
	a.Logout(tok)
	if _, e = a.Session(tok); e == nil {
		t.Fatal("logged-out session survived")
	}
}

func TestFirstAdminGetsOwnerMembership(t *testing.T) {
	st, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	a := New(st)
	if e = a.CreateAdmin("admin@example.com", "secret7"); e != nil {
		t.Fatal(e)
	}
	org, e := st.PrimaryOrgID(context.Background())
	if e != nil {
		t.Fatal(e)
	}
	rows, e := sqlite.Query(st.DB, `SELECT user_id,role FROM organization_memberships WHERE organization_id=?`, org)
	if e != nil {
		t.Fatal(e)
	}
	if len(rows) != 1 {
		t.Fatalf("expected exactly one owner membership, got %d", len(rows))
	}
	if rows[0]["role"].Text != "owner" {
		t.Fatalf("expected owner role, got %q", rows[0]["role"].Text)
	}
}

func TestConcurrentFirstAdminRaceGuard(t *testing.T) {
	st, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	a := New(st)
	const n = 8
	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = a.CreateAdmin("admin"+string(rune('a'+i))+"@example.com", "secret7")
		}(i)
	}
	wg.Wait()
	created := 0
	for _, err := range errs {
		if err == nil {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("expected exactly one successful first-admin creation, got %d", created)
	}
	rows, e := sqlite.Query(st.DB, `SELECT COUNT(*) n FROM users`)
	if e != nil {
		t.Fatal(e)
	}
	if rows[0]["n"].Int64 != 1 {
		t.Fatalf("expected exactly one user, got %d", rows[0]["n"].Int64)
	}
}
