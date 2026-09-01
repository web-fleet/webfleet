package apitokens

import (
	"github.com/web-fleet/webfleet/internal/sqlite"
	"github.com/web-fleet/webfleet/internal/store"
	"testing"
)

func TestScopeAndRevoke(t *testing.T) {
	st, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	sqlite.Exec(st.DB, `INSERT INTO organizations(name,created_at) SELECT 'Default',? WHERE NOT EXISTS(SELECT 1 FROM organizations WHERE id=1)`, store.Now())
	sqlite.Exec(st.DB, `INSERT INTO users(email,password_hash,created_at) VALUES('a@b.c','x',?)`, store.Now())
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
	s.Revoke(x.ID, uid)
	if _, e = s.Authenticate(x.Token, "sites:read"); e == nil {
		t.Fatal("revoked accepted")
	}
}
