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

// NewAPI creates the trigger/metrics API on the given port.
func NewAPI(updater *Updater, healer *Healer, metrics *Metrics, monitor *Monitor, al *audit.Logger, port string) *API {
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
			Addr:         "127.0.0.1:" + port,
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
					a.updater.handlePollError(ctx, svc, err)
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
	// Resource usage — populated when cpu_threshold or memory_threshold is configured.
	CPUPercent    float64 `json:"cpu_percent,omitempty"`
	MemoryPercent float64 `json:"memory_percent,omitempty"`
	MemoryUsageMB float64 `json:"memory_usage_mb,omitempty"`
	MemoryLimitMB float64 `json:"memory_limit_mb,omitempty"`
}

// GET /status - aggregated state for all configured services
func (a *API) handleStatusAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	snap := a.stateSnapshot()
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

	snap := a.stateSnapshot()
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
}

func (a *API) stateSnapshot() stateSnap {
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
	return snap
}

func (a *API) buildServiceStatus(svc config.Service, snap stateSnap) serviceStatus {
	s := serviceStatus{
		Name:       svc.Name,
		AutoUpdate: svc.AutoUpdate,
		AutoStart:  svc.AutoStart,
		AutoHeal:   svc.AutoHeal,
		Deploying:  a.updater.IsDeploying(svc.Name),
		Blocked:    snap.blocked[svc.Name],
		NotFound:   snap.notFound[svc.Name],
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
		s.CPUPercent    = st.CPUPercent
		s.MemoryPercent = st.MemoryPercent
		s.MemoryUsageMB = st.MemoryUsageMB
		s.MemoryLimitMB = st.MemoryLimitMB
	}
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

	snap := a.stateSnapshot()
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
<style>
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:monospace;font-size:13px;background:#0f0f0f;color:#c8c8c8;padding:24px 32px}
header{margin-bottom:24px;color:#555;font-size:12px}
h2{font-size:11px;font-weight:normal;color:#444;text-transform:uppercase;letter-spacing:.08em;margin-bottom:8px}
table{width:100%;border-collapse:collapse;margin-bottom:32px}
th{text-align:left;color:#3a3a3a;font-weight:normal;padding:5px 12px;border-bottom:1px solid #1c1c1c;font-size:11px;text-transform:uppercase;letter-spacing:.05em}
td{padding:6px 12px;border-bottom:1px solid #161616;vertical-align:middle}
tr:hover td{background:#121212}
.ok{color:#4caf50}.unhealthy{color:#ef5350}.deploying{color:#42a5f5}
.degraded{color:#ffa726}.exhausted{color:#ef5350}.blocked{color:#ffa726}
.not_found{color:#555}.errored{color:#ef5350}.unknown{color:#333}
.info{color:#42a5f5}.warning{color:#ffa726}.error{color:#ef5350}
button{background:#171717;color:#666;border:1px solid #222;padding:3px 10px;cursor:pointer;font-family:monospace;font-size:11px}
button:hover{background:#1e1e1e;color:#bbb;border-color:#333}
form{display:inline}
</style>
</head>
<body>
<header>Dockward &mdash; {{.Hostname}} &mdash; uptime {{.Uptime}}</header>

<h2>Services</h2>
<table>
<thead><tr><th>Name</th><th>Status</th><th>CPU / Mem</th><th>Updates</th><th>Rollbacks</th><th>Restarts</th><th>Actions</th></tr></thead>
<tbody id="status-body">
{{range .Services}}
<tr>
  <td>{{.Name}}</td>
  <td class="{{.Status}}">{{.Status}}</td>
  <td>{{if or .CPUPercent .MemoryPercent}}{{printf "%.0f" .CPUPercent}}% / {{printf "%.0f" .MemoryPercent}}%{{else}}--{{end}}</td>
  <td>{{.UpdatesTotal}}</td>
  <td>{{.RollbacksTotal}}</td>
  <td>{{.RestartsTotal}}</td>
  <td>
    <form method="POST" action="/trigger/{{.Name}}?redirect=ui"><button type="submit">Trigger</button></form>
    {{if .Blocked}}&nbsp;<form method="POST" action="/unblock/{{.Name}}"><button type="submit">Unblock</button></form>{{end}}
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
  <td style="white-space:nowrap;color:#444">{{.Timestamp.Format "2006-01-02 15:04:05"}}</td>
  <td style="color:#666">{{.Service}}</td>
  <td>{{.Event}}</td>
  <td class="{{.Level}}">{{.Level}}</td>
  <td style="color:#666">{{.Message}}</td>
</tr>
{{end}}
</tbody>
</table>

<script>
var es = new EventSource('/ui/events');
es.onmessage = function(evt) {
  var e;
  try { e = JSON.parse(evt.data); } catch(_) { return; }
  var tbody = document.getElementById('events-body');
  if (!tbody) return;
  var ts = e.timestamp ? e.timestamp.replace('T',' ').slice(0,19) : '';
  var row = '<tr>' +
    '<td style="white-space:nowrap;color:#444">' + ts + '</td>' +
    '<td style="color:#666">' + (e.service||'') + '</td>' +
    '<td>' + (e.event||'') + '</td>' +
    '<td class="' + (e.level||'') + '">' + (e.level||'') + '</td>' +
    '<td style="color:#666">' + (e.message||'') + '</td>' +
    '</tr>';
  tbody.insertAdjacentHTML('afterbegin', row);
  while (tbody.rows.length > 50) { tbody.deleteRow(-1); }
};

function refreshStatus() {
  fetch('/status').then(function(r) { return r.json(); }).then(function(data) {
    var tbody = document.getElementById('status-body');
    if (!tbody) return;
    var rows = '';
    for (var i = 0; i < data.services.length; i++) {
      var s = data.services[i];
      var cpu = (s.cpu_percent || s.memory_percent)
        ? Math.round(s.cpu_percent) + '% / ' + Math.round(s.memory_percent) + '%'
        : '--';
      var actions = '<form method="POST" action="/trigger/' + s.name + '?redirect=ui"><button type="submit">Trigger</button></form>';
      if (s.blocked) {
        actions += ' <form method="POST" action="/unblock/' + s.name + '"><button type="submit">Unblock</button></form>';
      }
      rows += '<tr>' +
        '<td>' + s.name + '</td>' +
        '<td class="' + s.status + '">' + s.status + '</td>' +
        '<td>' + cpu + '</td>' +
        '<td>' + (s.updates_total||0) + '</td>' +
        '<td>' + (s.rollbacks_total||0) + '</td>' +
        '<td>' + (s.restarts_total||0) + '</td>' +
        '<td>' + actions + '</td>' +
        '</tr>';
    }
    tbody.innerHTML = rows;
  }).catch(function(){});
}
setInterval(refreshStatus, 15000);
</script>
</body>
</html>`
