package tlshealth

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"github.com/web-fleet/webfleet/internal/netguard"
	"github.com/web-fleet/webfleet/internal/sqlite"
	"github.com/web-fleet/webfleet/internal/store"
	"net"
	"net/url"
	"time"
)

type Service struct {
	store *store.Store
	guard netguard.Guard
	roots *x509.CertPool
}
type Observation struct {
	ID            int64  `json:"id"`
	SiteID        int64  `json:"site_id"`
	Valid         bool   `json:"valid"`
	HostnameValid bool   `json:"hostname_valid"`
	Issuer        string `json:"issuer"`
	Subject       string `json:"subject"`
	Serial        string `json:"serial"`
	NotBefore     string `json:"not_before"`
	NotAfter      string `json:"not_after"`
	DaysRemaining int    `json:"days_remaining"`
	ErrorClass    string `json:"error_class"`
	Error         string `json:"error"`
	CheckedAt     string `json:"checked_at"`
}

func New(st *store.Store) *Service { return &Service{store: st, guard: netguard.New()} }
func NewForTests(st *store.Store, g netguard.Guard, roots *x509.CertPool) *Service {
	return &Service{store: st, guard: g, roots: roots}
}
func (s *Service) InspectSite(ctx context.Context, siteID int64) (Observation, error) {
	r, e := s.store.DB.Query(`SELECT primary_url FROM sites WHERE id=?`, siteID)
	if e != nil || len(r) == 0 {
		return Observation{}, errors.New("site not found")
	}
	u, e := url.Parse(r[0]["primary_url"].Text)
	if e != nil {
		return Observation{}, e
	}
	obs := Observation{SiteID: siteID, CheckedAt: store.Now()}
	if u.Scheme != "https" {
		obs.ErrorClass = "not_https"
		obs.Error = "site does not use HTTPS"
		return s.persist(obs)
	}
	if e = s.guard.ValidateHost(ctx, u.Hostname()); e != nil {
		obs.ErrorClass = "blocked"
		obs.Error = e.Error()
		return s.persist(obs)
	}
	port := u.Port()
	if port == "" {
		port = "443"
	}
	raw, e := s.guard.DialContext(ctx, "tcp", net.JoinHostPort(u.Hostname(), port))
	if e != nil {
		obs.ErrorClass = "connection"
		obs.Error = e.Error()
		return s.persist(obs)
	}
	defer raw.Close()
	cfg := &tls.Config{ServerName: u.Hostname(), MinVersion: tls.VersionTLS12, RootCAs: s.roots}
	conn := tls.Client(raw, cfg)
	if e = conn.HandshakeContext(ctx); e != nil {
		obs.ErrorClass = "tls"
		obs.Error = e.Error()
		return s.persist(obs)
	}
	defer conn.Close()
	state := conn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		obs.ErrorClass = "tls"
		obs.Error = "server returned no certificate"
		return s.persist(obs)
	}
	cert := state.PeerCertificates[0]
	obs.Valid = true
	obs.HostnameValid = cert.VerifyHostname(u.Hostname()) == nil
	obs.Issuer = cert.Issuer.String()
	obs.Subject = cert.Subject.String()
	obs.Serial = cert.SerialNumber.Text(16)
	obs.NotBefore = cert.NotBefore.UTC().Format(time.RFC3339)
	obs.NotAfter = cert.NotAfter.UTC().Format(time.RFC3339)
	obs.DaysRemaining = int(time.Until(cert.NotAfter).Hours() / 24)
	if !obs.HostnameValid {
		obs.Valid = false
		obs.ErrorClass = "hostname"
		obs.Error = "certificate hostname mismatch"
	}
	return s.persist(obs)
}
func (s *Service) persist(o Observation) (Observation, error) {
	r, e := s.store.DB.Query(`INSERT INTO tls_observations(site_id,valid,hostname_valid,issuer,subject,serial,not_before,not_after,days_remaining,error_class,error,checked_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?) RETURNING id`, o.SiteID, o.Valid, o.HostnameValid, o.Issuer, o.Subject, o.Serial, o.NotBefore, o.NotAfter, o.DaysRemaining, o.ErrorClass, o.Error, o.CheckedAt)
	if e != nil {
		return Observation{}, e
	}
	o.ID = r[0]["id"].Int64
	return o, nil
}
func (s *Service) Latest(siteID int64) (Observation, error) {
	r, e := s.store.DB.Query(`SELECT id,site_id,valid,hostname_valid,issuer,subject,serial,not_before,not_after,days_remaining,error_class,error,checked_at FROM tls_observations WHERE site_id=? ORDER BY id DESC LIMIT 1`, siteID)
	if e != nil || len(r) == 0 {
		return Observation{}, errors.New("no TLS observation")
	}
	return row(r[0]), nil
}
func (s *Service) FleetWarnings(days int) ([]Observation, error) {
	if days < 1 {
		days = 30
	}
	r, e := s.store.DB.Query(`SELECT t.id,t.site_id,t.valid,t.hostname_valid,t.issuer,t.subject,t.serial,t.not_before,t.not_after,t.days_remaining,t.error_class,t.error,t.checked_at FROM tls_observations t JOIN (SELECT site_id,MAX(id) id FROM tls_observations GROUP BY site_id) x ON x.id=t.id WHERE t.valid=0 OR t.days_remaining<=? ORDER BY t.days_remaining`, days)
	if e != nil {
		return nil, e
	}
	out := make([]Observation, 0, len(r))
	for _, x := range r {
		out = append(out, row(x))
	}
	return out, nil
}
func row(r sqlite.Row) Observation {
	return Observation{ID: r["id"].Int64, SiteID: r["site_id"].Int64, Valid: r["valid"].Int64 != 0, HostnameValid: r["hostname_valid"].Int64 != 0, Issuer: r["issuer"].Text, Subject: r["subject"].Text, Serial: r["serial"].Text, NotBefore: r["not_before"].Text, NotAfter: r["not_after"].Text, DaysRemaining: int(r["days_remaining"].Int64), ErrorClass: r["error_class"].Text, Error: r["error"].Text, CheckedAt: r["checked_at"].Text}
}
