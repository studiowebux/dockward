package watcher

import (
	"context"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/studiowebux/dockward/internal/audit"
	"github.com/studiowebux/dockward/internal/compose"
	"github.com/studiowebux/dockward/internal/config"
	"github.com/studiowebux/dockward/internal/hub"
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

// broadcaster adapts hub.Hub to the audit.Broadcaster interface.
// Pattern: adapter.
type broadcaster struct {
	hub *hub.Hub
}

func (b *broadcaster) Broadcast(e audit.Entry) {
	data, err := json.Marshal(e)
	if err != nil {
		log.Printf("[api] broadcaster: marshal error: %v", err)
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
			Addr:         address + ":" + port,
			Handler:      mux,
			ReadTimeout:  5 * time.Second,
			WriteTimeout: 30 * time.Second,
		},
	}

	mux.HandleFunc("/trigger", api.handleTriggerAll)
	mux.HandleFunc("/trigger/", api.handleTriggerService)
	mux.HandleFunc("/blocked", api.handleListBlocked)
	mux.HandleFunc("/blocked/", api.handleUnblockService)
	mux.HandleFunc("/not-found", api.handleListNotFound)
	mux.HandleFunc("/errored", api.handleListErrored)
	mux.HandleFunc("/status", api.handleStatusAll)
	mux.HandleFunc("/status/", api.handleStatusService)
	mux.HandleFunc("/health", api.handleHealth)
	mux.HandleFunc("/metrics", api.handleMetrics)
	mux.HandleFunc("/audit", api.handleAudit)
	mux.HandleFunc("/ui", api.handleUI)
	mux.HandleFunc("/ui/events", api.handleUIEvents)
	mux.HandleFunc("/unblock/", api.handleUnblockPost)
	mux.HandleFunc("/redeploy/", api.handleForceRedeploy)
	mux.HandleFunc("/command-preview/", api.handleCommandPreview)

	return api
}

// Run starts the HTTP server. Blocks until ctx is cancelled.
func (a *API) Run(ctx context.Context) {
	go func() {
		<-ctx.Done()
		if err := a.server.Close(); err != nil {
			log.Printf("[api] server close error: %v", err)
		}
	}()

	log.Printf("[api] listening on %s", a.server.Addr)
	if err := a.server.ListenAndServe(); err != http.ErrServerClosed {
		log.Printf("[api] server error: %v", err)
	}
}

// POST /trigger - trigger update check for all services
func (a *API) handleTriggerAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	log.Printf("[api] manual trigger: all services")
	go a.updater.pollAll(context.Background())

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
	if serviceName == "" {
		http.Error(w, "service name required", http.StatusBadRequest)
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
			log.Printf("[api] manual trigger: %s", svc.Name)
			go func() {
				ctx := context.Background()
				if err := a.updater.checkAndUpdate(ctx, svc); err != nil {
					// Use non-suppressing error handler for manual triggers
					a.updater.handlePollErrorAlways(ctx, svc, err)
				}
			}()
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
	if serviceName == "" {
		http.Error(w, "service name required", http.StatusBadRequest)
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
	if serviceName == "" {
		http.Error(w, "service name required", http.StatusBadRequest)
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
		log.Printf("[api] metrics write error: %v", err)
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
		log.Printf("[api] audit read error: %v", err)
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

	// Clear the server-level WriteTimeout so this long-lived connection is not
	// killed after 30 s. Uses ResponseController (Go 1.20+).
	rc := http.NewResponseController(w)
	if err := rc.SetWriteDeadline(time.Time{}); err != nil {
		log.Printf("[api] ui/events: could not clear write deadline: %v", err)
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")

	// Replay recent entries so the browser gets immediate content on connect.
	recent, err := a.audit.Recent(50)
	if err != nil {
		log.Printf("[api] ui/events: audit read error: %v", err)
	}
	for _, e := range recent { // oldest-first (as stored)
		data, merr := json.Marshal(e)
		if merr != nil {
			continue
		}
		fmt.Fprintf(w, "data: %s\n\n", data)
	}
	flusher.Flush()

	ch := a.hub.Subscribe()
	defer a.hub.Unsubscribe(ch)

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
				log.Printf("[api] ui/events: write error: %v", werr)
				return
			}
			flusher.Flush()
		}
	}
}

func writeJSON(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(data); err != nil {
		log.Printf("[api] json encode error: %v", err)
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
		log.Printf("[api] ui: audit read error: %v", err)
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
		log.Printf("[api] ui: template error: %v", err)
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
	if serviceName == "" {
		http.Error(w, "service name required", http.StatusBadRequest)
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
    .then(function(){btn.textContent='Triggered!';setTimeout(function(){btn.disabled=false;btn.textContent='Trigger';},2000);})
    .catch(function(){btn.disabled=false;btn.textContent='Error';setTimeout(function(){btn.textContent='Trigger';},2000);});
}
function redeployService(name){
  if(!confirm('Redeploy '+name+'? This will run docker compose up -d.'))return;
  fetch('/command-preview/'+encodeURIComponent(name))
    .then(function(r){return r.json();})
    .then(function(data){
      if(confirm('Execute: '+data.command+'?')){
        var btn=event.target;
        btn.disabled=true;
        btn.textContent='Redeploying...';
        fetch('/redeploy/'+encodeURIComponent(name),{method:'POST'})
          .then(function(){btn.textContent='Started!';setTimeout(function(){btn.disabled=false;btn.textContent='Redeploy';},2000);})
          .catch(function(){btn.disabled=false;btn.textContent='Error';setTimeout(function(){btn.textContent='Redeploy';},2000);});
      }
    })
    .catch(function(){alert('Failed to get command preview');});
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

function refreshStatus(){
  fetch('/status').then(function(r){return r.json();}).then(function(data){
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

      // Calculate next check timing
      var nextCheckText='--';
      if(s.next_check){
        var next=new Date(s.next_check);
        var now=new Date();
        var diff=Math.round((next-now)/1000);
        if(diff>0){
          if(diff<60)nextCheckText=diff+'s';
          else if(diff<3600)nextCheckText=Math.round(diff/60)+'m';
          else nextCheckText=Math.round(diff/3600)+'h';
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
      // Build config flags
      var configFlags='';
      configFlags+='<span style="color:'+(s.auto_update?'var(--success)':'var(--text-dim)')+';font-size:11px" title="Auto-update">U</span> ';
      configFlags+='<span style="color:'+(s.auto_heal?'var(--success)':'var(--text-dim)')+';font-size:11px" title="Auto-heal">H</span> ';
      configFlags+='<span style="color:'+(s.auto_start?'var(--success)':'var(--text-dim)')+';font-size:11px" title="Auto-start">S</span>';

      // Build health indicator
      var healthIcon='<span style="color:var(--text-dim)">?</span>';
      if(s.healthy===true)healthIcon='<span style="color:var(--success)">✓</span>';
      else if(s.healthy===false)healthIcon='<span style="color:var(--error)">✗</span>';

      // Build status with error tooltip
      var statusCell='<span class="'+esc(s.status)+'">'+esc(s.status)+'</span>';
      if(s.errored)statusCell+=' <span title="'+esc(s.errored)+'" style="cursor:help">⚠️</span>';

      // Build resources display
      var resources='--';
      if(s.has_stats){
        resources='CPU: '+Math.round(s.cpu_percent)+'%<br/>';
        resources+='MEM: '+Math.round(s.memory_usage_mb||0)+'MB/'+(s.memory_limit_mb?Math.round(s.memory_limit_mb)+'MB':'?')+' ('+Math.round(s.memory_percent)+'%)';
      }

      // Build deploy stats
      var deployStats='U:'+s.updates_total+' R:'+s.rollbacks_total+'<br/>';
      deployStats+='H:'+s.restarts_total+' F:'+s.failures_total;

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

// POST /redeploy/<service> - force redeploy without image check
func (a *API) handleForceRedeploy(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	serviceName := strings.TrimPrefix(r.URL.Path, "/redeploy/")
	if serviceName == "" {
		http.Error(w, "service name required", http.StatusBadRequest)
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

	log.Printf("[api] force redeploy: %s", svc.Name)
	go func() {
		ctx := context.Background()
		if err := compose.Up(ctx, svc.ComposeFiles, svc.ComposeProject, svc.EnvFile); err != nil {
			log.Printf("[api] ERROR: force redeploy failed for %s: %v", svc.Name, err)
			if werr := a.audit.Write(audit.Entry{
				Service: svc.Name,
				Event:   "force_redeploy_failed",
				Message: fmt.Sprintf("Force redeploy failed: %v", err),
				Level:   "error",
			}); werr != nil {
				log.Printf("[api] ERROR: audit write error: %v", werr)
			}
			return
		}

		if werr := a.audit.Write(audit.Entry{
			Service: svc.Name,
			Event:   "force_redeploy",
			Message: "Forced redeploy via API",
			Level:   "info",
		}); werr != nil {
			log.Printf("[api] ERROR: audit write error: %v", werr)
		}
	}()

	writeJSON(w, map[string]string{"status": "redeploying", "service": serviceName})
}

// GET /command-preview/<service> - show docker compose command that would be executed
func (a *API) handleCommandPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	serviceName := strings.TrimPrefix(r.URL.Path, "/command-preview/")
	if serviceName == "" {
		http.Error(w, "service name required", http.StatusBadRequest)
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

	// Build the docker compose command
	cmd := "docker compose"
	for _, f := range svc.ComposeFiles {
		cmd += fmt.Sprintf(" -f %s", f)
	}
	if svc.ComposeProject != "" {
		cmd += fmt.Sprintf(" -p %s", svc.ComposeProject)
	}
	if svc.EnvFile != "" {
		cmd += fmt.Sprintf(" --env-file %s", svc.EnvFile)
	}
	cmd += " up -d"

	writeJSON(w, map[string]string{
		"service": serviceName,
		"command": cmd,
	})
}
