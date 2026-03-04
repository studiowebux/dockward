package watcher

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/studiowebux/dockward/internal/hub"
	"github.com/studiowebux/dockward/internal/logger"
)

// sendStatus marshals and writes a status SSE event to the client.
func (a *API) sendStatus(w http.ResponseWriter, flusher http.Flusher) {
	snap := a.stateSnapshot(context.Background())
	services := make([]serviceStatus, 0, len(a.updater.cfg.Services))
	for _, svc := range a.updater.cfg.Services {
		services = append(services, a.buildServiceStatus(svc, snap))
	}

	status := map[string]interface{}{
		"services":       services,
		"uptime_seconds": a.metrics.Meta().UptimeSeconds,
	}

	if statusData, err := json.Marshal(status); err == nil {
		fmt.Fprintf(w, "event: status\ndata: %s\n\n", statusData)
		flusher.Flush()
	}
}

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

	// Clear the server-level WriteTimeout so this long-lived connection is not
	// killed after 30 s. Uses ResponseController (Go 1.20+).
	rc := http.NewResponseController(w)
	if err := rc.SetWriteDeadline(time.Time{}); err != nil {
		logger.Printf("[api] ui/stream: could not clear write deadline: %v", err)
	}

	// Extract client IP for connection limiting
	clientIP := hub.ExtractClientIP(r)

	// Subscribe to audit events with connection limiting
	ch, err := a.hub.Subscribe(clientIP)
	if err != nil {
		if err == hub.ErrTooManyConnections {
			http.Error(w, "too many connections", http.StatusTooManyRequests)
		} else {
			http.Error(w, "subscription failed", http.StatusInternalServerError)
		}
		return
	}
	defer a.hub.Unsubscribe(ch)

	// Subscribe to status-refresh notifications
	statusCh := a.subscribeStatus()
	defer a.unsubscribeStatus(statusCh)

	// Send initial status
	a.sendStatus(w, flusher)

	// Send recent audit events so the UI is populated on refresh (F5)
	if recent, err := a.audit.Recent(50); err == nil && len(recent) > 0 {
		for _, entry := range recent {
			if data, err := json.Marshal(entry); err == nil {
				fmt.Fprintf(w, "event: audit\ndata: %s\n\n", data)
			}
		}
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

		case <-statusCh:
			// State changed — push fresh status immediately
			a.sendStatus(w, flusher)

		case <-ticker.C:
			// Periodic status refresh (fallback)
			a.sendStatus(w, flusher)

		case <-heartbeat.C:
			// Keep connection alive
			fmt.Fprintf(w, ": heartbeat\n\n")
			flusher.Flush()
		}
	}
}