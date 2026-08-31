package dnsobs

import (
	"context"
	"errors"
	"github.com/web-fleet/webfleet/internal/netguard"
	"github.com/web-fleet/webfleet/internal/sqlite"
	"github.com/web-fleet/webfleet/internal/store"
	"net"
	"net/netip"
	"net/url"
	"sort"
	"strings"
)

type Resolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
	LookupCNAME(context.Context, string) (string, error)
}
type defaultResolver struct{ *net.Resolver }
type Service struct {
	store    *store.Store
	resolver Resolver
	guard    netguard.Guard
}
type Observation struct {
	ID, SiteID                      int64    `json:"id"`
	A, AAAA                         []string `json:"a"`
	CNAME, Status, Error, CheckedAt string   `json:"cname"`
	Changed                         bool     `json:"changed"`
}

func New(st *store.Store) *Service {
	r := defaultResolver{net.DefaultResolver}
	return &Service{store: st, resolver: r, guard: netguard.Guard{Resolver: r}}
}
func NewForTests(st *store.Store, r Resolver, allowPrivate bool) *Service {
	return &Service{store: st, resolver: r, guard: netguard.Guard{Resolver: r, AllowPrivate: allowPrivate}}
}
func (s *Service) ObserveSite(ctx context.Context, siteID int64) (Observation, error) {
	r, e := s.store.DB.Query(`SELECT primary_url FROM sites WHERE id=?`, siteID)
	if e != nil || len(r) == 0 {
		return Observation{}, errors.New("site not found")
	}
	u, e := url.Parse(r[0]["primary_url"].Text)
	if e != nil {
		return Observation{}, e
	}
	o := Observation{SiteID: siteID, Status: "ok", CheckedAt: store.Now()}
	ips, e := s.resolver.LookupNetIP(ctx, "ip", u.Hostname())
	if e != nil {
		o.Status = "error"
		o.Error = e.Error()
		return s.persist(o)
	}
	for _, ip := range ips {
		if !s.guard.AllowPrivate && netguard.Blocked(ip) {
			o.Status = "blocked"
			o.Error = "hostname resolved to a private/reserved address"
			return s.persist(o)
		}
		ip = ip.Unmap()
		if ip.Is4() {
			o.A = append(o.A, ip.String())
		} else {
			o.AAAA = append(o.AAAA, ip.String())
		}
	}
	sort.Strings(o.A)
	sort.Strings(o.AAAA)
	if c, e := s.resolver.LookupCNAME(ctx, u.Hostname()); e == nil && strings.TrimSuffix(strings.ToLower(c), ".") != strings.TrimSuffix(strings.ToLower(u.Hostname()), ".") {
		o.CNAME = strings.TrimSuffix(c, ".")
	}
	prev, _ := s.LatestSuccessful(siteID)
	if prev.ID > 0 {
		o.Changed = strings.Join(prev.A, ",") != strings.Join(o.A, ",") || strings.Join(prev.AAAA, ",") != strings.Join(o.AAAA, ",") || prev.CNAME != o.CNAME
	}
	return s.persist(o)
}
func (s *Service) persist(o Observation) (Observation, error) {
	r, e := s.store.DB.Query(`INSERT INTO dns_observations(site_id,a_records,aaaa_records,cname,status,changed,error,checked_at) VALUES(?,?,?,?,?,?,?,?) RETURNING id`, o.SiteID, strings.Join(o.A, ","), strings.Join(o.AAAA, ","), o.CNAME, o.Status, o.Changed, o.Error, o.CheckedAt)
	if e != nil {
		return Observation{}, e
	}
	o.ID = r[0]["id"].Int64
	return o, nil
}
func (s *Service) Latest(siteID int64) (Observation, error) {
	r, e := s.store.DB.Query(`SELECT id,site_id,a_records,aaaa_records,cname,status,changed,error,checked_at FROM dns_observations WHERE site_id=? ORDER BY id DESC LIMIT 1`, siteID)
	if e != nil || len(r) == 0 {
		return Observation{}, errors.New("no DNS observation")
	}
	return row(r[0]), nil
}
func (s *Service) LatestSuccessful(siteID int64) (Observation, error) {
	r, e := s.store.DB.Query(`SELECT id,site_id,a_records,aaaa_records,cname,status,changed,error,checked_at FROM dns_observations WHERE site_id=? AND status='ok' ORDER BY id DESC LIMIT 1`, siteID)
	if e != nil || len(r) == 0 {
		return Observation{}, errors.New("no successful DNS observation")
	}
	return row(r[0]), nil
}
func (s *Service) History(siteID int64) ([]Observation, error) {
	r, e := s.store.DB.Query(`SELECT id,site_id,a_records,aaaa_records,cname,status,changed,error,checked_at FROM dns_observations WHERE site_id=? ORDER BY id DESC LIMIT 50`, siteID)
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
	o := Observation{ID: r["id"].Int64, SiteID: r["site_id"].Int64, CNAME: r["cname"].Text, Status: r["status"].Text, Changed: r["changed"].Int64 != 0, Error: r["error"].Text, CheckedAt: r["checked_at"].Text}
	if r["a_records"].Text != "" {
		o.A = strings.Split(r["a_records"].Text, ",")
	}
	if r["aaaa_records"].Text != "" {
		o.AAAA = strings.Split(r["aaaa_records"].Text, ",")
	}
	return o
}
