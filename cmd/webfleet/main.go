package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
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
	"io"
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

// serviceCommand is the fully parsed `webfleet service <verb>` invocation.
type serviceCommand struct {
	verb     string
	data     string
	listen   string
	follow   bool
	artifact string
	sha      string
}

// serviceSuccessMessage returns the state-neutral success message for a
// completed service command. The lifecycle deliberately preserves prior state
// (a stopped service stays stopped across reinstall/update/rollback), so
// install/update/rollback must not claim the service is active or was
// restarted; only the verbs whose effect is inherently a state change
// (start/stop/restart/enable/disable) state it.
func serviceSuccessMessage(c serviceCommand) string {
	switch c.verb {
	case "install":
		return "webfleet.service installed."
	case "uninstall":
		return "webfleet.service uninstalled. Persistent data was preserved."
	case "start":
		return "webfleet.service started."
	case "stop":
		return "webfleet.service stopped."
	case "restart":
		return "webfleet.service restarted."
	case "enable":
		return "webfleet.service enabled at boot."
	case "disable":
		return "webfleet.service disabled at boot."
	case "update":
		return "webfleet.service updated."
	case "rollback":
		return "webfleet.service rolled back."
	default:
		return ""
	}
}

// execServiceCommand dispatches a parsed service command to the lifecycle,
// returning the success message (or status/logs body) to print. It is a
// variable so the CLI can be tested end-to-end without root or systemd.
var execServiceCommand = func(c serviceCommand) (string, error) {
	switch c.verb {
	case "install":
		if err := service.Install(service.Executable(), c.data, c.listen); err != nil {
			return "", err
		}
	case "uninstall":
		if err := service.Uninstall(); err != nil {
			return "", err
		}
	case "start":
		if err := service.Start(); err != nil {
			return "", err
		}
	case "stop":
		if err := service.Stop(); err != nil {
			return "", err
		}
	case "restart":
		if err := service.Restart(); err != nil {
			return "", err
		}
	case "enable":
		if err := service.Enable(); err != nil {
			return "", err
		}
	case "disable":
		if err := service.Disable(); err != nil {
			return "", err
		}
	case "status":
		var b bytes.Buffer
		if err := service.Status(&b); err != nil {
			return b.String(), err
		}
		return b.String(), nil
	case "logs":
		var b bytes.Buffer
		if err := service.Logs(c.follow, &b); err != nil {
			return b.String(), err
		}
		return b.String(), nil
	case "update":
		if err := service.Update(c.artifact, c.sha); err != nil {
			return "", err
		}
	case "rollback":
		if err := service.Rollback(); err != nil {
			return "", err
		}
	}
	return serviceSuccessMessage(c), nil
}

// parseServiceCommand deterministically parses `webfleet service <verb>` with
// command-local flags, preserving `--flag value` pairs that the previous
// separator-based parser could not (it stripped flag values into positionals).
// Exit-code model: 0 success, 1 operational failure, 2 usage error.
func parseServiceCommand(args []string, defaultListen string) (serviceCommand, error) {
	if len(args) == 0 {
		args = []string{"status"}
	}
	verb := args[0]
	rest := args[1:]
	cmd := serviceCommand{verb: verb}
	switch verb {
	case "install":
		fs := flag.NewFlagSet("install", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		data := fs.String("data", "", "data directory")
		listen := fs.String("listen", "", "listen address")
		if err := fs.Parse(rest); err != nil {
			return cmd, fmt.Errorf("install: %v", err)
		}
		if fs.NArg() != 0 {
			return cmd, fmt.Errorf("install takes no positional arguments: %s", strings.Join(fs.Args(), " "))
		}
		cmd.data = *data
		cmd.listen = *listen
		if cmd.data == "" {
			cmd.data = service.DefaultDataDir
		}
		if cmd.listen == "" {
			cmd.listen = defaultListen
		}
		return cmd, nil
	case "logs":
		fs := flag.NewFlagSet("logs", flag.ContinueOnError)
		fs.SetOutput(io.Discard)
		follow := fs.Bool("follow", false, "tail live journal output")
		if err := fs.Parse(rest); err != nil {
			return cmd, fmt.Errorf("logs: %v", err)
		}
		if fs.NArg() != 0 {
			return cmd, fmt.Errorf("logs takes no positional arguments: %s", strings.Join(fs.Args(), " "))
		}
		cmd.follow = *follow
		return cmd, nil
	case "update":
		for _, a := range rest {
			if strings.HasPrefix(a, "-") {
				return cmd, fmt.Errorf("update takes no flags: %s", a)
			}
		}
		if len(rest) != 2 {
			return cmd, fmt.Errorf("usage: webfleet service update ARTIFACT SHA256")
		}
		cmd.artifact = rest[0]
		cmd.sha = rest[1]
		return cmd, nil
	case "uninstall", "start", "stop", "restart", "enable", "disable", "status", "rollback":
		for _, a := range rest {
			if strings.HasPrefix(a, "-") {
				return cmd, fmt.Errorf("%s takes no flags: %s", verb, a)
			}
		}
		if len(rest) != 0 {
			return cmd, fmt.Errorf("%s takes no positional arguments: %s", verb, strings.Join(rest, " "))
		}
		return cmd, nil
	default:
		return cmd, fmt.Errorf("unknown service command %q\n\nUsage: webfleet service <install|uninstall|start|stop|restart|status|enable|disable|logs|update|rollback> [flags]", verb)
	}
}

// runServiceIO runs a parsed `webfleet service` command against the lifecycle
// seam and writes diagnostics to the supplied writers. It is the testable core
// of runService.
func runServiceIO(out, errOut io.Writer, args []string, defaultListen string) int {
	cmd, err := parseServiceCommand(args, defaultListen)
	if err != nil {
		fmt.Fprintf(errOut, "webfleet service: %v\n", err)
		return 2
	}
	output, err := execServiceCommand(cmd)
	if err != nil {
		fmt.Fprintf(errOut, "webfleet service %s: %v\n", cmd.verb, err)
		return 1
	}
	if output != "" {
		fmt.Fprintln(out, output)
	}
	return 0
}

// runService dispatches `webfleet service <command>` with the same CLI shape,
// exit-code model and diagnostics as the sibling projects (Cortex, Warden,
// Trestle, Watchpost), while operating the Web Fleet systemd **system** unit.
func runService(args []string, defaultListen string) int {
	return runServiceIO(os.Stdout, os.Stderr, args, defaultListen)
}
