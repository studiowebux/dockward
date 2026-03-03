package docker

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// HealthChecker periodically checks Docker daemon connectivity and tracks health status.
type HealthChecker struct {
	client        *Client
	checkInterval time.Duration
	timeout       time.Duration
	onCheck       func(healthy bool, consecutiveFails int) // Optional callback after each check

	mu                sync.RWMutex
	healthy           bool
	lastCheck         time.Time
	lastError         error
	consecutiveFails  int
	dockerVersion     string
	apiVersion        string
	lastHealthyCheck  time.Time
}

// HealthStatus represents the current health state of the Docker daemon.
type HealthStatus struct {
	Healthy          bool      `json:"healthy"`
	LastCheck        time.Time `json:"last_check"`
	LastHealthyCheck time.Time `json:"last_healthy_check,omitempty"`
	LastError        string    `json:"last_error,omitempty"`
	ConsecutiveFails int       `json:"consecutive_fails"`
	DockerVersion    string    `json:"docker_version,omitempty"`
	APIVersion       string    `json:"api_version,omitempty"`
}

// NewHealthChecker creates a Docker health checker with the specified check interval and timeout.
// If checkInterval is 0, defaults to 30 seconds. If timeout is 0, defaults to 5 seconds.
func NewHealthChecker(client *Client, checkInterval, timeout time.Duration) *HealthChecker {
	if checkInterval == 0 {
		checkInterval = 30 * time.Second
	}
	if timeout == 0 {
		timeout = 5 * time.Second
	}

	return &HealthChecker{
		client:        client,
		checkInterval: checkInterval,
		timeout:       timeout,
		healthy:       false, // Assume unhealthy until first successful check
	}
}

// Start begins periodic health checks. Blocks until ctx is cancelled.
// Performs an immediate check on startup, then checks at regular intervals.
func (hc *HealthChecker) Start(ctx context.Context) {
	// Immediate check on startup
	hc.check(ctx)

	ticker := time.NewTicker(hc.checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			hc.check(ctx)
		}
	}
}

// check performs a single health check against the Docker daemon.
func (hc *HealthChecker) check(ctx context.Context) {
	checkCtx, cancel := context.WithTimeout(ctx, hc.timeout)
	defer cancel()

	now := time.Now()
	err := hc.ping(checkCtx)

	hc.mu.Lock()
	hc.lastCheck = now

	if err != nil {
		hc.healthy = false
		hc.lastError = err
		hc.consecutiveFails++
	} else {
		hc.healthy = true
		hc.lastError = nil
		hc.consecutiveFails = 0
		hc.lastHealthyCheck = now
	}

	// Call optional callback with current state
	healthy := hc.healthy
	consecutiveFails := hc.consecutiveFails
	onCheck := hc.onCheck
	hc.mu.Unlock()

	if onCheck != nil {
		onCheck(healthy, consecutiveFails)
	}
}

// ping sends a request to the Docker daemon's /_ping endpoint.
// On success, extracts version information from response headers.
func (hc *HealthChecker) ping(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, hc.client.url("/_ping"), nil)
	if err != nil {
		return fmt.Errorf("create ping request: %w", err)
	}

	resp, err := hc.client.http.Do(req) // #nosec G704 -- unix socket only
	if err != nil {
		return fmt.Errorf("ping docker: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("ping returned HTTP %d", resp.StatusCode)
	}

	// Extract version information from response headers
	hc.mu.Lock()
	hc.apiVersion = resp.Header.Get("API-Version")
	hc.dockerVersion = resp.Header.Get("Server")
	hc.mu.Unlock()

	return nil
}

// Status returns the current health status.
func (hc *HealthChecker) Status() HealthStatus {
	hc.mu.RLock()
	defer hc.mu.RUnlock()

	status := HealthStatus{
		Healthy:          hc.healthy,
		LastCheck:        hc.lastCheck,
		LastHealthyCheck: hc.lastHealthyCheck,
		ConsecutiveFails: hc.consecutiveFails,
		DockerVersion:    hc.dockerVersion,
		APIVersion:       hc.apiVersion,
	}

	if hc.lastError != nil {
		status.LastError = hc.lastError.Error()
	}

	return status
}

// IsHealthy returns true if the most recent check succeeded.
func (hc *HealthChecker) IsHealthy() bool {
	hc.mu.RLock()
	defer hc.mu.RUnlock()
	return hc.healthy
}

// SetOnCheck registers a callback to be invoked after each health check.
// The callback receives the current health status and consecutive failure count.
func (hc *HealthChecker) SetOnCheck(callback func(healthy bool, consecutiveFails int)) {
	hc.mu.Lock()
	hc.onCheck = callback
	hc.mu.Unlock()
}
