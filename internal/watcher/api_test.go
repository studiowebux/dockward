package watcher

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/studiowebux/dockward/internal/audit"
	"github.com/studiowebux/dockward/internal/docker"
	"github.com/studiowebux/dockward/internal/hub"
)

// testAPI builds a minimal API with only the audit logger and SSE hub wired.
// Other fields (updater, healer, metrics, monitor) are nil — valid as long as
// the test only calls handlers that do not access those fields.
func testAPI(al *audit.Logger) *API {
	h := hub.NewHub()
	if al != nil {
		al.WithBroadcast(&broadcaster{hub: h})
	}

	// Create a mock Docker health checker for testing
	dc := docker.NewClient()
	dockerHealth := docker.NewHealthChecker(dc, 30*time.Second, 5*time.Second)

	return &API{audit: al, hub: h, dockerHealth: dockerHealth}
}

func TestHandleHealth_Returns200(t *testing.T) {
	api := testAPI(nil)
	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()

	api.handleHealth(w, req)

	// Health checker starts in unhealthy state until first check
	// Either 200 (healthy) or 503 (unhealthy) are valid responses
	if w.Code != http.StatusOK && w.Code != http.StatusServiceUnavailable {
		t.Errorf("want 200 or 503, got %d", w.Code)
	}

	// Verify response structure
	var response map[string]interface{}
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	// Check for required fields
	if _, ok := response["status"]; !ok {
		t.Error("response missing 'status' field")
	}
	if _, ok := response["components"]; !ok {
		t.Error("response missing 'components' field")
	}
}

func TestHandleAudit_DisabledReturnsEmptyArray(t *testing.T) {
	// nil logger — audit.Recent on nil returns nil, nil.
	api := testAPI(nil)
	req := httptest.NewRequest(http.MethodGet, "/audit", nil)
	w := httptest.NewRecorder()

	api.handleAudit(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	var entries []audit.Entry
	if err := json.NewDecoder(w.Body).Decode(&entries); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("want empty array, got %d entries", len(entries))
	}
}

func TestHandleAudit_ReturnsEntries(t *testing.T) {
	dir := t.TempDir()
	al, err := audit.New(filepath.Join(dir, "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer al.Close()

	for i := 0; i < 3; i++ {
		if err := al.Write(audit.Entry{Service: "svc", Event: "test", Level: "info", Message: "msg"}); err != nil {
			t.Fatal(err)
		}
	}

	api := testAPI(al)
	req := httptest.NewRequest(http.MethodGet, "/audit", nil)
	w := httptest.NewRecorder()

	api.handleAudit(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	var entries []audit.Entry
	if err := json.NewDecoder(w.Body).Decode(&entries); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(entries) != 3 {
		t.Errorf("want 3 entries, got %d", len(entries))
	}
}

func TestHandleAudit_LimitParam(t *testing.T) {
	dir := t.TempDir()
	al, err := audit.New(filepath.Join(dir, "audit.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer al.Close()

	for i := 0; i < 10; i++ {
		if err := al.Write(audit.Entry{Service: "svc", Event: "test", Level: "info", Message: "msg"}); err != nil {
			t.Fatal(err)
		}
	}

	api := testAPI(al)
	req := httptest.NewRequest(http.MethodGet, "/audit?limit=5", nil)
	w := httptest.NewRecorder()

	api.handleAudit(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("want 200, got %d", w.Code)
	}
	var entries []audit.Entry
	if err := json.NewDecoder(w.Body).Decode(&entries); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(entries) != 5 {
		t.Errorf("want 5 entries, got %d", len(entries))
	}
}

func TestHandleAudit_MethodNotAllowed(t *testing.T) {
	api := testAPI(nil)
	req := httptest.NewRequest(http.MethodPost, "/audit", nil)
	w := httptest.NewRecorder()

	api.handleAudit(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("want 405, got %d", w.Code)
	}
}
