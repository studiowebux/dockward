package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/studiowebux/dockward/internal/audit"
	"github.com/studiowebux/dockward/internal/config"
	"github.com/studiowebux/dockward/internal/docker"
	"github.com/studiowebux/dockward/internal/logger"
	"github.com/studiowebux/dockward/internal/notify"
	"github.com/studiowebux/dockward/internal/push"
	"github.com/studiowebux/dockward/internal/registry"
	"github.com/studiowebux/dockward/internal/saferun"
	"github.com/studiowebux/dockward/internal/shutdown"
	"github.com/studiowebux/dockward/internal/warden"
	"github.com/studiowebux/dockward/internal/watcher"
	"github.com/studiowebux/dockward/internal/wizard"
)

// version is set at build time via ldflags: -X main.version=v0.1.0
var version = "dev"

func main() {
	// Initialize logger with syslog support
	logger.Init("dockward")
	defer logger.Close()

	// Subcommand: dockward config [--config <path>]
	// Interactive wizard to create or edit the agent config file.
	if len(os.Args) > 1 && os.Args[1] == "config" {
		fs := flag.NewFlagSet("config", flag.ExitOnError)
		configPath := fs.String("config", "/etc/dockward/config.json", "path to config file")
		if err := fs.Parse(os.Args[2:]); err != nil {
			logger.Fatalf("config: %v", err)
		}
		if err := wizard.Run(*configPath); err != nil {
			logger.Fatalf("config wizard: %v", err)
		}
		return
	}

	// Subcommand: dockward warden-config [--config <path>]
	// Interactive wizard to create or edit the warden config file.
	if len(os.Args) > 1 && os.Args[1] == "warden-config" {
		fs := flag.NewFlagSet("warden-config", flag.ExitOnError)
		configPath := fs.String("config", "/etc/dockward/warden.json", "path to warden config file")
		if err := fs.Parse(os.Args[2:]); err != nil {
			logger.Fatalf("warden-config: %v", err)
		}
		if err := wizard.RunWarden(*configPath); err != nil {
			logger.Fatalf("warden config wizard: %v", err)
		}
		return
	}

	configPath := flag.String("config", "/etc/dockward/config.json", "path to config file")
	mode := flag.String("mode", "agent", "operating mode: agent|warden")
	showVersion := flag.Bool("version", false, "print version and exit")
	verbose := flag.Bool("verbose", false, "enable debug-level logging")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	watcher.SetVerbose(*verbose)

	// Route to warden mode when requested.
	if *mode == "warden" {
		runWarden(*configPath)
		return
	}

	// Load configuration.
	cfg, err := config.Load(*configPath)
	if err != nil {
		logger.Fatalf("failed to load config: %v", err)
	}
	logger.Printf("loaded config: %d services, poll interval %ds", len(cfg.Services), cfg.Registry.PollInterval)

	// Build notifiers.
	dispatcher := buildDispatcher(cfg)

	// Create clients.
	dc := docker.NewClient()
	rc := registry.NewClient(cfg.Registry.URL, cfg.Registry.Insecure)

	// Create Docker health checker with configured intervals
	dockerHealth := docker.NewHealthChecker(
		dc,
		time.Duration(cfg.DockerHealth.CheckInterval)*time.Second,
		time.Duration(cfg.DockerHealth.Timeout)*time.Second,
	)

	// Create audit logger (no-op when path is empty).
	auditLog, err := audit.New(cfg.Audit.Path)
	if err != nil {
		logger.Fatalf("failed to open audit log: %v", err)
	}
	defer auditLog.Close()
	if cfg.Audit.Path != "" {
		logger.Printf("audit log: %s", cfg.Audit.Path)
	}

	// Attach push client if warden_url is configured.
	if cfg.Push.WardenURL != "" {
		pc := push.New(cfg.Push.WardenURL, cfg.Push.Token, cfg.Push.MachineID)
		auditLog.WithPush(pc)
		logger.Printf("push: forwarding audit entries to warden at %s (machine=%s)", cfg.Push.WardenURL, cfg.Push.MachineID)
	}

	// Create metrics, updater, healer, monitor, and API.
	metrics := watcher.NewMetrics()
	svcNames := make([]string, 0, len(cfg.Services))
	for _, svc := range cfg.Services {
		svcNames = append(svcNames, svc.Name)
	}
	metrics.SeedServices(svcNames)

	// Wire Docker health checker to update metrics
	dockerHealth.SetOnCheck(func(healthy bool, consecutiveFails int) {
		metrics.SetDockerHealth(healthy, consecutiveFails)
	})

	updater := watcher.NewUpdater(cfg, dc, rc, dispatcher, metrics, auditLog)
	healer := watcher.NewHealer(cfg, dc, dispatcher, updater, metrics, auditLog)
	monitor := watcher.NewMonitor(cfg, dc, dispatcher, auditLog)
	api := watcher.NewAPI(updater, healer, metrics, monitor, auditLog, dockerHealth, cfg.API.Address, cfg.API.Port)

	// Create shutdown coordinator and register managers
	coordinator := shutdown.NewCoordinator()
	coordinator.Register(updater)
	coordinator.Register(healer)
	coordinator.Register(monitor)
	coordinator.Register(api)
	coordinator.Register(auditLog)

	// Context with signal handling for graceful shutdown.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	saferun.Go("signal-handler", func() {
		sig := <-sigCh
		logger.Printf("received %s, starting graceful shutdown", sig)

		// Create a context for shutdown with timeout
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer shutdownCancel()

		// Perform graceful shutdown
		if err := coordinator.Shutdown(shutdownCtx); err != nil {
			logger.Printf("graceful shutdown failed: %v", err)
		}

		// Cancel the main context to stop all goroutines
		cancel()
	})

	// Start goroutines with panic recovery.
	saferun.RunWithRecovery("docker-health", ctx, dockerHealth.Start)
	saferun.RunWithRecovery("updater", ctx, updater.Run)
	saferun.RunWithRecovery("healer", ctx, healer.Run)
	saferun.RunWithRecovery("monitor", ctx, monitor.Run)
	saferun.RunWithRecovery("api", ctx, api.Run)

	logger.Printf("dockward %s started", version)

	// Send startup notification.
	dispatcher.Send(ctx, notify.Alert{
		Service: "dockward",
		Event:   "started",
		Message: "Dockward started.",
		Level:   notify.LevelInfo,
	})

	// Block until shutdown.
	<-ctx.Done()
	logger.Printf("stopped")
}

func runWarden(configPath string) {
	wcfg, err := warden.LoadWarden(configPath)
	if err != nil {
		logger.Fatalf("failed to load warden config: %v", err)
	}
	logger.Printf("warden mode: %d agent(s) configured, port %s", len(wcfg.Agents), wcfg.API.Port)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	saferun.Go("warden-signal-handler", func() {
		sig := <-sigCh
		logger.Printf("received %s, shutting down", sig)
		cancel()
	})

	srv := warden.NewServer(wcfg)
	srv.Run(ctx)
}

func buildDispatcher(cfg *config.Config) *notify.Dispatcher {
	var notifiers []notify.Notifier

	if cfg.Notifications.Discord != nil && cfg.Notifications.Discord.WebhookURL != "" {
		notifiers = append(notifiers, notify.NewDiscord(cfg.Notifications.Discord.WebhookURL))
		logger.Printf("notification: discord enabled")
	}

	if cfg.Notifications.SMTP != nil && cfg.Notifications.SMTP.Host != "" {
		s := cfg.Notifications.SMTP
		notifiers = append(notifiers, notify.NewSMTP(s.Host, s.Port, s.From, s.To, s.Username, s.Password))
		logger.Printf("notification: smtp enabled (%s -> %s)", s.From, s.To)
	}

	for _, wh := range cfg.Notifications.Webhooks {
		w, err := notify.NewWebhook(wh.Name, wh.URL, wh.Method, wh.Headers, wh.Body)
		if err != nil {
			logger.Fatalf("webhook %q: %v", wh.Name, err)
		}
		notifiers = append(notifiers, w)
		logger.Printf("notification: webhook %q enabled", wh.Name)
	}

	return notify.NewDispatcher(notifiers...)
}
