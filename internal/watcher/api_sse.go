package watcher

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// handleUIStream serves SSE endpoint for data-star UI
func (a *API) handleUIStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// Subscribe to audit events
	ch := a.hub.Subscribe()
	defer a.hub.Unsubscribe(ch)

	// Send initial status
	snap := a.stateSnapshot(r.Context())
	services := make([]serviceStatus, 0, len(a.updater.cfg.Services))
	for _, svc := range a.updater.cfg.Services {
		services = append(services, a.buildServiceStatus(svc, snap))
	}

	status := map[string]interface{}{
		"services": services,
		"uptime":   a.metrics.Meta().UptimeSeconds,
	}

	if statusData, err := json.Marshal(status); err == nil {
		fmt.Fprintf(w, "event: status\ndata: %s\n\n", statusData)
		flusher.Flush()
	}

	// Create ticker for periodic updates
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	// Create heartbeat ticker
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	// Event loop
	for {
		select {
		case <-r.Context().Done():
			return

		case msg, ok := <-ch:
			if !ok {
				return
			}
			// Forward audit events
			fmt.Fprintf(w, "event: audit\ndata: %s\n\n", msg)
			flusher.Flush()

		case <-ticker.C:
			// Send periodic status update
			snap := a.stateSnapshot(context.Background())
			services := make([]serviceStatus, 0, len(a.updater.cfg.Services))
			for _, svc := range a.updater.cfg.Services {
				services = append(services, a.buildServiceStatus(svc, snap))
			}

			status := map[string]interface{}{
				"services": services,
				"uptime":   a.metrics.Meta().UptimeSeconds,
			}

			if statusData, err := json.Marshal(status); err == nil {
				fmt.Fprintf(w, "event: status\ndata: %s\n\n", statusData)
				flusher.Flush()
			}

		case <-heartbeat.C:
			// Keep connection alive
			fmt.Fprintf(w, ": heartbeat\n\n")
			flusher.Flush()
		}
	}
}