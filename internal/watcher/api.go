package watcher

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
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
type API struct {
	updater        *Updater
	healer         *Healer
	metrics        *Metrics
	monitor        *Monitor
	audit          *audit.Logger
	hub            *hub.Hub
	dockerHealth   *docker.HealthChecker
	configWarnings []string // Invalid services from config validation
	configPath     string   // Path to config file for write-back on mutations
	servers        []*http.Server // one per listen address, all share the same mux

	// cfgMu guards config mutations made via the config API endpoints.
	// Running goroutines (updater, healer, monitor) share the same *config.Config
	// pointer — mutations under this lock are visible to all components.
	cfgMu sync.RWMutex

	// statusMu guards statusSubs — the set of channels notified when service
	// state changes (audit entry written → status should be re-pushed to SSE).
	statusMu   sync.Mutex
	statusSubs map[chan struct{}]struct{}
}

// subscribeStatus returns a channel that receives a signal whenever service
// state changes and the UI should refresh.  Buffer of 1 so the broadcaster
// never blocks.
func (a *API) subscribeStatus() chan struct{} {
	ch := make(chan struct{}, 1)
	a.statusMu.Lock()
	if a.statusSubs == nil {
		a.statusSubs = make(map[chan struct{}]struct{})
	}
	a.statusSubs[ch] = struct{}{}
	a.statusMu.Unlock()
	return ch
}

func (a *API) unsubscribeStatus(ch chan struct{}) {
	a.statusMu.Lock()
	delete(a.statusSubs, ch)
	a.statusMu.Unlock()
}

// notifyStatus signals all SSE clients that service state changed.
func (a *API) notifyStatus() {
	a.statusMu.Lock()
	defer a.statusMu.Unlock()
	for ch := range a.statusSubs {
		select {
		case ch <- struct{}{}:
		default: // already pending, skip
		}
	}
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
// Pattern: adapter.  Also notifies SSE clients to refresh status.
type broadcaster struct {
	hub      *hub.Hub
	onNotify func() // called after broadcast to trigger status push
}

func (b *broadcaster) Broadcast(e audit.Entry) {
	data, err := json.Marshal(e)
	if err != nil {
		logger.Printf("[api] broadcaster: marshal error: %v", err)
		return
	}
	b.hub.Broadcast(data)
	if b.onNotify != nil {
		b.onNotify()
	}
}

// NewAPI creates the trigger/metrics API on the given addresses.
// Each address is a "host:port" string; one http.Server is created per address,
// all sharing the same handler mux. configPath is the file path used by the
// config mutation endpoints to persist changes to disk.
func NewAPI(updater *Updater, healer *Healer, metrics *Metrics, monitor *Monitor, al *audit.Logger, dockerHealth *docker.HealthChecker, configWarnings []string, addresses []string, configPath string) *API {
	h := hub.NewHub()
	bc := &broadcaster{hub: h}
	al.WithBroadcast(bc)

	mux := http.NewServeMux()

	servers := make([]*http.Server, len(addresses))
	for i, addr := range addresses {
		servers[i] = &http.Server{
			Addr:              addr,
			Handler:           mux,
			ReadTimeout:       10 * time.Second,
			ReadHeaderTimeout: 5 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       120 * time.Second,
			MaxHeaderBytes:    1 << 20, // 1 MB
		}
	}

	api := &API{
		updater:        updater,
		healer:         healer,
		metrics:        metrics,
		monitor:        monitor,
		audit:          al,
		dockerHealth:   dockerHealth,
		configWarnings: configWarnings,
		configPath:     configPath,
		hub:            h,
		statusSubs:     make(map[chan struct{}]struct{}),
		servers:        servers,
	}

	// Wire status-refresh notification: every audit broadcast triggers an
	// immediate status push to all connected SSE clients.
	bc.onNotify = api.notifyStatus

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

	// Config API — read and mutate the running config, persisted to disk.
	// /config/download and /config/services/ must be registered before /config
	// so the more-specific patterns take precedence.
	mux.HandleFunc("/config/download", withTimeout(api.handleConfigDownload, defaultTimeout))
	mux.HandleFunc("/config/services/", limitRequestBody(withTimeout(api.handleConfigService, defaultTimeout), maxRequestBodySize))
	mux.HandleFunc("/config/registry", limitRequestBody(withTimeout(api.handlePutRegistry, defaultTimeout), maxRequestBodySize))
	mux.HandleFunc("/config/monitor", limitRequestBody(withTimeout(api.handlePutMonitor, defaultTimeout), maxRequestBodySize))
	mux.HandleFunc("/config/notifications", limitRequestBody(withTimeout(api.handlePutNotifications, defaultTimeout), maxRequestBodySize))
	mux.HandleFunc("/config", withTimeout(api.handleGetConfig, defaultTimeout))

	return api
}

// Run starts all HTTP servers. Blocks until ctx is cancelled.
func (a *API) Run(ctx context.Context) {
	// Fallback: forcefully close all servers if graceful shutdown doesn't finish.
	saferun.Go("api-shutdown", func() {
		<-ctx.Done()
		time.Sleep(35 * time.Second)
		for _, srv := range a.servers {
			if err := srv.Close(); err != nil {
				logger.Printf("[api] server close error (%s): %v", srv.Addr, err)
			}
		}
	})

	// Launch a listener goroutine for every address after the first.
	for i := 1; i < len(a.servers); i++ {
		srv := a.servers[i]
		saferun.Go("api-listen-"+srv.Addr, func() {
			logger.Printf("[api] listening on %s", srv.Addr)
			if err := srv.ListenAndServe(); err != http.ErrServerClosed {
				logger.Printf("[api] server error (%s): %v", srv.Addr, err)
			}
		})
	}

	// Block on the first server in the calling goroutine (keeps Run blocking).
	logger.Printf("[api] listening on %s", a.servers[0].Addr)
	if err := a.servers[0].ListenAndServe(); err != http.ErrServerClosed {
		logger.Printf("[api] server error (%s): %v", a.servers[0].Addr, err)
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
	if werr := a.audit.Write(audit.Entry{
		Event:   "manual_trigger",
		Message: "Manual update check requested for all services",
		Level:   "info",
	}); werr != nil {
		logger.Printf("[api] ERROR: audit write error: %v", werr)
	}
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
			if werr := a.audit.Write(audit.Entry{
				Service: svc.Name,
				Event:   "manual_trigger",
				Message: "Manual update check requested",
				Level:   "info",
			}); werr != nil {
				logger.Printf("[api] ERROR: audit write error: %v", werr)
			}
			saferun.Go("manual-trigger-"+svc.Name, func() {
				ctx := context.Background()
				if err := a.updater.checkAndUpdate(ctx, svc, true); err != nil {
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
// MountInfo describes a single volume/bind mount on a container.
type MountInfo struct {
	Type        string `json:"type"`        // bind, volume, tmpfs
	Name        string `json:"name,omitempty"` // volume name (empty for binds)
	Source      string `json:"source"`      // host path
	Destination string `json:"destination"` // container path
	ReadOnly    bool   `json:"read_only"`
}

// ImageInfo describes a deployed image for a service.
type ImageInfo struct {
	Image  string `json:"image"`            // full image reference (e.g. localhost:5000/myapp:latest)
	Digest string `json:"digest"`           // full digest (sha256:...)
	Short  string `json:"short"`            // short digest for display (sha256:abcdef012)
	SizeMB int64  `json:"size_mb,omitempty"` // uncompressed size in megabytes
}

type ContainerInfo struct {
	ID            string      `json:"id"`
	Name          string      `json:"name"`
	State         string      `json:"state"`
	Status        string      `json:"status"`
	Image         string      `json:"image"`
	Mounts        []MountInfo `json:"mounts,omitempty"`
	CPUPercent    float64     `json:"cpu_percent,omitempty"`
	MemoryPercent float64     `json:"memory_percent,omitempty"`
	MemoryUsageMB float64     `json:"memory_usage_mb,omitempty"`
	MemoryLimitMB float64     `json:"memory_limit_mb,omitempty"`
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
	Images []ImageInfo `json:"images,omitempty"`
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
			sizeMB := d.Size / (1024 * 1024)
			s.Images = append(s.Images, ImageInfo{
				Image:  d.Image,
				Digest: d.Digest,
				Short:  shortDigest(d.Digest),
				SizeMB: sizeMB,
			})
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
		if werr := a.audit.Write(audit.Entry{
			Service: serviceName,
			Event:   "unblocked",
			Message: "Service manually unblocked via API",
			Level:   "info",
		}); werr != nil {
			logger.Printf("[api] ERROR: audit write error: %v", werr)
		}
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
	out := a.metrics.Prometheus()
	out += "# HELP watcher_active_deploys Number of services currently deploying\n"
	out += "# TYPE watcher_active_deploys gauge\n"
	out += fmt.Sprintf("watcher_active_deploys %d\n", a.updater.DeployingCount())
	if _, err := w.Write([]byte(out)); err != nil {
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

	if a.updater.UnblockService(serviceName) {
		if werr := a.audit.Write(audit.Entry{
			Service: serviceName,
			Event:   "unblocked",
			Message: "Service manually unblocked via web UI",
			Level:   "info",
		}); werr != nil {
			logger.Printf("[api] ERROR: audit write error: %v", werr)
		}
	}
	http.Redirect(w, r, "/ui", http.StatusSeeOther)
}



const dataStarHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>Dockward</title>
  <!-- Prevent flash: apply theme before paint -->
  <script>
    (function(){
      var t = localStorage.getItem('dw-theme');
      if (!t) t = window.matchMedia('(prefers-color-scheme:light)').matches ? 'light' : 'dark';
      document.documentElement.setAttribute('data-theme', t);
    })();
  </script>
  <style>
    :root, [data-theme="dark"] {
      --bg: #0e0e0e;
      --surface: #181818;
      --surface2: #242424;
      --border: #2a2a2a;
      --text: #f0f0f0;
      --text-dim: #b0b0b0;
      --text-faint: #808080;
      --success: #4caf50;
      --error: #ef5350;
      --warning: #ffa726;
      --info: #42a5f5;
      --success-text: #fff;
      --error-text: #fff;
      --info-text: #fff;
      --warning-text: #000;
    }
    [data-theme="light"] {
      --bg: #f5f5f5;
      --surface: #fff;
      --surface2: #e8e8e8;
      --border: #c0c0c0;
      --text: #111;
      --text-dim: #444;
      --text-faint: #666;
      --success: #2e7d32;
      --error: #c62828;
      --warning: #e65100;
      --info: #1565c0;
      --success-text: #fff;
      --error-text: #fff;
      --info-text: #fff;
      --warning-text: #fff;
    }
    @import url('https://fonts.googleapis.com/css2?family=JetBrains+Mono:wght@400;500;600;700&display=swap');
    *, *::before, *::after { margin:0; padding:0; box-sizing:border-box; }
    body { font-family: 'JetBrains Mono', 'SF Mono', 'Cascadia Code', 'Fira Code', Menlo, Consolas, monospace; font-size:0.9rem; background:var(--bg); color:var(--text); line-height:1.5; }
    .wrap { max-width:1400px; margin:0 auto; padding:1.25rem; }

    /* Header */
    header { display:flex; justify-content:space-between; align-items:center; padding:0.5rem 0 0.75rem; border-bottom:1px solid var(--border); margin-bottom:1rem; }
    header h1 { font-size:1.1rem; font-weight:600; letter-spacing:1px; text-transform:uppercase; }
    .header-right { display:flex; align-items:center; gap:0.75rem; font-size:0.85rem; color:var(--text-dim); }
    .dot { width:7px; height:7px; border-radius:50%; display:inline-block; }
    .dot.ok { background:var(--success); }
    .dot.err { background:var(--error); }
    .dot.wait { background:var(--warning); animation:pulse 1.2s infinite; }
    @keyframes pulse { 0%,100%{opacity:1;} 50%{opacity:0.4;} }

    /* Section */
    section { margin-bottom:1.5rem; }
    section h2 { font-size:0.8rem; font-weight:600; text-transform:uppercase; letter-spacing:1.5px; color:var(--text-dim); margin-bottom:0.5rem; }

    /* Table */
    table { width:100%; border-collapse:collapse; }
    th { text-align:left; padding:0.5rem 0.75rem; background:var(--surface); border-bottom:1px solid var(--border); font-size:0.8rem; text-transform:uppercase; letter-spacing:1px; color:var(--text-faint); font-weight:500; }
    td { padding:0.5rem 0.75rem; border-bottom:1px solid var(--border); vertical-align:top; }
    tr.checking { background:rgba(66,165,245,0.04); }

    /* Badge */
    .badge { display:inline-block; padding:2px 8px; border-radius:3px; font-size:0.8rem; font-weight:600; text-transform:uppercase; letter-spacing:0.5px; }
    .badge.ok { background:var(--success); color:var(--success-text); }
    .badge.unknown { background:var(--surface2); color:var(--text-dim); }
    .badge.unhealthy, .badge.degraded, .badge.exhausted, .badge.errored { background:var(--error); color:var(--error-text); }
    .badge.deploying { background:var(--info); color:var(--info-text); }
    .badge.blocked, .badge.not_found { background:var(--warning); color:var(--warning-text); }
    .badge.info { background:var(--info); color:var(--info-text); }
    .badge.warning { background:var(--warning); color:var(--warning-text); }
    .badge.error, .badge.critical { background:var(--error); color:var(--error-text); }

    /* Tooltip */
    [data-tip] { position:relative; cursor:help; }
    [data-tip]:hover::after { content:attr(data-tip); position:absolute; bottom:calc(100% + 4px); left:50%; transform:translateX(-50%); background:var(--text); color:var(--bg); padding:2px 6px; border-radius:3px; font-size:0.7rem; font-weight:400; white-space:nowrap; z-index:10; pointer-events:none; }
    [data-tip].tip-block:hover::after { white-space:normal; max-width:320px; text-align:left; }

    /* Config flags */
    .flags { display:flex; gap:3px; }
    .flag { width:22px; height:22px; border-radius:3px; display:inline-flex; align-items:center; justify-content:center; font-size:0.8rem; font-weight:700; }
    .flag.on { background:var(--success); color:#fff; }
    .flag.off { background:var(--surface2); color:var(--text-faint); }

    /* Containers */
    .containers { margin-top:4px; }
    .ct-row { display:flex; align-items:baseline; gap:0.5rem; padding:2px 0; font-size:0.85rem; }
    .ct-name { color:var(--text); font-weight:500; }
    .ct-state { color:var(--text-dim); }
    .ct-stats { color:var(--text-faint); font-size:0.8rem; }

    /* Mounts toggle */
    .mounts-toggle { background:none; border:none; color:var(--text-faint); font-size:0.8rem; cursor:pointer; padding:2px 0; margin-left:0; font-family:inherit; }
    .mounts-toggle:hover { color:var(--text-dim); }
    .mounts-list { margin-left:0; padding-left:12px; border-left:1px solid var(--border); margin-top:2px; }
    .mounts-list.hidden { display:none; }
    .mt-row { display:flex; align-items:baseline; gap:0.35rem; padding:1px 0; font-size:0.8rem; color:var(--text-faint); }
    .mt-type { color:var(--text-faint); min-width:36px; }
    .mt-path { color:var(--text-dim); word-break:break-all; }
    .mt-ro { color:var(--warning); font-size:0.8rem; }

    /* Images */
    .images { margin-top:4px; }
    .im-row { display:flex; align-items:baseline; gap:0.5rem; padding:2px 0; font-size:0.85rem; }
    .im-name { color:var(--text); }
    .im-digest { color:var(--text-faint); font-size:0.8rem; }
    .im-size { color:var(--text-faint); font-size:0.8rem; }

    /* Event detail */
    .ev-detail { font-size:0.8rem; color:var(--text-faint); margin-top:2px; }
    .ev-output { font-size:0.8rem; color:var(--text-faint); margin-top:3px; white-space:pre-wrap; word-break:break-all; background:var(--surface); border-radius:3px; padding:4px 6px; max-height:6rem; overflow-y:auto; }

    /* Stats */
    .stats { font-size:0.85rem; color:var(--text-dim); line-height:1.6; white-space:nowrap; }

    /* Buttons */
    .actions { display:flex; gap:4px; }
    .btn { padding:4px 10px; background:var(--surface2); color:var(--text); border:1px solid var(--border); border-radius:3px; font-size:0.85rem; font-family:inherit; cursor:pointer; transition:background 0.15s, border-color 0.15s; }
    .btn:hover { background:var(--surface); border-color:var(--text-dim); }
    .btn:disabled { opacity:0.3; cursor:not-allowed; }

    /* Events table */
    .ev-time { font-size:0.85rem; color:var(--text-dim); white-space:nowrap; }
    .ev-msg { font-size:0.85rem; color:var(--text-dim); }

    /* Empty state */
    .empty { text-align:center; padding:2rem; color:var(--text-faint); font-size:0.9rem; }

    /* Config section */
    .cfg-msg { min-height:1.4rem; font-size:0.85rem; margin-bottom:0.5rem; }
    .cfg-msg.ok { color:var(--success); }
    .cfg-msg.error { color:var(--error); }
    .cfg-accordion { border:1px solid var(--border); border-radius:3px; margin-bottom:0.4rem; }
    .cfg-accordion-btn { width:100%; text-align:left; padding:0.45rem 0.75rem; background:var(--surface); border:none; color:var(--text); font-family:inherit; font-size:0.85rem; font-weight:600; cursor:pointer; display:flex; justify-content:space-between; align-items:center; }
    .cfg-accordion-btn:hover { background:var(--surface2); }
    .cfg-accordion-body { padding:0.75rem; border-top:1px solid var(--border); }
    .form-row { display:flex; gap:0.6rem; flex-wrap:wrap; margin-bottom:0.5rem; }
    .form-group { display:flex; flex-direction:column; gap:0.2rem; flex:1; min-width:140px; }
    .form-group.full { flex-basis:100%; min-width:100%; }
    .form-label { font-size:0.78rem; color:var(--text-dim); }
    .form-input { background:var(--surface); border:1px solid var(--border); color:var(--text); padding:4px 8px; border-radius:3px; font-family:inherit; font-size:0.85rem; }
    .form-input:focus { outline:none; border-color:var(--text-dim); }
    textarea.form-input { resize:vertical; min-height:64px; }
    .form-check { flex-direction:row !important; align-items:center; gap:0.4rem; min-width:auto; flex:none; }
    .form-check input[type=checkbox] { accent-color:var(--info); width:14px; height:14px; flex-shrink:0; }
    .cfg-svc-title { display:flex; justify-content:space-between; align-items:center; margin:0.9rem 0 0.4rem; }
    .cfg-svc-label { font-size:0.8rem; font-weight:600; text-transform:uppercase; letter-spacing:1.5px; color:var(--text-dim); }
    .field-group-label { font-size:0.75rem; font-weight:600; text-transform:uppercase; letter-spacing:1px; color:var(--text-faint); margin:0.65rem 0 0.25rem; }

    /* Modal */
    .modal-overlay { position:fixed; inset:0; background:rgba(0,0,0,0.55); z-index:100; display:flex; align-items:center; justify-content:center; }
    .modal-overlay.hidden { display:none; }
    .modal-box { background:var(--surface); border:1px solid var(--border); border-radius:4px; width:min(680px, 95vw); max-height:88vh; display:flex; flex-direction:column; }
    .modal-header { display:flex; justify-content:space-between; align-items:center; padding:0.65rem 1rem; border-bottom:1px solid var(--border); }
    .modal-header h3 { font-size:0.95rem; font-weight:600; }
    .modal-close { background:none; border:none; color:var(--text-dim); font-size:1.3rem; cursor:pointer; line-height:1; padding:0 4px; font-family:inherit; }
    .modal-close:hover { color:var(--text); }
    .modal-body { padding:1rem; overflow-y:auto; flex:1; }
    .modal-footer { padding:0.65rem 1rem; border-top:1px solid var(--border); display:flex; gap:0.5rem; justify-content:flex-end; }
    .btn-primary { background:var(--info); color:var(--info-text); border-color:var(--info); }
    .btn-primary:hover { background:var(--info); filter:brightness(1.1); }
  </style>
</head>
<body>
<div class="wrap">
  <header>
    <h1>Dockward</h1>
    <div class="header-right">
      <button class="btn" id="theme-btn" onclick="toggleTheme()" data-tip="Toggle light/dark mode"></button>
      <span class="dot wait" id="conn-dot"></span>
      <span id="conn-label">Connecting...</span>
      <span id="updated"></span>
    </div>
  </header>

  <section>
    <h2>Services</h2>
    <table>
      <thead>
        <tr>
          <th>Service</th>
          <th>Status</th>
          <th data-tip="Auto Update / Auto Heal / Auto Start">Config</th>
          <th data-tip="Time until next update check">Next</th>
          <th data-tip="U=Updates R=Rollbacks H=Heals F=Failures">Stats</th>
          <th>Actions</th>
        </tr>
      </thead>
      <tbody id="svc-body">
        <tr><td colspan="6" class="empty">Loading...</td></tr>
      </tbody>
    </table>
  </section>

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
      <tbody id="ev-body">
        <tr><td colspan="5" class="empty">No events yet</td></tr>
      </tbody>
    </table>
  </section>
  <section>
    <h2>Config</h2>
    <div id="cfg-msg" class="cfg-msg"></div>

    <div class="cfg-accordion">
      <button class="cfg-accordion-btn" onclick="toggleCfgSection('cfg-registry')"><span>Registry</span><span>&#9662;</span></button>
      <div id="cfg-registry" class="cfg-accordion-body hidden">
        <div class="form-row">
          <div class="form-group" style="flex:2">
            <label class="form-label" for="reg-url">URL</label>
            <input id="reg-url" type="text" class="form-input" placeholder="http://localhost:5000">
          </div>
          <div class="form-group">
            <label class="form-label" for="reg-interval">Poll Interval (s)</label>
            <input id="reg-interval" type="number" class="form-input" min="10" max="86400" placeholder="300">
          </div>
          <div class="form-group form-check" style="align-self:flex-end;padding-bottom:2px">
            <input id="reg-insecure" type="checkbox">
            <label class="form-label" for="reg-insecure">Skip TLS</label>
          </div>
        </div>
        <button class="btn" onclick="saveRegistry()">Save Registry</button>
      </div>
    </div>

    <div class="cfg-accordion">
      <button class="cfg-accordion-btn" onclick="toggleCfgSection('cfg-monitor')"><span>Monitor</span><span>&#9662;</span></button>
      <div id="cfg-monitor" class="cfg-accordion-body hidden">
        <div class="form-row">
          <div class="form-group" style="max-width:220px">
            <label class="form-label" for="mon-interval">Stats Interval (s) &mdash; 0 = use poll interval</label>
            <input id="mon-interval" type="number" class="form-input" min="0" placeholder="0">
          </div>
        </div>
        <button class="btn" onclick="saveMonitor()">Save Monitor</button>
      </div>
    </div>

    <div class="cfg-accordion">
      <button class="cfg-accordion-btn" onclick="toggleCfgSection('cfg-notifications')"><span>Notifications</span><span>&#9662;</span></button>
      <div id="cfg-notifications" class="cfg-accordion-body hidden">
        <div class="form-row">
          <div class="form-group full">
            <label class="form-label" for="notif-discord-url">Discord Webhook URL</label>
            <input id="notif-discord-url" type="text" class="form-input" placeholder="https://discord.com/api/webhooks/...">
          </div>
        </div>
        <button class="btn" onclick="saveNotifications()">Save Notifications</button>
      </div>
    </div>

    <div class="cfg-svc-title">
      <span class="cfg-svc-label">Services</span>
      <button class="btn" onclick="openServiceModal(null)">+ Add Service</button>
    </div>
    <table>
      <thead>
        <tr>
          <th>Name</th>
          <th>Images</th>
          <th data-tip="Auto Update / Auto Heal / Auto Start">Config</th>
          <th>Actions</th>
        </tr>
      </thead>
      <tbody id="cfg-svc-body">
        <tr><td colspan="4" class="empty">Loading...</td></tr>
      </tbody>
    </table>
  </section>
</div>

<div id="svc-modal" class="modal-overlay hidden">
  <div class="modal-box">
    <div class="modal-header">
      <h3 id="svc-modal-title">Add Service</h3>
      <button class="modal-close" onclick="closeServiceModal()">&times;</button>
    </div>
    <div class="modal-body">
      <div class="field-group-label">Identity</div>
      <div class="form-row">
        <div class="form-group">
          <label class="form-label" for="svc-name">Name *</label>
          <input id="svc-name" type="text" class="form-input" placeholder="my-app">
        </div>
        <div class="form-group">
          <label class="form-label" for="svc-compose-project">Compose Project</label>
          <input id="svc-compose-project" type="text" class="form-input" placeholder="my-app">
        </div>
        <div class="form-group">
          <label class="form-label" for="svc-container-name">Container Name</label>
          <input id="svc-container-name" type="text" class="form-input" placeholder="my-app-1">
        </div>
      </div>
      <div class="field-group-label">Images &amp; Files</div>
      <div class="form-row">
        <div class="form-group">
          <label class="form-label" for="svc-images">Images (one per line)</label>
          <textarea id="svc-images" class="form-input" placeholder="localhost:5000/my-app:latest"></textarea>
        </div>
        <div class="form-group">
          <label class="form-label" for="svc-compose-files">Compose Files (one per line, absolute paths)</label>
          <textarea id="svc-compose-files" class="form-input" placeholder="/opt/my-app/docker-compose.yml"></textarea>
        </div>
      </div>
      <div class="form-row">
        <div class="form-group full">
          <label class="form-label" for="svc-env-file">Env File (absolute path)</label>
          <input id="svc-env-file" type="text" class="form-input" placeholder="/opt/my-app/.env">
        </div>
      </div>
      <div class="field-group-label">Behaviour</div>
      <div class="form-row" style="flex-wrap:wrap;gap:0.75rem 1.25rem">
        <div class="form-group form-check"><input id="svc-auto-update" type="checkbox"><label class="form-label" for="svc-auto-update">Auto Update</label></div>
        <div class="form-group form-check"><input id="svc-auto-heal" type="checkbox"><label class="form-label" for="svc-auto-heal">Auto Heal</label></div>
        <div class="form-group form-check"><input id="svc-auto-start" type="checkbox"><label class="form-label" for="svc-auto-start">Auto Start</label></div>
        <div class="form-group form-check"><input id="svc-compose-watch" type="checkbox"><label class="form-label" for="svc-compose-watch">Compose Watch</label></div>
        <div class="form-group form-check"><input id="svc-silent" type="checkbox"><label class="form-label" for="svc-silent">Silent</label></div>
      </div>
      <div class="field-group-label">Timing</div>
      <div class="form-row">
        <div class="form-group">
          <label class="form-label" for="svc-health-grace">Health Grace (s)</label>
          <input id="svc-health-grace" type="number" class="form-input" min="0" value="60">
        </div>
        <div class="form-group">
          <label class="form-label" for="svc-heal-cooldown">Heal Cooldown (s)</label>
          <input id="svc-heal-cooldown" type="number" class="form-input" min="0" value="300">
        </div>
        <div class="form-group">
          <label class="form-label" for="svc-heal-max-restarts">Max Restarts</label>
          <input id="svc-heal-max-restarts" type="number" class="form-input" min="0" value="3">
        </div>
      </div>
      <div class="field-group-label">Thresholds (0 = disabled)</div>
      <div class="form-row">
        <div class="form-group">
          <label class="form-label" for="svc-cpu-threshold">CPU % Alert</label>
          <input id="svc-cpu-threshold" type="number" class="form-input" min="0" max="100" step="0.1" value="0">
        </div>
        <div class="form-group">
          <label class="form-label" for="svc-memory-threshold">Memory % Alert</label>
          <input id="svc-memory-threshold" type="number" class="form-input" min="0" max="100" step="0.1" value="0">
        </div>
      </div>
    </div>
    <div class="modal-footer">
      <button class="btn" onclick="closeServiceModal()">Cancel</button>
      <button class="btn btn-primary" onclick="saveService()">Save</button>
    </div>
  </div>
</div>

<script>
(function(){
  var events = [];
  var MAX_EVENTS = 50;
  var lastServices = [];

  // ---- helpers ----
  function esc(s) {
    if (s == null) return '';
    return String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;');
  }

  function fmtCountdown(ts) {
    if (!ts) return '--';
    var d = new Date(ts) - Date.now();
    if (d <= 0) return 'now';
    var s = Math.round(d / 1000);
    if (s < 60) return s + 's';
    var m = Math.floor(s / 60), rs = s % 60;
    if (m < 60) return m + 'm ' + (rs < 10 ? '0' : '') + rs + 's';
    return Math.floor(m / 60) + 'h ' + (m % 60) + 'm';
  }

  function fmtTime(ts) {
    if (!ts) return '--';
    var d = new Date(ts) - Date.now();
    if (d < 0) return 'now';
    if (d < 60000) return Math.round(d/1000) + 's';
    if (d < 3600000) return Math.round(d/60000) + 'm';
    return Math.round(d/3600000) + 'h';
  }

  // ---- countdown ticker — updates Next column every second without full re-render ----
  function tickCountdowns() {
    for (var i = 0; i < lastServices.length; i++) {
      var el = document.getElementById('next-' + i);
      if (!el) continue;
      var s = lastServices[i];
      if (s.check_status === 'checking') {
        el.innerHTML = '<span style="color:var(--warning)">checking</span>';
      } else {
        el.innerHTML = '<span style="color:var(--text-dim)">' + fmtCountdown(s.next_check) + '</span>';
      }
    }
  }
  setInterval(tickCountdowns, 1000);

  function flagHtml(label, on) {
    return '<span class="flag ' + (on ? 'on' : 'off') + '" data-tip="' + esc(label) + '">' + esc(label.charAt(0)) + '</span>';
  }

  // ---- mount toggle (global) ----
  window.toggleMounts = function(id) {
    var el = document.getElementById(id);
    var btn = el.previousElementSibling;
    if (el.classList.contains('hidden')) {
      el.classList.remove('hidden');
      btn.textContent = btn.textContent.replace('\u25b8', '\u25be');
    } else {
      el.classList.add('hidden');
      btn.textContent = btn.textContent.replace('\u25be', '\u25b8');
    }
  };

  // ---- render services ----
  function renderServices(services) {
    lastServices = services || [];
    var tb = document.getElementById('svc-body');
    if (!services || !services.length) {
      tb.innerHTML = '<tr><td colspan="6" class="empty">No services</td></tr>';
      return;
    }

    var html = '';
    for (var i = 0; i < services.length; i++) {
      var s = services[i];
      var isChecking = s.check_status === 'checking';

      html += '<tr class="' + (isChecking ? 'checking' : '') + '">';

      // Service name + containers + mounts + images
      html += '<td><strong>' + esc(s.name) + '</strong>';
      if (s.containers && s.containers.length) {
        html += '<div class="containers">';
        for (var c = 0; c < s.containers.length; c++) {
          var ct = s.containers[c];
          var cCpu = Math.round(ct.cpu_percent || 0);
          var cMem = Math.round(ct.memory_usage_mb || 0);
          html += '<div class="ct-row">';
          html += '<span class="ct-name">' + esc(ct.name) + '</span>';
          html += '<span class="ct-state">' + esc(ct.state) + '</span>';
          if (ct.cpu_percent || ct.memory_usage_mb) {
            html += '<span class="ct-stats">' + cCpu + '% cpu &middot; ' + cMem + 'MB mem</span>';
          }
          html += '</div>';
          // Mounts: sorted, collapsed by default
          if (ct.mounts && ct.mounts.length) {
            var sorted = ct.mounts.slice().sort(function(a, b) {
              return (a.destination || '').localeCompare(b.destination || '');
            });
            var mid = 'mt-' + i + '-' + c;
            html += '<button class="mounts-toggle" onclick="toggleMounts(\'' + mid + '\')">\u25b8 ' + sorted.length + ' mount' + (sorted.length !== 1 ? 's' : '') + '</button>';
            html += '<div class="mounts-list hidden" id="' + mid + '">';
            for (var m = 0; m < sorted.length; m++) {
              var mt = sorted[m];
              var label = mt.name || mt.source;
              html += '<div class="mt-row">';
              html += '<span class="mt-type">' + esc(mt.type) + '</span>';
              html += '<span class="mt-path">' + esc(label) + ' \u2192 ' + esc(mt.destination) + '</span>';
              if (mt.read_only) html += ' <span class="mt-ro">ro</span>';
              html += '</div>';
            }
            html += '</div>';
          }
        }
        html += '</div>';
      }
      if (s.images && s.images.length) {
        html += '<div class="images">';
        for (var im = 0; im < s.images.length; im++) {
          var img = s.images[im];
          html += '<div class="im-row">';
          html += '<span class="im-name">' + esc(img.image) + '</span>';
          if (img.size_mb) html += '<span class="im-size">' + img.size_mb + 'MB</span>';
          html += '<span class="im-digest">' + esc(img.short) + '</span>';
          html += '</div>';
        }
        html += '</div>';
      }
      html += '</td>';

      // Status
      html += '<td><span class="badge ' + esc(s.status) + '">' + esc(s.status) + '</span>';
      if (s.errored) html += ' <span class="tip-block" data-tip="' + esc(s.errored) + '">&#9888;</span>';
      html += '</td>';

      // Config flags
      html += '<td><div class="flags">';
      html += flagHtml('Update', s.auto_update);
      html += flagHtml('Heal', s.auto_heal);
      html += flagHtml('Start', s.auto_start);
      html += '</div></td>';

      // Next check — id used by tickCountdowns() for in-place updates
      html += '<td id="next-' + i + '">';
      if (isChecking) {
        html += '<span style="color:var(--warning)">checking</span>';
      } else {
        html += '<span style="color:var(--text-dim)">' + fmtCountdown(s.next_check) + '</span>';
      }
      html += '</td>';

      // Stats
      html += '<td class="stats">';
      html += '<span data-tip="Updates deployed">U:' + (s.updates_total||0) + '</span> <span data-tip="Rollbacks performed">R:' + (s.rollbacks_total||0) + '</span><br>';
      html += '<span data-tip="Heals (container restarts)">H:' + (s.restarts_total||0) + '</span> <span data-tip="Failures">F:' + (s.failures_total||0) + '</span>';
      html += '</td>';

      // Actions
      html += '<td><div class="actions">';
      html += '<button class="btn" onclick="triggerSvc(\'' + esc(s.name) + '\')"' + (isChecking ? ' disabled' : '') + '>Trigger</button>';
      html += '<button class="btn" onclick="redeploySvc(\'' + esc(s.name) + '\')">Redeploy</button>';
      if (s.blocked) {
        html += '<button class="btn" onclick="unblockSvc(\'' + esc(s.name) + '\')">Unblock</button>';
      }
      html += '</div></td>';

      html += '</tr>';
    }
    tb.innerHTML = html;
  }

  // ---- render events ----
  function shortSha(d) {
    if (!d) return '';
    return d.length > 19 ? d.substring(0, 19) : d;
  }

  function renderEvents() {
    var tb = document.getElementById('ev-body');
    if (!events.length) {
      tb.innerHTML = '<tr><td colspan="5" class="empty">No events yet</td></tr>';
      return;
    }
    var html = '';
    for (var i = 0; i < events.length; i++) {
      var e = events[i];
      var t = e.timestamp ? new Date(e.timestamp).toLocaleString() : '';
      var lvl = e.level || 'info';
      if (lvl === 'critical') lvl = 'error';
      html += '<tr>';
      html += '<td class="ev-time">' + esc(t) + '</td>';
      html += '<td>' + esc(e.service) + '</td>';
      html += '<td>' + esc(e.event) + '</td>';
      html += '<td><span class="badge ' + esc(lvl) + '">' + esc(e.level) + '</span></td>';
      html += '<td class="ev-msg">' + esc(e.message);
      // Details line: digest transition, container, reason
      var details = [];
      if (e.old_digest && e.new_digest) {
        details.push(shortSha(e.old_digest) + ' &rarr; ' + shortSha(e.new_digest));
      } else if (e.new_digest) {
        details.push(shortSha(e.new_digest));
      }
      if (e.container) details.push(esc(e.container));
      if (e.reason) details.push(esc(e.reason));
      if (details.length) {
        html += '<div class="ev-detail">' + details.join(' &middot; ') + '</div>';
      }
      if (e.output) {
        html += '<div class="ev-output">' + esc(e.output) + '</div>';
      }
      html += '</td>';
      html += '</tr>';
    }
    tb.innerHTML = html;
  }

  // ---- connection status ----
  function setConn(status) {
    var dot = document.getElementById('conn-dot');
    var label = document.getElementById('conn-label');
    dot.className = 'dot ' + (status === 'connected' ? 'ok' : status === 'error' ? 'err' : 'wait');
    label.textContent = status === 'connected' ? 'Live' : status === 'error' ? 'Disconnected' : 'Connecting...';
  }

  function setUpdated() {
    document.getElementById('updated').textContent = 'Updated ' + new Date().toLocaleTimeString();
  }

  // ---- SSE ----
  function connect() {
    var es = new EventSource('/ui/stream');

    es.addEventListener('status', function(e) {
      try {
        var data = JSON.parse(e.data);
        renderServices(data.services || []);
        setConn('connected');
        setUpdated();
      } catch(err) {
        console.error('status parse error:', err);
      }
    });

    es.addEventListener('audit', function(e) {
      try {
        var ev = JSON.parse(e.data);
        events.unshift(ev);
        if (events.length > MAX_EVENTS) events.length = MAX_EVENTS;
        renderEvents();
      } catch(err) {
        console.error('audit parse error:', err);
      }
    });

    es.onerror = function() {
      setConn('error');
    };

    es.onopen = function() {
      setConn('connected');
    };
  }

  // ---- actions (global) ----
  window.triggerSvc = function(name) {
    fetch('/trigger/' + encodeURIComponent(name), { method: 'POST' });
  };

  window.redeploySvc = function(name) {
    if (!confirm('Redeploy ' + name + '?')) return;
    fetch('/command-preview/' + encodeURIComponent(name))
      .then(function(r) { return r.json(); })
      .then(function(p) {
        if (confirm('Execute: ' + p.command + '?')) {
          fetch('/redeploy/' + encodeURIComponent(name), { method: 'POST' });
        }
      });
  };

  window.unblockSvc = function(name) {
    fetch('/unblock/' + encodeURIComponent(name), { method: 'POST' });
  };

  // ---- theme toggle ----
  function updateThemeBtn() {
    var cur = document.documentElement.getAttribute('data-theme') || 'dark';
    document.getElementById('theme-btn').textContent = cur === 'dark' ? 'Light' : 'Dark';
  }
  window.toggleTheme = function() {
    var cur = document.documentElement.getAttribute('data-theme') || 'dark';
    var next = cur === 'dark' ? 'light' : 'dark';
    document.documentElement.setAttribute('data-theme', next);
    localStorage.setItem('dw-theme', next);
    updateThemeBtn();
  };
  updateThemeBtn();

  // ---- config management ----
  var currentConfig = null;
  var editingServiceName = null;

  function loadConfig() {
    fetch('/config')
      .then(function(r) { return r.json(); })
      .then(function(cfg) {
        currentConfig = cfg;
        renderConfigServices(cfg.services || []);
        populateRegistryForm(cfg.registry || {});
        populateMonitorForm(cfg.monitor || {});
        populateNotificationsForm(cfg.notifications || {});
      })
      .catch(function(e) { console.error('config load error:', e); });
  }

  function renderConfigServices(services) {
    var tb = document.getElementById('cfg-svc-body');
    if (!services || !services.length) {
      tb.innerHTML = '<tr><td colspan="4" class="empty">No services configured</td></tr>';
      return;
    }
    var html = '';
    for (var i = 0; i < services.length; i++) {
      var s = services[i];
      html += '<tr>';
      html += '<td><strong>' + esc(s.name) + '</strong></td>';
      html += '<td style="font-size:0.8rem;color:var(--text-dim)">' + esc((s.images || []).join(', ')) + '</td>';
      html += '<td><div class="flags">' + flagHtml('Update', s.auto_update) + flagHtml('Heal', s.auto_heal) + flagHtml('Start', s.auto_start) + '</div></td>';
      html += '<td><div class="actions">';
      html += '<button class="btn" onclick="openServiceModal(\'' + esc(s.name) + '\')">Edit</button>';
      html += '<button class="btn" onclick="deleteService(\'' + esc(s.name) + '\')">Delete</button>';
      html += '</div></td>';
      html += '</tr>';
    }
    tb.innerHTML = html;
  }

  function populateRegistryForm(reg) {
    document.getElementById('reg-url').value = reg.url || '';
    document.getElementById('reg-interval').value = reg.poll_interval || 300;
    document.getElementById('reg-insecure').checked = !!reg.insecure;
  }

  function populateMonitorForm(mon) {
    document.getElementById('mon-interval').value = mon.stats_interval || 0;
  }

  function populateNotificationsForm(notif) {
    document.getElementById('notif-discord-url').value = (notif && notif.discord && notif.discord.webhook_url) || '';
  }

  window.toggleCfgSection = function(id) {
    document.getElementById(id).classList.toggle('hidden');
  };

  window.openServiceModal = function(name) {
    editingServiceName = name || null;
    document.getElementById('svc-modal-title').textContent = name ? 'Edit Service' : 'Add Service';
    var nameInput = document.getElementById('svc-name');
    nameInput.disabled = !!name;

    // reset
    document.getElementById('svc-images').value = '';
    document.getElementById('svc-compose-files').value = '';
    document.getElementById('svc-compose-project').value = '';
    document.getElementById('svc-container-name').value = '';
    document.getElementById('svc-env-file').value = '';
    document.getElementById('svc-auto-update').checked = false;
    document.getElementById('svc-auto-heal').checked = false;
    document.getElementById('svc-auto-start').checked = false;
    document.getElementById('svc-compose-watch').checked = false;
    document.getElementById('svc-silent').checked = false;
    document.getElementById('svc-health-grace').value = 60;
    document.getElementById('svc-heal-cooldown').value = 300;
    document.getElementById('svc-heal-max-restarts').value = 3;
    document.getElementById('svc-cpu-threshold').value = 0;
    document.getElementById('svc-memory-threshold').value = 0;

    if (name && currentConfig) {
      var svc = null;
      for (var i = 0; i < (currentConfig.services || []).length; i++) {
        if (currentConfig.services[i].name === name) { svc = currentConfig.services[i]; break; }
      }
      if (svc) {
        nameInput.value = svc.name || '';
        document.getElementById('svc-images').value = (svc.images || []).join('\n');
        document.getElementById('svc-compose-files').value = (svc.compose_files || []).join('\n');
        document.getElementById('svc-compose-project').value = svc.compose_project || '';
        document.getElementById('svc-container-name').value = svc.container_name || '';
        document.getElementById('svc-env-file').value = svc.env_file || '';
        document.getElementById('svc-auto-update').checked = !!svc.auto_update;
        document.getElementById('svc-auto-heal').checked = !!svc.auto_heal;
        document.getElementById('svc-auto-start').checked = !!svc.auto_start;
        document.getElementById('svc-compose-watch').checked = !!svc.compose_watch;
        document.getElementById('svc-silent').checked = !!svc.silent;
        document.getElementById('svc-health-grace').value = svc.health_grace || 60;
        document.getElementById('svc-heal-cooldown').value = svc.heal_cooldown || 300;
        document.getElementById('svc-heal-max-restarts').value = svc.heal_max_restarts || 3;
        document.getElementById('svc-cpu-threshold').value = svc.cpu_threshold || 0;
        document.getElementById('svc-memory-threshold').value = svc.memory_threshold || 0;
      }
    } else {
      nameInput.value = '';
    }
    document.getElementById('svc-modal').classList.remove('hidden');
  };

  window.closeServiceModal = function() {
    document.getElementById('svc-modal').classList.add('hidden');
    editingServiceName = null;
  };

  window.saveService = function() {
    var name = editingServiceName || document.getElementById('svc-name').value.trim();
    if (!name) { showCfgMsg('Service name is required', 'error'); return; }
    var splitLines = function(v) { return v.split('\n').map(function(s){return s.trim();}).filter(Boolean); };
    var svc = {
      name: name,
      images: splitLines(document.getElementById('svc-images').value),
      compose_files: splitLines(document.getElementById('svc-compose-files').value),
      compose_project: document.getElementById('svc-compose-project').value.trim(),
      container_name: document.getElementById('svc-container-name').value.trim(),
      env_file: document.getElementById('svc-env-file').value.trim(),
      auto_update: document.getElementById('svc-auto-update').checked,
      auto_heal: document.getElementById('svc-auto-heal').checked,
      auto_start: document.getElementById('svc-auto-start').checked,
      compose_watch: document.getElementById('svc-compose-watch').checked,
      silent: document.getElementById('svc-silent').checked,
      health_grace: parseInt(document.getElementById('svc-health-grace').value) || 60,
      heal_cooldown: parseInt(document.getElementById('svc-heal-cooldown').value) || 300,
      heal_max_restarts: parseInt(document.getElementById('svc-heal-max-restarts').value) || 3,
      cpu_threshold: parseFloat(document.getElementById('svc-cpu-threshold').value) || 0,
      memory_threshold: parseFloat(document.getElementById('svc-memory-threshold').value) || 0
    };
    fetch('/config/services/' + encodeURIComponent(name), {
      method: 'PUT',
      headers: {'Content-Type': 'application/json'},
      body: JSON.stringify(svc)
    })
    .then(function(r) { if (!r.ok) return r.text().then(function(t) { throw new Error(t); }); return r.json(); })
    .then(function() { closeServiceModal(); loadConfig(); showCfgMsg('Service saved', 'ok'); })
    .catch(function(e) { showCfgMsg('Error: ' + e.message, 'error'); });
  };

  window.deleteService = function(name) {
    if (!confirm('Delete service "' + name + '"?')) return;
    fetch('/config/services/' + encodeURIComponent(name), {method: 'DELETE'})
      .then(function(r) { if (!r.ok) return r.text().then(function(t) { throw new Error(t); }); return r.json(); })
      .then(function() { loadConfig(); showCfgMsg('Service deleted', 'ok'); })
      .catch(function(e) { showCfgMsg('Error: ' + e.message, 'error'); });
  };

  window.saveRegistry = function() {
    var reg = {
      url: document.getElementById('reg-url').value.trim(),
      poll_interval: parseInt(document.getElementById('reg-interval').value) || 300,
      insecure: document.getElementById('reg-insecure').checked
    };
    fetch('/config/registry', {method:'PUT', headers:{'Content-Type':'application/json'}, body:JSON.stringify(reg)})
      .then(function(r) { if (!r.ok) return r.text().then(function(t) { throw new Error(t); }); return r.json(); })
      .then(function() { showCfgMsg('Registry saved', 'ok'); loadConfig(); })
      .catch(function(e) { showCfgMsg('Error: ' + e.message, 'error'); });
  };

  window.saveMonitor = function() {
    var mon = {stats_interval: parseInt(document.getElementById('mon-interval').value) || 0};
    fetch('/config/monitor', {method:'PUT', headers:{'Content-Type':'application/json'}, body:JSON.stringify(mon)})
      .then(function(r) { if (!r.ok) return r.text().then(function(t) { throw new Error(t); }); return r.json(); })
      .then(function() { showCfgMsg('Monitor saved', 'ok'); loadConfig(); })
      .catch(function(e) { showCfgMsg('Error: ' + e.message, 'error'); });
  };

  window.saveNotifications = function() {
    var discordURL = document.getElementById('notif-discord-url').value.trim();
    var notif = {};
    if (discordURL) notif.discord = {webhook_url: discordURL};
    if (currentConfig && currentConfig.notifications) {
      if (currentConfig.notifications.smtp) notif.smtp = currentConfig.notifications.smtp;
      if (currentConfig.notifications.webhooks) notif.webhooks = currentConfig.notifications.webhooks;
    }
    fetch('/config/notifications', {method:'PUT', headers:{'Content-Type':'application/json'}, body:JSON.stringify(notif)})
      .then(function(r) { if (!r.ok) return r.text().then(function(t) { throw new Error(t); }); return r.json(); })
      .then(function() { showCfgMsg('Notifications saved', 'ok'); loadConfig(); })
      .catch(function(e) { showCfgMsg('Error: ' + e.message, 'error'); });
  };

  function showCfgMsg(msg, type) {
    var el = document.getElementById('cfg-msg');
    el.textContent = msg;
    el.className = 'cfg-msg ' + type;
    setTimeout(function() { el.textContent = ''; el.className = 'cfg-msg'; }, 3000);
  }

  // close modal on overlay click
  document.getElementById('svc-modal').addEventListener('click', function(e) {
    if (e.target === this) closeServiceModal();
  });

  // ---- initial load + connect ----
  fetch('/status')
    .then(function(r) { return r.json(); })
    .then(function(data) {
      renderServices(data.services || []);
      setConn('connected');
      setUpdated();
    })
    .catch(function() {
      setConn('error');
    });

  loadConfig();
  connect();
})();
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
	svcCopy := *svc // copy for goroutine safety
	saferun.Go("force-redeploy-"+svc.Name, func() {
		ctx := context.Background()
		a.updater.tryStartDeploy(svcCopy.Name)
		composeOut, err := compose.Up(ctx, a.updater.cfg.Runtime, svcCopy.ComposeFiles, svcCopy.ComposeProject, svcCopy.EnvFile)
		if err != nil {
			a.updater.clearDeploying(svcCopy.Name)
			logger.Printf("[api] ERROR: force redeploy failed for %s: %v", svcCopy.Name, err)
			if werr := a.audit.Write(audit.Entry{
				Service: svcCopy.Name,
				Event:   "force_redeploy_failed",
				Message: fmt.Sprintf("Force redeploy failed: %v", err),
				Level:   "error",
				Output:  composeOut,
			}); werr != nil {
				logger.Printf("[api] ERROR: audit write error: %v", werr)
			}
			return
		}

		if werr := a.audit.Write(audit.Entry{
			Service: svcCopy.Name,
			Event:   "force_redeploy",
			Message: "Forced redeploy via API",
			Level:   "info",
			Output:  composeOut,
		}); werr != nil {
			logger.Printf("[api] ERROR: audit write error: %v", werr)
		}

		go a.updater.verifyHealthAfterCompose(ctx, svcCopy) // clears deploying when done
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
