package netguard

import (
	"net/netip"
	"testing"
)

func FuzzBlockedNeverPanics(f *testing.F) {
	for _, s := range []string{"127.0.0.1", "10.0.0.1", "8.8.8.8", "::1", "2001:4860:4860::8888"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		if ip, e := netip.ParseAddr(s); e == nil {
			_ = Blocked(ip)
		}
	})
}
