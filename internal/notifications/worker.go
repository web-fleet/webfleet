package notifications

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/webfleet-cv/webfleet/internal/netguard"
	"github.com/webfleet-cv/webfleet/internal/sqlite"
	"github.com/webfleet-cv/webfleet/internal/store"
)

const (
	deliveryTimeout          = 8 * time.Second
	deliveryRetries          = 3
	deliveryBackoffBase      = 200 * time.Millisecond
	deliveryBatch            = 20
	deliveryWorkerInterval   = 5 * time.Second
	deliveryWorkerConcurrent = 2
	maxResponseBody          = 64 << 10
)

// Worker delivers webhook outbox rows created by product events (currently
// incident open/recover). Monitoring and incident state commit independently in
// the transaction that enqueues the row; the worker performs the external HTTP
// afterwards, so a slow or malicious webhook can never block an incident
// transition. Delivery uses the same public-network guard as monitoring,
// crawling and Audit: destinations are validated at creation and re-resolved
// at dial time (DNS rebinding and mixed public/private answers fail closed),
// and redirects are never followed.
type Worker struct {
	st       *store.Store
	guard    netguard.Guard
	client   *http.Client
	log      *slog.Logger
	interval time.Duration
	sem      chan struct{}
	cancel   context.CancelFunc
	wg       sync.WaitGroup
}

func NewWorker(st *store.Store, log *slog.Logger) *Worker {
	return NewWorkerWithGuard(st, log, netguard.New())
}

// NewWorkerWithGuard is a test seam for injecting a resolver/allow-private
// guard; production uses netguard.New().
func NewWorkerWithGuard(st *store.Store, log *slog.Logger, guard netguard.Guard) *Worker {
	tr := &http.Transport{
		Proxy:             nil,
		DialContext:       guard.DialContext,
		DisableKeepAlives: true,
		TLSClientConfig:   &tls.Config{MinVersion: tls.VersionTLS12},
	}
	client := &http.Client{
		Transport: tr,
		Timeout:   deliveryTimeout,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	return &Worker{st: st, guard: guard, client: client, log: log, interval: deliveryWorkerInterval, sem: make(chan struct{}, deliveryWorkerConcurrent)}
}

func (w *Worker) Start(ctx context.Context) {
	ctx, w.cancel = context.WithCancel(ctx)
	w.wg.Add(1)
	go w.loop(ctx)
}

func (w *Worker) Stop() {
	if w.cancel != nil {
		w.cancel()
	}
	w.wg.Wait()
}

func (w *Worker) loop(ctx context.Context) {
	defer w.wg.Done()
	t := time.NewTicker(w.interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			w.runOnce(ctx)
		}
	}
}

func (w *Worker) runOnce(ctx context.Context) {
	rows, e := sqlite.Query(w.st.DB, `SELECT d.id,d.webhook_id,d.event_kind,d.payload_json,w.url,w.secret FROM notification_deliveries d JOIN notification_webhooks w ON w.id=d.webhook_id WHERE w.enabled=1 AND d.status='pending' ORDER BY d.id LIMIT ?`, deliveryBatch)
	if e != nil {
		w.log.Error("webhook outbox read failed", "error", e)
		return
	}
	var wg sync.WaitGroup
	for _, r := range rows {
		wg.Add(1)
		go func(r sqlite.Row) {
			defer wg.Done()
			select {
			case w.sem <- struct{}{}:
				defer func() { <-w.sem }()
			case <-ctx.Done():
				return
			}
			w.deliver(ctx, r["id"].Int64, r["event_kind"].Text, r["payload_json"].Text, r["url"].Text, r["secret"].Text)
		}(r)
	}
	wg.Wait()
}

// deliver sends one outbox row with bounded retries. Each attempt signs the
// exact body being delivered. 3xx responses are treated as failures because
// redirects are never followed (a webhook could otherwise redirect to a
// private destination). The envelope carries the stable delivery row id as
// event_id, which is the deduplication identity a receiver can rely on even if
// the worker re-processes the row after a restart (sent_at then differs).
func (w *Worker) deliver(ctx context.Context, id int64, kind, payload, url, secret string) {
	body := buildEnvelope(kind, payload, id)
	var lastErr error
	code := 0
	for attempt := 1; attempt <= deliveryRetries; attempt++ {
		req, e := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(body))
		if e != nil {
			lastErr = e
			break
		}
		req.Header.Set("Content-Type", "application/json")
		mac := hmac.New(sha256.New, []byte(secret))
		mac.Write(body)
		req.Header.Set("X-WebFleet-Signature", "sha256="+hex.EncodeToString(mac.Sum(nil)))
		resp, e := w.client.Do(req)
		if e == nil {
			code = resp.StatusCode
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBody))
			_ = resp.Body.Close()
			if code >= 200 && code < 300 {
				_ = sqlite.Exec(w.st.DB, `UPDATE notification_deliveries SET status='delivered',attempts=?,response_code=?,delivered_at=? WHERE id=?`, attempt, code, store.Now(), id)
				return
			}
			lastErr = fmt.Errorf("webhook returned HTTP %d", code)
		} else {
			lastErr = e
		}
		_ = sqlite.Exec(w.st.DB, `UPDATE notification_deliveries SET attempts=?,response_code=?,error=? WHERE id=?`, attempt, code, redact(lastErr.Error()), id)
		if attempt < deliveryRetries {
			select {
			case <-ctx.Done():
				return
			case <-time.After(deliveryBackoffBase * time.Duration(attempt)):
			}
		}
	}
	_ = sqlite.Exec(w.st.DB, `UPDATE notification_deliveries SET status='failed' WHERE id=?`, id)
}

// buildEnvelope constructs the delivered JSON and is the input to the HMAC
// signature, so the signature always covers exactly the delivered bytes. The
// event_id is the stable delivery row id, the receiver's deduplication
// identity.
func buildEnvelope(kind, payload string, eventID int64) []byte {
	var data any
	_ = json.Unmarshal([]byte(payload), &data)
	env := map[string]any{"event": kind, "event_id": eventID, "data": data, "sent_at": store.Now()}
	b, _ := json.Marshal(env)
	return b
}
