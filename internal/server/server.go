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

	"github.com/web-fleet/webfleet/internal/analytics"
	"github.com/web-fleet/webfleet/internal/apitokens"
	"github.com/web-fleet/webfleet/internal/audit"
	"github.com/web-fleet/webfleet/internal/auth"
	"github.com/web-fleet/webfleet/internal/config"
	"github.com/web-fleet/webfleet/internal/crawler"
	"github.com/web-fleet/webfleet/internal/databasesetup"
	"github.com/web-fleet/webfleet/internal/dnsobs"
	"github.com/web-fleet/webfleet/internal/fleet"
	"github.com/web-fleet/webfleet/internal/incidents"
	"github.com/web-fleet/webfleet/internal/maintenance"
	"github.com/web-fleet/webfleet/internal/monitor"
	"github.com/web-fleet/webfleet/internal/oidc"
	"github.com/web-fleet/webfleet/internal/performance"
	"github.com/web-fleet/webfleet/internal/rbac"
	"github.com/web-fleet/webfleet/internal/sites"
	"github.com/web-fleet/webfleet/internal/store"
	"github.com/web-fleet/webfleet/internal/tlshealth"
)

//go:embed web/* web/assets/css/* web/assets/js/*
var embedded embed.FS

type Server struct {
	cfg         config.Config
	store       *store.Store
	analytics   *analytics.Service
	audit       *audit.Service
	auth        *auth.Service
	sites       *sites.Service
	monitor     *monitor.Service
	maintenance *maintenance.Service
	rbac        *rbac.Service
	oidc        *oidc.Service
	incidents   *incidents.Service
	tls         *tlshealth.Service
	dns         *dnsobs.Service
	crawler     *crawler.Service
	log         *slog.Logger
	http        *http.Server
	mux         *http.ServeMux
}

func New(cfg config.Config, st *store.Store, log *slog.Logger) *Server {
	s := &Server{cfg: cfg, store: st, analytics: analytics.New(st), tokens: apitokens.New(st), audit: audit.New(st), auth: auth.New(st), sites: sites.New(st), monitor: monitor.New(st), maintenance: maintenance.New(st), rbac: rbac.New(st), incidents: incidents.New(st), tls: tlshealth.New(st), dns: dnsobs.New(st), crawler: crawler.New(st), log: log, mux: http.NewServeMux()}
	s.oidc = oidc.New(st, s.auth)
	s.routes()
	s.http = &http.Server{Addr: cfg.Listen, Handler: s.mux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	return s
}

func NewAnalyticsIngest(cfg config.Config, st *store.Store, log *slog.Logger) *Server {
	s := &Server{cfg: cfg, store: st, analytics: analytics.New(st), log: log, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true, "mode": "analytics-ingest"})
	})
	s.mux.HandleFunc("GET /wf.js", s.handleTracker)
	s.mux.HandleFunc("POST /api/analytics/event", s.handleAnalyticsEvent)
	s.http = &http.Server{Addr: cfg.Listen, Handler: s.mux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	return s
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, 200, map[string]any{"ok": true}) })
	s.mux.HandleFunc("GET /wf.js", s.handleTracker)
	s.mux.HandleFunc("POST /api/analytics/event", s.handleAnalyticsEvent)
	s.mux.HandleFunc("GET /api/setup/status", s.handleSetupStatus)
	s.mux.HandleFunc("GET /api/setup/database", s.handleDatabaseSetupStatus)
	s.mux.HandleFunc("POST /api/setup/database", s.handleDatabaseSetup)
	s.mux.HandleFunc("POST /api/setup", s.handleSetup)
	s.mux.HandleFunc("POST /api/login", s.handleLogin)
	s.mux.HandleFunc("GET /api/oidc/login", s.handleOIDCLogin)
	s.mux.HandleFunc("GET /api/oidc/callback", s.handleOIDCCallback)
	s.mux.HandleFunc("GET /api/oidc/config", s.withSession(s.handleOIDCConfig, false))
	s.mux.HandleFunc("PUT /api/oidc/config", s.withSession(s.handleOIDCConfigSave, true))
	s.mux.HandleFunc("POST /api/logout", s.withSession(s.handleLogout, true))
	s.mux.HandleFunc("GET /api/session", s.withSession(s.handleSession, false))
	s.mux.HandleFunc("POST /api/tokens", s.withSession(s.handleCreateToken, true))
	s.mux.HandleFunc("DELETE /api/tokens/{id}", s.withSession(s.handleRevokeToken, true))
	s.mux.HandleFunc("GET /api/organization/members", s.withSession(s.handleMembers, false))
	s.mux.HandleFunc("POST /api/organization/members", s.withSession(s.handleMemberUpdate, true))
	s.mux.HandleFunc("GET /api/groups", s.withSession(s.handleGroups, false))
	s.mux.HandleFunc("POST /api/groups", s.withSession(s.handleCreateGroup, true))
	s.mux.HandleFunc("GET /api/sites", s.withSession(s.handleSites, false))
	s.mux.HandleFunc("POST /api/sites", s.withSession(s.handleCreateSite, true))
	s.mux.HandleFunc("GET /api/sites/{id}", s.withSession(s.handleSite, false))
	s.mux.HandleFunc("PUT /api/sites/{id}", s.withSession(s.handleUpdateSite, true))
	s.mux.HandleFunc("POST /api/sites/{id}/archive", s.withSession(s.handleArchiveSite, true))
	s.mux.HandleFunc("DELETE /api/sites/{id}", s.withSession(s.handleDeleteSite, true))
	s.mux.HandleFunc("GET /api/sites/{id}/analytics", s.withSession(s.handleAnalyticsProperty, false))
	s.mux.HandleFunc("POST /api/sites/{id}/analytics", s.withSession(s.handleEnableAnalytics, true))
	s.mux.HandleFunc("GET /api/sites/{id}/analytics/summary", s.withSession(s.handleAnalyticsSummary, false))
	s.mux.HandleFunc("GET /api/sites/{id}/analytics/goals", s.withSession(s.handleAnalyticsGoals, false))
	s.mux.HandleFunc("POST /api/sites/{id}/analytics/goals", s.withSession(s.handleCreateAnalyticsGoal, true))
	s.mux.HandleFunc("GET /api/maintenance", s.withSession(s.handleMaintenance, false))
	s.mux.HandleFunc("PUT /api/maintenance", s.withSession(s.handleMaintenanceUpdate, true))
	s.mux.HandleFunc("POST /api/maintenance/run", s.withSession(s.handleMaintenanceRun, true))
	s.mux.HandleFunc("GET /api/fleet", s.withSession(s.handleFleet, false))
	s.mux.HandleFunc("GET /api/fleet/analytics", s.withSession(s.handleFleetAnalytics, false))
	s.mux.HandleFunc("GET /api/sites/{id}/audit", s.withSession(s.handleAudit, false))
	s.mux.HandleFunc("POST /api/sites/{id}/audit", s.withSession(s.handleRunAudit, true))
	s.mux.HandleFunc("PUT /api/sites/{id}/audit/history", s.withSession(s.handleAuditHistorySetting, true))
	s.mux.HandleFunc("POST /api/audits/resolve", s.withSession(s.handleResolveAudits, true))
	s.mux.HandleFunc("POST /api/audits/batch", s.withSession(s.handleBatchAudits, true))
	s.mux.HandleFunc("POST /api/sites/{id}/check", s.withSession(s.handleCheckSite, true))
	s.mux.HandleFunc("GET /api/sites/{id}/checks", s.withSession(s.handleSiteChecks, false))
	s.mux.HandleFunc("GET /api/sites/{id}/performance", s.withSession(s.handleSitePerformance, false))
	s.mux.HandleFunc("GET /api/sites/{id}/incidents", s.withSession(s.handleSiteIncidents, false))
	s.mux.HandleFunc("GET /api/sites/{id}/tls", s.withSession(s.handleSiteTLS, false))
	s.mux.HandleFunc("POST /api/sites/{id}/tls/inspect", s.withSession(s.handleInspectTLS, true))
	s.mux.HandleFunc("GET /api/fleet/tls", s.withSession(s.handleFleetTLS, false))
	s.mux.HandleFunc("GET /api/sites/{id}/dns", s.withSession(s.handleSiteDNS, false))
	s.mux.HandleFunc("POST /api/sites/{id}/dns/observe", s.withSession(s.handleObserveDNS, true))
	s.mux.HandleFunc("GET /api/sites/{id}/http-observations", s.withSession(s.handleHTTPObservations, false))
	s.mux.HandleFunc("GET /api/sites/{id}/crawl", s.withSession(s.handleSiteCrawl, false))
	s.mux.HandleFunc("POST /api/sites/{id}/crawl", s.withSession(s.handleCrawlSite, true))
	s.mux.HandleFunc("GET /api/fleet/link-regressions", s.withSession(s.handleFleetLinkRegressions, false))
	s.mux.HandleFunc("GET /api/sites/{id}/header-expectations", s.withSession(s.handleHeaderExpectations, false))
	s.mux.HandleFunc("PUT /api/sites/{id}/header-expectations", s.withSession(s.handleHeaderExpectationUpdate, true))
	s.mux.HandleFunc("POST /api/incidents/{id}/ack", s.withSession(s.handleAckIncident, true))
	sub, _ := fs.Sub(embedded, "web")
	s.mux.Handle("/", http.FileServer(http.FS(sub)))
}

func (s *Server) handleTracker(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "public,max-age=3600")
	_, _ = w.Write([]byte(analytics.Tracker))
}
func (s *Server) handleAnalyticsEvent(w http.ResponseWriter, r *http.Request) {
	var in analytics.Event
	if !decodeJSON(w, r, &in) {
		return
	}
	origin := r.Header.Get("Origin")
	ip := r.RemoteAddr
	if i := strings.LastIndex(ip, ":"); i > 0 {
		ip = ip[:i]
	}
	if err := s.analytics.Ingest(in, origin, ip, r.UserAgent()); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
func (s *Server) handleMaintenance(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	x, e := s.maintenance.Status()
	if e != nil {
		writeError(w, 500, "maintenance status unavailable")
		return
	}
	writeJSON(w, 200, x)
}
func (s *Server) handleMaintenanceUpdate(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	var v maintenance.Settings
	if !decodeJSON(w, r, &v) {
		return
	}
	if e := s.maintenance.Set(v); e != nil {
		writeError(w, 400, e.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
func (s *Server) handleMaintenanceRun(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	if e := s.maintenance.Run(); e != nil {
		writeError(w, 500, e.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
func (s *Server) handleFleetAnalytics(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	out, err := s.analytics.Fleet(days)
	if err != nil {
		writeError(w, 500, "fleet analytics unavailable")
		return
	}
	writeJSON(w, 200, out)
}
func (s *Server) handleAnalyticsGoals(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id, ok := pathSiteID(w, r)
	if !ok {
		return
	}
	g, e := s.analytics.Goals(id)
	if e != nil {
		writeError(w, 500, "analytics goals unavailable")
		return
	}
	writeJSON(w, 200, map[string]any{"goals": g})
}
func (s *Server) handleCreateAnalyticsGoal(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id, ok := pathSiteID(w, r)
	if !ok {
		return
	}
	var in struct {
		Name      string `json:"name"`
		EventKind string `json:"event_kind"`
		PathMatch string `json:"path_match"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	g, e := s.analytics.CreateGoal(id, in.Name, in.EventKind, in.PathMatch)
	if e != nil {
		writeError(w, 400, e.Error())
		return
	}
	writeJSON(w, 201, g)
}
func (s *Server) handleAnalyticsSummary(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id, ok := pathSiteID(w, r)
	if !ok {
		return
	}
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	out, err := s.analytics.Summary(id, days)
	if err != nil {
		writeError(w, 500, "analytics summary unavailable")
		return
	}
	writeJSON(w, 200, out)
}
func (s *Server) handleAnalyticsProperty(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id, ok := pathSiteID(w, r)
	if !ok {
		return
	}
	p, err := s.analytics.Property(id)
	if err != nil {
		writeError(w, 500, "analytics unavailable")
		return
	}
	writeJSON(w, 200, map[string]any{"property": p})
}
func (s *Server) handleEnableAnalytics(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id, ok := pathSiteID(w, r)
	if !ok {
		return
	}
	p, err := s.analytics.Enable(id)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 201, p)
}

func (s *Server) handleAudit(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id, ok := pathSiteID(w, r)
	if !ok {
		return
	}
	runs, err := s.audit.History(id)
	if err != nil {
		writeError(w, 500, "audit unavailable")
		return
	}
	hist, _ := s.audit.HistoryEnabled(id)
	writeJSON(w, 200, map[string]any{"history_enabled": hist, "runs": runs})
}
func (s *Server) handleRunAudit(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id, ok := pathSiteID(w, r)
	if !ok {
		return
	}
	out, err := s.audit.Run(r.Context(), id)
	if err != nil {
		writeJSON(w, 200, out)
		return
	}
	writeJSON(w, 200, out)
}
func (s *Server) handleAuditHistorySetting(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id, ok := pathSiteID(w, r)
	if !ok {
		return
	}
	var in struct {
		Enabled bool `json:"enabled"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := s.audit.SetHistory(id, in.Enabled); err != nil {
		writeError(w, 500, "audit setting unavailable")
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
func (s *Server) handleResolveAudits(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	var in audit.BatchFilter
	if !decodeJSON(w, r, &in) {
		return
	}
	ids, err := s.audit.ResolveBatch(in)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"site_ids": ids, "count": len(ids)})
}
func (s *Server) handleBatchAudits(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	var in struct {
		SiteIDs []int64 `json:"site_ids"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if len(in.SiteIDs) > 100 {
		writeError(w, 400, "batch audit limit is 100 sites")
		return
	}
	writeJSON(w, 200, map[string]any{"results": s.audit.RunBatch(r.Context(), in.SiteIDs)})
}

func (s *Server) handleDatabaseSetupStatus(w http.ResponseWriter, r *http.Request) {
	x, e := databasesetup.StateFor(s.store, s.cfg.DataDir)
	if e != nil {
		writeError(w, 500, "database setup unavailable")
		return
	}
	writeJSON(w, 200, x)
}
func (s *Server) handleDatabaseSetup(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Provider string `json:"provider"`
		URL      string `json:"url"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	x, e := databasesetup.Apply(r.Context(), s.store, s.cfg.DataDir, in.Provider, in.URL)
	if e != nil {
		writeError(w, 400, e.Error())
		return
	}
	writeJSON(w, 200, x)
}
func (s *Server) oidcRedirect(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	return scheme + "://" + r.Host + "/api/oidc/callback"
}
func (s *Server) handleOIDCLogin(w http.ResponseWriter, r *http.Request) {
	u, e := s.oidc.Begin(r.Context(), s.oidcRedirect(r))
	if e != nil {
		writeError(w, 400, e.Error())
		return
	}
	http.Redirect(w, r, u, http.StatusFound)
}
func (s *Server) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	tok, _, e := s.oidc.Callback(r.Context(), r.URL.Query().Get("state"), r.URL.Query().Get("code"), s.oidcRedirect(r))
	if e != nil {
		writeError(w, 401, e.Error())
		return
	}
	setSessionCookie(w, r, tok)
	http.Redirect(w, r, "/", http.StatusFound)
}
func (s *Server) handleOIDCConfig(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	c, e := s.oidc.Config()
	if e != nil {
		writeJSON(w, 200, map[string]any{"config": nil})
		return
	}
	writeJSON(w, 200, map[string]any{"config": c})
}
func (s *Server) handleOIDCConfigSave(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	role, e := s.rbac.Role(sess.UserID, 1)
	if e != nil || !rbac.Can(role, "membership.update") {
		writeError(w, 403, "permission denied")
		return
	}
	var c oidc.Config
	if !decodeJSON(w, r, &c) {
		return
	}
	if e = s.oidc.Save(c); e != nil {
		writeError(w, 400, e.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
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
func (s *Server) handleSiteCrawl(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id, ok := pathSiteID(w, r)
	if !ok {
		return
	}
	detail, err := s.crawler.LatestDetail(id)
	if err != nil {
		writeJSON(w, 200, map[string]any{"crawl": nil})
		return
	}
	writeJSON(w, 200, map[string]any{"crawl": detail})
}

func (s *Server) handleCrawlSite(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id, ok := pathSiteID(w, r)
	if !ok {
		return
	}
	detail, err := s.crawler.CrawlSite(r.Context(), id)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, detail)
}

func (s *Server) handleFleetLinkRegressions(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	runs, err := s.crawler.FleetRegressions()
	if err != nil {
		writeError(w, 500, "link regressions unavailable")
		return
	}
	out := make([]map[string]any, 0, len(runs))
	for _, run := range runs {
		site, err := s.sites.Get(run.SiteID)
		if err != nil {
			continue
		}
		out = append(out, map[string]any{"site": site, "crawl": run})
	}
	writeJSON(w, 200, map[string]any{"regressions": out})
}

func (s *Server) handleHTTPObservations(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id, ok := pathSiteID(w, r)
	if !ok {
		return
	}
	rows, err := s.monitor.HTTPHistory(id)
	if err != nil {
		writeError(w, 500, "HTTP observations unavailable")
		return
	}
	writeJSON(w, 200, map[string]any{"observations": rows})
}
func (s *Server) handleHeaderExpectations(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id, ok := pathSiteID(w, r)
	if !ok {
		return
	}
	x, err := s.monitor.HeaderExpectations(id)
	if err != nil {
		writeError(w, 500, "header expectations unavailable")
		return
	}
	writeJSON(w, 200, map[string]any{"expectations": x})
}
func (s *Server) handleHeaderExpectationUpdate(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id, ok := pathSiteID(w, r)
	if !ok {
		return
	}
	var in struct {
		Name     string `json:"name"`
		Required bool   `json:"required"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if err := s.monitor.SetHeaderExpectation(id, in.Name, in.Required); err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) handleSiteDNS(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id, ok := pathSiteID(w, r)
	if !ok {
		return
	}
	rows, err := s.dns.History(id)
	if err != nil {
		writeError(w, 500, "DNS history unavailable")
		return
	}
	writeJSON(w, 200, map[string]any{"observations": rows})
}
func (s *Server) handleObserveDNS(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id, ok := pathSiteID(w, r)
	if !ok {
		return
	}
	obs, err := s.dns.ObserveSite(r.Context(), id)
	if err != nil {
		writeError(w, 400, err.Error())
		return
	}
	writeJSON(w, 200, obs)
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
	_, _ = s.dns.ObserveSite(r.Context(), id)
	writeJSON(w, 200, res)
}
func (s *Server) handleSitePerformance(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id, ok := pathSiteID(w, r)
	if !ok {
		return
	}
	out, err := performance.ForSite(s.store, id, 200)
	if err != nil {
		writeError(w, 500, "performance history unavailable")
		return
	}
	writeJSON(w, 200, out)
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

func (s *Server) handleCreateToken(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	var in struct {
		Name   string   `json:"name"`
		Scopes []string `json:"scopes"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	x, e := s.tokens.Create(sess.UserID, 1, in.Name, in.Scopes)
	if e != nil {
		writeError(w, 400, e.Error())
		return
	}
	writeJSON(w, 201, x)
}
func (s *Server) handleRevokeToken(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	id, e := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if e != nil {
		writeError(w, 400, "invalid token id")
		return
	}
	if e = s.tokens.Revoke(id, sess.UserID); e != nil {
		writeError(w, 400, e.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}
func (s *Server) handleMembers(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	role, e := s.rbac.Role(sess.UserID, 1)
	if e != nil || !rbac.Can(role, "organization.read") {
		writeError(w, 403, "permission denied")
		return
	}
	m, e := s.rbac.Members(1)
	if e != nil {
		writeError(w, 500, "members unavailable")
		return
	}
	writeJSON(w, 200, map[string]any{"members": m, "role": role})
}
func (s *Server) handleMemberUpdate(w http.ResponseWriter, r *http.Request, sess auth.Session) {
	role, e := s.rbac.Role(sess.UserID, 1)
	if e != nil || !rbac.Can(role, "membership.update") {
		writeError(w, 403, "permission denied")
		return
	}
	var in struct {
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	if !decodeJSON(w, r, &in) {
		return
	}
	if e = s.rbac.Add(sess.UserID, 1, in.Email, in.Role); e != nil {
		writeError(w, 400, e.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
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
