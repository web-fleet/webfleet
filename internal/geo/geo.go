// Package geo resolves the coarse country of a visitor source IP at analytics
// ingestion time. It is deliberately offline/local: visitor IPs are never sent
// to a third-party geolocation service. The raw IP is discarded immediately
// after the country code is derived; only the coarse country code is persisted.
//
// LookupCountry currently returns "" (unknown) for non-public addresses and
// for public addresses when no local GeoIP dataset is configured. The
// analytics pipeline stores the returned code, so a country dataset (e.g. a
// local GeoIP/MaxMind-style database exposed through an adapter here) can be
// added without changing ingestion or aggregation.
package geo

import "net/netip"

// LookupCountry returns the two-letter ISO country code (e.g. "AU") for a
// visitor source IP, or "" when it cannot be determined. Private/loopback
// addresses always resolve to "".
func LookupCountry(ip string) string {
	a, err := netip.ParseAddr(ip)
	if err != nil {
		return ""
	}
	if !a.IsGlobalUnicast() {
		return ""
	}
	// Without a bundled local GeoIP dataset, public addresses are reported as
	// unknown rather than guessed. A country lookup adapter can be wired here.
	return ""
}
