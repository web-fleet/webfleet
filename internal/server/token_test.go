package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/web-fleet/webfleet/internal/sqlite"
	"github.com/web-fleet/webfleet/internal/store"
)

func createToken(t *testing.T, s *Server, c *client, name, scopesJSON string) string {
	t.Helper()
	rr := doReq(t, s, c, "POST", "/api/tokens", fmt.Sprintf(`{"name":%q,"scopes":%s}`, name, scopesJSON))
	if rr.Code != 201 {
		t.Fatalf("create token %d %s", rr.Code, rr.Body.String())
	}
	var out struct{ Token string `json:"token"` }
	if err := json.Unmarshal(rr.Body.Bytes(), &out); err != nil || out.Token == "" {
		t.Fatalf("token response: %v %s", err, rr.Body.String())
	}
	return out.Token
}

func doReqWithBearer(t *testing.T, s *Server, token, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, rd)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, req)
	return rr
}

func TestTokenAuthenticationHappyPath(t *testing.T) {
	s, _ := newRBACServer(t)
	admin := setupAdmin(t, s)
	tok := createToken(t, s, admin, "ci", `["sites:read"]`)
	rr := doReqWithBearer(t, s, tok, "GET", "/api/sites", "")
	if rr.Code != 200 {
		t.Fatalf("token GET /api/sites = %d %s", rr.Code, rr.Body.String())
	}
	// Token-authenticated mutations do not require a CSRF header.
	tok2 := createToken(t, s, admin, "ciw", `["sites:write"]`)
	rr = doReqWithBearer(t, s, tok2, "POST", "/api/sites", `{"name":"x","primary_url":"https://127.0.0.1:1/"}`)
	if rr.Code != 201 {
		t.Fatalf("token POST /api/sites without CSRF = %d %s", rr.Code, rr.Body.String())
	}
}

func TestTokenRejectsMalformedUnknownRevoked(t *testing.T) {
	s, _ := newRBACServer(t)
	admin := setupAdmin(t, s)
	tok := createToken(t, s, admin, "ci", `["sites:read"]`)
	// Malformed / missing bearer.
	if rr := doReqWithBearer(t, s, "", "GET", "/api/sites", ""); rr.Code != 401 {
		t.Fatalf("no bearer = %d", rr.Code)
	}
	if rr := doReqWithBearer(t, s, "wf_unknown", "GET", "/api/sites", ""); rr.Code != 401 {
		t.Fatalf("unknown token = %d", rr.Code)
	}
	// Missing scope: a sites:read token cannot reach fleet.
	if rr := doReqWithBearer(t, s, tok, "GET", "/api/fleet", ""); rr.Code != 403 {
		t.Fatalf("scope denied = %d", rr.Code)
	}
	// Revoked token.
	var id struct{ ID int64 `json:"id"` }
	rr := doReq(t, s, admin, "POST", "/api/tokens", `{"name":"rev","scopes":["sites:read"]}`)
	_ = json.Unmarshal(rr.Body.Bytes(), &id)
	if rr := doReq(t, s, admin, "DELETE", fmt.Sprintf("/api/tokens/%d", id.ID), ""); rr.Code != 200 {
		t.Fatalf("revoke %d", rr.Code)
	}
	if rr := doReq(t, s, admin, "POST", "/api/tokens", `{"name":"rev","scopes":["sites:read"]}`); rr.Code == 201 {
		// ignore; we need the raw token before revoke — recreate flow below.
		_ = rr
	}
	// Revoke the first token and confirm it no longer authenticates.
	rt := doReq(t, s, admin, "POST", "/api/tokens", `{"name":"rev2","scopes":["sites:read"]}`)
	var rtOut struct {
		ID    int64  `json:"id"`
		Token string `json:"token"`
	}
	_ = json.Unmarshal(rt.Body.Bytes(), &rtOut)
	if rr := doReq(t, s, admin, "DELETE", fmt.Sprintf("/api/tokens/%d", rtOut.ID), ""); rr.Code != 200 {
		t.Fatalf("revoke %d", rr.Code)
	}
	if rr := doReqWithBearer(t, s, rtOut.Token, "GET", "/api/sites", ""); rr.Code != 401 {
		t.Fatalf("revoked token = %d, want 401", rr.Code)
	}
}

func TestTokenScopeMatrix(t *testing.T) {
	s, st := newRBACServer(t)
	admin := setupAdmin(t, s)
	siteID := createSiteViaAPI(t, s, admin, "Example", "https://127.0.0.1:1/")
	cases := []struct {
		scopes   string
		path     string
		method   string
		body     string
		want     int
	}{
		{`["sites:read"]`, "/api/sites", "GET", "", 200},
		{`["sites:read"]`, "/api/sites", "POST", `{"name":"x","primary_url":"https://example.com"}`, 403},
		{`["sites:read"]`, "/api/fleet", "GET", "", 403},
		{`["sites:write"]`, "/api/sites", "POST", `{"name":"x","primary_url":"https://127.0.0.1:1/"}`, 201},
		{`["sites:write"]`, "/api/sites", "GET", "", 403},
		{`["fleet:read"]`, "/api/fleet", "GET", "", 200},
		{`["fleet:read"]`, "/api/sites", "GET", "", 403},
		{`["analytics:read"]`, fmt.Sprintf("/api/sites/%d/analytics/summary", siteID), "GET", "", 200},
		{`["analytics:read"]`, "/api/fleet", "GET", "", 403},
		{`["audit:run"]`, "/api/audits/resolve", "POST", `{"search":""}`, 200},
		{`["audit:run"]`, "/api/sites", "GET", "", 403},
	}
	for _, tc := range cases {
		tok := createToken(t, s, admin, "m", tc.scopes)
		if rr := doReqWithBearer(t, s, tok, tc.method, tc.path, tc.body); rr.Code != tc.want {
			t.Errorf("token %s %s %s = %d, want %d", tc.scopes, tc.method, tc.path, rr.Code, tc.want)
		}
	}
	_ = st
}

func TestTokenCannotAccessSessionOnlyRoutes(t *testing.T) {
	s, _ := newRBACServer(t)
	admin := setupAdmin(t, s)
	tok := createToken(t, s, admin, "ci", `["sites:read","sites:write","fleet:read","analytics:read","audit:run"]`)
	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/notifications/webhooks"},
		{"POST", "/api/tokens"},
		{"GET", "/api/organization/members"},
		{"GET", "/api/maintenance"},
	} {
		if rr := doReqWithBearer(t, s, tok, tc.method, tc.path, ""); rr.Code != 401 {
			t.Errorf("token on session-only %s %s = %d, want 401", tc.method, tc.path, rr.Code)
		}
	}
}

func TestTokenOrgIsolation(t *testing.T) {
	s, st := newRBACServer(t)
	admin := setupAdmin(t, s)
	mySite := createSiteViaAPI(t, s, admin, "Mine", "https://127.0.0.1:1/")
	if e := sqlite.Exec(st.DB, `INSERT INTO organizations(name,created_at) VALUES('Other',?)`, store.Now()); e != nil {
		t.Fatal(e)
	}
	var otherOrg int64
	if e := st.DB.QueryRow(`SELECT id FROM organizations WHERE name='Other'`).Scan(&otherOrg); e != nil {
		t.Fatal(e)
	}
	if e := sqlite.Exec(st.DB, `INSERT INTO sites(organization_id,name,primary_url,created_at,updated_at) VALUES(?,'Theirs','https://example.com',?,?)`, otherOrg, store.Now(), store.Now()); e != nil {
		t.Fatal(e)
	}
	var otherSite int64
	if e := st.DB.QueryRow(`SELECT id FROM sites WHERE organization_id=?`, otherOrg).Scan(&otherSite); e != nil {
		t.Fatal(e)
	}
	tok := createToken(t, s, admin, "ci", `["sites:read"]`)
	rr := doReqWithBearer(t, s, tok, "GET", fmt.Sprintf("/api/sites/%d", mySite), "")
	if rr.Code != 200 {
		t.Fatalf("own-org token site = %d", rr.Code)
	}
	rr = doReqWithBearer(t, s, tok, "GET", fmt.Sprintf("/api/sites/%d", otherSite), "")
	if rr.Code != 404 {
		t.Fatalf("token cross-org site = %d, want 404", rr.Code)
	}
}

func TestTokenSecretIsHashedAtRestAndNotReusableFromDB(t *testing.T) {
	s, st := newRBACServer(t)
	admin := setupAdmin(t, s)
	tok := createToken(t, s, admin, "ci", `["sites:read"]`)
	rows, e := sqlite.Query(st.DB, `SELECT name,token_hash,prefix FROM api_tokens WHERE prefix=?`, tok[:10])
	if e != nil || len(rows) != 1 {
		t.Fatalf("token row: %v %v", rows, e)
	}
	if rows[0]["token_hash"].Text == tok {
		t.Fatal("raw token persisted instead of a hash")
	}
	if rows[0]["name"].Text != "ci" {
		t.Fatalf("unexpected row: %+v", rows[0])
	}
}

func TestTokenLastUsedAtUpdated(t *testing.T) {
	s, st := newRBACServer(t)
	admin := setupAdmin(t, s)
	tok := createToken(t, s, admin, "ci", `["sites:read"]`)
	if rr := doReqWithBearer(t, s, tok, "GET", "/api/sites", ""); rr.Code != 200 {
		t.Fatalf("auth %d", rr.Code)
	}
	rows, e := sqlite.Query(st.DB, `SELECT last_used_at FROM api_tokens WHERE prefix=?`, tok[:10])
	if e != nil || len(rows) != 1 || rows[0]["last_used_at"].Text == "" {
		t.Fatalf("last_used_at not updated: %v %v", rows, e)
	}
}

// assertSingleJSONError proves the error response is exactly one valid JSON
// document with the intended status. This catches the double-response bug
// where two concatenated JSON objects were emitted.
func assertSingleJSONError(t *testing.T, rr *httptest.ResponseRecorder, wantStatus int) {
	t.Helper()
	if rr.Code != wantStatus {
		t.Fatalf("status = %d, want %d: %s", rr.Code, wantStatus, rr.Body.String())
	}
	var v map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &v); err != nil {
		t.Fatalf("response is not a single JSON document: %v (%s)", err, rr.Body.String())
	}
	if _, ok := v["error"]; !ok {
		t.Fatalf("response has no error key: %s", rr.Body.String())
	}
}

func TestBearerErrorResponsesAreSingleJSON(t *testing.T) {
	s, _ := newRBACServer(t)
	admin := setupAdmin(t, s)
	tok := createToken(t, s, admin, "ci", `["sites:read"]`)
	// Revoke a token to exercise the revoked path.
	rt := doReq(t, s, admin, "POST", "/api/tokens", `{"name":"rev","scopes":["sites:read"]}`)
	var rev struct {
		ID    int64  `json:"id"`
		Token string `json:"token"`
	}
	_ = json.Unmarshal(rt.Body.Bytes(), &rev)
	if rr := doReq(t, s, admin, "DELETE", fmt.Sprintf("/api/tokens/%d", rev.ID), ""); rr.Code != 200 {
		t.Fatalf("revoke %d", rr.Code)
	}
	cases := []struct {
		name, token, method, path string
		want                       int
	}{
		{"malformed-empty", "wf_", "GET", "/api/sites", 401},
		{"unknown", "wf_doesnotexist", "GET", "/api/sites", 401},
		{"revoked", rev.Token, "GET", "/api/sites", 401},
		{"missing-scope", tok, "GET", "/api/fleet", 403},
		{"session-only-route", tok, "GET", "/api/notifications/webhooks", 401},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assertSingleJSONError(t, doReqWithBearer(t, s, tc.token, tc.method, tc.path, ""), tc.want)
		})
	}
}

func TestTokenInvalidAuthThrottled(t *testing.T) {
	s, _ := newRBACServer(t)
	for i := 0; i < 20; i++ {
		if rr := doReqWithBearer(t, s, "wf_bogus", "GET", "/api/sites", ""); rr.Code != 401 {
			t.Fatalf("invalid attempt %d = %d, want 401", i, rr.Code)
		}
	}
	if rr := doReqWithBearer(t, s, "wf_bogus", "GET", "/api/sites", ""); rr.Code != 429 {
		t.Fatalf("over-limit invalid token = %d, want 429", rr.Code)
	}
	// The throttle keys on the resolved client address, so a different source
	// address is not limited together with the first.
	req := strings.NewReader("")
	r := httptest.NewRequest("GET", "/api/sites", req)
	r.RemoteAddr = "198.51.100.9:9999"
	r.Header.Set("Authorization", "Bearer wf_bogus")
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, r)
	if rr.Code != 401 {
		t.Fatalf("different client address = %d, want 401", rr.Code)
	}
}

func TestTokenEmptyBearerThrottled(t *testing.T) {
	s, _ := newRBACServer(t)
	// An empty/malformed Bearer value is a failed authentication attempt and
	// must enter the abuse-control path, not silently fall back to ordinary
	// unauthenticated handling.
	for i := 0; i < 20; i++ {
		req := strings.NewReader("")
		r := httptest.NewRequest("GET", "/api/sites", req)
		r.Header.Set("Authorization", "Bearer ")
		rr := httptest.NewRecorder()
		s.Handler().ServeHTTP(rr, r)
		if rr.Code != 401 {
			t.Fatalf("empty bearer attempt %d = %d, want 401", i, rr.Code)
		}
	}
	req := strings.NewReader("")
	r := httptest.NewRequest("GET", "/api/sites", req)
	r.Header.Set("Authorization", "Bearer ")
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, r)
	if rr.Code != 429 {
		t.Fatalf("empty bearer over-limit = %d, want 429", rr.Code)
	}
}

func TestTokenInvalidAuthThrottleSurvivesSpoofedForwardedFor(t *testing.T) {
	s, _ := newTrustedServer(t, "127.0.0.1/32")
	for i := 0; i < 20; i++ {
		req := strings.NewReader("")
		r := httptest.NewRequest("GET", "/api/sites", req)
		r.RemoteAddr = "127.0.0.1:9999"
		// The attacker can vary the leftmost spoofed entry; the trusted proxy
		// appends the real client as the rightmost untrusted value.
		r.Header.Set("X-Forwarded-For", fmt.Sprintf("203.0.113.%d, 198.51.100.7", i))
		r.Header.Set("Authorization", "Bearer wf_bogus")
		rr := httptest.NewRecorder()
		s.Handler().ServeHTTP(rr, r)
		if rr.Code != 401 {
			t.Fatalf("attempt %d = %d, want 401", i, rr.Code)
		}
	}
	req := strings.NewReader("")
	r := httptest.NewRequest("GET", "/api/sites", req)
	r.RemoteAddr = "127.0.0.1:9999"
	r.Header.Set("X-Forwarded-For", "203.0.113.99, 198.51.100.7")
	r.Header.Set("Authorization", "Bearer wf_bogus")
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, r)
	if rr.Code != 429 {
		t.Fatalf("spoofed forwarded-for bypassed throttle: %d", rr.Code)
	}
}