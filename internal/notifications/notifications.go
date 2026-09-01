package notifications

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"github.com/web-fleet/webfleet/internal/netguard"
	"github.com/web-fleet/webfleet/internal/sqlite"
	"github.com/web-fleet/webfleet/internal/store"
	"net/url"
	"strings"
	"time"
)

type Webhook struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	Enabled   bool   `json:"enabled"`
	CreatedAt string `json:"created_at"`
}
type Delivery struct {
	ID           int64  `json:"id"`
	WebhookID    int64  `json:"webhook_id"`
	EventKind    string `json:"event_kind"`
	Status       string `json:"status"`
	Attempts     int64  `json:"attempts"`
	ResponseCode int64  `json:"response_code"`
	Error        string `json:"error,omitempty"`
	CreatedAt    string `json:"created_at"`
	DeliveredAt  string `json:"delivered_at,omitempty"`
}
type Service struct {
	st *store.Store
}

func New(st *store.Store) *Service {
	return &Service{st: st}
}
func (s *Service) Create(org int64, name, rawURL string) (Webhook, string, error) {
	u, e := url.Parse(rawURL)
	if e != nil || u.Scheme != "https" || u.Host == "" {
		return Webhook{}, "", errors.New("webhook URL must use HTTPS")
	}
	// Webhook destinations are SSRF-sensitive: HTTPS alone is not enough. The
	// host must resolve to a public address under the same public-network guard
	// used by monitoring, crawling and Audit.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if e := netguard.New().ValidateHost(ctx, u.Hostname()); e != nil {
		return Webhook{}, "", errors.New("webhook URL must resolve to a public address")
	}
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	secret := hex.EncodeToString(b)
	r, e := sqlite.Query(s.st.DB, `INSERT INTO notification_webhooks(organization_id,name,url,secret,created_at) VALUES(?,?,?,?,?) RETURNING id,created_at`, org, strings.TrimSpace(name), rawURL, secret, store.Now())
	if e != nil {
		return Webhook{}, "", e
	}
	return Webhook{r[0]["id"].Int64, name, rawURL, true, r[0]["created_at"].Text}, secret, nil
}
func (s *Service) List(org int64) ([]Webhook, error) {
	r, e := sqlite.Query(s.st.DB, `SELECT id,name,url,enabled,created_at FROM notification_webhooks WHERE organization_id=? ORDER BY id`, org)
	if e != nil {
		return nil, e
	}
	out := []Webhook{}
	for _, x := range r {
		out = append(out, Webhook{x["id"].Int64, x["name"].Text, x["url"].Text, x["enabled"].Int64 != 0, x["created_at"].Text})
	}
	return out, nil
}
func redact(v string) string {
	if len(v) > 500 {
		v = v[:500]
	}
	if strings.Contains(strings.ToLower(v), "secret") {
		return "delivery failed (redacted)"
	}
	return v
}
func (s *Service) History(org int64) ([]Delivery, error) {
	r, e := sqlite.Query(s.st.DB, `SELECT d.* FROM notification_deliveries d JOIN notification_webhooks w ON w.id=d.webhook_id WHERE w.organization_id=? ORDER BY d.id DESC LIMIT 100`, org)
	if e != nil {
		return nil, e
	}
	out := []Delivery{}
	for _, x := range r {
		out = append(out, Delivery{x["id"].Int64, x["webhook_id"].Int64, x["event_kind"].Text, x["status"].Text, x["attempts"].Int64, x["response_code"].Int64, x["error"].Text, x["created_at"].Text, x["delivered_at"].Text})
	}
	return out, nil
}
