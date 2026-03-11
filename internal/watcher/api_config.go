package watcher

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/studiowebux/dockward/internal/config"
	"github.com/studiowebux/dockward/internal/logger"
)

// GET /config — return the current in-memory config as JSON.
func (a *API) handleGetConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	a.cfgMu.RLock()
	defer a.cfgMu.RUnlock()
	writeJSON(w, a.updater.cfg)
}

// GET /config/download — download the current in-memory config as a JSON file attachment.
func (a *API) handleConfigDownload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	a.cfgMu.RLock()
	defer a.cfgMu.RUnlock()

	data, err := json.MarshalIndent(a.updater.cfg, "", "  ")
	if err != nil {
		logger.Printf("[api] config download marshal error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="dockward-config.json"`)
	if _, err := w.Write(data); err != nil {
		logger.Printf("[api] config download write error: %v", err)
	}
}

// PUT /config/registry — replace registry settings and persist to disk.
// Note: poll_interval changes take effect on the next poll cycle.
func (a *API) handlePutRegistry(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var reg config.Registry
	if err := json.NewDecoder(r.Body).Decode(&reg); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()

	a.updater.cfg.Registry = reg
	a.updater.cfg.ApplyDefaults()
	if err := a.updater.cfg.Save(a.configPath); err != nil {
		logger.Printf("[api] config save error: %v", err)
		http.Error(w, "failed to persist config", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "ok", "note": "poll_interval changes apply on next cycle"})
}

// PUT /config/monitor — replace monitor settings and persist to disk.
func (a *API) handlePutMonitor(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var mon config.Monitor
	if err := json.NewDecoder(r.Body).Decode(&mon); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()

	a.updater.cfg.Monitor = mon
	a.updater.cfg.ApplyDefaults()
	if err := a.updater.cfg.Save(a.configPath); err != nil {
		logger.Printf("[api] config save error: %v", err)
		http.Error(w, "failed to persist config", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

// PUT /config/notifications — replace notification settings and persist to disk.
func (a *API) handlePutNotifications(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPut {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var notif config.Notifications
	if err := json.NewDecoder(r.Body).Decode(&notif); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()

	a.updater.cfg.Notifications = notif
	if err := a.updater.cfg.Save(a.configPath); err != nil {
		logger.Printf("[api] config save error: %v", err)
		http.Error(w, "failed to persist config", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "ok"})
}

// handleConfigService routes PUT and DELETE on /config/services/{name}.
func (a *API) handleConfigService(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPut:
		a.handleUpsertService(w, r)
	case http.MethodDelete:
		a.handleDeleteService(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

// PUT /config/services/{name} — create or replace a service entry and persist to disk.
// The service name in the URL takes precedence over any name field in the body.
func (a *API) handleUpsertService(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/config/services/")
	name = validateServiceName(name)
	if name == "" {
		http.Error(w, "invalid service name: must match ^[a-zA-Z0-9_-]{1,64}$", http.StatusBadRequest)
		return
	}

	var svc config.Service
	if err := json.NewDecoder(r.Body).Decode(&svc); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	svc.Name = name // URL name is authoritative

	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()

	found := false
	for i, s := range a.updater.cfg.Services {
		if s.Name == name {
			a.updater.cfg.Services[i] = svc
			found = true
			break
		}
	}
	if !found {
		a.updater.cfg.Services = append(a.updater.cfg.Services, svc)
	}

	a.updater.cfg.ApplyDefaults()
	if err := a.updater.cfg.Save(a.configPath); err != nil {
		logger.Printf("[api] config save error: %v", err)
		http.Error(w, "failed to persist config", http.StatusInternalServerError)
		return
	}

	status := "created"
	if found {
		status = "updated"
	}
	writeJSON(w, map[string]string{"status": status, "service": name})
}

// DELETE /config/services/{name} — remove a service entry and persist to disk.
func (a *API) handleDeleteService(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimPrefix(r.URL.Path, "/config/services/")
	name = validateServiceName(name)
	if name == "" {
		http.Error(w, "invalid service name: must match ^[a-zA-Z0-9_-]{1,64}$", http.StatusBadRequest)
		return
	}

	a.cfgMu.Lock()
	defer a.cfgMu.Unlock()

	svcs := a.updater.cfg.Services
	newSvcs := make([]config.Service, 0, len(svcs))
	found := false
	for _, s := range svcs {
		if s.Name == name {
			found = true
			continue
		}
		newSvcs = append(newSvcs, s)
	}
	if !found {
		http.Error(w, "service not found", http.StatusNotFound)
		return
	}

	a.updater.cfg.Services = newSvcs
	if err := a.updater.cfg.Save(a.configPath); err != nil {
		logger.Printf("[api] config save error: %v", err)
		http.Error(w, "failed to persist config", http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]string{"status": "deleted", "service": name})
}
