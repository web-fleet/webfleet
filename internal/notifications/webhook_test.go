package notifications

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/web-fleet/webfleet/internal/incidents"
	"github.com/web-fleet/webfleet/internal/netguard"
	"github.com/web-fleet/webfleet/internal/sqlite"
	"github.com/web-fleet/webfleet/internal/store"
)

type mapResolver map[string][]netip.Addr

func (m mapResolver) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	if ips, ok := m[host]; ok {
		return ips, nil
	}
	return nil, &net.DNSError{Err: "no such host", Name: host}
}

func testGuard(allowPrivate bool) netguard.Guard {
	g := netguard.New()
	g.AllowPrivate = allowPrivate
	return g
}

func insertSite(t *testing.T, st *store.Store) int64 {
	t.Helper()
	r, e := sqlite.Query(st.DB, `INSERT INTO sites(organization_id,name,primary_url,created_at,updated_at) VALUES(1,'s','https://example.com',?,?) RETURNING id`, store.Now(), store.Now())
	if e != nil {
		t.Fatal(e)
	}
	return r[0]["id"].Int64
}

func TestIncidentTransitionEnqueuesWebhookOutbox(t *testing.T) {
	st, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	// Create a webhook to a public IP literal (allowed by the guard) so the
	// outbox enqueue has a destination.
	w, _, e := New(st).Create(1, "hook", "https://8.8.8.8/hook")
	if e != nil {
		t.Fatal(e)
	}
	siteID := insertSite(t, st)
	svc := incidents.New(st)
	now := store.Now()
	if e = svc.Transition(siteID, "unknown", "down", now); e != nil {
		t.Fatal(e)
	}
	rows, e := sqlite.Query(st.DB, `SELECT event_kind,status FROM notification_deliveries WHERE webhook_id=?`, w.ID)
	if e != nil || len(rows) != 1 || rows[0]["event_kind"].Text != "incident.open" || rows[0]["status"].Text != "pending" {
		t.Fatalf("open outbox: %v %v", rows, e)
	}
	if e = svc.Transition(siteID, "down", "healthy", store.Now()); e != nil {
		t.Fatal(e)
	}
	rows, e = sqlite.Query(st.DB, `SELECT event_kind FROM notification_deliveries WHERE webhook_id=? AND event_kind='incident.recover'`, w.ID)
	if e != nil || len(rows) != 1 {
		t.Fatalf("recover outbox: %v %v", rows, e)
	}
}

// TestIncidentTransitionOrgIsolation proves webhook enqueue is tenant-scoped:
// an incident in organization A must never queue to organization B's webhooks,
// and disabled same-org webhooks receive nothing.
func TestIncidentTransitionOrgIsolation(t *testing.T) {
	st, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	// Organization A: one enabled webhook, one disabled webhook.
	wa, _, e := New(st).Create(1, "a-enabled", "https://8.8.8.8/a")
	if e != nil {
		t.Fatal(e)
	}
	if e := sqlite.Exec(st.DB, `INSERT INTO notification_webhooks(organization_id,name,url,secret,enabled,created_at) VALUES(1,'a-disabled','https://8.8.8.8/ad','s',0,?)`, store.Now()); e != nil {
		t.Fatal(e)
	}
	// Organization B: an enabled webhook that must never see org A events.
	if e := sqlite.Exec(st.DB, `INSERT INTO organizations(name,created_at) VALUES('B',?)`, store.Now()); e != nil {
		t.Fatal(e)
	}
	var orgB int64
	if e := st.DB.QueryRow(`SELECT id FROM organizations WHERE name='B'`).Scan(&orgB); e != nil {
		t.Fatal(e)
	}
	if e := sqlite.Exec(st.DB, `INSERT INTO notification_webhooks(organization_id,name,url,secret,enabled,created_at) VALUES(?,'b-enabled','https://8.8.8.8/b','s',1,?)`, orgB, store.Now()); e != nil {
		t.Fatal(e)
	}
	siteA := insertSite(t, st)
	// Org B site too (same site id numbering cannot cross).
	r, e := sqlite.Query(st.DB, `INSERT INTO sites(organization_id,name,primary_url,created_at,updated_at) VALUES(?,'b','https://example.com',?,?) RETURNING id`, orgB, store.Now(), store.Now())
	if e != nil {
		t.Fatal(e)
	}
	siteB := r[0]["id"].Int64

	svc := incidents.New(st)
	now := store.Now()
	if e = svc.Transition(siteA, "unknown", "down", now); e != nil {
		t.Fatal(e)
	}
	rows, e := sqlite.Query(st.DB, `SELECT d.webhook_id FROM notification_deliveries d`)
	if e != nil {
		t.Fatal(e)
	}
	if len(rows) != 1 || rows[0]["webhook_id"].Int64 != wa.ID {
		t.Fatalf("org A incident queued to wrong webhooks: %v", rows)
	}
	// Recovery for org A also stays inside org A.
	if e = svc.Transition(siteA, "down", "healthy", store.Now()); e != nil {
		t.Fatal(e)
	}
	rows, _ = sqlite.Query(st.DB, `SELECT event_kind FROM notification_deliveries WHERE webhook_id=?`, wa.ID)
	if len(rows) != 2 {
		t.Fatalf("org A recover not enqueued: %v", rows)
	}
	// Org B site transitions queue only to org B webhook.
	if e = svc.Transition(siteB, "unknown", "down", store.Now()); e != nil {
		t.Fatal(e)
	}
	rows, _ = sqlite.Query(st.DB, `SELECT webhook_id FROM notification_deliveries WHERE event_kind='incident.open'`)
	ids := map[int64]bool{}
	for _, r := range rows {
		ids[r["webhook_id"].Int64] = true
	}
	if len(ids) != 2 || !ids[wa.ID] {
		t.Fatalf("expected deliveries to org A and org B webhooks only: %v", ids)
	}
	// Disabled org A webhook received nothing.
	rows, _ = sqlite.Query(st.DB, `SELECT COUNT(*) n FROM notification_deliveries d JOIN notification_webhooks w ON w.id=d.webhook_id WHERE w.enabled=0`)
	if rows[0]["n"].Int64 != 0 {
		t.Fatalf("disabled webhook received a delivery")
	}
}

func TestWorkerDeliversWithValidSignature(t *testing.T) {
	st, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	got := make(chan []byte, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		sig := r.Header.Get("X-WebFleet-Signature")
		mac := hmac.New(sha256.New, []byte("hook-secret"))
		mac.Write(body)
		want := "sha256=" + hex.EncodeToString(mac.Sum(nil))
		if sig != want {
			w.WriteHeader(400)
			return
		}
		got <- body
		w.WriteHeader(204)
	}))
	defer srv.Close()
	hostport := strings.TrimPrefix(srv.URL, "http://")
	host := strings.Split(hostport, ":")[0]
	g := testGuard(true)
	g.Resolver = mapResolver{host: {netip.MustParseAddr("127.0.0.1")}}

	if e := sqlite.Exec(st.DB, `INSERT INTO notification_webhooks(organization_id,name,url,secret,enabled,created_at) VALUES(1,'hook',?,?,1,?)`, srv.URL, "hook-secret", store.Now()); e != nil {
		t.Fatal(e)
	}
	var wid int64
	if e := st.DB.QueryRow(`SELECT id FROM notification_webhooks`).Scan(&wid); e != nil {
		t.Fatal(e)
	}
	if e := sqlite.Exec(st.DB, `INSERT INTO notification_deliveries(webhook_id,event_kind,status,payload_json,created_at) VALUES(?,'incident.open','pending','{"site_id":1,"incident_id":9}',?)`, wid, store.Now()); e != nil {
		t.Fatal(e)
	}
	w := NewWorkerWithGuard(st, slog.New(slog.NewTextHandler(io.Discard, nil)), g)
	w.runOnce(context.Background())
	select {
	case body := <-got:
		var env struct {
			Event   string          `json:"event"`
			EventID int64           `json:"event_id"`
			Data    json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(body, &env); err != nil || env.Event != "incident.open" {
			t.Fatalf("envelope %s", body)
		}
		// The stable event_id is the delivery row id, the receiver's
		// deduplication identity across reprocessing after restart.
		if env.EventID != 1 {
			t.Fatalf("event_id = %d, want 1 (delivery row id)", env.EventID)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("webhook never delivered")
	}
	rows, e := sqlite.Query(st.DB, `SELECT status,attempts FROM notification_deliveries WHERE id=?`, 1)
	if e != nil || len(rows) != 1 || rows[0]["status"].Text != "delivered" {
		t.Fatalf("delivery state: %v %v", rows, e)
	}
}

func TestWorkerRetriesThenFails(t *testing.T) {
	st, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.WriteHeader(500)
	}))
	defer srv.Close()
	hostport := strings.TrimPrefix(srv.URL, "http://")
	host := strings.Split(hostport, ":")[0]
	g := testGuard(true)
	g.Resolver = mapResolver{host: {netip.MustParseAddr("127.0.0.1")}}

	if e := sqlite.Exec(st.DB, `INSERT INTO notification_webhooks(organization_id,name,url,secret,enabled,created_at) VALUES(1,'hook',?,?,1,?)`, srv.URL, "secret", store.Now()); e != nil {
		t.Fatal(e)
	}
	var wid int64
	if e := st.DB.QueryRow(`SELECT id FROM notification_webhooks`).Scan(&wid); e != nil {
		t.Fatal(e)
	}
	if e := sqlite.Exec(st.DB, `INSERT INTO notification_deliveries(webhook_id,event_kind,status,payload_json,created_at) VALUES(?,'incident.open','pending','{}',?)`, wid, store.Now()); e != nil {
		t.Fatal(e)
	}
	w := NewWorkerWithGuard(st, slog.New(slog.NewTextHandler(io.Discard, nil)), g)
	w.runOnce(context.Background())
	if hits != deliveryRetries {
		t.Fatalf("attempts = %d, want %d", hits, deliveryRetries)
	}
	rows, e := sqlite.Query(st.DB, `SELECT status,attempts,response_code FROM notification_deliveries WHERE id=?`, 1)
	if e != nil || len(rows) != 1 || rows[0]["status"].Text != "failed" || rows[0]["attempts"].Int64 != deliveryRetries || rows[0]["response_code"].Int64 != 500 {
		t.Fatalf("failure state: %v %v", rows, e)
	}
}

func TestWorkerDoesNotFollowRedirects(t *testing.T) {
	st, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://127.0.0.1:1/", http.StatusFound)
	}))
	defer srv.Close()
	hostport := strings.TrimPrefix(srv.URL, "http://")
	host := strings.Split(hostport, ":")[0]
	g := testGuard(true)
	g.Resolver = mapResolver{host: {netip.MustParseAddr("127.0.0.1")}}
	if e := sqlite.Exec(st.DB, `INSERT INTO notification_webhooks(organization_id,name,url,secret,enabled,created_at) VALUES(1,'hook',?,?,1,?)`, srv.URL, "s", store.Now()); e != nil {
		t.Fatal(e)
	}
	var wid int64
	_ = st.DB.QueryRow(`SELECT id FROM notification_webhooks`).Scan(&wid)
	_ = sqlite.Exec(st.DB, `INSERT INTO notification_deliveries(webhook_id,event_kind,status,payload_json,created_at) VALUES(?,'incident.open','pending','{}',?)`, wid, store.Now())
	w := NewWorkerWithGuard(st, slog.New(slog.NewTextHandler(io.Discard, nil)), g)
	w.runOnce(context.Background())
	rows, _ := sqlite.Query(st.DB, `SELECT status FROM notification_deliveries WHERE id=?`, 1)
	if len(rows) != 1 || rows[0]["status"].Text != "failed" {
		t.Fatalf("redirect followed or not failed: %v", rows)
	}
}

func TestWorkerSkipsDisabledWebhook(t *testing.T) {
	st, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	if e := sqlite.Exec(st.DB, `INSERT INTO notification_webhooks(organization_id,name,url,secret,enabled,created_at) VALUES(1,'hook','https://8.8.8.8/hook','s',0,?)`, store.Now()); e != nil {
		t.Fatal(e)
	}
	var wid int64
	_ = st.DB.QueryRow(`SELECT id FROM notification_webhooks`).Scan(&wid)
	_ = sqlite.Exec(st.DB, `INSERT INTO notification_deliveries(webhook_id,event_kind,status,payload_json,created_at) VALUES(?,'incident.open','pending','{}',?)`, wid, store.Now())
	w := NewWorkerWithGuard(st, slog.New(slog.NewTextHandler(io.Discard, nil)), testGuard(false))
	w.runOnce(context.Background())
	rows, _ := sqlite.Query(st.DB, `SELECT status FROM notification_deliveries WHERE id=?`, 1)
	if len(rows) != 1 || rows[0]["status"].Text != "pending" {
		t.Fatalf("disabled webhook received an event: %v", rows)
	}
}

func TestWebhookCreateRejectsPrivateURL(t *testing.T) {
	st, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	s := New(st)
	for _, u := range []string{
		"https://127.0.0.1:1/hook",
		"https://169.254.169.254/latest/meta-data",
		"https://10.0.0.1/hook",
		"https://[::1]:1/hook",
	} {
		if _, _, e := s.Create(1, "hook", u); e == nil {
			t.Fatalf("private webhook URL accepted: %s", u)
		}
	}
}

func TestWorkerBlocksPrivateDestinationAtDial(t *testing.T) {
	st, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	// A tampered delivery row pointing at a loopback destination must fail even
	// though it bypassed create-time validation.
	if e := sqlite.Exec(st.DB, `INSERT INTO notification_webhooks(organization_id,name,url,secret,enabled,created_at) VALUES(1,'hook','https://127.0.0.1:1/hook','s',1,?)`, store.Now()); e != nil {
		t.Fatal(e)
	}
	var wid int64
	_ = st.DB.QueryRow(`SELECT id FROM notification_webhooks`).Scan(&wid)
	_ = sqlite.Exec(st.DB, `INSERT INTO notification_deliveries(webhook_id,event_kind,status,payload_json,created_at) VALUES(?,'incident.open','pending','{}',?)`, wid, store.Now())
	w := NewWorkerWithGuard(st, slog.New(slog.NewTextHandler(io.Discard, nil)), testGuard(false))
	w.runOnce(context.Background())
	rows, _ := sqlite.Query(st.DB, `SELECT status FROM notification_deliveries WHERE id=?`, 1)
	if len(rows) != 1 || rows[0]["status"].Text != "failed" {
		t.Fatalf("private webhook destination delivered: %v", rows)
	}
}

func TestIncidentTransitionIndependentOfWebhook(t *testing.T) {
	st, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	defer st.Close()
	// The webhook URL is unreachable; the incident transition must still commit.
	if e := sqlite.Exec(st.DB, `INSERT INTO notification_webhooks(organization_id,name,url,secret,enabled,created_at) VALUES(1,'hook','https://8.8.8.8/hook','s',1,?)`, store.Now()); e != nil {
		t.Fatal(e)
	}
	siteID := insertSite(t, st)
	if e := incidents.New(st).Transition(siteID, "unknown", "down", store.Now()); e != nil {
		t.Fatalf("transition must not depend on webhook delivery: %v", e)
	}
	rows, _ := sqlite.Query(st.DB, `SELECT state FROM incidents WHERE site_id=?`, siteID)
	if len(rows) != 1 || rows[0]["state"].Text != "down" {
		t.Fatalf("incident state: %v", rows)
	}
}