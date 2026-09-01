package audit

import (
	"context"
	"github.com/web-fleet/webfleet/internal/sqlite"
	"github.com/web-fleet/webfleet/internal/store"
	"testing"
)

type fake struct{}

func (fake) Run(context.Context, string) (Result, error) {
	return Result{Status: "complete", Performance: 90, Accessibility: 91, BestPractices: 92, Discoverability: 93, Findings: []string{"x"}}, nil
}
func TestHistoryOptIn(t *testing.T) {
	st, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	sqlite.Exec(st.DB, `INSERT INTO sites(organization_id,name,primary_url,created_at,updated_at) VALUES(1,'x','https://example.com',?,?)`, store.Now(), store.Now())
	s := NewWithRunner(st, fake{})
	s.Run(context.Background(), 1)
	s.Run(context.Background(), 1)
	h, _ := s.History(1)
	if len(h) != 1 {
		t.Fatalf("history default %d", len(h))
	}
	s.SetHistory(1, true)
	s.Run(context.Background(), 1)
	h, _ = s.History(1)
	if len(h) != 2 {
		t.Fatalf("history enabled %d", len(h))
	}
}
