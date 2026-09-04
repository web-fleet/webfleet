package sites

import (
	"github.com/webfleet-cv/webfleet/internal/sqlite"
	"github.com/webfleet-cv/webfleet/internal/store"
	"testing"
)

func TestCanonicalURL(t *testing.T) {
	u, e := CanonicalURL(" HTTPS://Example.COM/path#frag ")
	if e != nil || u != "https://example.com/path" {
		t.Fatalf("url=%q err=%v", u, e)
	}
	for _, bad := range []string{"file:///tmp/x", "https://user:pass@example.com", "example.com"} {
		if _, e := CanonicalURL(bad); e == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
}
func TestSiteCRUDSearchGroupPagination(t *testing.T) {
	st, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	svc := New(st)
	g, e := svc.CreateGroup(1, "Clients")
	if e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 25; i++ {
		name := "Site"
		if i == 7 {
			name = "Needle"
		}
		if _, e = svc.Create(1, name, "https://example.com", g.ID); e != nil {
			t.Fatal(e)
		}
	}
	list, e := svc.List(1, "", g.ID, 2, 10, false)
	if e != nil || len(list.Sites) != 10 || list.Total != 25 || list.Pages != 3 {
		t.Fatalf("list=%+v err=%v", list, e)
	}
	found, e := svc.List(1, "needle", 0, 1, 20, false)
	if e != nil || found.Total != 1 {
		t.Fatalf("search=%+v err=%v", found, e)
	}
	id := found.Sites[0].ID
	if e = svc.Delete(1, id); e == nil {
		t.Fatal("deleted unarchived site")
	}
	if e = svc.Archive(1, id, true); e != nil {
		t.Fatal(e)
	}
	if e = svc.Delete(1, id); e != nil {
		t.Fatal(e)
	}
}

func TestListByTagCombinationsAndIsolation(t *testing.T) {
	st, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	svc := New(st)
	g, e := svc.CreateGroup(1, "Clients")
	if e != nil {
		t.Fatal(e)
	}
	for i, name := range []string{"Alpha", "Beta", "Gamma"} {
		s, e := svc.Create(1, name, "https://example.com", g.ID)
		if e != nil {
			t.Fatal(e)
		}
		if i < 2 {
			if e := svc.SetTags(1, s.ID, []string{"prod"}); e != nil {
				t.Fatal(e)
			}
		}
	}
	// Tag filter narrows to tagged sites.
	tagged, e := svc.ListByTag(1, "", 0, "prod", 1, 20)
	if e != nil || tagged.Total != 2 {
		t.Fatalf("tag filter: %+v %v", tagged, e)
	}
	// Tag + text search.
	search, e := svc.ListByTag(1, "alpha", 0, "prod", 1, 20)
	if e != nil || search.Total != 1 || search.Sites[0].Name != "Alpha" {
		t.Fatalf("tag+search: %+v %v", search, e)
	}
	// Tag + group + pagination.
	page, e := svc.ListByTag(1, "", g.ID, "prod", 1, 1)
	if e != nil || len(page.Sites) != 1 || page.Total != 2 || page.Pages != 2 {
		t.Fatalf("tag+group+page: %+v %v", page, e)
	}
	// Organization isolation: a second org's tagged sites are invisible.
	if e := sqlite.Exec(st.DB, `INSERT INTO organizations(name,created_at) VALUES('B',?)`, store.Now()); e != nil {
		t.Fatal(e)
	}
	var orgB int64
	_ = st.DB.QueryRow(`SELECT id FROM organizations WHERE name='B'`).Scan(&orgB)
	sB, e := svc.Create(orgB, "Bsite", "https://b.example", 0)
	if e != nil {
		t.Fatal(e)
	}
	_ = svc.SetTags(orgB, sB.ID, []string{"prod"})
	isolated, e := svc.ListByTag(1, "", 0, "prod", 1, 20)
	if e != nil || isolated.Total != 2 {
		t.Fatalf("cross-org tag leak: %+v %v", isolated, e)
	}
}
