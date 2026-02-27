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
	interval := time.Duration(m.cfg.Registry.PollInterval) * time.Second
	log.Printf("[monitor] polling resources every %s", interval)

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
		if svc.CPUThreshold <= 0 && svc.MemoryThreshold <= 0 {
			continue
		}
		m.checkService(ctx, svc)
	}
}

func (m *Monitor) checkService(ctx context.Context, svc config.Service) {
	containerID := m.findContainer(ctx, svc)
	if containerID == "" {
		return
	}

	raw, err := m.docker.ContainerStats(ctx, containerID)
	if err != nil {
		log.Printf("[monitor] %s: stats error: %v", svc.Name, err)
		return
	}

	stats := ServiceStats{
		CPUPercent:    raw.CPUPercent,
		MemoryPercent: raw.MemoryPercent,
		MemoryUsageMB: float64(raw.MemoryUsage) / 1024 / 1024,
		MemoryLimitMB: float64(raw.MemoryLimit) / 1024 / 1024,
	}

	m.latestMu.Lock()
	m.latest[svc.Name] = stats
	m.latestMu.Unlock()

	cooldown := time.Duration(svc.HealCooldown) * time.Second
	if cooldown <= 0 {
		cooldown = 5 * time.Minute
	}

	if svc.CPUThreshold > 0 && stats.CPUPercent > svc.CPUThreshold {
		m.maybeAlert(ctx, svc, "cpu", cooldown,
			fmt.Sprintf("CPU usage %.1f%% exceeds threshold %.1f%%", stats.CPUPercent, svc.CPUThreshold),
		)
	}

	if svc.MemoryThreshold > 0 && stats.MemoryPercent > svc.MemoryThreshold {
		m.maybeAlert(ctx, svc, "memory", cooldown,
			fmt.Sprintf("Memory usage %.1f%% (%.0f MB / %.0f MB) exceeds threshold %.1f%%",
				stats.MemoryPercent, stats.MemoryUsageMB, stats.MemoryLimitMB, svc.MemoryThreshold),
		)
	}
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

// findContainer returns the first running container ID for a service,
// matching by compose project label or container name.
func (m *Monitor) findContainer(ctx context.Context, svc config.Service) string {
	if svc.ComposeProject != "" {
		containers, err := m.docker.ListContainersByProject(ctx, svc.ComposeProject)
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
		containers, err := m.docker.ListContainersFiltered(ctx, filter)
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
