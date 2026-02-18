package watcher

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"strings"
	"time"
)

// API exposes HTTP endpoints for triggering updates, health, and metrics.
// Listens on localhost only.
type API struct {
	updater *Updater
	metrics *Metrics
	server  *http.Server
}

// NewAPI creates the trigger/metrics API on the given port.
func NewAPI(updater *Updater, metrics *Metrics, port string) *API {
	mux := http.NewServeMux()
	api := &API{
		updater: updater,
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
				if err := a.updater.checkAndUpdate(context.Background(), svc); err != nil {
					log.Printf("[api] trigger %s error: %v", svc.Name, err)
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
