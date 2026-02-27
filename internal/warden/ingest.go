package warden

import (
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/studiowebux/dockward/internal/audit"
)

// handleIngest handles POST /ingest.
// Authenticates via Bearer token matched against agents[].token.
// On success: appends to store, broadcasts to SSE clients, returns 204.
func (s *Server) handleIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	token := bearerToken(r)
	if !s.validAgentToken(token) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20)) // 1 MB limit
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}

	var entry audit.Entry
	if err := json.Unmarshal(body, &entry); err != nil {
		http.Error(w, "bad request: invalid JSON", http.StatusBadRequest)
		return
	}

	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now().UTC()
	}

	s.store.Append(entry)

	data, err := json.Marshal(entry)
	if err != nil {
		log.Printf("warden: marshal broadcast entry: %v", err)
	} else {
		s.hub.Broadcast(data)
	}

	w.WriteHeader(http.StatusNoContent)
}

// bearerToken extracts the token from "Authorization: Bearer <token>".
func bearerToken(r *http.Request) string {
	hdr := r.Header.Get("Authorization")
	if !strings.HasPrefix(hdr, "Bearer ") {
		return ""
	}
	return strings.TrimPrefix(hdr, "Bearer ")
}

// validAgentToken reports whether token matches any configured agent token.
func (s *Server) validAgentToken(token string) bool {
	if token == "" {
		return false
	}
	for _, a := range s.cfg.Agents {
		if a.Token == token {
			return true
		}
	}
	return false
}
