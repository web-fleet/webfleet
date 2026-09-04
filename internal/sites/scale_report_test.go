package sites_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/webfleet-cv/webfleet/internal/fleet"
	"github.com/webfleet-cv/webfleet/internal/sites"
	"github.com/webfleet-cv/webfleet/internal/sqlite"
	"github.com/webfleet-cv/webfleet/internal/store"
)

// TestScaleReport measures representative large-fleet paths at 100/1,000/10,000
// sites. It is intentionally not part of the normal suite: run it explicitly
// with WEBFLEET_SCALE=1 to record evidence (scripts/scale.sh).
func TestScaleReport(t *testing.T) {
	if os.Getenv("WEBFLEET_SCALE") == "" {
		t.Skip("set WEBFLEET_SCALE=1 to run the scale report")
	}
	for _, n := range []int{100, 1000, 10000} {
		t.Run(fmt.Sprintf("sites-%d", n), func(t *testing.T) {
			st, e := store.Open(t.TempDir())
			if e != nil {
				t.Fatal(e)
			}
			defer st.Close()
			now := store.Now()
			seedStart := time.Now()
			for i := 0; i < n; i++ {
				if e := sqlite.Exec(st.DB, `INSERT INTO sites(organization_id,name,primary_url,created_at,updated_at) VALUES(1,?,?,?,?)`, fmt.Sprintf("Site %04d", i), fmt.Sprintf("https://s%d.example", i), now, now); e != nil {
					t.Fatal(e)
				}
			}
			t.Logf("seed %d sites: %s", n, time.Since(seedStart))
			svc := sites.New(st)

			measure := func(label string, fn func() error) {
				start := time.Now()
				if err := fn(); err != nil {
					t.Fatal(err)
				}
				t.Logf("%s: %s", label, time.Since(start))
			}

			measure("list page", func() error {
				l, err := svc.List(1, "", 0, 1, 100, false)
				if l.Total != n {
					t.Fatalf("list total %d", l.Total)
				}
				return err
			})
			measure("search page", func() error {
				_, err := svc.List(1, "0500", 0, 1, 50, false)
				return err
			})
			measure("fleet summary", func() error {
				s, err := fleet.SummaryFor(st, 1)
				if s.Total != int64(n) {
					t.Fatalf("fleet total %d", s.Total)
				}
				return err
			})
			measure("tag filter (no tags)", func() error {
				_, err := svc.ListByTag(1, "", 0, "nonexistent", 1, 20)
				return err
			})
			measure("scheduler claim overhead (all sites)", func() error {
				rows, err := sqlite.Query(st.DB, `SELECT id FROM sites WHERE enabled=1 AND archived_at IS NULL`)
				if err != nil {
					return err
				}
				now := time.Now().UTC()
				start := time.Now()
				for _, r := range rows {
					if _, _, err := st.ClaimDue(context.Background(), "check", r["id"].Int64, "worker-a", now, now.Add(time.Minute)); err != nil {
						return err
					}
				}
				t.Logf("claim loop over %d rows: %s", len(rows), time.Since(start))
				return nil
			})
		})
	}
}

// TestScaleReportPostgres measures scheduler claim contention and listing on a
// real PostgreSQL server when WEBFLEET_TEST_POSTGRES_URL is set.
func TestScaleReportPostgres(t *testing.T) {
	base := os.Getenv("WEBFLEET_TEST_POSTGRES_URL")
	if os.Getenv("WEBFLEET_SCALE") == "" || base == "" {
		t.Skip("set WEBFLEET_SCALE=1 and WEBFLEET_TEST_POSTGRES_URL to run")
	}
	u, e := url.Parse(base)
	if e != nil {
		t.Fatal(e)
	}
	name := fmt.Sprintf("wf_scale_%d", time.Now().UnixNano())
	admin, e := store.OpenPostgres(context.Background(), base)
	if e != nil {
		t.Fatal(e)
	}
	if _, e := admin.DB.ExecContext(context.Background(), "CREATE DATABASE "+name); e != nil {
		admin.Close()
		t.Fatal(e)
	}
	admin.Close()
	u.Path = "/" + name
	st, e := store.OpenPostgres(context.Background(), u.String())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() {
		st.Close()
		admin, e := store.OpenPostgres(context.Background(), base)
		if e == nil {
			_, _ = admin.DB.ExecContext(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
			admin.Close()
		}
	})
	now := store.Now()
	n := 1000
	for i := 0; i < n; i++ {
		if e := sqlite.Exec(st.DB, `INSERT INTO sites(organization_id,name,primary_url,created_at,updated_at) VALUES(1,?,?,?,?)`, fmt.Sprintf("Site %04d", i), fmt.Sprintf("https://s%d.example", i), now, now); e != nil {
			t.Fatal(e)
		}
	}
	tc := time.Now().UTC()
	start := time.Now()
	for i := 1; i <= n; i++ {
		if _, _, e := st.ClaimDue(context.Background(), "check", int64(i), "worker-a", tc, tc.Add(time.Minute)); e != nil {
			t.Fatal(e)
		}
	}
	t.Logf("postgres claim loop over %d rows: %s", n, time.Since(start))
	lstart := time.Now()
	l, e := sites.New(st).List(1, "0500", 0, 1, 50, false)
	if e != nil || l.Total != 1 {
		t.Fatalf("pg list: %+v %v", l, e)
	}
	t.Logf("postgres search page: %s", time.Since(lstart))
}
