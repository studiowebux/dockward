package watcher

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/studiowebux/dockward/internal/config"
)

// API exposes HTTP endpoints for triggering updates, health, and metrics.
// Listens on localhost only.
type API struct {
	updater *Updater
	healer  *Healer
	metrics *Metrics
	server  *http.Server
}

// NewAPI creates the trigger/metrics API on the given port.
func NewAPI(updater *Updater, healer *Healer, metrics *Metrics, port string) *API {
	mux := http.NewServeMux()
	api := &API{
		updater: updater,
		healer:  healer,
		metrics: metrics,
		server: &http.Server{
			Addr:         "127.0.0.1:" + port,
			Handler:      mux,
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 30 * time.Second,
		},
	}

	mux.HandleFunc("/trigger", api.handleTriggerAll)
	mux.HandleFunc("/trigger/", api.handleTriggerService)
	mux.HandleFunc("/blocked", api.handleListBlocked)
	mux.HandleFunc("/blocked/", api.handleUnblockService)
	mux.HandleFunc("/not-found", api.handleListNotFound)
	mux.HandleFunc("/errored", api.handleListErrored)
	mux.HandleFunc("/status", api.handleStatusAll)
	mux.HandleFunc("/status/", api.handleStatusService)
	mux.HandleFunc("/health", api.handleHealth)
	mux.HandleFunc("/metrics", api.handleMetrics)

	return api
}

// Run starts the HTTP server. Blocks until ctx is cancelled.
func (a *API) Run(ctx context.Context) {
	go func() {
		<-ctx.Done()
		if err := a.server.Close(); err != nil {
			log.Printf("[api] server close error: %v", err)
		}
	}()

	log.Printf("[api] listening on %s", a.server.Addr)
	if err := a.server.ListenAndServe(); err != http.ErrServerClosed {
		log.Printf("[api] server error: %v", err)
	}
}

// POST /trigger - trigger update check for all services
func (a *API) handleTriggerAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	log.Printf("[api] manual trigger: all services")
	go a.updater.pollAll(context.Background())

	writeJSON(w, map[string]string{"status": "triggered", "scope": "all"})
}

// POST /trigger/<service> - trigger update check for a specific service
func (a *API) handleTriggerService(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	serviceName := strings.TrimPrefix(r.URL.Path, "/trigger/")
	if serviceName == "" {
		http.Error(w, "service name required", http.StatusBadRequest)
		return
	}

	found := false
	for _, svc := range a.updater.cfg.Services {
		if svc.Name == serviceName {
			if !svc.AutoUpdate {
				writeJSON(w, map[string]string{"status": "skipped", "reason": "auto_update is false"})
				return
			}
			if a.updater.IsDeploying(serviceName) {
				writeJSON(w, map[string]string{"status": "skipped", "reason": "deploy in progress"})
				return
			}
			found = true
			log.Printf("[api] manual trigger: %s", svc.Name)
			go func() {
				ctx := context.Background()
				if err := a.updater.checkAndUpdate(ctx, svc); err != nil {
					a.updater.handlePollError(ctx, svc, err)
				}
			}()
			break
		}
	}

	if !found {
		http.Error(w, "service not found", http.StatusNotFound)
		return
	}

	writeJSON(w, map[string]string{"status": "triggered", "scope": serviceName})
}

// GET /blocked - list blocked service digests
func (a *API) handleListBlocked(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, a.updater.BlockedDigests())
}

// GET /not-found - list services with unresolvable local digests
func (a *API) handleListNotFound(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, a.updater.NotFoundServices())
}

// GET /errored - list services with persistent poll errors
func (a *API) handleListErrored(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, a.updater.ErroredServices())
}

// serviceStatus is the per-service state returned by /status.
type serviceStatus struct {
	Name       string `json:"name"`
	AutoUpdate bool   `json:"auto_update"`
	AutoStart  bool   `json:"auto_start"`
	AutoHeal   bool   `json:"auto_heal"`
	Healthy    *bool  `json:"healthy,omitempty"`
	Deploying  bool   `json:"deploying"`
	Blocked    string `json:"blocked,omitempty"`
	NotFound   string `json:"not_found,omitempty"`
	Errored    string `json:"errored,omitempty"`
	Degraded   bool   `json:"degraded"`
	Exhausted  bool   `json:"exhausted"`
	Restarts   int    `json:"restart_failures"`
}

// GET /status - aggregated state for all configured services
func (a *API) handleStatusAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	snap := a.stateSnapshot()
	services := make([]serviceStatus, 0, len(a.updater.cfg.Services))
	for _, svc := range a.updater.cfg.Services {
		services = append(services, a.buildServiceStatus(svc, snap))
	}

	writeJSON(w, services)
}

// GET /status/<name> - aggregated state for a single service
func (a *API) handleStatusService(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	serviceName := strings.TrimPrefix(r.URL.Path, "/status/")
	if serviceName == "" {
		http.Error(w, "service name required", http.StatusBadRequest)
		return
	}

	snap := a.stateSnapshot()
	for _, svc := range a.updater.cfg.Services {
		if svc.Name == serviceName {
			writeJSON(w, a.buildServiceStatus(svc, snap))
			return
		}
	}

	http.Error(w, "service not found", http.StatusNotFound)
}

// stateSnap holds a point-in-time snapshot of all state maps.
type stateSnap struct {
	blocked       map[string]string
	notFound      map[string]string
	errored       map[string]string
	degraded      map[string]bool
	exhausted     map[string]bool
	restartCounts map[string]int
	healthGauges  map[string]bool
}

func (a *API) stateSnapshot() stateSnap {
	return stateSnap{
		blocked:       a.updater.BlockedDigests(),
		notFound:      a.updater.NotFoundServices(),
		errored:       a.updater.ErroredServices(),
		degraded:      a.healer.DegradedServices(),
		exhausted:     a.healer.ExhaustedServices(),
		restartCounts: a.healer.RestartCounts(),
		healthGauges:  a.metrics.HealthSnapshot(),
	}
}

func (a *API) buildServiceStatus(svc config.Service, snap stateSnap) serviceStatus {
	s := serviceStatus{
		Name:       svc.Name,
		AutoUpdate: svc.AutoUpdate,
		AutoStart:  svc.AutoStart,
		AutoHeal:   svc.AutoHeal,
		Deploying:  a.updater.IsDeploying(svc.Name),
		Blocked:    snap.blocked[svc.Name],
		NotFound:   snap.notFound[svc.Name],
		Errored:    snap.errored[svc.Name],
		Degraded:   snap.degraded[svc.Name],
		Exhausted:  snap.exhausted[svc.Name],
		Restarts:   snap.restartCounts[svc.Name],
	}
	if h, ok := snap.healthGauges[svc.Name]; ok {
		s.Healthy = &h
	}
	return s
}

// DELETE /blocked/<service> - unblock a service
func (a *API) handleUnblockService(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	serviceName := strings.TrimPrefix(r.URL.Path, "/blocked/")
	if serviceName == "" {
		http.Error(w, "service name required", http.StatusBadRequest)
		return
	}

	if a.updater.UnblockService(serviceName) {
		writeJSON(w, map[string]string{"status": "unblocked", "service": serviceName})
	} else {
		writeJSON(w, map[string]string{"status": "not_blocked", "service": serviceName})
	}
}

// GET /health - watcher health check
func (a *API) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]string{"status": "ok"})
}

// GET /metrics - Prometheus-compatible metrics
func (a *API) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	if _, err := w.Write([]byte(a.metrics.Prometheus())); err != nil {
		log.Printf("[api] metrics write error: %v", err)
	}
}

func writeJSON(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Printf("[api] json encode error: %v", err)
	}
}
