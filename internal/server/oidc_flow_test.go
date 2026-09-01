package server

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/web-fleet/webfleet/internal/config"
	"github.com/web-fleet/webfleet/internal/store"
)

// fakeOIDCProvider is a minimal standards-shaped provider for server-level
// OIDC flow tests (discovery, JWKS, token).
type fakeOIDCProvider struct {
	t      *testing.T
	srv    *httptest.Server
	key    *rsa.PrivateKey
	issuer string
	mu     sync.Mutex
	nonce  string
	email  string
}

func newFakeOIDCProvider(t *testing.T, email string) *fakeOIDCProvider {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	fp := &fakeOIDCProvider{t: t, key: key, email: email}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", fp.discovery)
	mux.HandleFunc("/jwks", fp.jwks)
	mux.HandleFunc("/token", fp.token)
	fp.srv = httptest.NewTLSServer(mux)
	fp.issuer = fp.srv.URL
	return fp
}

func (f *fakeOIDCProvider) setNonce(n string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nonce = n
}

func (f *fakeOIDCProvider) discovery(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"issuer":                 f.issuer,
		"authorization_endpoint": f.issuer + "/authorize",
		"token_endpoint":         f.issuer + "/token",
		"userinfo_endpoint":      f.issuer + "/userinfo",
		"jwks_uri":               f.issuer + "/jwks",
	})
}

func (f *fakeOIDCProvider) jwks(w http.ResponseWriter, r *http.Request) {
	e := ""
	for v := f.key.E; v > 0; v >>= 8 {
		e = string(rune(v&0xff)) + e
	}
	n := base64.RawURLEncoding.EncodeToString(f.key.N.Bytes())
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"keys": []any{map[string]any{
		"kty": "RSA", "kid": "testkey", "use": "sig", "alg": "RS256", "n": n, "e": base64.RawURLEncoding.EncodeToString([]byte(e)),
	}}})
}

func (f *fakeOIDCProvider) token(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	claims := map[string]any{
		"iss": f.issuer, "sub": "s-1", "aud": "wf-client",
		"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(),
		"nonce": f.nonce, "email": f.email, "email_verified": true,
	}
	hb, _ := json.Marshal(map[string]any{"alg": "RS256", "kid": "testkey", "typ": "JWT"})
	cb, _ := json.Marshal(claims)
	b64 := func(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }
	msg := b64(hb) + "." + b64(cb)
	h := sha256.Sum256([]byte(msg))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, h[:])
	if err != nil {
		f.t.Fatal(err)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "id_token": msg + "." + b64(sig)})
}

func newOIDCServer(t *testing.T, trusted []string) (*Server, *store.Store) {
	t.Helper()
	st, e := store.Open(t.TempDir())
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = st.Close() })
	cfg := config.Config{}
	for _, p := range trusted {
		cfg.TrustedProxies = append(cfg.TrustedProxies, netip.MustParsePrefix(p))
	}
	s := New(cfg, st, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return s, st
}

func TestOIDCFlowCrossBrowserBinding(t *testing.T) {
	fp := newFakeOIDCProvider(t, "oidc@example.com")
	defer fp.srv.Close()
	s, _ := newOIDCServer(t, nil)
	s.cfg.PublicURL = "https://wf.example.com"
	s.oidc.SetHTTPClient(fp.srv.Client())
	admin := setupAdmin(t, s)

	// Configure OIDC through the server (admin + CSRF).
	if rr := doReq(t, s, admin, "PUT", "/api/oidc/config", `{"issuer":"`+fp.issuer+`","client_id":"wf-client","client_secret":"secret","enabled":true,"auto_provision":true}`); rr.Code != 200 {
		t.Fatalf("save config %d %s", rr.Code, rr.Body.String())
	}

	// Begin a login; the binding cookie is issued alongside the redirect.
	login := doReq(t, s, nil, "GET", "/api/oidc/login", "")
	if login.Code != 302 {
		t.Fatalf("login %d %s", login.Code, login.Body.String())
	}
	var binding *http.Cookie
	for _, c := range login.Result().Cookies() {
		if c.Name == oidcBindingCookie {
			binding = c
		}
	}
	if binding == nil || !binding.HttpOnly || binding.MaxAge != 600 {
		t.Fatalf("binding cookie missing/malformed: %+v", binding)
	}
	loc, _ := url.Parse(login.Header().Get("Location"))
	state := loc.Query().Get("state")
	nonce := loc.Query().Get("nonce")
	fp.setNonce(nonce)

	// Cross-browser attack: a different browser must neither establish a
	// session nor destroy the legitimate browser's valid state.
	other := &http.Cookie{Name: oidcBindingCookie, Value: "other-browser"}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/api/oidc/callback?state="+state+"&code=x", nil)
	req.AddCookie(other)
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != 401 {
		t.Fatalf("cross-browser callback = %d, want 401", rr.Code)
	}
	for _, c := range rr.Result().Cookies() {
		if c.Name == "webfleet_session" {
			t.Fatal("cross-browser callback established a session")
		}
	}

	// The legitimate browser can still consume the same valid state.
	rr = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/oidc/callback?state="+state+"&code=x", nil)
	req.AddCookie(binding)
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != 302 {
		t.Fatalf("correct browser after wrong-browser attempt = %d %s", rr.Code, rr.Body.String())
	}
	gotSession := false
	clearedBinding := false
	for _, c := range rr.Result().Cookies() {
		switch c.Name {
		case "webfleet_session":
			gotSession = true
		case oidcBindingCookie:
			clearedBinding = c.MaxAge < 0
		}
	}
	if !gotSession || !clearedBinding {
		t.Fatalf("session=%v binding-cleared=%v", gotSession, clearedBinding)
	}

	// Replay by the legitimate browser is rejected.
	rr = httptest.NewRecorder()
	req = httptest.NewRequest("GET", "/api/oidc/callback?state="+state+"&code=x", nil)
	req.AddCookie(binding)
	s.Handler().ServeHTTP(rr, req)
	if rr.Code != 401 {
		t.Fatalf("replayed callback after successful consumption = %d, want 401", rr.Code)
	}
}

func TestOIDCRedirectCanonicalOrigin(t *testing.T) {
	// Configured public URL is the canonical origin; Host and forwarded headers
	// cannot rewrite it, in direct or trusted-proxy deployments.
	s, _ := newOIDCServer(t, nil)
	s.cfg.PublicURL = "https://webfleet.example.com"
	r := httptest.NewRequest("GET", "http://evil.example/api/oidc/login", nil)
	r.Host = "evil.example"
	r.Header.Set("X-Forwarded-Host", "spoofed.example")
	r.Header.Set("Forwarded", "host=spoofed.example")
	if got := s.oidcRedirect(r); got != "https://webfleet.example.com/api/oidc/callback" {
		t.Fatalf("canonical redirect = %q", got)
	}
	r2 := httptest.NewRequest("GET", "http://wf.example.com/api/oidc/login", nil)
	r2.Host = "wf.example.com"
	r2.RemoteAddr = "127.0.0.1:9999"
	r2.Header.Set("X-Forwarded-Proto", "https")
	if got := s.oidcRedirect(r2); got != "https://webfleet.example.com/api/oidc/callback" {
		t.Fatalf("canonical redirect behind trusted proxy = %q", got)
	}
	// Without a configured canonical origin, OIDC fails closed: no redirect URI
	// is produced from the Host header at all.
	s2, _ := newOIDCServer(t, nil)
	r3 := httptest.NewRequest("GET", "http://evil.example/api/oidc/login", nil)
	r3.Host = "evil.example"
	if got := s2.oidcRedirect(r3); got != "" {
		t.Fatalf("no-origin redirect = %q, want empty", got)
	}
	// The login and config-save handlers reject clearly instead of using Host.
	if rr := doReq(t, s2, nil, "GET", "/api/oidc/login", ""); rr.Code != 400 {
		t.Fatalf("no-origin login = %d, want 400", rr.Code)
	}
	admin := setupAdmin(t, s2)
	if rr := doReq(t, s2, admin, "PUT", "/api/oidc/config", `{"issuer":"https://example.com","client_id":"c","client_secret":"s","enabled":true,"auto_provision":true}`); rr.Code != 400 {
		t.Fatalf("enabling OIDC without canonical origin = %d, want 400", rr.Code)
	}
	// Local password authentication still works without WEBFLEET_PUBLIC_URL.
	rr := doReq(t, s2, nil, "POST", "/api/login", `{"email":"admin@example.com","password":"secret7"}`)
	if rr.Code != 200 {
		t.Fatalf("local login without canonical origin = %d", rr.Code)
	}
}
