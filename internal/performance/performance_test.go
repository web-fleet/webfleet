package performance

import (
	"github.com/webfleet-cv/webfleet/internal/sqlite"
	"github.com/webfleet-cv/webfleet/internal/store"
	"testing"
)

func TestSummary(t *testing.T) {
	st, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	sqlite.Exec(st.DB, `INSERT INTO sites(organization_id,name,primary_url,created_at,updated_at) VALUES(1,'x','https://example.com',?,?)`, store.Now(), store.Now())
	for i := int64(1); i <= 5; i++ {
		sqlite.Exec(st.DB, `INSERT INTO monitors(site_id,kind,created_at) VALUES(1,'x'||?,?)`, i, store.Now())
		sqlite.Exec(st.DB, `INSERT INTO check_results(site_id,monitor_id,ok,latency_ms,response_bytes,checked_at) VALUES(1,?,1,?,?,?)`, i, i*10, i*100, store.Now())
	}
	s, e := ForSite(st, 1, 100)
	if e != nil || s.Samples != 5 || s.MedianLatencyMS != 30 {
		t.Fatalf("%+v %v", s, e)
	}
}
