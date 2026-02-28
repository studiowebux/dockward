package warden

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
)

// handleSSE serves the GET /events SSE stream.
// Authenticates via ?token= query parameter (EventSource cannot set headers).
// Replays the last 50 events on connect, then streams live events.
func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("token")), []byte(s.cfg.API.Token)) != 1 {
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
