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
	s := New(st)
	x, e := s.Create(1, 1, "ci", []string{"sites:read"})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = s.Authenticate(x.Token, "sites:read"); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Authenticate(x.Token, "sites:write"); e == nil {
		t.Fatal("scope bypass")
	}
	s.Revoke(x.ID, 1)
	if _, e = s.Authenticate(x.Token, "sites:read"); e == nil {
		t.Fatal("revoked accepted")
	}
}
