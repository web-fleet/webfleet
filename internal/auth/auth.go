package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"github.com/web-fleet/webfleet/internal/password"
	"github.com/web-fleet/webfleet/internal/sqlite"
	"github.com/web-fleet/webfleet/internal/store"
	"strings"
	"time"
)

const MinPasswordLength = 7

type Session struct {
	UserID      int64
	Email, CSRF string
	Expires     time.Time
}
type Service struct{ store *store.Store }

func New(s *store.Store) *Service { return &Service{store: s} }
func (a *Service) NeedsSetup() (bool, error) {
	r, e := sqlite.Query(a.store.DB, `SELECT COUNT(*) n FROM users`)
	if e != nil {
		return false, e
	}
	return r[0]["n"].Int64 == 0, nil
}
func (a *Service) CreateAdmin(email, pw string) error {
	email = strings.TrimSpace(strings.ToLower(email))
	if !strings.Contains(email, "@") || len(pw) < MinPasswordLength {
		return errors.New("valid email and password of at least 7 characters required")
	}
	need, e := a.NeedsSetup()
	if e != nil {
		return e
	}
	if !need {
		return errors.New("setup already completed")
	}
	h, e := password.Hash(pw)
	if e != nil {
		return e
	}
	if e = sqlite.Exec(a.store.DB, `INSERT INTO users(email,password_hash,role,created_at) VALUES(?,?, 'admin',?)`, email, h, store.Now()); e != nil {
		return e
	}
	return a.audit("first_admin_created", email)
}
func (a *Service) Login(email, pw string) (string, Session, error) {
	r, e := sqlite.Query(a.store.DB, `SELECT id,email,password_hash FROM users WHERE email=? LIMIT 1`, strings.TrimSpace(strings.ToLower(email)))
	if e != nil || len(r) == 0 {
		return "", Session{}, errors.New("invalid credentials")
	}
	if !password.Verify(r[0]["password_hash"].Text, pw) {
		return "", Session{}, errors.New("invalid credentials")
	}
	raw := token(32)
	csrf := token(24)
	sum := sha256.Sum256([]byte(raw))
	exp := time.Now().UTC().Add(24 * time.Hour)
	if e = sqlite.Exec(a.store.DB, `INSERT INTO sessions(token_hash,user_id,csrf_token,expires_at,created_at) VALUES(?,?,?,?,?)`, sum[:], r[0]["id"].Int64, csrf, exp.Format(time.RFC3339Nano), store.Now()); e != nil {
		return "", Session{}, e
	}
	_ = a.audit("login", r[0]["email"].Text)
	return raw, Session{UserID: r[0]["id"].Int64, Email: r[0]["email"].Text, CSRF: csrf, Expires: exp}, nil
}
func (a *Service) CreateSessionForUser(userID int64, email string) (string, Session, error) {
	raw := token(32)
	csrf := token(24)
	sum := sha256.Sum256([]byte(raw))
	exp := time.Now().UTC().Add(24 * time.Hour)
	if e := sqlite.Exec(a.store.DB, `INSERT INTO sessions(token_hash,user_id,csrf_token,expires_at,created_at) VALUES(?,?,?,?,?)`, sum[:], userID, csrf, exp.Format(time.RFC3339Nano), store.Now()); e != nil {
		return "", Session{}, e
	}
	_ = a.audit("oidc_login", email)
	return raw, Session{UserID: userID, Email: email, CSRF: csrf, Expires: exp}, nil
}
func (a *Service) Session(raw string) (Session, error) {
	if raw == "" {
		return Session{}, errors.New("no session")
	}
	sum := sha256.Sum256([]byte(raw))
	r, e := sqlite.Query(a.store.DB, `SELECT s.user_id,s.csrf_token,s.expires_at,u.email FROM sessions s JOIN users u ON u.id=s.user_id WHERE s.token_hash=? LIMIT 1`, sum[:])
	if e != nil || len(r) == 0 {
		return Session{}, errors.New("invalid session")
	}
	exp, e := time.Parse(time.RFC3339Nano, r[0]["expires_at"].Text)
	if e != nil || time.Now().After(exp) {
		_ = sqlite.Exec(a.store.DB, `DELETE FROM sessions WHERE token_hash=?`, sum[:])
		return Session{}, errors.New("expired session")
	}
	return Session{UserID: r[0]["user_id"].Int64, Email: r[0]["email"].Text, CSRF: r[0]["csrf_token"].Text, Expires: exp}, nil
}
func (a *Service) Logout(raw string) {
	sum := sha256.Sum256([]byte(raw))
	_ = sqlite.Exec(a.store.DB, `DELETE FROM sessions WHERE token_hash=?`, sum[:])
	_ = a.audit("logout", "")
}
func (a *Service) audit(kind, detail string) error {
	return sqlite.Exec(a.store.DB, `INSERT INTO audit_events(kind,detail,created_at) VALUES(?,?,?)`, kind, detail, store.Now())
}
func token(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

var _ = sqlite.Row{}
