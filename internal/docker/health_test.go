package docker

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// testTransport wraps a test server and intercepts requests to redirect them.
type testTransport struct {
	server *httptest.Server
}

func (t *testTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	// Redirect all requests to the test server
	req.URL.Scheme = "http"
	req.URL.Host = strings.TrimPrefix(t.server.URL, "http://")
	return t.server.Client().Transport.RoundTrip(req)
}

// newTestClient creates a Docker client that uses a test HTTP server.
func newTestClient(server *httptest.Server) *Client {
	return &Client{
		http: &http.Client{
			Transport: &testTransport{server: server},
			Timeout:   5 * time.Second,
		},
	}
}

func TestHealthChecker_Ping_Success(t *testing.T) {
	// Mock Docker daemon that responds successfully
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/_ping") {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("API-Version", "1.45")
		w.Header().Set("Server", "Docker/24.0.7 (linux)")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := newTestClient(server)
	hc := NewHealthChecker(client, 1*time.Second, 1*time.Second)

	ctx := context.Background()
	err := hc.ping(ctx)

	if err != nil {
		t.Errorf("ping failed: %v", err)
	}

	status := hc.Status()
	if status.APIVersion != "1.45" {
		t.Errorf("expected API version 1.45, got %s", status.APIVersion)
	}
	if status.DockerVersion != "Docker/24.0.7 (linux)" {
		t.Errorf("expected Docker version 'Docker/24.0.7 (linux)', got %s", status.DockerVersion)
	}
}

func TestHealthChecker_Ping_Failure(t *testing.T) {
	// Mock Docker daemon that returns error
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := newTestClient(server)
	hc := NewHealthChecker(client, 1*time.Second, 1*time.Second)

	ctx := context.Background()
	err := hc.ping(ctx)

	if err == nil {
		t.Error("expected error from failed ping, got nil")
	}

	expectedMsg := "ping returned HTTP 500"
	if !strings.Contains(err.Error(), expectedMsg) {
		t.Errorf("expected error to contain %q, got %q", expectedMsg, err.Error())
	}
}

func TestHealthChecker_ConsecutiveFailures(t *testing.T) {
	failCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		failCount++
		if failCount <= 3 {
			w.WriteHeader(http.StatusInternalServerError)
		} else {
			w.Header().Set("API-Version", "1.45")
			w.WriteHeader(http.StatusOK)
		}
	}))
	defer server.Close()

	client := newTestClient(server)
	hc := NewHealthChecker(client, 1*time.Second, 1*time.Second)

	ctx := context.Background()

	// First 3 checks should fail
	for i := 0; i < 3; i++ {
		hc.check(ctx)
		status := hc.Status()
		if status.Healthy {
			t.Errorf("check %d: expected unhealthy, got healthy", i+1)
		}
		if status.ConsecutiveFails != i+1 {
			t.Errorf("check %d: expected %d consecutive fails, got %d", i+1, i+1, status.ConsecutiveFails)
		}
	}

	// 4th check should succeed and reset counter
	hc.check(ctx)
	status := hc.Status()
	if !status.Healthy {
		t.Error("expected healthy after recovery")
	}
	if status.ConsecutiveFails != 0 {
		t.Errorf("expected 0 consecutive fails after recovery, got %d", status.ConsecutiveFails)
	}
}

func TestHealthChecker_Timeout(t *testing.T) {
	// Mock server that delays response beyond timeout
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	// Create client with short timeout
	client := &Client{
		http: &http.Client{
			Transport: &testTransport{server: server},
			Timeout:   100 * time.Millisecond,
		},
	}

	hc := NewHealthChecker(client, 1*time.Second, 100*time.Millisecond)

	ctx := context.Background()
	err := hc.ping(ctx)

	if err == nil {
		t.Error("expected timeout error, got nil")
	}

	// Both context deadline and client timeout are acceptable
	errStr := err.Error()
	if !strings.Contains(errStr, "context deadline exceeded") && !strings.Contains(errStr, "Client.Timeout") {
		t.Errorf("expected timeout error, got: %v", err)
	}
}

func TestHealthChecker_IsHealthy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := newTestClient(server)
	hc := NewHealthChecker(client, 1*time.Second, 1*time.Second)

	// Initially unhealthy
	if hc.IsHealthy() {
		t.Error("expected unhealthy before first check")
	}

	// After successful check, should be healthy
	ctx := context.Background()
	hc.check(ctx)

	if !hc.IsHealthy() {
		t.Error("expected healthy after successful check")
	}
}

func TestHealthChecker_DefaultIntervals(t *testing.T) {
	client := &Client{http: &http.Client{}}

	// Test with zero intervals (should use defaults)
	hc := NewHealthChecker(client, 0, 0)

	if hc.checkInterval != 30*time.Second {
		t.Errorf("expected default check interval 30s, got %v", hc.checkInterval)
	}
	if hc.timeout != 5*time.Second {
		t.Errorf("expected default timeout 5s, got %v", hc.timeout)
	}
}

func TestHealthChecker_Status_ReturnsSnapshot(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("API-Version", "1.45")
		w.Header().Set("Server", "Docker/24.0.7")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := newTestClient(server)
	hc := NewHealthChecker(client, 1*time.Second, 1*time.Second)

	ctx := context.Background()
	hc.check(ctx)

	status := hc.Status()

	if !status.Healthy {
		t.Error("expected healthy status")
	}
	if status.LastCheck.IsZero() {
		t.Error("expected LastCheck to be set")
	}
	if status.LastHealthyCheck.IsZero() {
		t.Error("expected LastHealthyCheck to be set")
	}
	if status.ConsecutiveFails != 0 {
		t.Errorf("expected 0 consecutive fails, got %d", status.ConsecutiveFails)
	}
	if status.APIVersion != "1.45" {
		t.Errorf("expected API version 1.45, got %s", status.APIVersion)
	}
	if status.DockerVersion != "Docker/24.0.7" {
		t.Errorf("expected Docker version Docker/24.0.7, got %s", status.DockerVersion)
	}
	if status.LastError != "" {
		t.Errorf("expected no error, got %s", status.LastError)
	}
}

func TestHealthChecker_Status_IncludesError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client := newTestClient(server)
	hc := NewHealthChecker(client, 1*time.Second, 1*time.Second)

	ctx := context.Background()
	hc.check(ctx)

	status := hc.Status()

	if status.Healthy {
		t.Error("expected unhealthy status")
	}
	if status.LastError == "" {
		t.Error("expected error message to be populated")
	}
}

func TestHealthChecker_ConcurrentStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(10 * time.Millisecond) // Small delay to create concurrency
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client := newTestClient(server)
	hc := NewHealthChecker(client, 1*time.Second, 1*time.Second)

	ctx := context.Background()

	// Run checks and status reads concurrently to verify thread safety
	done := make(chan bool)

	// Goroutine 1: Perform checks
	go func() {
		for i := 0; i < 10; i++ {
			hc.check(ctx)
			time.Sleep(5 * time.Millisecond)
		}
		done <- true
	}()

	// Goroutine 2: Read status
	go func() {
		for i := 0; i < 20; i++ {
			_ = hc.Status()
			_ = hc.IsHealthy()
			time.Sleep(2 * time.Millisecond)
		}
		done <- true
	}()

	// Wait for both goroutines
	<-done
	<-done

	// If we get here without panic, concurrent access is safe
	fmt.Println("Concurrent access test passed")
}
