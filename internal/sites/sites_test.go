package sites

import (
	"github.com/web-fleet/webfleet/internal/store"
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
