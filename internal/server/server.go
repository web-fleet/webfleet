package server

import (
	"context"
	"embed"
	"encoding/json"
	"io/fs"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/web-fleet/webfleet/internal/auth"
	"github.com/web-fleet/webfleet/internal/config"
	"github.com/web-fleet/webfleet/internal/fleet"
	"github.com/web-fleet/webfleet/internal/incidents"
	"github.com/web-fleet/webfleet/internal/monitor"
	"github.com/web-fleet/webfleet/internal/sites"
	"github.com/web-fleet/webfleet/internal/store"
	"github.com/web-fleet/webfleet/internal/tlshealth"
)

//go:embed web/* web/assets/css/* web/assets/js/*
var embedded embed.FS

type Server struct {
	cfg       config.Config
	store     *store.Store
	auth      *auth.Service
	sites     *sites.Service
	monitor   *monitor.Service
	incidents *incidents.Service
	tls       *tlshealth.Service
	log       *slog.Logger
	http      *http.Server
	mux       *http.ServeMux
}

func New(cfg config.Config, st *store.Store, log *slog.Logger) *Server {
	s := &Server{cfg: cfg, store: st, auth: auth.New(st), sites: sites.New(st), monitor: monitor.New(st), incidents: incidents.New(st), tls: tlshealth.New(st), log: log, mux: http.NewServeMux()}
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
	s.mux.HandleFunc("GET /api/groups", s.withSession(s.handleGroups, false))
	s.mux.HandleFunc("POST /api/groups", s.withSession(s.handleCreateGroup, true))
	s.mux.HandleFunc("GET /api/sites", s.withSession(s.handleSites, false))
	s.mux.HandleFunc("POST /api/sites", s.withSession(s.handleCreateSite, true))
	s.mux.HandleFunc("GET /api/sites/{id}", s.withSession(s.handleSite, false))
	s.mux.HandleFunc("PUT /api/sites/{id}", s.withSession(s.handleUpdateSite, true))
	s.mux.HandleFunc("POST /api/sites/{id}/archive", s.withSession(s.handleArchiveSite, true))
	s.mux.HandleFunc("DELETE /api/sites/{id}", s.withSession(s.handleDeleteSite, true))
	s.mux.HandleFunc("GET /api/fleet", s.withSession(s.handleFleet, false))
	s.mux.HandleFunc("POST /api/sites/{id}/check", s.withSession(s.handleCheckSite, true))
	s.mux.HandleFunc("GET /api/sites/{id}/checks", s.withSession(s.handleSiteChecks, false))
	s.mux.HandleFunc("GET /api/sites/{id}/incidents", s.withSession(s.handleSiteIncidents, false))
	s.mux.HandleFunc("GET /api/sites/{id}/tls", s.withSession(s.handleSiteTLS, false))
	s.mux.HandleFunc("POST /api/sites/{id}/tls/inspect", s.withSession(s.handleInspectTLS, true))
	s.mux.HandleFunc("GET /api/fleet/tls", s.withSession(s.handleFleetTLS, false))
	s.mux.HandleFunc("POST /api/incidents/{id}/ack", s.withSession(s.handleAckIncident, true))
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
func (s *Server) handleSiteTLS(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id, ok := pathSiteID(w, r)
	if !ok {
		return
	}
	obs, err := s.tls.Latest(id)
	if err != nil {
		writeJSON(w, 200, map[string]any{"observation": nil})
		return
	}
	writeJSON(w, 200, map[string]any{"observation": obs})
}
func (s *Server) handleInspectTLS(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id, ok := pathSiteID(w, r)
	if !ok {
		return
	}
	obs, err := s.tls.InspectSite(r.Context(), id)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, obs)
}
func (s *Server) handleFleetTLS(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	rows, err := s.tls.FleetWarnings(30)
	if err != nil {
		writeError(w, 500, "TLS warnings unavailable")
		return
	}
	writeJSON(w, 200, map[string]any{"warnings": rows})
}

func (s *Server) handleSiteIncidents(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id, ok := pathSiteID(w, r)
	if !ok {
		return
	}
	rows, err := s.incidents.List(id)
	if err != nil {
		writeError(w, 500, "incident history unavailable")
		return
	}
	writeJSON(w, 200, map[string]any{"incidents": rows})
}
func (s *Server) handleAckIncident(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		writeError(w, 400, "invalid incident id")
		return
	}
	if err = s.incidents.Acknowledge(id, store.Now()); err != nil {
		writeError(w, 404, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) handleFleet(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	sum, err := fleet.SummaryFor(s.store)
	if err != nil {
		writeError(w, 500, "fleet summary unavailable")
		return
	}
	writeJSON(w, 200, sum)
}
func (s *Server) handleCheckSite(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id, ok := pathSiteID(w, r)
	if !ok {
		return
	}
	res, err := s.monitor.CheckSite(r.Context(), id)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	_, _ = s.tls.InspectSite(r.Context(), id)
	writeJSON(w, 200, res)
}
func (s *Server) handleSiteChecks(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id, ok := pathSiteID(w, r)
	if !ok {
		return
	}
	rows, err := s.monitor.Recent(id, 50)
	if err != nil {
		writeError(w, 500, "check history unavailable")
		return
	}
	writeJSON(w, 200, map[string]any{"checks": rows})
}

func (s *Server) handleGroups(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	groups, err := s.sites.Groups()
	if err != nil {
		writeError(w, 500, "groups unavailable")
		return
	}
	writeJSON(w, 200, map[string]any{"groups": groups})
}
func (s *Server) handleCreateGroup(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	var in struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	g, err := s.sites.CreateGroup(in.Name)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 201, g)
}
func (s *Server) handleSites(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	q := r.URL.Query()
	group, _ := strconv.ParseInt(q.Get("group"), 10, 64)
	page, _ := strconv.Atoi(q.Get("page"))
	size, _ := strconv.Atoi(q.Get("page_size"))
	out, err := s.sites.List(q.Get("q"), group, page, size, q.Get("archived") == "1")
	if err != nil {
		writeError(w, 500, "sites unavailable")
		return
	}
	writeJSON(w, 200, out)
}
func (s *Server) handleCreateSite(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	var in struct {
		Name       string `json:"name"`
		PrimaryURL string `json:"primary_url"`
		GroupID    int64  `json:"group_id"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	site, err := s.sites.Create(in.Name, in.PrimaryURL, in.GroupID)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 201, site)
}
func pathSiteID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := sites.ParseID(r.PathValue("id"))
	if err != nil {
		writeError(w, 400, "invalid site id")
		return 0, false
	}
	return id, true
}
func (s *Server) handleSite(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id, ok := pathSiteID(w, r)
	if !ok {
		return
	}
	site, err := s.sites.Get(id)
	if err != nil {
		writeError(w, 404, "site not found")
		return
	}
	writeJSON(w, 200, site)
}
func (s *Server) handleUpdateSite(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id, ok := pathSiteID(w, r)
	if !ok {
		return
	}
	var in struct {
		Name       string `json:"name"`
		PrimaryURL string `json:"primary_url"`
		GroupID    int64  `json:"group_id"`
		Enabled    bool   `json:"enabled"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	site, err := s.sites.Update(id, in.Name, in.PrimaryURL, in.GroupID, in.Enabled)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, site)
}
func (s *Server) handleArchiveSite(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id, ok := pathSiteID(w, r)
	if !ok {
		return
	}
	var in struct {
		Archived bool `json:"archived"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := s.sites.Archive(id, in.Archived); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
func (s *Server) handleDeleteSite(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id, ok := pathSiteID(w, r)
	if !ok {
		return
	}
	if err := s.sites.Delete(id); err != nil {
		writeError(w, 400, err.Error())
		return
	}
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
