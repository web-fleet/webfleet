package maintenance

import (
	"github.com/web-fleet/webfleet/internal/sqlite"
	"github.com/web-fleet/webfleet/internal/store"
	"testing"
	"time"
)

func TestRetention(t *testing.T) {
	st, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	sqlite.Exec(st.DB, `INSERT INTO sites(organization_id,name,primary_url,created_at,updated_at) VALUES(1,'x','https://x',?,?)`, store.Now(), store.Now())
	sqlite.Exec(st.DB, `INSERT INTO monitors(site_id,kind,created_at) VALUES(1,'http',?)`, store.Now())
	old := time.Now().UTC().AddDate(0, 0, -100).Format(time.RFC3339Nano)
	sqlite.Exec(st.DB, `INSERT INTO check_results(site_id,monitor_id,ok,checked_at) VALUES(1,1,1,?)`, old)
	s := New(st)
	if e = s.Run(); e != nil {
		t.Fatal(e)
	}
	x, _ := s.Status()
	if x.Checks != 0 {
		t.Fatal(x.Checks)
	}
}
