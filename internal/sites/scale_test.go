package sites

import (
	"fmt"
	"github.com/web-fleet/webfleet/internal/sqlite"
	"github.com/web-fleet/webfleet/internal/store"
	"testing"
	"time"
)

func TestListThousandSites(t *testing.T) {
	st, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	now := store.Now()
	for i := 0; i < 1000; i++ {
		if e = sqlite.Exec(st.DB, `INSERT INTO sites(organization_id,name,primary_url,created_at,updated_at) VALUES(1,?,?,?,?)`, fmt.Sprintf("Site %04d", i), fmt.Sprintf("https://s%d.example", i), now, now); e != nil {
			t.Fatal(e)
		}
	}
	start := time.Now()
	x, e := New(st).List(1, "Site 09", 0, 1, 50, false)
	if e != nil {
		t.Fatal(e)
	}
	if x.Total != 100 {
		t.Fatalf("total %d", x.Total)
	}
	if time.Since(start) > time.Second {
		t.Fatalf("large fleet list too slow: %v", time.Since(start))
	}
}
