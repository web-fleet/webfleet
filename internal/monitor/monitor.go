package monitor

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/web-fleet/webfleet/internal/incidents"
	"github.com/web-fleet/webfleet/internal/sqlite"
	"github.com/web-fleet/webfleet/internal/store"
)

type Resolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}
type netResolver struct{ r *net.Resolver }

func (n netResolver) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	return n.r.LookupNetIP(ctx, network, host)
}

type Service struct {
	store        *store.Store
	resolver     Resolver
	allowPrivate bool
}
type Result struct {
	ID, SiteID, MonitorID int64  `json:"id"`
	OK                    bool   `json:"ok"`
	StatusCode            int    `json:"status_code"`
	LatencyMS             int64  `json:"latency_ms"`
	FinalURL              string `json:"final_url"`
	ErrorClass            string `json:"error_class"`
	Error                 string `json:"error"`
	CheckedAt             string `json:"checked_at"`
}

func New(st *store.Store) *Service {
	return &Service{store: st, resolver: netResolver{net.DefaultResolver}}
}
func NewForTests(st *store.Store, r Resolver, allowPrivate bool) *Service {
	return &Service{store: st, resolver: r, allowPrivate: allowPrivate}
}

func (s *Service) CheckSite(ctx context.Context, siteID int64) (Result, error) {
	rows, e := s.store.DB.Query(`SELECT m.id monitor_id,s.primary_url,m.timeout_ms,m.expected_min,m.expected_max FROM monitors m JOIN sites s ON s.id=m.site_id WHERE s.id=? AND s.enabled=1 AND s.archived_at IS NULL LIMIT 1`, siteID)
	if e != nil {
		return Result{}, e
	}
	if len(rows) == 0 {
		return Result{}, errors.New("enabled site monitor not found")
	}
	r := rows[0]
	return s.check(ctx, siteID, r["monitor_id"].Int64, r["primary_url"].Text, time.Duration(r["timeout_ms"].Int64)*time.Millisecond, int(r["expected_min"].Int64), int(r["expected_max"].Int64))
}
func (s *Service) check(ctx context.Context, siteID, monitorID int64, raw string, timeout time.Duration, minStatus, maxStatus int) (Result, error) {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, DialContext: s.safeDialer()}
	client := &http.Client{Transport: transport, Timeout: timeout, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 10 {
			return errors.New("too many redirects")
		}
		return s.validateURL(req.Context(), req.URL)
	}}
	u, e := url.Parse(raw)
	if e != nil {
		return Result{}, e
	}
	if e = s.validateURL(ctx, u); e != nil {
		return s.persist(siteID, monitorID, Result{OK: false, FinalURL: raw, ErrorClass: "blocked", Error: e.Error(), CheckedAt: store.Now()})
	}
	start := time.Now()
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if e != nil {
		return Result{}, e
	}
	req.Header.Set("User-Agent", "WebFleet/0.1 monitor")
	resp, e := client.Do(req)
	lat := time.Since(start).Milliseconds()
	res := Result{SiteID: siteID, MonitorID: monitorID, LatencyMS: lat, FinalURL: raw, CheckedAt: store.Now()}
	if e != nil {
		res.ErrorClass = classifyError(e)
		res.Error = e.Error()
		return s.persist(siteID, monitorID, res)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 64<<10))
	res.StatusCode = resp.StatusCode
	res.FinalURL = resp.Request.URL.String()
	res.OK = resp.StatusCode >= minStatus && resp.StatusCode <= maxStatus
	if !res.OK {
		res.ErrorClass = "http_status"
		res.Error = fmt.Sprintf("unexpected HTTP status %d", resp.StatusCode)
	}
	return s.persist(siteID, monitorID, res)
}
func (s *Service) persist(siteID, monitorID int64, res Result) (Result, error) {
	rows, e := s.store.DB.Query(`INSERT INTO check_results(site_id,monitor_id,ok,status_code,latency_ms,final_url,error_class,error,checked_at) VALUES(?,?,?,?,?,?,?,?,?) RETURNING id`, siteID, monitorID, res.OK, res.StatusCode, res.LatencyMS, res.FinalURL, res.ErrorClass, res.Error, res.CheckedAt)
	if e != nil {
		return Result{}, e
	}
	res.ID = rows[0]["id"].Int64
	if err := s.updateHealth(res); err != nil {
		return Result{}, err
	}
	return res, nil
}
func (s *Service) updateHealth(res Result) error {
	rows, err := s.store.DB.Query(`SELECT state,consecutive_failures FROM site_health WHERE site_id=?`, res.SiteID)
	if err != nil {
		return err
	}
	state := "unknown"
	fails := int64(0)
	if len(rows) > 0 {
		state = rows[0]["state"].Text
		fails = rows[0]["consecutive_failures"].Int64
	}
	next := state
	now := res.CheckedAt
	var success any = nil
	var failure any = nil
	if res.OK {
		fails = 0
		next = "healthy"
		success = now
	} else {
		fails++
		failure = now
		if fails == 1 {
			next = "warning"
		} else {
			next = "down"
		}
		if res.ErrorClass == "http_status" && fails == 1 {
			next = "degraded"
		}
	}
	changed := next != state
	if len(rows) == 0 {
		err = s.store.DB.Exec(`INSERT INTO site_health(site_id,state,consecutive_failures,last_check_id,last_change_at,last_success_at,last_failure_at) VALUES(?,?,?,?,?,?,?)`, res.SiteID, next, fails, res.ID, now, success, failure)
	} else if changed {
		err = s.store.DB.Exec(`UPDATE site_health SET state=?,consecutive_failures=?,last_check_id=?,last_change_at=?,last_success_at=COALESCE(?,last_success_at),last_failure_at=COALESCE(?,last_failure_at) WHERE site_id=?`, next, fails, res.ID, now, success, failure, res.SiteID)
	} else {
		err = s.store.DB.Exec(`UPDATE site_health SET consecutive_failures=?,last_check_id=?,last_success_at=COALESCE(?,last_success_at),last_failure_at=COALESCE(?,last_failure_at) WHERE site_id=?`, fails, res.ID, success, failure, res.SiteID)
	}
	if err != nil {
		return err
	}
	if changed {
		return incidents.New(s.store).Transition(res.SiteID, state, next, now)
	}
	return nil
}

func (s *Service) Recent(siteID int64, limit int) ([]Result, error) {
	if limit < 1 {
		limit = 20
	}
	if limit > 200 {
		limit = 200
	}
	rows, e := s.store.DB.Query(`SELECT id,site_id,monitor_id,ok,status_code,latency_ms,final_url,error_class,error,checked_at FROM check_results WHERE site_id=? ORDER BY id DESC LIMIT ?`, siteID, limit)
	if e != nil {
		return nil, e
	}
	out := make([]Result, 0, len(rows))
	for _, r := range rows {
		out = append(out, resultRow(r))
	}
	return out, nil
}
func resultRow(r sqlite.Row) Result {
	return Result{ID: r["id"].Int64, SiteID: r["site_id"].Int64, MonitorID: r["monitor_id"].Int64, OK: r["ok"].Int64 != 0, StatusCode: int(r["status_code"].Int64), LatencyMS: r["latency_ms"].Int64, FinalURL: r["final_url"].Text, ErrorClass: r["error_class"].Text, Error: r["error"].Text, CheckedAt: r["checked_at"].Text}
}
func (s *Service) validateURL(ctx context.Context, u *url.URL) error {
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("only http/https targets are allowed")
	}
	host := u.Hostname()
	if host == "" {
		return errors.New("target has no hostname")
	}
	ips, e := s.resolver.LookupNetIP(ctx, "ip", host)
	if e != nil {
		return fmt.Errorf("resolve target: %w", e)
	}
	if len(ips) == 0 {
		return errors.New("target resolved to no addresses")
	}
	for _, ip := range ips {
		if !s.allowPrivate && blockedIP(ip) {
			return fmt.Errorf("target resolved to blocked address %s", ip)
		}
	}
	return nil
}
func (s *Service) safeDialer() func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, e := net.SplitHostPort(address)
		if e != nil {
			return nil, e
		}
		ips, e := s.resolver.LookupNetIP(ctx, "ip", host)
		if e != nil {
			return nil, &net.DNSError{Err: e.Error(), Name: host}
		}
		d := net.Dialer{Timeout: 5 * time.Second}
		var last error
		for _, ip := range ips {
			if !s.allowPrivate && blockedIP(ip) {
				return nil, fmt.Errorf("blocked target address %s", ip)
			}
			c, e := d.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if e == nil {
				return c, nil
			}
			last = e
		}
		if last == nil {
			last = errors.New("no usable target addresses")
		}
		return nil, last
	}
}
func blockedIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	blocks := []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32"}
	for _, raw := range blocks {
		p := netip.MustParsePrefix(raw)
		if p.Contains(ip) {
			return true
		}
	}
	return false
}
func classifyError(err error) string {
	msg := strings.ToLower(err.Error())
	switch {
	case errors.Is(err, context.DeadlineExceeded) || strings.Contains(msg, "timeout") || strings.Contains(msg, "deadline exceeded"):
		return "timeout"
	case strings.Contains(msg, "blocked target"):
		return "blocked"
	case strings.Contains(msg, "no such host") || strings.Contains(msg, "lookup") || strings.Contains(msg, "resolve"):
		return "dns"
	case strings.Contains(msg, "tls") || strings.Contains(msg, "certificate") || strings.Contains(msg, "x509"):
		return "tls"
	case strings.Contains(msg, "redirect"):
		return "redirect"
	default:
		return "connection"
	}
}
