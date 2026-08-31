package server

import (
	"context"
	"embed"
	"encoding/json"
	"github.com/web-fleet/webfleet/internal/config"
	"github.com/web-fleet/webfleet/internal/store"
	"io/fs"
	"log/slog"
	"net/http"
	"time"
)

//go:embed web/*
var embedded embed.FS

type Server struct {
	cfg   config.Config
	store *store.Store
	log   *slog.Logger
	http  *http.Server
	mux   *http.ServeMux
}

func New(cfg config.Config, st *store.Store, log *slog.Logger) *Server {
	s := &Server{cfg: cfg, store: st, log: log, mux: http.NewServeMux()}
	s.routes()
	s.http = &http.Server{Addr: cfg.Listen, Handler: s.mux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	return s
}
func (s *Server) routes() {
	s.mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	sub, _ := fs.Sub(embedded, "web")
	s.mux.Handle("/", http.FileServer(http.FS(sub)))
}
func (s *Server) ListenAndServe() error              { return s.http.ListenAndServe() }
func (s *Server) Shutdown(ctx context.Context) error { return s.http.Shutdown(ctx) }
func (s *Server) Handler() http.Handler              { return s.mux }
