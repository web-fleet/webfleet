package apitokens

import (
	"github.com/web-fleet/webfleet/internal/auth"
	"github.com/web-fleet/webfleet/internal/store"
	"testing"
)

func TestScopeAndRevoke(t *testing.T) {
	st, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	a := auth.New(st)
	if e = a.CreateAdmin("a@b.c", "password"); e != nil {
		t.Fatal(e)
	}
	var uid, oid int64
	if e = st.DB.QueryRow(`SELECT id FROM users WHERE email='a@b.c'`).Scan(&uid); e != nil {
		t.Fatal(e)
	}
	if e = st.DB.QueryRow(`SELECT id FROM organizations ORDER BY id LIMIT 1`).Scan(&oid); e != nil {
		t.Fatal(e)
	}
	s := New(st)
	x, e := s.Create(uid, oid, "ci", []string{"sites:read"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Authenticate(x.Token, "sites:read"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Authenticate(x.Token, "sites:write"); e == nil {
		t.Fatal("scope bypass")
	}
	s.Revoke(x.ID, uid, oid)
	if _, e = s.Authenticate(x.Token, "sites:read"); e == nil {
		t.Fatal("revoked accepted")
	}
}
