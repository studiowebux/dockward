package warden

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"sync"
)

// Hub manages SSE client subscriptions and broadcasts events to all clients.
// Pattern: publish-subscribe hub (fan-out broadcaster).
type Hub struct {
	mu      sync.Mutex
	clients map[chan []byte]struct{}
}

// NewHub creates an empty Hub.
func NewHub() *Hub {
	return &Hub{
		clients: make(map[chan []byte]struct{}),
	}
}

// Subscribe creates and registers a new event channel for a client.
func (h *Hub) Subscribe() chan []byte {
	ch := make(chan []byte, 64)
	h.mu.Lock()
	h.clients[ch] = struct{}{}
	h.mu.Unlock()
	return ch
}

// Unsubscribe removes the channel from the hub and closes it.
func (h *Hub) Unsubscribe(ch chan []byte) {
	h.mu.Lock()
	delete(h.clients, ch)
	h.mu.Unlock()
	close(ch)
}

// Broadcast sends data to all subscribed clients.
// Drops the message for a client whose channel is full (non-blocking).
func (h *Hub) Broadcast(data []byte) {
	h.mu.Lock()
	defer h.mu.Unlock()

	for ch := range h.clients {
		select {
		case ch <- data:
		default:
			// Slow client: drop message rather than block.
		}
	}
}

// handleSSE serves the GET /events SSE stream.
// Authenticates via ?token= query parameter (EventSource cannot set headers).
// Replays the last 50 events on connect, then streams live events.
func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if r.URL.Query().Get("token") != s.cfg.API.Token {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable nginx buffering

	// Replay recent events so the browser gets immediate content.
	recent := s.store.Recent(50)
	for i := len(recent) - 1; i >= 0; i-- { // oldest first
		data, err := json.Marshal(recent[i])
		if err != nil {
			continue
		}
		fmt.Fprintf(w, "data: %s\n\n", data)
	}
	flusher.Flush()

	ch := s.hub.Subscribe()
	defer s.hub.Unsubscribe(ch)

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			_, err := fmt.Fprintf(w, "data: %s\n\n", msg)
			if err != nil {
				log.Printf("warden: SSE write error: %v", err)
				return
			}
			flusher.Flush()
		}
	}
}
