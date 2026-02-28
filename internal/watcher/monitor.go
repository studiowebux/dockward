package watcher

import (
	"context"
	"fmt"
	"log"
	"net/url"
	"sync"
	"time"

	"github.com/studiowebux/dockward/internal/audit"
	"github.com/studiowebux/dockward/internal/config"
	"github.com/studiowebux/dockward/internal/docker"
	"github.com/studiowebux/dockward/internal/notify"
)

// ServiceStats is a point-in-time resource snapshot for a single service.
type ServiceStats struct {
	CPUPercent    float64
	MemoryPercent float64
	MemoryUsageMB float64
	MemoryLimitMB float64
}

// Monitor polls container resource usage and fires alerts when thresholds are exceeded.
// Pattern: background goroutine, interval = poll_interval, cooldown = heal_cooldown.
type Monitor struct {
	cfg        *config.Config
	docker     *docker.Client
	dispatcher *notify.Dispatcher
	audit      *audit.Logger

	// latest holds the most recent stats snapshot per service for the status API.
	latest   map[string]ServiceStats
	latestMu sync.RWMutex

	// alertedAt tracks the last alert time per "service:metric" key to prevent spam.
	alertedAt   map[string]time.Time
	alertedAtMu sync.Mutex
}

// NewMonitor creates a resource monitor.
func NewMonitor(cfg *config.Config, dc *docker.Client, dispatcher *notify.Dispatcher, al *audit.Logger) *Monitor {
	return &Monitor{
		cfg:        cfg,
		docker:     dc,
		dispatcher: dispatcher,
		audit:      al,
		latest:     make(map[string]ServiceStats),
		alertedAt:  make(map[string]time.Time),
	}
}

// Run starts the monitor ticker. Interval matches the registry poll interval.
// Blocks until ctx is cancelled.
func (m *Monitor) Run(ctx context.Context) {
	interval := time.Duration(m.cfg.Monitor.StatsInterval) * time.Second
	log.Printf("[monitor] polling resources every %s", interval)

	// Poll immediately so stats are available before the first tick.
	m.pollAll(ctx)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.pollAll(ctx)
		}
	}
}

// StatsSnapshot returns a copy of the latest resource stats per service.
// Used by the status API to populate cpu/mem fields.
func (m *Monitor) StatsSnapshot() map[string]ServiceStats {
	m.latestMu.RLock()
	defer m.latestMu.RUnlock()
	result := make(map[string]ServiceStats, len(m.latest))
	for k, v := range m.latest {
		result[k] = v
	}
	return result
}

func (m *Monitor) pollAll(ctx context.Context) {
	for _, svc := range m.cfg.Services {
		if ctx.Err() != nil {
			return
		}
		m.checkService(ctx, svc)
	}
}

func (m *Monitor) checkService(ctx context.Context, svc config.Service) {
	containerIDs := findRunningContainerIDs(ctx, m.docker, svc)
	if len(containerIDs) == 0 {
		return
	}

	cooldown := time.Duration(svc.HealCooldown) * time.Second
	if cooldown <= 0 {
		cooldown = 5 * time.Minute
	}

	// Collect per-container stats and aggregate for display.
	var totalCPU float64
	var totalMemUsage, totalMemLimit uint64

	for _, id := range containerIDs {
		raw, err := m.docker.ContainerStats(ctx, id)
		if err != nil {
			log.Printf("[monitor] %s: stats error for %s: %v", svc.Name, id, err)
			continue
		}

		totalCPU += raw.CPUPercent
		totalMemUsage += raw.MemoryUsage
		totalMemLimit += raw.MemoryLimit

		// Per-container threshold alerts use container-scoped cooldown keys.
		if svc.CPUThreshold > 0 && raw.CPUPercent > svc.CPUThreshold {
			m.maybeAlert(ctx, svc, "cpu:"+id, cooldown,
				fmt.Sprintf("Container %s CPU %.1f%% exceeds threshold %.1f%%", id[:12], raw.CPUPercent, svc.CPUThreshold),
			)
		}

		if svc.MemoryThreshold > 0 && raw.MemoryLimit > 0 {
			memPct := float64(raw.MemoryUsage) / float64(raw.MemoryLimit) * 100
			if memPct > svc.MemoryThreshold {
				m.maybeAlert(ctx, svc, "memory:"+id, cooldown,
					fmt.Sprintf("Container %s memory %.1f%% (%.0f MB / %.0f MB) exceeds threshold %.1f%%",
						id[:12], memPct, float64(raw.MemoryUsage)/1024/1024, float64(raw.MemoryLimit)/1024/1024, svc.MemoryThreshold),
				)
			}
		}
	}

	// Aggregate stats stored for the status API.
	var memPct float64
	if totalMemLimit > 0 {
		memPct = float64(totalMemUsage) / float64(totalMemLimit) * 100
	}

	m.latestMu.Lock()
	m.latest[svc.Name] = ServiceStats{
		CPUPercent:    totalCPU,
		MemoryPercent: memPct,
		MemoryUsageMB: float64(totalMemUsage) / 1024 / 1024,
		MemoryLimitMB: float64(totalMemLimit) / 1024 / 1024,
	}
	m.latestMu.Unlock()
}

func (m *Monitor) maybeAlert(ctx context.Context, svc config.Service, metric string, cooldown time.Duration, message string) {
	key := svc.Name + ":" + metric

	m.alertedAtMu.Lock()
	last, ok := m.alertedAt[key]
	if ok && time.Since(last) < cooldown {
		m.alertedAtMu.Unlock()
		return
	}
	m.alertedAt[key] = time.Now()
	m.alertedAtMu.Unlock()

	log.Printf("[monitor] %s: %s", svc.Name, message)

	m.dispatcher.Send(ctx, notify.Alert{
		Service: svc.Name,
		Event:   "resource_alert",
		Message: message,
		Level:   notify.LevelWarning,
	})

	if err := m.audit.Write(audit.Entry{
		Service: svc.Name,
		Event:   "resource_alert",
		Message: message,
		Level:   "warning",
	}); err != nil {
		log.Printf("[monitor] %s: audit write error: %v", svc.Name, err)
	}
}

// findRunningContainerIDs returns all running container IDs for a service.
// Used by the monitor to collect stats across multi-container services.
func findRunningContainerIDs(ctx context.Context, dc *docker.Client, svc config.Service) []string {
	var ids []string

	if svc.ComposeProject != "" {
		containers, err := dc.ListContainersByProject(ctx, svc.ComposeProject)
		if err == nil {
			for _, c := range containers {
				if c.State == "running" {
					ids = append(ids, c.ID)
				}
			}
		}
	}

	if len(ids) == 0 && svc.ContainerName != "" {
		// Fallback to container_name for heal-only services with no compose project.
		if id := findRunningContainerID(ctx, dc, svc); id != "" {
			ids = append(ids, id)
		}
	}

	return ids
}

// findRunningContainerID returns the first running container ID for a service,
// matching by compose project label or container name.
// Package-level so both Monitor and Healer can use it.
func findRunningContainerID(ctx context.Context, dc *docker.Client, svc config.Service) string {
	if svc.ComposeProject != "" {
		containers, err := dc.ListContainersByProject(ctx, svc.ComposeProject)
		if err == nil {
			for _, c := range containers {
				if c.State == "running" {
					return c.ID
				}
			}
		}
	}

	if svc.ContainerName != "" {
		filter := url.QueryEscape(fmt.Sprintf(`{"name":["%s"]}`, svc.ContainerName))
		containers, err := dc.ListContainersFiltered(ctx, filter)
		if err == nil {
			for _, c := range containers {
				if c.State == "running" {
					return c.ID
				}
			}
		}
	}

	return ""
}
