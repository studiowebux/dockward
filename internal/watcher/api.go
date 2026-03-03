package watcher

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"github.com/studiowebux/dockward/internal/logger"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/studiowebux/dockward/internal/audit"
	"github.com/studiowebux/dockward/internal/compose"
	"github.com/studiowebux/dockward/internal/config"
	"github.com/studiowebux/dockward/internal/hub"
	"github.com/studiowebux/dockward/internal/saferun"
)

// API exposes HTTP endpoints for triggering updates, health, and metrics.
// Listens on localhost only.
type API struct {
	updater *Updater
	healer  *Healer
	metrics *Metrics
	monitor *Monitor
	audit   *audit.Logger
	hub     *hub.Hub
	server  *http.Server
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
func NewAPI(updater *Updater, healer *Healer, metrics *Metrics, monitor *Monitor, al *audit.Logger, address string, port string) *API {
	h := hub.NewHub()
	al.WithBroadcast(&broadcaster{hub: h})

	mux := http.NewServeMux()
	api := &API{
		updater: updater,
		healer:  healer,
		metrics: metrics,
		monitor: monitor,
		audit:   al,
		hub:     h,
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
	mux.HandleFunc("/ui/v2", withTimeout(api.handleUIDataStar, defaultTimeout))
	mux.HandleFunc("/command-preview/", withTimeout(api.handleCommandPreview, defaultTimeout))

	// SSE endpoints - no timeout (long-lived connections)
	mux.HandleFunc("/ui/events", withTimeout(api.handleUIEvents, sseTimeout))
	mux.HandleFunc("/ui/stream", withTimeout(api.handleUIStream, sseTimeout))

	return api
}

// Run starts the HTTP server. Blocks until ctx is cancelled.
func (a *API) Run(ctx context.Context) {
	saferun.Go("api-shutdown", func() {
		<-ctx.Done()
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
	Name   string `json:"name"`
	State  string `json:"state"`
	Status string `json:"status"`
	Image  string `json:"image"`
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
	blocked       map[string]string
	notFound      map[string]string
	errored       map[string]string
	degraded      map[string]bool
	exhausted     map[string]bool
	restartCounts map[string]int
	healthGauges  map[string]bool
	counters      map[string]ServiceCounters
	stats         map[string]ServiceStats
	deployed      map[string]DeployedInfo
	containers    map[string][]ContainerInfo
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
	}
	snap.deployed = a.updater.DeployedInfos()

	containers := make(map[string][]ContainerInfo)
	for _, svc := range a.updater.cfg.Services {
		if svc.Silent || svc.ComposeProject == "" {
			continue
		}
		if ci := a.updater.serviceContainerInfos(ctx, svc.ComposeProject); len(ci) > 0 {
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

// GET /health - watcher health check
func (a *API) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]string{"status": "ok"})
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

// uiData is the template context for GET /ui.
type uiData struct {
	Hostname string
	Uptime   string
	Services []serviceStatus
	Events   []audit.Entry
}

// uiTemplate is compiled once at startup.
var uiTemplate = template.Must(template.New("ui").Parse(uiHTML))

// GET /ui - web dashboard
func (a *API) handleUI(w http.ResponseWriter, r *http.Request) {
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

	events, err := a.audit.Recent(20)
	if err != nil {
		logger.Printf("[api] ui: audit read error: %v", err)
	}
	// Reverse so newest event appears first.
	for i, j := 0, len(events)-1; i < j; i, j = i+1, j-1 {
		events[i], events[j] = events[j], events[i]
	}

	hostname, _ := os.Hostname()

	data := uiData{
		Hostname: hostname,
		Uptime:   formatUptime(meta.UptimeSeconds),
		Services: services,
		Events:   events,
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := uiTemplate.Execute(w, data); err != nil {
		logger.Printf("[api] ui: template error: %v", err)
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

const uiHTML = `<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="utf-8">
<title>Dockward</title>
<script>
(function(){
  var t=localStorage.getItem('theme')||(window.matchMedia('(prefers-color-scheme:light)').matches?'light':'dark');
  if(t==='light')document.documentElement.setAttribute('data-theme','light');
})();
</script>
<style>
:root{
  --bg:#0f0f0f;--surface:#161616;--border:#2a2a2a;
  --text:#d0d0d0;--text-muted:#888;--text-label:#999;--text-dim:#666;
  --ok:#4caf50;--warn:#ffa726;--err:#ef5350;--info:#42a5f5;--unknown:#888;
  --btn-bg:#1c1c1c;--btn-border:#333;--btn-text:#999;
  --btn-hover-bg:#252525;--btn-hover-text:#ccc;
  --row-hover:#131313;
}
[data-theme="light"]{
  --bg:#f4f4f4;--surface:#fff;--border:#e0e0e0;
  --text:#1a1a1a;--text-muted:#555;--text-label:#444;--text-dim:#888;
  --ok:#2e7d32;--warn:#e65100;--err:#c62828;--info:#1565c0;--unknown:#777;
  --btn-bg:#fff;--btn-border:#ccc;--btn-text:#555;
  --btn-hover-bg:#f0f0f0;--btn-hover-text:#111;
  --row-hover:#fafafa;
}
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:monospace;font-size:13px;background:var(--bg);color:var(--text);padding:24px 32px}
header{display:flex;justify-content:space-between;align-items:center;margin-bottom:24px;color:var(--text-muted);font-size:12px}
h2{font-size:11px;font-weight:normal;color:var(--text-label);text-transform:uppercase;letter-spacing:.08em;margin-bottom:8px}
table{width:100%;border-collapse:collapse;margin-bottom:32px}
th{text-align:left;color:var(--text-label);font-weight:normal;padding:5px 12px;border-bottom:1px solid var(--border);font-size:11px;text-transform:uppercase;letter-spacing:.05em}
td{padding:6px 12px;border-bottom:1px solid var(--border);vertical-align:middle;color:var(--text)}
tr:hover td{background:var(--row-hover)}
.ok{color:var(--ok)}.unhealthy{color:var(--err)}.deploying{color:var(--info)}
.degraded{color:var(--warn)}.exhausted{color:var(--err)}.blocked{color:var(--warn)}
.not_found{color:var(--text-muted)}.errored{color:var(--err)}.unknown{color:var(--unknown)}
.info{color:var(--info)}.warning{color:var(--warn)}.error{color:var(--err)}.critical{color:var(--err)}
button{background:var(--btn-bg);color:var(--btn-text);border:1px solid var(--btn-border);padding:3px 10px;cursor:pointer;font-family:monospace;font-size:11px}
button:hover{background:var(--btn-hover-bg);color:var(--btn-hover-text)}
#theme-toggle{background:none;border:none;color:var(--text-muted);cursor:pointer;font-family:monospace;font-size:11px;padding:0}
#theme-toggle:hover{color:var(--text)}
.ctrs{margin-top:4px}
.ctr{display:flex;gap:8px;font-size:11px;color:var(--text-muted);padding:1px 0}
.ctr-name{min-width:120px}.ctr-state{min-width:60px}
</style>
</head>
<body>
<header>
  <span>Dockward &mdash; {{.Hostname}} &mdash; uptime {{.Uptime}}</span>
  <button id="theme-toggle" onclick="toggleTheme()">[light]</button>
</header>

<h2>Services</h2>
<table>
<thead><tr><th>Name</th><th>Status</th><th>Config</th><th>Next Check</th><th>Health</th><th>Image</th><th>Resources</th><th>Deploy Stats</th><th>Actions</th></tr></thead>
<tbody id="status-body">
{{range .Services}}
<tr>
  <td>
    {{.Name}}
    {{if .Containers}}
    <details data-svc="{{.Name}}" style="margin-top:3px">
      <summary style="cursor:pointer;color:var(--text-muted);font-size:11px">{{len .Containers}} container(s)</summary>
      <div class="ctrs">
        {{range .Containers}}
        <div class="ctr"><span class="ctr-name">{{.Name}}</span><span class="ctr-state">{{.State}}</span><span>{{.Status}}</span></div>
        {{end}}
      </div>
    </details>
    {{end}}
  </td>
  <td class="{{.Status}}">
    {{.Status}}
    {{if .Errored}}<span title="{{.Errored}}" style="cursor:help"> ⚠️</span>{{end}}
  </td>
  <td style="font-size:11px">
    {{if .AutoUpdate}}<span style="color:var(--success)" title="Auto-update enabled">U</span>{{else}}<span style="color:var(--text-dim)">-</span>{{end}}
    {{if .AutoHeal}}<span style="color:var(--success)" title="Auto-heal enabled">H</span>{{else}}<span style="color:var(--text-dim)">-</span>{{end}}
    {{if .AutoStart}}<span style="color:var(--success)" title="Auto-start enabled">S</span>{{else}}<span style="color:var(--text-dim)">-</span>{{end}}
  </td>
  <td style="color:var(--text-muted);font-size:11px">
    {{if .NextCheck}}
      <span title="Next check at {{.NextCheck.Format "15:04:05"}}">{{.NextCheck.Sub $.Now | formatDuration}}</span>
    {{else}}--{{end}}
    {{if eq .CheckStatus "checking"}}
      <span style="color:var(--warning)"> ⏳</span>
    {{end}}
  </td>
  <td style="font-size:11px">
    {{if eq .Healthy true}}<span style="color:var(--success)">✓</span>
    {{else if eq .Healthy false}}<span style="color:var(--error)">✗</span>
    {{else}}<span style="color:var(--text-dim)">?</span>{{end}}
  </td>
  <td style="color:var(--text-muted);font-size:11px">
    {{if .Image}}{{.Image}}<br/>{{end}}
    {{if .ImageDigest}}<span style="color:var(--text-dim)">{{.ImageDigest}}</span>{{else}}--{{end}}
  </td>
  <td style="font-size:11px">
    {{if .HasStats}}
      CPU: {{printf "%.0f" .CPUPercent}}%<br/>
      MEM: {{printf "%.0f" .MemoryUsageMB}}MB/{{printf "%.0f" .MemoryLimitMB}}MB ({{printf "%.0f" .MemoryPercent}}%)
    {{else}}--{{end}}
  </td>
  <td style="font-size:11px">
    U:{{.UpdatesTotal}} R:{{.RollbacksTotal}}<br/>
    H:{{.RestartsTotal}} F:{{.FailuresTotal}}
  </td>
  <td>
    <button onclick="triggerService('{{.Name}}')">Trigger</button>
    <button onclick="redeployService('{{.Name}}')">Redeploy</button>
    {{if .Blocked}}&nbsp;<button onclick="unblockService('{{.Name}}')">Unblock</button>{{end}}
  </td>
</tr>
{{end}}
</tbody>
</table>

<h2>Recent Events</h2>
<table>
<thead><tr><th>Time</th><th>Service</th><th>Event</th><th>Level</th><th>Message</th></tr></thead>
<tbody id="events-body">
{{range .Events}}
<tr>
  <td style="white-space:nowrap;color:var(--text-dim)" title="{{.Timestamp.Format "2006-01-02T15:04:05Z07:00"}}">{{.Timestamp.Format "2006-01-02 15:04:05"}}</td>
  <td style="color:var(--text-muted)">{{.Service}}</td>
  <td>{{.Event}}</td>
  <td class="{{.Level}}">{{.Level}}</td>
  <td style="color:var(--text-muted)">{{.Message}}</td>
</tr>
{{end}}
</tbody>
</table>

<script>
function esc(s){return String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');}

function triggerService(name){
  var btn=event.target;
  btn.disabled=true;
  btn.textContent='Triggering...';
  fetch('/trigger/'+encodeURIComponent(name),{method:'POST'})
    .then(function(r){
      return r.json();
    })
    .then(function(data){
      if(data.status==='skipped' && data.reason){
        btn.textContent=data.reason==='auto_update is false'?'Disabled':'Skipped';
      } else {
        btn.textContent='Triggered!';
      }
      setTimeout(function(){btn.disabled=false;btn.textContent='Trigger';},2000);
    })
    .catch(function(){
      btn.disabled=false;
      btn.textContent='Error';
      setTimeout(function(){btn.textContent='Trigger';},2000);
    });
}
function redeployService(name){
  if(!name){
    alert('Service name is missing');
    return;
  }
  if(!confirm('Redeploy '+name+'? This will run docker compose up -d.'))return;

  var btn=event.target;
  btn.disabled=true;
  btn.textContent='Loading...';

  fetch('/command-preview/'+encodeURIComponent(name))
    .then(function(r){
      if(!r.ok) throw new Error('Service not found or no compose files');
      return r.json();
    })
    .then(function(data){
      if(!data || !data.command){
        btn.disabled=false;
        btn.textContent='Redeploy';
        alert('No command available for this service');
        return;
      }
      if(confirm('Execute: '+data.command+'?')){
        btn.textContent='Redeploying...';
        return fetch('/redeploy/'+encodeURIComponent(name),{method:'POST'})
          .then(function(r){
            if(!r.ok) throw new Error('Redeploy failed');
            btn.textContent='Started!';
            setTimeout(function(){btn.disabled=false;btn.textContent='Redeploy';},2000);
          });
      } else {
        btn.disabled=false;
        btn.textContent='Redeploy';
      }
    })
    .catch(function(err){
      btn.disabled=false;
      btn.textContent='Error';
      alert('Failed: '+(err.message||'Unknown error'));
      setTimeout(function(){btn.textContent='Redeploy';},2000);
    });
}
function unblockService(name){
  fetch('/unblock/'+encodeURIComponent(name),{method:'POST'}).catch(function(){});
}

function toggleTheme(){
  var cur=document.documentElement.getAttribute('data-theme')||'dark';
  var next=cur==='light'?'dark':'light';
  document.documentElement.setAttribute('data-theme',next);
  localStorage.setItem('theme',next);
  document.getElementById('theme-toggle').textContent=next==='light'?'[dark]':'[light]';
}
(function(){
  var saved=localStorage.getItem('theme')||(window.matchMedia('(prefers-color-scheme:light)').matches?'light':'dark');
  document.getElementById('theme-toggle').textContent=saved==='light'?'[dark]':'[light]';
})();

var es=new EventSource('/ui/events');
var sseRetryCount=0;
es.onmessage=function(evt){
  var e;try{e=JSON.parse(evt.data);}catch(_){return;}
  var tbody=document.getElementById('events-body');
  if(!tbody)return;
  var ts=e.timestamp?e.timestamp.replace('T',' ').slice(0,19):'';
  var row='<tr>'+
    '<td style="white-space:nowrap;color:var(--text-dim)">'+esc(ts)+'</td>'+
    '<td style="color:var(--text-muted)">'+esc(e.service||'')+'</td>'+
    '<td>'+esc(e.event||'')+'</td>'+
    '<td class="'+esc(e.level||'')+'">'+esc(e.level||'')+'</td>'+
    '<td style="color:var(--text-muted)">'+esc(e.message||'')+'</td>'+
    '</tr>';
  tbody.insertAdjacentHTML('afterbegin',row);
  while(tbody.rows.length>50){tbody.deleteRow(-1);}
};
es.onerror=function(){
  sseRetryCount++;
  if(sseRetryCount>5){
    es.close();
    console.error('SSE connection failed after 5 retries');
  }
};
es.onopen=function(){
  sseRetryCount=0;
};

function refreshStatus(){
  fetch('/status')
    .then(function(r){
      if(!r.ok){
        console.error('Failed to fetch status:', r.status);
        return null;
      }
      return r.json();
    })
    .then(function(data){
      if(!data || !data.services){
        console.error('Invalid status response');
        return;
      }
      var tbody=document.getElementById('status-body');
      if(!tbody)return;
    var openDetails={};
    tbody.querySelectorAll('details[data-svc]').forEach(function(d){
      if(d.open)openDetails[d.getAttribute('data-svc')]=true;
    });
    var rows='';
    for(var i=0;i<data.services.length;i++){
      var s=data.services[i];
      var cpu=s.has_stats?Math.round(s.cpu_percent)+'% / '+Math.round(s.memory_percent)+'%':'--';
      var actions='<button onclick="triggerService(\''+esc(s.name)+'\')">Trigger</button>';
      actions+=' <button onclick="redeployService(\''+esc(s.name)+'\')">Redeploy</button>';
      if(s.blocked){actions+=' <button onclick="unblockService(\''+esc(s.name)+'\')">Unblock</button>';}

      // Calculate next check timing with proper null handling
      var nextCheckText='--';
      if(s.next_check){
        try {
          var next=new Date(s.next_check);
          var now=new Date();
          var diff=Math.round((next-now)/1000);
          if(!isNaN(diff) && diff>0){
            if(diff<60)nextCheckText=diff+'s';
            else if(diff<3600)nextCheckText=Math.round(diff/60)+'m';
            else nextCheckText=Math.round(diff/3600)+'h';
          } else if(diff<=0) {
            nextCheckText='now';
          }
        } catch(e) {
          nextCheckText='--';
        }
      }
      if(s.check_status==='checking')nextCheckText+=' <span style="color:var(--warning)">(checking...)</span>';

      var nameCell=esc(s.name);
      if(s.containers&&s.containers.length){
        var cdivs='';
        for(var j=0;j<s.containers.length;j++){
          var c=s.containers[j];
          cdivs+='<div class="ctr"><span class="ctr-name">'+esc(c.name||'')+'</span><span class="ctr-state">'+esc(c.state||'')+'</span><span>'+esc(c.status||'')+'</span></div>';
        }
        nameCell+='\n<details data-svc="'+esc(s.name)+'" style="margin-top:3px"><summary style="cursor:pointer;color:var(--text-muted);font-size:11px">'+s.containers.length+' container(s)</summary><div class="ctrs">'+cdivs+'</div></details>';
      }
      // Build config flags with explicit boolean checks
      var configFlags='';
      configFlags+='<span style="color:'+(s.auto_update===true?'var(--success)':'var(--text-dim)')+';font-size:11px" title="Auto-update '+(s.auto_update===true?'enabled':'disabled')+'">U</span> ';
      configFlags+='<span style="color:'+(s.auto_heal===true?'var(--success)':'var(--text-dim)')+';font-size:11px" title="Auto-heal '+(s.auto_heal===true?'enabled':'disabled')+'">H</span> ';
      configFlags+='<span style="color:'+(s.auto_start===true?'var(--success)':'var(--text-dim)')+';font-size:11px" title="Auto-start '+(s.auto_start===true?'enabled':'disabled')+'">S</span>';

      // Build health indicator
      var healthIcon='<span style="color:var(--text-dim)">?</span>';
      if(s.healthy===true)healthIcon='<span style="color:var(--success)">✓</span>';
      else if(s.healthy===false)healthIcon='<span style="color:var(--error)">✗</span>';

      // Build status with error tooltip
      var statusCell='<span class="'+esc(s.status)+'">'+esc(s.status)+'</span>';
      if(s.errored)statusCell+=' <span title="'+esc(s.errored)+'" style="cursor:help">⚠️</span>';

      // Build resources display with proper null handling
      var resources='--';
      if(s.has_stats){
        var cpuPct = s.cpu_percent !== undefined ? Math.round(s.cpu_percent) : 0;
        var memUsed = s.memory_usage_mb !== undefined ? Math.round(s.memory_usage_mb) : 0;
        var memLimit = s.memory_limit_mb !== undefined ? Math.round(s.memory_limit_mb) : null;
        var memPct = s.memory_percent !== undefined ? Math.round(s.memory_percent) : 0;

        resources='CPU: '+cpuPct+'%<br/>';
        if(memLimit !== null && memLimit > 0){
          resources+='MEM: '+memUsed+'MB/'+memLimit+'MB ('+memPct+'%)';
        } else {
          resources+='MEM: '+memUsed+'MB (no limit)';
        }
      }

      // Build deploy stats with null safety
      var deployStats='U:'+(s.updates_total||0)+' R:'+(s.rollbacks_total||0)+'<br/>';
      deployStats+='H:'+(s.restarts_total||0)+' F:'+(s.failures_total||0);

      rows+='<tr>'+
        '<td>'+nameCell+'</td>'+
        '<td>'+statusCell+'</td>'+
        '<td style="font-size:11px">'+configFlags+'</td>'+
        '<td style="color:var(--text-muted);font-size:11px">'+nextCheckText+'</td>'+
        '<td style="font-size:11px">'+healthIcon+'</td>'+
        '<td style="color:var(--text-muted);font-size:11px">'+(s.image?esc(s.image)+'<br/>':'')+(s.image_digest?'<span style="color:var(--text-dim)">'+esc(s.image_digest)+'</span>':'--')+'</td>'+
        '<td style="font-size:11px">'+resources+'</td>'+
        '<td style="font-size:11px">'+deployStats+'</td>'+
        '<td>'+actions+'</td>'+
        '</tr>';
    }
    tbody.innerHTML=rows;
    tbody.querySelectorAll('details[data-svc]').forEach(function(d){
      if(openDetails[d.getAttribute('data-svc')])d.open=true;
    });
  }).catch(function(){});
}
setInterval(refreshStatus,15000);
</script>
</body>
</html>`

const dataStarHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>Dockward - data-star UI</title>

  <!-- data-star.dev -->
  <script type="module">
    import { datastar } from 'https://cdn.jsdelivr.net/npm/@data-star/core@0.0.17/+esm'

    // Initialize data-star with SSE plugin
    datastar({
      plugins: [
        // SSE plugin for real-time updates
        {
          name: 'sse',
          onload: (ctx) => {
            // Connect to SSE endpoint
            const eventSource = new EventSource('/ui/stream')

            // Handle status updates
            eventSource.addEventListener('status', (e) => {
              const data = JSON.parse(e.data)
              ctx.store.services = data.services
              ctx.store.uptime = data.uptime
              ctx.store.lastUpdate = new Date().toISOString()
            })

            // Handle audit events
            eventSource.addEventListener('audit', (e) => {
              const event = JSON.parse(e.data)
              // Prepend to events array (keep last 50)
              ctx.store.events = [event, ...ctx.store.events].slice(0, 50)
            })

            // Handle connection errors
            eventSource.onerror = () => {
              ctx.store.connectionStatus = 'error'
              setTimeout(() => {
                ctx.store.connectionStatus = 'reconnecting'
              }, 5000)
            }

            eventSource.onopen = () => {
              ctx.store.connectionStatus = 'connected'
            }
          }
        }
      ]
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
func (a *API) handleUIDataStar(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write([]byte(dataStarHTML)); err != nil {
		logger.Printf("[api] ERROR: failed to write data-star UI: %v", err)
	}
}

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
