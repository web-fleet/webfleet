package auth

import (
	"github.com/web-fleet/webfleet/internal/store"
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
