package main

import (
	"context"
	"errors"
	"github.com/web-fleet/webfleet/internal/config"
	"github.com/web-fleet/webfleet/internal/crawler"
	"github.com/web-fleet/webfleet/internal/dnsobs"
	"github.com/web-fleet/webfleet/internal/monitor"
	"github.com/web-fleet/webfleet/internal/notifications"
	"github.com/web-fleet/webfleet/internal/scheduler"
	"github.com/web-fleet/webfleet/internal/server"
	"github.com/web-fleet/webfleet/internal/service"
	"github.com/web-fleet/webfleet/internal/store"
	"github.com/web-fleet/webfleet/internal/tlshealth"
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
	if len(os.Args) >= 3 && os.Args[1] == "backup" {
		provider := store.Provider(cfg.DatabaseURL)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if provider == "postgres" {
			if err := store.PostgresBackup(ctx, cfg.DatabaseURL, os.Args[2]); err != nil {
				log.Error("postgres backup failed", "error", err)
				os.Exit(1)
			}
		} else {
			st, err := store.Open(cfg.DataDir)
			if err != nil {
				log.Error("database failed", "error", err)
				os.Exit(1)
			}
			defer st.Close()
			if err := st.Backup(os.Args[2]); err != nil {
				log.Error("backup failed", "error", err)
				os.Exit(1)
			}
		}
		log.Info("backup complete", "provider", provider, "path", os.Args[2])
		return
	}
	if len(os.Args) >= 2 && os.Args[1] == "service" {
		if len(os.Args) < 3 {
			log.Error("usage: webfleet service install|uninstall|update|rollback")
			os.Exit(2)
		}
		switch os.Args[2] {
		case "install":
			if err := service.Install(service.Executable(), cfg.DataDir, cfg.Listen); err != nil {
				log.Error("service install failed", "error", err)
				os.Exit(1)
			}
		case "uninstall":
			if err := service.Uninstall(); err != nil {
				log.Error("service uninstall failed", "error", err)
				os.Exit(1)
			}
		case "update":
			if len(os.Args) != 5 {
				log.Error("usage: webfleet service update ARTIFACT SHA256")
				os.Exit(2)
			}
			if err := service.Update(os.Args[3], os.Args[4]); err != nil {
				log.Error("service update failed", "error", err)
				os.Exit(1)
			}
		case "rollback":
			if err := service.Rollback(); err != nil {
				log.Error("service rollback failed", "error", err)
				os.Exit(1)
			}
		default:
			log.Error("unknown service command")
			os.Exit(2)
		}
		return
	}
	if len(os.Args) >= 3 && os.Args[1] == "restore" {
		provider := store.Provider(cfg.DatabaseURL)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
		defer cancel()
		if provider == "postgres" {
			if err := store.PostgresRestore(ctx, cfg.DatabaseURL, os.Args[2]); err != nil {
				log.Error("postgres restore failed", "error", err)
				os.Exit(1)
			}
		} else {
			if err := store.Restore(cfg.DataDir, os.Args[2]); err != nil {
				log.Error("restore failed", "error", err)
				os.Exit(1)
			}
		}
		log.Info("restore complete", "provider", provider, "source", os.Args[2])
		return
	}
	var st *store.Store
	if cfg.DatabaseURL != "" {
		st, err = store.OpenPostgres(context.Background(), cfg.DatabaseURL)
	} else {
		st, err = store.Open(cfg.DataDir)
	}
	if err != nil {
		log.Error("database failed", "error", err)
		os.Exit(1)
	}
	defer st.Close()
	mode := "integrated"
	if len(os.Args) >= 2 && map[string]bool{"serve": true, "worker": true, "analytics-ingest": true}[os.Args[1]] {
		mode = os.Args[1]
	}
	mon := monitor.New(st)
	tlsSvc := tlshealth.New(st)
	dnsSvc := dnsobs.New(st)
	crawlSvc := crawler.New(st)
	var sched *scheduler.Scheduler
	if mode == "integrated" || mode == "worker" {
		sched = scheduler.New(st, mon, tlsSvc, dnsSvc, crawlSvc, cfg.CheckInterval, cfg.CrawlInterval, cfg.CheckConcurrency, log)
		sched.Start(context.Background())
		defer sched.Stop()
		// Webhook outbox delivery runs wherever background work runs, so a
		// slow webhook can never block an incident transition.
		nw := notifications.NewWorker(st, log)
		nw.Start(context.Background())
		defer nw.Stop()
	}
	if mode == "worker" {
		log.Info("webfleet worker started")
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		<-sig
		return
	}
	var srv *server.Server
	if mode == "analytics-ingest" {
		srv = server.NewAnalyticsIngest(cfg, st, log)
	} else {
		srv = server.New(cfg, st, log)
	}
	errc := make(chan error, 1)
	go func() {
		log.Info("webfleet starting", "listen", cfg.Listen, "data", cfg.DataDir, "mode", mode)
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
