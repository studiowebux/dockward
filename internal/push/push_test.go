package push

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/studiowebux/dockward/internal/audit"
)

func TestSend_SetsAuthHeader(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := New(srv.URL, "my-token", "ovh-01")
	err := c.Send(context.Background(), audit.Entry{Service: "svc", Event: "deploy", Level: "info"})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotAuth != "Bearer my-token" {
		t.Errorf("Authorization header: got %q, want %q", gotAuth, "Bearer my-token")
	}
}

func TestSend_SetsMachineID(t *testing.T) {
	var gotEntry audit.Entry
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&gotEntry)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := New(srv.URL, "tok", "my-machine")
	entry := audit.Entry{Service: "svc", Event: "heal", Level: "warning"}

	if err := c.Send(context.Background(), entry); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotEntry.Machine != "my-machine" {
		t.Errorf("Machine: got %q, want %q", gotEntry.Machine, "my-machine")
	}
}

func TestSend_PostsToIngestPath(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := New(srv.URL, "tok", "m1")
	_ = c.Send(context.Background(), audit.Entry{})

	if gotPath != "/ingest" {
		t.Errorf("want POST to /ingest, got %q", gotPath)
	}
}

func TestSend_ReturnsErrorOnNon2xx(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
	}{
		{"unauthorized", http.StatusUnauthorized},
		{"server error", http.StatusInternalServerError},
		{"bad request", http.StatusBadRequest},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.statusCode)
			}))
			defer srv.Close()

			c := New(srv.URL, "tok", "m1")
			err := c.Send(context.Background(), audit.Entry{})

			if err == nil {
				t.Errorf("want error for status %d, got nil", tc.statusCode)
			}
		})
	}
}

func TestSend_DoesNotMutateCallerEntry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	c := New(srv.URL, "tok", "server-01")
	original := audit.Entry{
		Service:   "svc",
		Machine:   "caller-set",
		Timestamp: time.Now(),
	}
	_ = c.Send(context.Background(), original)

	// Send receives a copy; caller's entry should be unchanged.
	if original.Machine != "caller-set" {
		t.Errorf("Send mutated caller's entry: Machine changed to %q", original.Machine)
	}
}
