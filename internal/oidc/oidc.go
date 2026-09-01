package oidc

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"github.com/web-fleet/webfleet/internal/auth"
	"github.com/web-fleet/webfleet/internal/sqlite"
	"github.com/web-fleet/webfleet/internal/store"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Config struct {
	Issuer        string `json:"issuer"`
	ClientID      string `json:"client_id"`
	ClientSecret  string `json:"client_secret,omitempty"`
	Enabled       bool   `json:"enabled"`
	AutoProvision bool   `json:"auto_provision"`
}
type Discovery struct {
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	UserinfoEndpoint      string `json:"userinfo_endpoint"`
}
type Service struct {
	st     *store.Store
	auth   *auth.Service
	client *http.Client
}

func New(st *store.Store, a *auth.Service) *Service {
	return &Service{st: st, auth: a, client: &http.Client{Timeout: 10 * time.Second}}
}
func (s *Service) Config() (*Config, error) {
	r, e := sqlite.Query(s.st.DB, `SELECT issuer,client_id,client_secret,enabled,auto_provision FROM oidc_config WHERE id=1`)
	if e != nil || len(r) == 0 {
		return nil, e
	}
	x := r[0]
	return &Config{x["issuer"].Text, x["client_id"].Text, "", x["enabled"].Int64 != 0, x["auto_provision"].Int64 != 0}, nil
}
func (s *Service) Save(c Config) error {
	c.Issuer = strings.TrimRight(strings.TrimSpace(c.Issuer), "/")
	if !strings.HasPrefix(c.Issuer, "https://") || c.ClientID == "" || c.ClientSecret == "" {
		return errors.New("OIDC requires HTTPS issuer, client id and secret")
	}
	if _, e := s.discovery(context.Background(), c.Issuer); e != nil {
		return e
	}
	return sqlite.Exec(s.st.DB, `INSERT INTO oidc_config(id,issuer,client_id,client_secret,enabled,auto_provision,updated_at) VALUES(1,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET issuer=excluded.issuer,client_id=excluded.client_id,client_secret=excluded.client_secret,enabled=excluded.enabled,auto_provision=excluded.auto_provision,updated_at=excluded.updated_at`, c.Issuer, c.ClientID, c.ClientSecret, c.Enabled, c.AutoProvision, store.Now())
}
func (s *Service) discovery(ctx context.Context, issuer string) (Discovery, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", issuer+"/.well-known/openid-configuration", nil)
	resp, e := s.client.Do(req)
	if e != nil {
		return Discovery{}, e
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return Discovery{}, errors.New("OIDC discovery failed")
	}
	var d Discovery
	e = json.NewDecoder(resp.Body).Decode(&d)
	if e != nil || d.AuthorizationEndpoint == "" || d.TokenEndpoint == "" || d.UserinfoEndpoint == "" {
		return d, errors.New("incomplete OIDC discovery")
	}
	return d, nil
}
func token(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}
func (s *Service) Begin(ctx context.Context, redirect string) (string, error) {
	r, e := sqlite.Query(s.st.DB, `SELECT issuer,client_id,enabled FROM oidc_config WHERE id=1`)
	if e != nil || len(r) == 0 || r[0]["enabled"].Int64 == 0 {
		return "", errors.New("OIDC is not enabled")
	}
	d, e := s.discovery(ctx, r[0]["issuer"].Text)
	if e != nil {
		return "", e
	}
	state, nonce := token(24), token(24)
	_ = sqlite.Exec(s.st.DB, `INSERT INTO oidc_states(state,nonce,expires_at) VALUES(?,?,?)`, state, nonce, time.Now().UTC().Add(10*time.Minute).Format(time.RFC3339Nano))
	v := url.Values{"client_id": {r[0]["client_id"].Text}, "response_type": {"code"}, "scope": {"openid email profile"}, "redirect_uri": {redirect}, "state": {state}, "nonce": {nonce}}
	return d.AuthorizationEndpoint + "?" + v.Encode(), nil
}
func (s *Service) Callback(ctx context.Context, state, code, redirect string) (string, auth.Session, error) {
	r, e := sqlite.Query(s.st.DB, `SELECT expires_at FROM oidc_states WHERE state=?`, state)
	if e != nil || len(r) == 0 {
		return "", auth.Session{}, errors.New("invalid OIDC state")
	}
	_ = sqlite.Exec(s.st.DB, `DELETE FROM oidc_states WHERE state=?`, state)
	exp, _ := time.Parse(time.RFC3339Nano, r[0]["expires_at"].Text)
	if time.Now().After(exp) {
		return "", auth.Session{}, errors.New("expired OIDC state")
	}
	cfg, _ := sqlite.Query(s.st.DB, `SELECT issuer,client_id,client_secret,auto_provision FROM oidc_config WHERE id=1 AND enabled=1`)
	if len(cfg) == 0 {
		return "", auth.Session{}, errors.New("OIDC disabled")
	}
	x := cfg[0]
	d, e := s.discovery(ctx, x["issuer"].Text)
	if e != nil {
		return "", auth.Session{}, e
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "redirect_uri": {redirect}, "client_id": {x["client_id"].Text}, "client_secret": {x["client_secret"].Text}}
	req, _ := http.NewRequestWithContext(ctx, "POST", d.TokenEndpoint, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, e := s.client.Do(req)
	if e != nil {
		return "", auth.Session{}, e
	}
	defer resp.Body.Close()
	var tr struct {
		AccessToken string `json:"access_token"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&tr) != nil || tr.AccessToken == "" {
		return "", auth.Session{}, errors.New("OIDC token exchange failed")
	}
	req, _ = http.NewRequestWithContext(ctx, "GET", d.UserinfoEndpoint, nil)
	req.Header.Set("Authorization", "Bearer "+tr.AccessToken)
	resp, e = s.client.Do(req)
	if e != nil {
		return "", auth.Session{}, e
	}
	defer resp.Body.Close()
	var ui struct {
		Email         string `json:"email"`
		EmailVerified bool   `json:"email_verified"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(resp.Body).Decode(&ui) != nil || ui.Email == "" || !ui.EmailVerified {
		return "", auth.Session{}, errors.New("OIDC verified email required")
	}
	u, e := sqlite.Query(s.st.DB, `SELECT id,email FROM users WHERE lower(email)=lower(?)`, ui.Email)
	if e != nil {
		return "", auth.Session{}, e
	}
	if len(u) == 0 {
		if x["auto_provision"].Int64 == 0 {
			return "", auth.Session{}, errors.New("OIDC account is not provisioned")
		}
		u, e = sqlite.Query(s.st.DB, `INSERT INTO users(email,password_hash,role,created_at) VALUES(?,'','viewer',?) RETURNING id,email`, strings.ToLower(ui.Email), store.Now())
		if e != nil {
			return "", auth.Session{}, e
		}
		_ = sqlite.Exec(s.st.DB, `INSERT INTO organization_memberships(organization_id,user_id,role,created_at) VALUES(1,?,'viewer',?)`, u[0]["id"].Int64, store.Now())
	}
	return s.auth.CreateSessionForUser(u[0]["id"].Int64, u[0]["email"].Text)
}
