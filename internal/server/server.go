package server

import (
	"context"
	"embed"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/web-fleet/webfleet/internal/auth"
	"github.com/web-fleet/webfleet/internal/config"
	"github.com/web-fleet/webfleet/internal/store"
)

//go:embed web/* web/assets/css/* web/assets/js/*
var embedded embed.FS

type Server struct {
	cfg   config.Config
	store *store.Store
	auth  *auth.Service
	log   *slog.Logger
	http  *http.Server
	mux   *http.ServeMux
}

func New(cfg config.Config, st *store.Store, log *slog.Logger) *Server {
	s := &Server{cfg: cfg, store: st, auth: auth.New(st), log: log, mux: http.NewServeMux()}
	s.routes()
	s.http = &http.Server{Addr: cfg.Listen, Handler: s.mux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	return s
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]any{"ok": true}) })
	s.mux.HandleFunc("GET /api/setup/status", s.handleSetupStatus)
	s.mux.HandleFunc("POST /api/setup", s.handleSetup)
	s.mux.HandleFunc("POST /api/login", s.handleLogin)
	s.mux.HandleFunc("POST /api/logout", s.withSession(s.handleLogout, true))
	s.mux.HandleFunc("GET /api/session", s.withSession(s.handleSession, false))
	sub, _ := fs.Sub(embedded, "web")
	s.mux.Handle("/", http.FileServer(http.FS(sub)))
}

func (s *Server) handleSetupStatus(w http.ResponseWriter, r *http.Request) {
	need, err := s.auth.NeedsSetup()
	if err != nil {
		writeError(w, 500, "setup status unavailable")
		return
	}
	writeJSON(w, 200, map[string]any{"needs_setup": need, "min_password_length": auth.MinPasswordLength})
}
func (s *Server) handleSetup(w http.ResponseWriter, r *http.Request) {
	var in struct{ Email, Password string }
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := s.auth.CreateAdmin(in.Email, in.Password); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	token, sess, err := s.auth.Login(in.Email, in.Password)
	if err != nil {
		writeError(w, 500, "admin created but login failed")
		return
	}
	setSessionCookie(w, r, token)
	writeJSON(w, 201, map[string]any{"email": sess.Email, "csrf": sess.CSRF})
}
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var in struct{ Email, Password string }
	if !decodeJSON(w, r, &in) {
		return
	}
	token, sess, err := s.auth.Login(in.Email, in.Password)
	if err != nil {
		writeError(w, 401, "invalid credentials")
		return
	}
	setSessionCookie(w, r, token)
	writeJSON(w, 200, map[string]any{"email": sess.Email, "csrf": sess.CSRF})
}
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	c, _ := r.Cookie("webfleet_session")
	if c != nil {
		s.auth.Logout(c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: "webfleet_session", Path: "/", HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: r.TLS != nil, MaxAge: -1})
	writeJSON(w, 200, map[string]any{"ok": true})
}
func (s *Server) handleSession(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	writeJSON(w, 200, map[string]any{"email": sess.Email, "csrf": sess.CSRF, "role": "admin"})
}

func (s *Server) withSession(next func(http.ResponseWriter, *http.Request, auth.Session), csrf bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("webfleet_session")
		if err != nil {
			writeError(w, 401, "authentication required")
			return
		}
		sess, err := s.auth.Session(c.Value)
		if err != nil {
			writeError(w, 401, "authentication required")
			return
		}
		if csrf && r.Header.Get("X-CSRF-Token") != sess.CSRF {
			writeError(w, 403, "invalid CSRF token")
			return
		}
		next(w, r, sess)
	}
}
func setSessionCookie(w http.ResponseWriter, r *http.Request, token string) {
	http.SetCookie(w, &http.Cookie{Name: "webfleet_session", Value: token, Path: "/", HttpOnly: true, Secure: r.TLS != nil, SameSite: http.SameSiteStrictMode, MaxAge: 86400})
}
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, 400, "invalid JSON request")
		return false
	}
	return true
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]any{"error": strings.TrimSpace(msg)})
}
func (s *Server) ListenAndServe() error              { return s.http.ListenAndServe() }
func (s *Server) Shutdown(ctx context.Context) error { return s.http.Shutdown(ctx) }
func (s *Server) Handler() http.Handler              { return s.mux }
