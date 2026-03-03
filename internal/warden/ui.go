package warden

import (
	"crypto/subtle"
	"fmt"
	"html/template"
	"github.com/studiowebux/dockward/internal/logger"
	"net/http"
	"os"
	"sort"
	"time"
)

var dashboardTmpl = template.Must(template.New("warden").Funcs(template.FuncMap{
	"formatTime": func(t time.Time) string {
		if t.IsZero() {
			return "never"
		}
		return t.UTC().Format("2006-01-02 15:04:05 UTC")
	},
}).Parse(`<!DOCTYPE html>
<html lang="en">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>Warden</title>
<style>
*{box-sizing:border-box;margin:0;padding:0}
body{font-family:monospace;background:#0d1117;color:#c9d1d9;font-size:13px}
header{padding:12px 16px;border-bottom:1px solid #21262d;display:flex;justify-content:space-between;align-items:center}
header h1{font-size:15px;font-weight:600;color:#e6edf3}
header span{color:#8b949e;font-size:12px}
.agents{display:flex;flex-wrap:wrap;gap:8px;padding:12px 16px;border-bottom:1px solid #21262d}
.card{padding:8px 12px;border:1px solid #21262d;border-radius:6px;min-width:160px}
.card.online{border-color:#238636}
.card.offline{border-color:#da3633}
.card-id{font-weight:600;color:#e6edf3;margin-bottom:4px}
.card-status{font-size:11px}
.card.online .card-status{color:#3fb950}
.card.offline .card-status{color:#f85149}
.card-seen{font-size:11px;color:#8b949e;margin-top:2px}
.controls{padding:8px 16px;border-bottom:1px solid #21262d;display:flex;gap:8px;align-items:center}
.controls label{color:#8b949e;font-size:11px}
.controls select{background:#161b22;color:#c9d1d9;border:1px solid #30363d;border-radius:4px;padding:3px 6px;font-size:11px;font-family:monospace}
#status{margin-left:auto;font-size:11px;color:#8b949e}
table{width:100%;border-collapse:collapse}
thead th{padding:6px 16px;text-align:left;font-size:11px;color:#8b949e;border-bottom:1px solid #21262d;position:sticky;top:0;background:#0d1117}
tbody tr{border-bottom:1px solid #161b22}
tbody tr:hover{background:#161b22}
td{padding:5px 16px;font-size:12px;white-space:nowrap}
.ts{color:#8b949e}
.machine{color:#79c0ff}
.svc{color:#d2a8ff}
.level-info{color:#3fb950}
.level-warning{color:#e3b341}
.level-error{color:#f85149}
.level-critical{color:#f85149;font-weight:600}
.msg{color:#c9d1d9;white-space:normal;max-width:500px}
.tbl-wrap{overflow-y:auto;max-height:calc(100vh - 220px)}
</style>
</head>
<body>
<header>
  <h1>Warden</h1>
  <span>{{.Hostname}} &mdash; up {{.Uptime}}</span>
</header>
<div class="agents">
{{range .Agents}}
  <div class="card {{if .Online}}online{{else}}offline{{end}}">
    <div class="card-id">{{.ID}}</div>
    <div class="card-status">{{if .Online}}online{{else}}offline{{end}}</div>
    <div class="card-seen">last seen: {{formatTime .LastSeen}}</div>
  </div>
{{end}}
</div>
<div class="controls">
  <label>machine</label>
  <select id="f-machine" onchange="applyFilter()">
    <option value="">all</option>
    {{range .Agents}}<option value="{{.ID}}">{{.ID}}</option>{{end}}
  </select>
  <label>level</label>
  <select id="f-level" onchange="applyFilter()">
    <option value="">all</option>
    <option value="info">info</option>
    <option value="warning">warning</option>
    <option value="error">error</option>
    <option value="critical">critical</option>
  </select>
  <span id="status">connecting...</span>
</div>
<div class="tbl-wrap">
<table>
<thead><tr>
  <th>Time</th><th>Machine</th><th>Service</th><th>Event</th><th>Level</th><th>Message</th>
</tr></thead>
<tbody id="feed">
{{range .Recent}}
<tr data-machine="{{.Machine}}" data-level="{{.Level}}">
  <td class="ts">{{formatTime .Timestamp}}</td>
  <td class="machine">{{.Machine}}</td>
  <td class="svc">{{.Service}}</td>
  <td>{{.Event}}</td>
  <td class="level-{{.Level}}">{{.Level}}</td>
  <td class="msg">{{.Message}}</td>
</tr>
{{end}}
</tbody>
</table>
</div>
<script>
const token = {{.Token}};
let fMachine = '', fLevel = '';

function applyFilter() {
  fMachine = document.getElementById('f-machine').value;
  fLevel   = document.getElementById('f-level').value;
  document.querySelectorAll('#feed tr').forEach(row => {
    const m = row.dataset.machine || '';
    const l = row.dataset.level   || '';
    row.style.display =
      (fMachine === '' || m === fMachine) &&
      (fLevel   === '' || l === fLevel)
        ? '' : 'none';
  });
}

function addRow(e) {
  const ts  = e.timestamp ? new Date(e.timestamp).toISOString().replace('T',' ').slice(0,19)+' UTC' : '';
  const row = document.createElement('tr');
  row.dataset.machine = e.machine || '';
  row.dataset.level   = e.level   || '';
  row.innerHTML =
    '<td class="ts">'                  + ts                      + '</td>' +
    '<td class="machine">'             + (e.machine  || '') + '</td>' +
    '<td class="svc">'                 + (e.service  || '') + '</td>' +
    '<td>'                             + (e.event    || '') + '</td>' +
    '<td class="level-'+(e.level||'')+'">' + (e.level || '') + '</td>' +
    '<td class="msg">'                 + (e.message  || '') + '</td>';

  const show =
    (fMachine === '' || row.dataset.machine === fMachine) &&
    (fLevel   === '' || row.dataset.level   === fLevel);
  if (!show) row.style.display = 'none';

  const feed = document.getElementById('feed');
  feed.insertBefore(row, feed.firstChild);
  // Keep at most 500 rows in the DOM.
  while (feed.children.length > 500) feed.removeChild(feed.lastChild);
}

const es = new EventSource('/events?token=' + encodeURIComponent(token));
es.onopen    = () => { document.getElementById('status').textContent = 'live'; };
es.onerror   = () => { document.getElementById('status').textContent = 'reconnecting...'; };
es.onmessage = e  => { try { addRow(JSON.parse(e.data)); } catch(_) {} };
</script>
</body>
</html>
`))

type dashboardData struct {
	Hostname string
	Uptime   string
	Token    string
	Agents   []AgentState
	Recent   interface{}
}

var startTime = time.Now()

// handleUI serves the warden multi-machine dashboard.
// Requires the warden API token as a cookie named "token" or as a query param
// "token" (the query param is accepted to make direct browser navigation easy
// during setup; cookies are preferred in production behind nginx).
func (s *Server) handleUI(w http.ResponseWriter, r *http.Request) {
	if !s.uiAuth(r) {
		http.SetCookie(w, &http.Cookie{Name: "token", Value: "", MaxAge: -1, Path: "/"})
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}

	// Set auth cookie so subsequent requests (including SSE EventSource) work.
	http.SetCookie(w, &http.Cookie{
		Name:     "token",
		Value:    s.cfg.API.Token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})

	hostname, _ := os.Hostname()

	agents := s.store.AgentStates()
	sort.Slice(agents, func(i, j int) bool { return agents[i].ID < agents[j].ID })

	data := dashboardData{
		Hostname: hostname,
		Uptime:   formatUptime(time.Since(startTime)),
		Token:    s.cfg.API.Token,
		Agents:   agents,
		Recent:   s.store.Recent(200),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := dashboardTmpl.Execute(w, data); err != nil {
		logger.Printf("warden: render dashboard: %v", err)
	}
}

// uiAuth validates the warden token from query param or cookie.
// Uses constant-time comparison to prevent timing attacks.
func (s *Server) uiAuth(r *http.Request) bool {
	want := []byte(s.cfg.API.Token)
	if subtle.ConstantTimeCompare([]byte(r.URL.Query().Get("token")), want) == 1 {
		return true
	}
	if c, err := r.Cookie("token"); err == nil {
		if subtle.ConstantTimeCompare([]byte(c.Value), want) == 1 {
			return true
		}
	}
	return false
}

func formatUptime(d time.Duration) string {
	d = d.Round(time.Second)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	sec := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh %dm %ds", h, m, sec)
	}
	if m > 0 {
		return fmt.Sprintf("%dm %ds", m, sec)
	}
	return fmt.Sprintf("%ds", sec)
}
