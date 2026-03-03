package watcher

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/studiowebux/dockward/internal/audit"
	"github.com/studiowebux/dockward/internal/compose"
	"github.com/studiowebux/dockward/internal/config"
	"github.com/studiowebux/dockward/internal/docker"
	"github.com/studiowebux/dockward/internal/hub"
	"github.com/studiowebux/dockward/internal/logger"
	"github.com/studiowebux/dockward/internal/saferun"
)

// API exposes HTTP endpoints for triggering updates, health, and metrics.
// Listens on localhost only.
type API struct {
	updater        *Updater
	healer         *Healer
	metrics        *Metrics
	monitor        *Monitor
	audit          *audit.Logger
	hub            *hub.Hub
	dockerHealth   *docker.HealthChecker
	configWarnings []string // Invalid services from config validation
	server         *http.Server
	httpServer     *http.Server // For graceful shutdown
}

var (
	// serviceNameRegex enforces strict service name validation: alphanumeric + dash + underscore only, 1-64 chars
	// Same pattern as compose project names for consistency
	serviceNameRegex = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)
)

// validateServiceName ensures the service name contains only safe characters.
// Returns empty string if invalid, or the validated name if valid.
func validateServiceName(name string) string {
	name = strings.TrimSpace(name)
	if !serviceNameRegex.MatchString(name) {
		return ""
	}
	return name
}

// HTTP request limits
const (
	maxRequestBodySize = 1 << 20  // 1 MB
	defaultTimeout     = 30 * time.Second
	sseTimeout        = 0  // No timeout for SSE connections
)

// limitRequestBody wraps an HTTP handler to enforce max request body size.
// Returns 413 Request Entity Too Large if body exceeds limit.
func limitRequestBody(h http.HandlerFunc, maxBytes int64) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
		h(w, r)
	}
}

// withTimeout wraps an HTTP handler with a timeout context.
// If timeout is 0, no timeout is applied (for SSE endpoints).
func withTimeout(h http.HandlerFunc, timeout time.Duration) http.HandlerFunc {
	if timeout == 0 {
		return h
	}
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), timeout)
		defer cancel()
		h(w, r.WithContext(ctx))
	}
}

// broadcaster adapts hub.Hub to the audit.Broadcaster interface.
// Pattern: adapter.
type broadcaster struct {
	hub *hub.Hub
}

func (b *broadcaster) Broadcast(e audit.Entry) {
	data, err := json.Marshal(e)
	if err != nil {
		logger.Printf("[api] broadcaster: marshal error: %v", err)
		return
	}
	b.hub.Broadcast(data)
}

// NewAPI creates the trigger/metrics API on the given address and port.
func NewAPI(updater *Updater, healer *Healer, metrics *Metrics, monitor *Monitor, al *audit.Logger, dockerHealth *docker.HealthChecker, configWarnings []string, address string, port string) *API {
	h := hub.NewHub()
	al.WithBroadcast(&broadcaster{hub: h})

	mux := http.NewServeMux()
	api := &API{
		updater:        updater,
		healer:         healer,
		metrics:        metrics,
		monitor:        monitor,
		audit:          al,
		dockerHealth:   dockerHealth,
		configWarnings: configWarnings,
		hub:            h,
		server: &http.Server{
			Addr:              address + ":" + port,
			Handler:           mux,
			ReadTimeout:       10 * time.Second,
			ReadHeaderTimeout: 5 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       120 * time.Second,
			MaxHeaderBytes:    1 << 20, // 1 MB
		},
	}

	// POST endpoints with request body limits and timeouts
	mux.HandleFunc("/trigger", limitRequestBody(withTimeout(api.handleTriggerAll, defaultTimeout), maxRequestBodySize))
	mux.HandleFunc("/trigger/", limitRequestBody(withTimeout(api.handleTriggerService, defaultTimeout), maxRequestBodySize))
	mux.HandleFunc("/unblock/", limitRequestBody(withTimeout(api.handleUnblockPost, defaultTimeout), maxRequestBodySize))
	mux.HandleFunc("/redeploy/", limitRequestBody(withTimeout(api.handleForceRedeploy, defaultTimeout), maxRequestBodySize))

	// GET endpoints with timeouts (no body limits needed)
	mux.HandleFunc("/blocked", withTimeout(api.handleListBlocked, defaultTimeout))
	mux.HandleFunc("/blocked/", withTimeout(api.handleUnblockService, defaultTimeout))
	mux.HandleFunc("/not-found", withTimeout(api.handleListNotFound, defaultTimeout))
	mux.HandleFunc("/errored", withTimeout(api.handleListErrored, defaultTimeout))
	mux.HandleFunc("/status", withTimeout(api.handleStatusAll, defaultTimeout))
	mux.HandleFunc("/status/", withTimeout(api.handleStatusService, defaultTimeout))
	mux.HandleFunc("/health", withTimeout(api.handleHealth, defaultTimeout))
	mux.HandleFunc("/metrics", withTimeout(api.handleMetrics, defaultTimeout))
	mux.HandleFunc("/audit", withTimeout(api.handleAudit, defaultTimeout))
	mux.HandleFunc("/ui", withTimeout(api.handleUI, defaultTimeout))
	mux.HandleFunc("/command-preview/", withTimeout(api.handleCommandPreview, defaultTimeout))

	// SSE endpoints - no timeout (long-lived connections)
	mux.HandleFunc("/ui/events", withTimeout(api.handleUIEvents, sseTimeout))
	mux.HandleFunc("/ui/stream", withTimeout(api.handleUIStream, sseTimeout))

	return api
}

// Run starts the HTTP server. Blocks until ctx is cancelled.
func (a *API) Run(ctx context.Context) {
	// Store the server reference for graceful shutdown
	a.httpServer = a.server

	saferun.Go("api-shutdown", func() {
		<-ctx.Done()
		// Graceful shutdown is now handled by the Shutdown() method
		// This is just a fallback for forceful close if needed
		time.Sleep(35 * time.Second) // Wait longer than graceful shutdown timeout
		if err := a.server.Close(); err != nil {
			logger.Printf("[api] server close error: %v", err)
		}
	})

	logger.Printf("[api] listening on %s", a.server.Addr)
	if err := a.server.ListenAndServe(); err != http.ErrServerClosed {
		logger.Printf("[api] server error: %v", err)
	}
}

// POST /trigger - trigger update check for all services
func (a *API) handleTriggerAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Basic rate limiting check - only one trigger per service at a time
	deployCount := 0
	for _, svc := range a.updater.cfg.Services {
		if a.updater.IsDeploying(svc.Name) {
			deployCount++
		}
	}
	if deployCount > 0 {
		writeJSON(w, map[string]string{
			"status": "rate_limited",
			"message": fmt.Sprintf("%d services already deploying", deployCount),
		})
		return
	}

	logger.Printf("[api] manual trigger: all services")
	saferun.Go("trigger-all", func() {
		a.updater.pollAll(context.Background())
	})

	writeJSON(w, map[string]string{"status": "triggered", "scope": "all"})
}

// POST /trigger/<service> - trigger update check for a specific service.
// Accepts ?redirect=ui to redirect to the web UI instead of returning JSON.
func (a *API) handleTriggerService(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	serviceName := strings.TrimPrefix(r.URL.Path, "/trigger/")
	serviceName = validateServiceName(serviceName)
	if serviceName == "" {
		http.Error(w, "invalid service name: must match ^[a-zA-Z0-9_-]{1,64}$", http.StatusBadRequest)
		return
	}

	redirectUI := r.URL.Query().Get("redirect") == "ui"

	found := false
	for _, svc := range a.updater.cfg.Services {
		if svc.Name == serviceName {
			if !svc.AutoUpdate {
				if redirectUI {
					http.Redirect(w, r, "/ui", http.StatusSeeOther)
					return
				}
				writeJSON(w, map[string]string{"status": "skipped", "reason": "auto_update is false"})
				return
			}
			if a.updater.IsDeploying(serviceName) {
				if redirectUI {
					http.Redirect(w, r, "/ui", http.StatusSeeOther)
					return
				}
				writeJSON(w, map[string]string{"status": "skipped", "reason": "deploy in progress"})
				return
			}
			found = true
			logger.Printf("[api] manual trigger: %s", svc.Name)
			saferun.Go("manual-trigger-"+svc.Name, func() {
				ctx := context.Background()
				if err := a.updater.checkAndUpdate(ctx, svc); err != nil {
					// Use non-suppressing error handler for manual triggers
					a.updater.handlePollErrorAlways(ctx, svc, err)
				}
			})
			break
		}
	}

	if !found {
		http.Error(w, "service not found", http.StatusNotFound)
		return
	}

	if redirectUI {
		http.Redirect(w, r, "/ui", http.StatusSeeOther)
		return
	}
	writeJSON(w, map[string]string{"status": "triggered", "scope": serviceName})
}

// GET /blocked - list blocked service digests
func (a *API) handleListBlocked(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, a.updater.BlockedDigests())
}

// GET /not-found - list services with unresolvable local digests
func (a *API) handleListNotFound(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, a.updater.NotFoundServices())
}

// GET /errored - list services with persistent poll errors
func (a *API) handleListErrored(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, a.updater.ErroredServices())
}

// statusResponse is the top-level wrapper returned by GET /status.
type statusResponse struct {
	UptimeSeconds int64           `json:"uptime_seconds"`
	LastPoll      *time.Time      `json:"last_poll,omitempty"`
	PollCount     int64           `json:"poll_count"`
	Services      []serviceStatus `json:"services"`
}

// ContainerInfo holds the live state of a single container for UI display.
type ContainerInfo struct {
	ID            string  `json:"id"`
	Name          string  `json:"name"`
	State         string  `json:"state"`
	Status        string  `json:"status"`
	Image         string  `json:"image"`
	CPUPercent    float64 `json:"cpu_percent,omitempty"`
	MemoryPercent float64 `json:"memory_percent,omitempty"`
	MemoryUsageMB float64 `json:"memory_usage_mb,omitempty"`
	MemoryLimitMB float64 `json:"memory_limit_mb,omitempty"`
}

// serviceStatus is the per-service state returned by /status and /status/<name>.
// Status is a synthesized single-word summary; the individual flag fields remain
// for programmatic consumers that need granular state.
type serviceStatus struct {
	Name       string `json:"name"`
	Status     string `json:"status"` // ok | deploying | degraded | exhausted | blocked | not_found | errored | unhealthy | unknown
	AutoUpdate bool   `json:"auto_update"`
	AutoStart  bool   `json:"auto_start"`
	AutoHeal   bool   `json:"auto_heal"`
	Healthy    *bool  `json:"healthy,omitempty"`
	Deploying  bool   `json:"deploying"`
	Blocked    string `json:"blocked,omitempty"`
	NotFound   string `json:"not_found,omitempty"`
	Errored    string `json:"errored,omitempty"`
	Degraded   bool   `json:"degraded"`
	Exhausted  bool   `json:"exhausted"`
	Restarts   int    `json:"restart_failures"`
	// Cumulative counters since process start.
	UpdatesTotal   int64 `json:"updates_total"`
	RollbacksTotal int64 `json:"rollbacks_total"`
	RestartsTotal  int64 `json:"restarts_total"`
	FailuresTotal  int64 `json:"failures_total"`
	// Deployed image info — populated each poll cycle.
	Image       string `json:"image,omitempty"`
	ImageDigest string `json:"image_digest,omitempty"`
	// Live containers for this compose project.
	Containers []ContainerInfo `json:"containers,omitempty"`
	// Resource usage — populated each monitor poll cycle for all running containers.
	HasStats      bool    `json:"has_stats"`
	CPUPercent    float64 `json:"cpu_percent,omitempty"`
	MemoryPercent float64 `json:"memory_percent,omitempty"`
	MemoryUsageMB float64 `json:"memory_usage_mb,omitempty"`
	MemoryLimitMB float64 `json:"memory_limit_mb,omitempty"`
	// Check timing information
	LastCheck   *time.Time `json:"last_check,omitempty"`
	NextCheck   *time.Time `json:"next_check,omitempty"`
	CheckStatus string     `json:"check_status,omitempty"` // idle | checking | deploying
}

// GET /status - aggregated state for all configured services
func (a *API) handleStatusAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	snap := a.stateSnapshot(r.Context())
	meta := a.metrics.Meta()

	services := make([]serviceStatus, 0, len(a.updater.cfg.Services))
	for _, svc := range a.updater.cfg.Services {
		services = append(services, a.buildServiceStatus(svc, snap))
	}

	resp := statusResponse{
		UptimeSeconds: meta.UptimeSeconds,
		PollCount:     meta.PollCount,
		Services:      services,
	}
	if !meta.LastPoll.IsZero() {
		t := meta.LastPoll
		resp.LastPoll = &t
	}

	writeJSON(w, resp)
}

// GET /status/<name> - aggregated state for a single service
func (a *API) handleStatusService(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	serviceName := strings.TrimPrefix(r.URL.Path, "/status/")
	serviceName = validateServiceName(serviceName)
	if serviceName == "" {
		http.Error(w, "invalid service name: must match ^[a-zA-Z0-9_-]{1,64}$", http.StatusBadRequest)
		return
	}

	snap := a.stateSnapshot(r.Context())
	for _, svc := range a.updater.cfg.Services {
		if svc.Name == serviceName {
			writeJSON(w, a.buildServiceStatus(svc, snap))
			return
		}
	}

	http.Error(w, "service not found", http.StatusNotFound)
}

// stateSnap holds a point-in-time snapshot of all state maps.
type stateSnap struct {
	blocked        map[string]string
	notFound       map[string]string
	errored        map[string]string
	degraded       map[string]bool
	exhausted      map[string]bool
	restartCounts  map[string]int
	healthGauges   map[string]bool
	counters       map[string]ServiceCounters
	stats          map[string]ServiceStats
	containerStats map[string]ContainerStats
	deployed       map[string]DeployedInfo
	containers     map[string][]ContainerInfo
}

func (a *API) stateSnapshot(ctx context.Context) stateSnap {
	snap := stateSnap{
		blocked:       a.updater.BlockedDigests(),
		notFound:      a.updater.NotFoundServices(),
		errored:       a.updater.ErroredServices(),
		degraded:      a.healer.DegradedServices(),
		exhausted:     a.healer.ExhaustedServices(),
		restartCounts: a.healer.RestartCounts(),
		healthGauges:  a.metrics.HealthSnapshot(),
		counters:      a.metrics.CountersSnapshot(),
	}
	if a.monitor != nil {
		snap.stats = a.monitor.StatsSnapshot()
		snap.containerStats = a.monitor.ContainerStatsSnapshot()
	}
	snap.deployed = a.updater.DeployedInfos()

	containers := make(map[string][]ContainerInfo)
	for _, svc := range a.updater.cfg.Services {
		if svc.Silent || svc.ComposeProject == "" {
			continue
		}
		if ci := a.updater.serviceContainerInfos(ctx, svc.ComposeProject); len(ci) > 0 {
			// Enrich containers with stats from monitor
			if snap.containerStats != nil {
				for i := range ci {
					if stats, ok := snap.containerStats[ci[i].ID]; ok {
						ci[i].CPUPercent = stats.CPUPercent
						ci[i].MemoryPercent = stats.MemoryPercent
						ci[i].MemoryUsageMB = stats.MemoryUsageMB
						ci[i].MemoryLimitMB = stats.MemoryLimitMB
					}
				}
			}
			containers[svc.Name] = ci
		}
	}
	snap.containers = containers

	return snap
}

// firstValueByPrefix returns the first map value whose key has the given prefix.
func firstValueByPrefix(m map[string]string, prefix string) string {
	for k, v := range m {
		if strings.HasPrefix(k, prefix) {
			return v
		}
	}
	return ""
}

func (a *API) buildServiceStatus(svc config.Service, snap stateSnap) serviceStatus {
	prefix := svc.Name + "/"
	s := serviceStatus{
		Name:       svc.Name,
		AutoUpdate: svc.AutoUpdate,
		AutoStart:  svc.AutoStart,
		AutoHeal:   svc.AutoHeal,
		Deploying:  a.updater.IsDeploying(svc.Name),
		Blocked:    firstValueByPrefix(snap.blocked, prefix),
		NotFound:   firstValueByPrefix(snap.notFound, prefix),
		Errored:    snap.errored[svc.Name],
		Degraded:   snap.degraded[svc.Name],
		Exhausted:  snap.exhausted[svc.Name],
		Restarts:   snap.restartCounts[svc.Name],
	}
	if h, ok := snap.healthGauges[svc.Name]; ok {
		s.Healthy = &h
	}
	if c, ok := snap.counters[svc.Name]; ok {
		s.UpdatesTotal   = c.Updates
		s.RollbacksTotal = c.Rollbacks
		s.RestartsTotal  = c.Restarts
		s.FailuresTotal  = c.Failures
	}
	if st, ok := snap.stats[svc.Name]; ok {
		s.HasStats      = true
		s.CPUPercent    = st.CPUPercent
		s.MemoryPercent = st.MemoryPercent
		s.MemoryUsageMB = st.MemoryUsageMB
		s.MemoryLimitMB = st.MemoryLimitMB
	}
	for k, d := range snap.deployed {
		if strings.HasPrefix(k, prefix) {
			s.Image      = d.Image
			s.ImageDigest = shortDigest(d.Digest)
			break
		}
	}
	s.Containers = snap.containers[svc.Name]

	// Add check timing information
	if lastCheck := a.updater.GetLastCheck(svc.Name); !lastCheck.IsZero() {
		s.LastCheck = &lastCheck
	}
	nextCheck := a.updater.GetNextCheck(svc.Name)
	s.NextCheck = &nextCheck
	s.CheckStatus = a.updater.GetCheckStatus(svc.Name)

	s.Status = synthesizeStatus(s)
	return s
}

// synthesizeStatus derives a single human-readable status word from service state.
// Priority order: exhausted > degraded > errored > blocked > not_found > deploying > ok/unhealthy/unknown.
func synthesizeStatus(s serviceStatus) string {
	switch {
	case s.Exhausted:
		return "exhausted"
	case s.Degraded:
		return "degraded"
	case s.Errored != "":
		return "errored"
	case s.Blocked != "":
		return "blocked"
	case s.NotFound != "":
		return "not_found"
	case s.Deploying:
		return "deploying"
	case s.Healthy != nil && *s.Healthy:
		return "ok"
	case s.Healthy != nil && !*s.Healthy:
		return "unhealthy"
	default:
		return "unknown"
	}
}

// DELETE /blocked/<service> - unblock a service
func (a *API) handleUnblockService(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	serviceName := strings.TrimPrefix(r.URL.Path, "/blocked/")
	serviceName = validateServiceName(serviceName)
	if serviceName == "" {
		http.Error(w, "invalid service name: must match ^[a-zA-Z0-9_-]{1,64}$", http.StatusBadRequest)
		return
	}

	if a.updater.UnblockService(serviceName) {
		writeJSON(w, map[string]string{"status": "unblocked", "service": serviceName})
	} else {
		writeJSON(w, map[string]string{"status": "not_blocked", "service": serviceName})
	}
}

// GET /health - watcher health check with component status
func (a *API) handleHealth(w http.ResponseWriter, _ *http.Request) {
	dockerStatus := a.dockerHealth.Status()

	// Overall status: healthy if Docker is healthy, unhealthy otherwise
	overallStatus := "healthy"
	statusCode := http.StatusOK

	if !dockerStatus.Healthy {
		overallStatus = "unhealthy"
		statusCode = http.StatusServiceUnavailable
	}

	response := map[string]interface{}{
		"status": overallStatus,
		"components": map[string]interface{}{
			"http": map[string]bool{
				"healthy": true,
			},
			"docker": map[string]interface{}{
				"healthy":           dockerStatus.Healthy,
				"last_check":        dockerStatus.LastCheck.Format(time.RFC3339),
				"consecutive_fails": dockerStatus.ConsecutiveFails,
			},
		},
	}

	// Add config warnings if any services were skipped during validation
	if len(a.configWarnings) > 0 {
		response["config_warnings"] = a.configWarnings
	}

	// Add optional fields only when available
	if !dockerStatus.LastHealthyCheck.IsZero() {
		response["components"].(map[string]interface{})["docker"].(map[string]interface{})["last_healthy_check"] = dockerStatus.LastHealthyCheck.Format(time.RFC3339)
	}
	if dockerStatus.DockerVersion != "" {
		response["components"].(map[string]interface{})["docker"].(map[string]interface{})["docker_version"] = dockerStatus.DockerVersion
	}
	if dockerStatus.APIVersion != "" {
		response["components"].(map[string]interface{})["docker"].(map[string]interface{})["api_version"] = dockerStatus.APIVersion
	}
	if dockerStatus.LastError != "" {
		response["components"].(map[string]interface{})["docker"].(map[string]interface{})["last_error"] = dockerStatus.LastError
	}

	w.WriteHeader(statusCode)
	writeJSON(w, response)
}

// GET /metrics - Prometheus-compatible metrics
func (a *API) handleMetrics(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	if _, err := w.Write([]byte(a.metrics.Prometheus())); err != nil {
		logger.Printf("[api] metrics write error: %v", err)
	}
}

// GET /audit - return recent audit log entries as JSON
func (a *API) handleAudit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	limit := 100
	if s := r.URL.Query().Get("limit"); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			if n > 500 {
				n = 500
			}
			limit = n
		}
	}

	entries, err := a.audit.Recent(limit)
	if err != nil {
		logger.Printf("[api] audit read error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if entries == nil {
		entries = []audit.Entry{}
	}
	writeJSON(w, entries)
}

// GET /ui/events - SSE stream of live audit entries.
// No auth — the agent API binds to 127.0.0.1 only.
// Replays the last 50 entries on connect, then streams live events.
func (a *API) handleUIEvents(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	// Extract client IP for connection limiting
	clientIP := hub.ExtractClientIP(r)

	// Subscribe to the hub with connection limiting
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

	// Clear the server-level WriteTimeout so this long-lived connection is not
	// killed after 30 s. Uses ResponseController (Go 1.20+).
	rc := http.NewResponseController(w)
	if err := rc.SetWriteDeadline(time.Time{}); err != nil {
		logger.Printf("[api] ui/events: could not clear write deadline: %v", err)
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	// Replay recent entries so the browser gets immediate content on connect.
	recent, err := a.audit.Recent(50)
	if err != nil {
		logger.Printf("[api] ui/events: audit read error: %v", err)
	}
	for _, e := range recent { // oldest-first (as stored)
		data, merr := json.Marshal(e)
		if merr != nil {
			continue
		}
		fmt.Fprintf(w, "data: %s\n\n", data)
	}
	flusher.Flush()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case msg, ok := <-ch:
			if !ok {
				return
			}
			if _, werr := fmt.Fprintf(w, "data: %s\n\n", msg); werr != nil {
				logger.Printf("[api] ui/events: write error: %v", werr)
				return
			}
			flusher.Flush()
		}
	}
}

func writeJSON(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(data); err != nil {
		logger.Printf("[api] json encode error: %v", err)
	}
}



// GET /ui - web dashboard
func (a *API) handleUI(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte(dataStarHTML)); err != nil {
		logger.Printf("[api] ERROR: failed to write UI: %v", err)
	}
}

// POST /unblock/<service> - HTML-form-compatible alias for DELETE /blocked/<service>.
// Unblocks the service and redirects to /ui.
func (a *API) handleUnblockPost(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	serviceName := strings.TrimPrefix(r.URL.Path, "/unblock/")
	serviceName = validateServiceName(serviceName)
	if serviceName == "" {
		http.Error(w, "invalid service name: must match ^[a-zA-Z0-9_-]{1,64}$", http.StatusBadRequest)
		return
	}

	a.updater.UnblockService(serviceName)
	http.Redirect(w, r, "/ui", http.StatusSeeOther)
}

// formatUptime converts a duration in seconds to a human-readable string.
func formatUptime(seconds int64) string {
	days := seconds / 86400
	hours := (seconds % 86400) / 3600
	minutes := (seconds % 3600) / 60
	switch {
	case days > 0:
		return fmt.Sprintf("%dd %dh %dm", days, hours, minutes)
	case hours > 0:
		return fmt.Sprintf("%dh %dm", hours, minutes)
	default:
		return fmt.Sprintf("%dm", minutes)
	}
}


const dataStarHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>Dockward - data-star UI</title>

  <!-- data-star.dev -->
  <script src="https://cdn.jsdelivr.net/gh/starfederation/datastar@1.0.0-RC.8/bundles/datastar.js"></script>
  <script>
    // Initialize SSE connection after page load
    window.addEventListener('load', function() {
      // Get the data-star store from the container element
      const container = document.querySelector('[data-store]')

      // Fetch initial status
      fetch('/status')
        .then(r => r.json())
        .then(data => {
          // Initialize store with current data
          container.setAttribute('data-store', JSON.stringify({
            services: data.services || [],
            events: [],
            connectionStatus: 'connected',
            uptime: data.uptime_seconds || 0,
            lastUpdate: new Date().toISOString()
          }))
        })
        .catch(err => {
          console.error('Failed to fetch initial status:', err)
        })

      // Connect to SSE endpoint for real-time updates
      const eventSource = new EventSource('/ui/stream')

      // Handle status updates
      eventSource.addEventListener('status', (e) => {
        try {
          const data = JSON.parse(e.data)
          const currentStore = JSON.parse(container.getAttribute('data-store') || '{}')
          currentStore.services = data.services || []
          currentStore.uptime = data.uptime_seconds || 0
          currentStore.lastUpdate = new Date().toISOString()
          currentStore.connectionStatus = 'connected'
          container.setAttribute('data-store', JSON.stringify(currentStore))
        } catch (err) {
          console.error('Failed to parse status update:', err)
        }
      })

      // Handle audit events
      eventSource.addEventListener('audit', (e) => {
        try {
          const event = JSON.parse(e.data)
          const currentStore = JSON.parse(container.getAttribute('data-store') || '{}')
          currentStore.events = [event, ...(currentStore.events || [])].slice(0, 50)
          container.setAttribute('data-store', JSON.stringify(currentStore))
        } catch (err) {
          console.error('Failed to parse audit event:', err)
        }
      })

      // Handle connection errors
      eventSource.onerror = () => {
        const currentStore = JSON.parse(container.getAttribute('data-store') || '{}')
        currentStore.connectionStatus = 'error'
        container.setAttribute('data-store', JSON.stringify(currentStore))
      }

      eventSource.onopen = () => {
        const currentStore = JSON.parse(container.getAttribute('data-store') || '{}')
        currentStore.connectionStatus = 'connected'
        container.setAttribute('data-store', JSON.stringify(currentStore))
      }
    })
  </script>

  <!-- Minimal CSS -->
  <style>
    :root {
      --bg: #1a1a1a;
      --surface: #2a2a2a;
      --text: #e0e0e0;
      --text-dim: #666;
      --success: #4caf50;
      --error: #f44336;
      --warning: #ff9800;
      --info: #2196f3;
    }

    * {
      margin: 0;
      padding: 0;
      box-sizing: border-box;
    }

    body {
      font-family: 'SF Mono', Monaco, monospace;
      font-size: 12px;
      background: var(--bg);
      color: var(--text);
      padding: 1rem;
    }

    .container {
      max-width: 1400px;
      margin: 0 auto;
    }

    header {
      display: flex;
      justify-content: space-between;
      align-items: center;
      padding: 1rem 0;
      border-bottom: 1px solid var(--surface);
      margin-bottom: 1rem;
    }

    .connection-indicator {
      display: inline-block;
      width: 8px;
      height: 8px;
      border-radius: 50%;
      margin-right: 0.5rem;
    }

    .connected { background: var(--success); }
    .reconnecting { background: var(--warning); animation: pulse 1s infinite; }
    .error { background: var(--error); }

    @keyframes pulse {
      0%, 100% { opacity: 1; }
      50% { opacity: 0.5; }
    }

    table {
      width: 100%;
      border-collapse: collapse;
      margin: 1rem 0;
    }

    th {
      text-align: left;
      padding: 0.5rem;
      background: var(--surface);
      border-bottom: 1px solid #444;
      font-weight: 500;
      font-size: 11px;
      text-transform: uppercase;
      letter-spacing: 0.5px;
    }

    td {
      padding: 0.5rem;
      border-bottom: 1px solid #333;
    }

    .service-row {
      transition: background 0.3s;
    }

    .service-row[data-updating="true"] {
      background: rgba(33, 150, 243, 0.1);
    }

    .status-badge {
      display: inline-block;
      padding: 2px 6px;
      border-radius: 3px;
      font-size: 10px;
      font-weight: 500;
    }

    .status-ok { background: var(--success); color: white; }
    .status-error { background: var(--error); color: white; }
    .status-deploying { background: var(--info); color: white; }
    .status-checking { background: var(--warning); color: white; }

    .config-flags {
      display: flex;
      gap: 4px;
    }

    .config-flag {
      width: 16px;
      height: 16px;
      border-radius: 2px;
      display: flex;
      align-items: center;
      justify-content: center;
      font-size: 10px;
      font-weight: bold;
    }

    .flag-enabled { background: var(--success); color: white; }
    .flag-disabled { background: #444; color: #888; }

    button {
      padding: 4px 8px;
      background: var(--surface);
      color: var(--text);
      border: 1px solid #444;
      border-radius: 3px;
      font-size: 11px;
      cursor: pointer;
      transition: all 0.2s;
    }

    button:hover:not(:disabled) {
      background: #3a3a3a;
      border-color: #666;
    }

    button:disabled {
      opacity: 0.5;
      cursor: not-allowed;
    }

    button.loading {
      position: relative;
      color: transparent;
    }

    button.loading::after {
      content: '';
      position: absolute;
      width: 12px;
      height: 12px;
      top: 50%;
      left: 50%;
      margin: -6px 0 0 -6px;
      border: 2px solid var(--text);
      border-radius: 50%;
      border-top-color: transparent;
      animation: spin 0.6s linear infinite;
    }

    @keyframes spin {
      to { transform: rotate(360deg); }
    }

    .next-check {
      font-size: 10px;
      color: var(--text-dim);
    }

    .container-details {
      margin-top: 4px;
      padding-left: 1rem;
      font-size: 10px;
      color: var(--text-dim);
    }

    .container-stat {
      display: inline-block;
      margin-right: 1rem;
    }

    .resource-bar {
      display: inline-block;
      width: 100px;
      height: 4px;
      background: #333;
      border-radius: 2px;
      overflow: hidden;
      margin: 0 4px;
      vertical-align: middle;
    }

    .resource-fill {
      height: 100%;
      background: var(--success);
      transition: width 0.3s, background 0.3s;
    }

    .resource-fill.warning { background: var(--warning); }
    .resource-fill.danger { background: var(--error); }
  </style>
</head>
<body>
  <div class="container" data-store="{
    services: [],
    events: [],
    connectionStatus: 'connecting',
    uptime: 0,
    lastUpdate: null
  }">

    <!-- Header -->
    <header>
      <h1>Dockward</h1>
      <div>
        <span
          class="connection-indicator"
          data-class="$connectionStatus"
        ></span>
        <span data-text="$connectionStatus === 'connected' ? 'Live' : $connectionStatus"></span>
        <span data-show="$lastUpdate" data-text="' • Updated: ' + new Date($lastUpdate).toLocaleTimeString()"></span>
      </div>
    </header>

    <!-- Services Table -->
    <section>
      <h2>Services</h2>
      <table>
        <thead>
          <tr>
            <th>Service</th>
            <th>Status</th>
            <th>Config</th>
            <th>Next Check</th>
            <th>Resources</th>
            <th>Stats</th>
            <th>Actions</th>
          </tr>
        </thead>
        <tbody data-each="service in $services">
          <tr
            class="service-row"
            data-updating="$service.check_status === 'checking'"
          >
            <!-- Service Name -->
            <td>
              <strong data-text="$service.name"></strong>
              <div data-show="$service.containers?.length" class="container-details">
                <div data-each="container in $service.containers">
                  <span class="container-stat">
                    <span data-text="$container.name"></span>:
                    <span data-text="$container.state"></span>
                  </span>
                </div>
              </div>
            </td>

            <!-- Status -->
            <td>
              <span
                class="status-badge"
                data-class="'status-' + $service.status"
                data-text="$service.status"
              ></span>
              <span
                data-show="$service.errored"
                data-text="' ⚠'"
                title="$service.errored"
                style="cursor: help;"
              ></span>
            </td>

            <!-- Config Flags -->
            <td>
              <div class="config-flags">
                <span
                  class="config-flag"
                  data-class="$service.auto_update ? 'flag-enabled' : 'flag-disabled'"
                  title="Auto Update"
                >U</span>
                <span
                  class="config-flag"
                  data-class="$service.auto_heal ? 'flag-enabled' : 'flag-disabled'"
                  title="Auto Heal"
                >H</span>
                <span
                  class="config-flag"
                  data-class="$service.auto_start ? 'flag-enabled' : 'flag-disabled'"
                  title="Auto Start"
                >S</span>
              </div>
            </td>

            <!-- Next Check -->
            <td>
              <span
                class="next-check"
                data-show="$service.next_check"
                data-text="formatTimeUntil($service.next_check)"
              ></span>
              <span
                data-show="$service.check_status === 'checking'"
                style="color: var(--warning);"
              >⏳ checking...</span>
            </td>

            <!-- Resources -->
            <td>
              <div data-show="$service.has_stats">
                <div>
                  CPU: <span data-text="Math.round($service.cpu_percent) + '%'"></span>
                  <span class="resource-bar">
                    <span
                      class="resource-fill"
                      data-style="'width: ' + $service.cpu_percent + '%'"
                      data-class="$service.cpu_percent > 80 ? 'danger' : $service.cpu_percent > 60 ? 'warning' : ''"
                    ></span>
                  </span>
                </div>
                <div>
                  MEM: <span data-text="Math.round($service.memory_usage_mb) + 'MB'"></span>
                  <span class="resource-bar">
                    <span
                      class="resource-fill"
                      data-style="'width: ' + $service.memory_percent + '%'"
                      data-class="$service.memory_percent > 85 ? 'danger' : $service.memory_percent > 70 ? 'warning' : ''"
                    ></span>
                  </span>
                </div>
              </div>
              <span data-show="!$service.has_stats" style="color: var(--text-dim);">--</span>
            </td>

            <!-- Stats -->
            <td style="font-size: 10px;">
              <div>U: <span data-text="$service.updates_total || 0"></span></div>
              <div>R: <span data-text="$service.rollbacks_total || 0"></span></div>
              <div>H: <span data-text="$service.restarts_total || 0"></span></div>
              <div>F: <span data-text="$service.failures_total || 0"></span></div>
            </td>

            <!-- Actions -->
            <td>
              <button
                data-on-click="triggerService($service.name)"
                data-disabled="$service.check_status === 'checking'"
              >Trigger</button>
              <button
                data-on-click="redeployService($service.name)"
              >Redeploy</button>
              <button
                data-show="$service.blocked"
                data-on-click="unblockService($service.name)"
              >Unblock</button>
            </td>
          </tr>
        </tbody>
      </table>
    </section>

    <!-- Events -->
    <section>
      <h2>Recent Events</h2>
      <table>
        <thead>
          <tr>
            <th>Time</th>
            <th>Service</th>
            <th>Event</th>
            <th>Level</th>
            <th>Message</th>
          </tr>
        </thead>
        <tbody data-each="event in $events">
          <tr>
            <td
              style="font-size: 10px; color: var(--text-dim);"
              data-text="new Date($event.timestamp).toLocaleString()"
            ></td>
            <td data-text="$event.service"></td>
            <td data-text="$event.event"></td>
            <td>
              <span
                class="status-badge"
                data-class="'status-' + ($event.level === 'critical' ? 'error' : $event.level)"
                data-text="$event.level"
              ></span>
            </td>
            <td
              style="font-size: 11px; color: var(--text-dim);"
              data-text="$event.message"
            ></td>
          </tr>
        </tbody>
      </table>
    </section>

  </div>

  <!-- Helper functions -->
  <script>
    // Format time until next check
    window.formatTimeUntil = function(timestamp) {
      if (!timestamp) return '--'
      const diff = new Date(timestamp) - new Date()
      if (diff < 0) return 'now'
      if (diff < 60000) return Math.round(diff / 1000) + 's'
      if (diff < 3600000) return Math.round(diff / 60000) + 'm'
      return Math.round(diff / 3600000) + 'h'
    }

    // Service actions
    window.triggerService = async function(name) {
      await fetch(` + "`" + `/trigger/${name}` + "`" + `, { method: 'POST' })
    }

    window.redeployService = async function(name) {
      if (!confirm(` + "`" + `Redeploy ${name}?` + "`" + `)) return
      const preview = await fetch(` + "`" + `/command-preview/${name}` + "`" + `).then(r => r.json())
      if (confirm(` + "`" + `Execute: ${preview.command}?` + "`" + `)) {
        await fetch(` + "`" + `/redeploy/${name}` + "`" + `, { method: 'POST' })
      }
    }

    window.unblockService = async function(name) {
      await fetch(` + "`" + `/unblock/${name}` + "`" + `, { method: 'POST' })
    }
  </script>
</body>
</html>`

// GET /ui/v2 - data-star.dev powered UI

// POST /redeploy/<service> - force redeploy without image check
func (a *API) handleForceRedeploy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	serviceName := strings.TrimPrefix(r.URL.Path, "/redeploy/")
	serviceName = validateServiceName(serviceName)
	if serviceName == "" {
		http.Error(w, "invalid service name: must match ^[a-zA-Z0-9_-]{1,64}$", http.StatusBadRequest)
		return
	}

	var svc *config.Service
	for i := range a.updater.cfg.Services {
		if a.updater.cfg.Services[i].Name == serviceName {
			svc = &a.updater.cfg.Services[i]
			break
		}
	}

	if svc == nil {
		http.Error(w, "service not found", http.StatusNotFound)
		return
	}

	if len(svc.ComposeFiles) == 0 {
		writeJSON(w, map[string]string{"status": "error", "message": "no compose files configured"})
		return
	}

	logger.Printf("[api] force redeploy: %s", svc.Name)
	saferun.Go("force-redeploy-"+svc.Name, func() {
		ctx := context.Background()
		if err := compose.Up(ctx, a.updater.cfg.Runtime, svc.ComposeFiles, svc.ComposeProject, svc.EnvFile); err != nil {
			logger.Printf("[api] ERROR: force redeploy failed for %s: %v", svc.Name, err)
			if werr := a.audit.Write(audit.Entry{
				Service: svc.Name,
				Event:   "force_redeploy_failed",
				Message: fmt.Sprintf("Force redeploy failed: %v", err),
				Level:   "error",
			}); werr != nil {
				logger.Printf("[api] ERROR: audit write error: %v", werr)
			}
			return
		}

		if werr := a.audit.Write(audit.Entry{
			Service: svc.Name,
			Event:   "force_redeploy",
			Message: "Forced redeploy via API",
			Level:   "info",
		}); werr != nil {
			logger.Printf("[api] ERROR: audit write error: %v", werr)
		}
	})

	writeJSON(w, map[string]string{"status": "redeploying", "service": serviceName})
}

// GET /command-preview/<service> - show docker compose command that would be executed
func (a *API) handleCommandPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	serviceName := strings.TrimPrefix(r.URL.Path, "/command-preview/")
	serviceName = validateServiceName(serviceName)
	if serviceName == "" {
		http.Error(w, "invalid service name: must match ^[a-zA-Z0-9_-]{1,64}$", http.StatusBadRequest)
		return
	}

	var svc *config.Service
	for i := range a.updater.cfg.Services {
		if a.updater.cfg.Services[i].Name == serviceName {
			svc = &a.updater.cfg.Services[i]
			break
		}
	}

	if svc == nil {
		http.Error(w, "service not found", http.StatusNotFound)
		return
	}

	// Build the compose command using configured runtime
	runtime := a.updater.cfg.Runtime
	if runtime == "" {
		runtime = "docker" // fallback for safety
	}
	cmd := fmt.Sprintf("%s compose", runtime)
	if svc.ComposeProject != "" {
		cmd += fmt.Sprintf(" -p %s", svc.ComposeProject)
	}
	for _, f := range svc.ComposeFiles {
		cmd += fmt.Sprintf(" -f %s", f)
	}
	// Note: env file is loaded into environment, not passed as --env-file
	cmd += " up -d"
	if svc.EnvFile != "" {
		cmd += fmt.Sprintf(" # (with env from %s)", svc.EnvFile)
	}

	writeJSON(w, map[string]string{
		"service": serviceName,
		"command": cmd,
	})
}
