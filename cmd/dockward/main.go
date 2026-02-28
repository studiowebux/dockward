package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/studiowebux/dockward/internal/audit"
	"github.com/studiowebux/dockward/internal/config"
	"github.com/studiowebux/dockward/internal/docker"
	"github.com/studiowebux/dockward/internal/notify"
	"github.com/studiowebux/dockward/internal/registry"
	"github.com/studiowebux/dockward/internal/watcher"
	"github.com/studiowebux/dockward/internal/wizard"
)

// version is set at build time via ldflags: -X main.version=v0.1.0
var version = "dev"

func main() {
	// Subcommand: dockward config [--config <path>]
	// Interactive wizard to create or edit the config file.
	if len(os.Args) > 1 && os.Args[1] == "config" {
		fs := flag.NewFlagSet("config", flag.ExitOnError)
		configPath := fs.String("config", "/etc/dockward/config.json", "path to config file")
		if err := fs.Parse(os.Args[2:]); err != nil {
			log.Fatalf("config: %v", err)
		}
		if err := wizard.Run(*configPath); err != nil {
			log.Fatalf("config wizard: %v", err)
		}
		return
	}

	configPath := flag.String("config", "/etc/dockward/config.json", "path to config file")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	log.SetFlags(log.LstdFlags | log.Lmsgprefix)
	log.SetPrefix("[dockward] ")

	// Load configuration.
	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("failed to load config: %v", err)
	}
	log.Printf("loaded config: %d services, poll interval %ds", len(cfg.Services), cfg.Registry.PollInterval)

	// Build notifiers.
	dispatcher := buildDispatcher(cfg)

	// Create clients.
	dc := docker.NewClient()
	rc := registry.NewClient(cfg.Registry.URL)

	// Create audit logger (no-op when path is empty).
	auditLog, err := audit.New(cfg.Audit.Path)
	if err != nil {
		log.Fatalf("failed to open audit log: %v", err)
	}
	defer auditLog.Close()
	if cfg.Audit.Path != "" {
		log.Printf("audit log: %s", cfg.Audit.Path)
	}

	// Create metrics, updater, healer, monitor, and API.
	metrics := watcher.NewMetrics()
	updater := watcher.NewUpdater(cfg, dc, rc, dispatcher, metrics, auditLog)
	healer := watcher.NewHealer(cfg, dc, dispatcher, updater, metrics, auditLog)
	monitor := watcher.NewMonitor(cfg, dc, dispatcher, auditLog)
	api := watcher.NewAPI(updater, healer, metrics, monitor, auditLog, cfg.API.Port)

	// Context with signal handling for graceful shutdown.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		sig := <-sigCh
		log.Printf("received %s, shutting down", sig)
		cancel()
	}()

	// Start goroutines.
	go updater.Run(ctx)
	go healer.Run(ctx)
	go monitor.Run(ctx)
	go api.Run(ctx)

	log.Printf("dockward %s started", version)

	// Send startup notification.
	dispatcher.Send(ctx, notify.Alert{
		Service: "dockward",
		Event:   "started",
		Message: "Dockward started.",
		Level:   notify.LevelInfo,
	})

	// Block until shutdown.
	<-ctx.Done()
	log.Printf("stopped")
}

func buildDispatcher(cfg *config.Config) *notify.Dispatcher {
	var notifiers []notify.Notifier

	if cfg.Notifications.Discord != nil && cfg.Notifications.Discord.WebhookURL != "" {
		notifiers = append(notifiers, notify.NewDiscord(cfg.Notifications.Discord.WebhookURL))
		log.Printf("notification: discord enabled")
	}

	if cfg.Notifications.SMTP != nil && cfg.Notifications.SMTP.Host != "" {
		s := cfg.Notifications.SMTP
		notifiers = append(notifiers, notify.NewSMTP(s.Host, s.Port, s.From, s.To, s.Username, s.Password))
		log.Printf("notification: smtp enabled (%s -> %s)", s.From, s.To)
	}

	for _, wh := range cfg.Notifications.Webhooks {
		w, err := notify.NewWebhook(wh.Name, wh.URL, wh.Method, wh.Headers, wh.Body)
		if err != nil {
			log.Fatalf("webhook %q: %v", wh.Name, err)
		}
		notifiers = append(notifiers, w)
		log.Printf("notification: webhook %q enabled", wh.Name)
	}

	return notify.NewDispatcher(notifiers...)
}
