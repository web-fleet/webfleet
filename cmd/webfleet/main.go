package main

import (
	"context"
	"errors"
	"fmt"
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
	"strings"
	"syscall"
	"time"
)

// version is overridden at release build time via -ldflags -X main.version.
var version = "dev"

func main() {
	// Service-management commands must remain usable even when the application
	// configuration is unhealthy, so dispatch before any runtime config load.
	if len(os.Args) >= 2 && os.Args[1] == "service" {
		os.Exit(runService(os.Args[2:], service.DefaultListen))
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg, err := config.Load()
	if err != nil {
		log.Error("configuration failed", "error", err)
		os.Exit(1)
	}
	log.Info("webfleet version", "version", version)
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

// runService dispatches `webfleet service <command>` with the same CLI shape,
// exit-code model and diagnostics as the sibling projects (Cortex, Warden,
// Trestle, Watchpost), while operating the Web Fleet systemd **system** unit.
// Exit codes: 0 success, 1 operational failure, 2 usage error.
func runService(args []string, defaultListen string) int {
	cmd := "status"
	var flags, positional []string
	for _, a := range args {
		if a != "" && !strings.HasPrefix(a, "-") {
			if cmd == "status" && len(positional) == 0 {
				cmd = a
				continue
			}
			positional = append(positional, a)
			continue
		}
		flags = append(flags, a)
	}
	usage := func(msg string) int {
		fmt.Fprintf(os.Stderr, "webfleet service %s: %s\n", cmd, msg)
		return 2
	}
	if len(flags) > 0 {
		switch cmd {
		case "install":
			for i := 0; i < len(flags); i++ {
				switch flags[i] {
				case "--data":
					if i+1 < len(flags) {
						i++
					} else {
						return usage("--data requires a path")
					}
				case "--listen":
					if i+1 < len(flags) {
						i++
					} else {
						return usage("--listen requires an address")
					}
				default:
					return usage("unknown flag " + flags[i])
				}
			}
		case "logs":
			if len(flags) > 1 || flags[0] != "--follow" {
				return usage("logs accepts only --follow")
			}
		default:
			return usage("no flags are accepted for " + cmd)
		}
	}
	switch cmd {
	case "install":
		if len(positional) != 0 {
			return usage("install takes no positional arguments")
		}
		data, listen := service.DefaultDataDir, defaultListen
		for i := 0; i < len(flags); i++ {
			switch flags[i] {
			case "--data":
				if i+1 < len(flags) {
					i++
					data = flags[i]
				}
			case "--listen":
				if i+1 < len(flags) {
					i++
					listen = flags[i]
				}
			}
		}
		if err := service.Install(service.Executable(), data, listen); err != nil {
			fmt.Fprintln(os.Stderr, "webfleet service install:", err)
			return 1
		}
		fmt.Fprintln(os.Stdout, "webfleet.service installed and active.")
		return 0
	case "uninstall":
		if len(positional) != 0 {
			return usage("uninstall takes no positional arguments")
		}
		if err := service.Uninstall(); err != nil {
			fmt.Fprintln(os.Stderr, "webfleet service uninstall:", err)
			return 1
		}
		fmt.Fprintln(os.Stdout, "webfleet.service uninstalled. Data in "+service.DefaultDataDir+" was preserved.")
		return 0
	case "start":
		if err := checkNoArgs(positional, cmd); err != "" {
			return usage(err)
		}
		if err := service.Start(); err != nil {
			fmt.Fprintln(os.Stderr, "webfleet service start:", err)
			return 1
		}
		fmt.Fprintln(os.Stdout, "webfleet.service started.")
		return 0
	case "stop":
		if err := checkNoArgs(positional, cmd); err != "" {
			return usage(err)
		}
		if err := service.Stop(); err != nil {
			fmt.Fprintln(os.Stderr, "webfleet service stop:", err)
			return 1
		}
		fmt.Fprintln(os.Stdout, "webfleet.service stopped.")
		return 0
	case "restart":
		if err := checkNoArgs(positional, cmd); err != "" {
			return usage(err)
		}
		if err := service.Restart(); err != nil {
			fmt.Fprintln(os.Stderr, "webfleet service restart:", err)
			return 1
		}
		fmt.Fprintln(os.Stdout, "webfleet.service restarted.")
		return 0
	case "enable":
		if err := checkNoArgs(positional, cmd); err != "" {
			return usage(err)
		}
		if err := service.Enable(); err != nil {
			fmt.Fprintln(os.Stderr, "webfleet service enable:", err)
			return 1
		}
		fmt.Fprintln(os.Stdout, "webfleet.service enabled at boot.")
		return 0
	case "disable":
		if err := checkNoArgs(positional, cmd); err != "" {
			return usage(err)
		}
		if err := service.Disable(); err != nil {
			fmt.Fprintln(os.Stderr, "webfleet service disable:", err)
			return 1
		}
		fmt.Fprintln(os.Stdout, "webfleet.service disabled at boot.")
		return 0
	case "status":
		if err := checkNoArgs(positional, cmd); err != "" {
			return usage(err)
		}
		if err := service.Status(os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "webfleet service status:", err)
			return 1
		}
		return 0
	case "logs":
		if err := checkNoArgs(positional, cmd); err != "" {
			return usage(err)
		}
		follow := len(flags) > 0 && flags[0] == "--follow"
		if err := service.Logs(follow, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, "webfleet service logs:", err)
			return 1
		}
		return 0
	case "update":
		if len(positional) != 2 {
			return usage("usage: webfleet service update ARTIFACT SHA256")
		}
		if err := service.Update(positional[0], positional[1]); err != nil {
			fmt.Fprintln(os.Stderr, "webfleet service update:", err)
			return 1
		}
		fmt.Fprintln(os.Stdout, "webfleet.service updated and restarted.")
		return 0
	case "rollback":
		if err := checkNoArgs(positional, cmd); err != "" {
			return usage(err)
		}
		if err := service.Rollback(); err != nil {
			fmt.Fprintln(os.Stderr, "webfleet service rollback:", err)
			return 1
		}
		fmt.Fprintln(os.Stdout, "webfleet.service rolled back and restarted.")
		return 0
	default:
		fmt.Fprintf(os.Stderr, "webfleet: unknown service command %q\n\nUsage: webfleet service <install|uninstall|start|stop|restart|status|enable|disable|logs|update|rollback> [flags]\n", cmd)
		return 2
	}
}

func checkNoArgs(positional []string, cmd string) string {
	if len(positional) != 0 {
		return fmt.Sprintf("%s takes no positional arguments", cmd)
	}
	return ""
}
