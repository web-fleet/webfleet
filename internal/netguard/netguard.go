package netguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"time"
)

type Resolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}
type DefaultResolver struct{ R *net.Resolver }

func (d DefaultResolver) LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error) {
	r := d.R
	if r == nil {
		r = net.DefaultResolver
	}
	return r.LookupNetIP(ctx, network, host)
}

type Guard struct {
	Resolver     Resolver
	AllowPrivate bool
}

func New() Guard { return Guard{Resolver: DefaultResolver{}} }
func (g Guard) ValidateURL(ctx context.Context, u *url.URL) error {
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("only http/https targets are allowed")
	}
	return g.ValidateHost(ctx, u.Hostname())
}
func (g Guard) ValidateHost(ctx context.Context, host string) error {
	if host == "" {
		return errors.New("target has no hostname")
	}
	ips, e := g.Resolver.LookupNetIP(ctx, "ip", host)
	if e != nil {
		return fmt.Errorf("resolve target: %w", e)
	}
	if len(ips) == 0 {
		return errors.New("target resolved to no addresses")
	}
	for _, ip := range ips {
		if !g.AllowPrivate && Blocked(ip) {
			return fmt.Errorf("target resolved to blocked address %s", ip)
		}
	}
	return nil
}
func (g Guard) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, e := net.SplitHostPort(address)
	if e != nil {
		return nil, e
	}
	ips, e := g.Resolver.LookupNetIP(ctx, "ip", host)
	if e != nil {
		return nil, &net.DNSError{Err: e.Error(), Name: host}
	}
	d := net.Dialer{Timeout: 5 * time.Second}
	var last error
	for _, ip := range ips {
		if !g.AllowPrivate && Blocked(ip) {
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
func Blocked(ip netip.Addr) bool {
	ip = ip.Unmap()
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() {
		return true
	}
	for _, raw := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32"} {
		if netip.MustParsePrefix(raw).Contains(ip) {
			return true
		}
	}
	return false
}
