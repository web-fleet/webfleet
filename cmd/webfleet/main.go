package main

import (
	"context"
	"errors"
	"github.com/web-fleet/webfleet/internal/config"
	"github.com/web-fleet/webfleet/internal/server"
	"github.com/web-fleet/webfleet/internal/store"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load()
	if err != nil {
		log.Error("configuration failed", "error", err)
		os.Exit(1)
	}
	st, err := store.Open(cfg.DataDir)
	if err != nil {
		log.Error("database failed", "error", err)
		os.Exit(1)
	}
	defer st.Close()
	srv := server.New(cfg, st, log)
	errc := make(chan error, 1)
	go func() {
		log.Info("webfleet starting", "listen", cfg.Listen, "data", cfg.DataDir)
		errc <- srv.ListenAndServe()
	}()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	select {
	case <-sig:
		log.Info("shutdown requested")
	case err := <-errc:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("server failed", "error", err)
			os.Exit(1)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Error("shutdown failed", "error", err)
	}
}
