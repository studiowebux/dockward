package watcher

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// SSE event types
const (
	SSEEventStatus = "status"
	SSEEventAudit  = "audit"
	SSEEventMetric = "metric"
)

// SSEMessage represents a server-sent event
type SSEMessage struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
}

// GET /ui/stream - optimized SSE endpoint for all updates
func (a *API) handleUIStream(w http.ResponseWriter, r *http.Request) {
	// Set SSE headers
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // Disable Nginx buffering

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// Create channels for different event types
	statusCh := make(chan []byte, 10)
	auditCh := make(chan []byte, 10)

	// Subscribe to audit events
	auditSub := a.hub.Subscribe()
	defer a.hub.Unsubscribe(auditSub)

	// Send initial status
	if status, err := a.getStatusJSON(r.Context()); err == nil {
		fmt.Fprintf(w, "event: status\ndata: %s\n\n", status)
		flusher.Flush()
	}

	// Status ticker - send full status every 30s
	statusTicker := time.NewTicker(30 * time.Second)
	defer statusTicker.Stop()

	// Heartbeat ticker - keep connection alive
	heartbeatTicker := time.NewTicker(15 * time.Second)
	defer heartbeatTicker.Stop()

	// Main event loop
	for {
		select {
		case <-r.Context().Done():
			return

		case msg := <-auditSub:
			// Forward audit events immediately
			fmt.Fprintf(w, "event: audit\ndata: %s\n\n", msg)
			flusher.Flush()

		case <-statusTicker.C:
			// Send periodic status updates
			if status, err := a.getStatusJSON(r.Context()); err == nil {
				fmt.Fprintf(w, "event: status\ndata: %s\n\n", status)
				flusher.Flush()
			}

		case <-heartbeatTicker.C:
			// Send heartbeat to keep connection alive
			fmt.Fprintf(w, ": heartbeat\n\n")
			flusher.Flush()
		}
	}
}

// getStatusJSON returns the current status as JSON
func (a *API) getStatusJSON(ctx context.Context) ([]byte, error) {
	snap := a.stateSnapshot(ctx)
	services := make([]serviceStatus, 0, len(a.updater.cfg.Services))

	for _, svc := range a.updater.cfg.Services {
		services = append(services, a.buildServiceStatus(svc, snap))
	}

	return json.Marshal(map[string]interface{}{
		"services": services,
		"uptime":   a.metrics.Meta().UptimeSeconds,
	})
}

// BroadcastStatusUpdate sends a status update to all SSE clients
func (a *API) BroadcastStatusUpdate(serviceName string) {
	// Find the specific service
	for _, svc := range a.updater.cfg.Services {
		if svc.Name == serviceName {
			snap := a.stateSnapshot(context.Background())
			status := a.buildServiceStatus(svc, snap)

			if data, err := json.Marshal(map[string]interface{}{
				"service": serviceName,
				"status":  status,
			}); err == nil {
				msg := SSEMessage{
					Event: SSEEventStatus,
					Data:  data,
				}
				if msgData, err := json.Marshal(msg); err == nil {
					a.hub.Broadcast(msgData)
				}
			}
			break
		}
	}
}