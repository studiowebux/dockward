package warden

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/studiowebux/dockward/internal/audit"
)

// testServer builds a minimal Server suitable for handler testing.
// It does not start the HTTP server or heartbeat goroutine.
func testServer() *Server {
	cfg := &WardenConfig{
		API: WardenAPI{Token: "warden-token"},
		Agents: []AgentConfig{
			{ID: "agent-1", URL: "http://agent1", Token: "agent-token-1"},
			{ID: "agent-2", URL: "http://agent2", Token: "agent-token-2"},
		},
	}
	return &Server{
		cfg:   cfg,
		store: NewStore(cfg.Agents),
		hub:   NewHub(),
	}
}

func TestHandleIngest_WrongMethod(t *testing.T) {
	s := testServer()
	r := httptest.NewRequest(http.MethodGet, "/ingest", nil)
	w := httptest.NewRecorder()

	s.handleIngest(w, r)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("want 405, got %d", w.Code)
	}
}

func TestHandleIngest_Auth(t *testing.T) {
	tests := []struct {
		name     string
		authHdr  string
		wantCode int
	}{
		{"missing token", "", http.StatusUnauthorized},
		{"wrong token", "Bearer bad-token", http.StatusUnauthorized},
		{"correct token agent-1", "Bearer agent-token-1", http.StatusNoContent},
		{"correct token agent-2", "Bearer agent-token-2", http.StatusNoContent},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := testServer()
			entry := audit.Entry{Service: "svc", Event: "deploy", Level: "info", Timestamp: time.Now().UTC()}
			body, _ := json.Marshal(entry)

			r := httptest.NewRequest(http.MethodPost, "/ingest", bytes.NewReader(body))
			if tc.authHdr != "" {
				r.Header.Set("Authorization", tc.authHdr)
			}
			w := httptest.NewRecorder()

			s.handleIngest(w, r)

			if w.Code != tc.wantCode {
				t.Errorf("want %d, got %d", tc.wantCode, w.Code)
			}
		})
	}
}

func TestHandleIngest_BadJSON(t *testing.T) {
	s := testServer()
	r := httptest.NewRequest(http.MethodPost, "/ingest", bytes.NewReader([]byte("not json")))
	r.Header.Set("Authorization", "Bearer agent-token-1")
	w := httptest.NewRecorder()

	s.handleIngest(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("want 400, got %d", w.Code)
	}
}

func TestHandleIngest_AppendsToStore(t *testing.T) {
	s := testServer()
	entry := audit.Entry{
		Service: "myapp",
		Event:   "deploy",
		Level:   "info",
		Message: "deployed v2",
	}
	body, _ := json.Marshal(entry)

	r := httptest.NewRequest(http.MethodPost, "/ingest", bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer agent-token-1")
	w := httptest.NewRecorder()

	s.handleIngest(w, r)

	if w.Code != http.StatusNoContent {
		t.Fatalf("want 204, got %d", w.Code)
	}

	stored := s.store.Recent(1)
	if len(stored) != 1 {
		t.Fatalf("want 1 entry in store, got %d", len(stored))
	}
	if stored[0].Service != "myapp" {
		t.Errorf("want service myapp, got %q", stored[0].Service)
	}
	if stored[0].Timestamp.IsZero() {
		t.Error("Timestamp should be set automatically")
	}
}

func TestHandleIngest_BroadcastsToSSEClients(t *testing.T) {
	s := testServer()
	ch := s.hub.Subscribe()
	defer s.hub.Unsubscribe(ch)

	entry := audit.Entry{Service: "svc", Event: "heal", Level: "warning", Message: "restarted"}
	body, _ := json.Marshal(entry)

	r := httptest.NewRequest(http.MethodPost, "/ingest", bytes.NewReader(body))
	r.Header.Set("Authorization", "Bearer agent-token-1")
	w := httptest.NewRecorder()

	s.handleIngest(w, r)

	select {
	case msg := <-ch:
		var got audit.Entry
		if err := json.Unmarshal(msg, &got); err != nil {
			t.Fatalf("broadcast message is not valid JSON: %v", err)
		}
		if got.Service != "svc" {
			t.Errorf("want service svc, got %q", got.Service)
		}
	default:
		t.Error("expected broadcast message in SSE channel")
	}
}
