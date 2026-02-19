package watcher

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/studiowebux/dockward/internal/config"
	"github.com/studiowebux/dockward/internal/docker"
	"github.com/studiowebux/dockward/internal/notify"
)

// Healer listens for Docker health events and restarts unhealthy containers.
type Healer struct {
	cfg        *config.Config
	docker     *docker.Client
	dispatcher *notify.Dispatcher
	updater    *Updater
	metrics    *Metrics

	// cooldowns tracks when each container can next be auto-restarted.
	cooldowns   map[string]time.Time
	cooldownsMu sync.Mutex

	// degraded tracks services that entered a bad state (died or unhealthy).
	// Keyed by service name. Cleared when a healthy event is received.
	degraded   map[string]bool
	degradedMu sync.Mutex

	// restartCounts tracks consecutive failed restart attempts per service.
	// Keyed by service name. Reset when a healthy event is received.
	restartCounts   map[string]int
	restartCountsMu sync.Mutex
}

// NewHealer creates a health monitor.
func NewHealer(cfg *config.Config, dc *docker.Client, dispatcher *notify.Dispatcher, updater *Updater, metrics *Metrics) *Healer {
	return &Healer{
		cfg:           cfg,
		docker:        dc,
		dispatcher:    dispatcher,
		updater:       updater,
		metrics:       metrics,
		cooldowns:     make(map[string]time.Time),
		degraded:      make(map[string]bool),
		restartCounts: make(map[string]int),
	}
}

// Run starts the Docker event stream listener. Blocks until ctx is cancelled.
func (h *Healer) Run(ctx context.Context) {
	log.Printf("[healer] listening for Docker health events")
	h.docker.StreamEvents(ctx, func(event docker.Event) {
		h.handleEvent(ctx, event)
	})
}

func (h *Healer) handleEvent(ctx context.Context, event docker.Event) {
	containerName := event.ContainerName()
	containerID := event.Actor.ID

	svc := h.findServiceByEvent(event)
	if svc == nil {
		return
	}

	switch {
	case strings.HasPrefix(event.Action, "health_status: unhealthy"):
		h.handleUnhealthy(ctx, svc, containerName, containerID)

	case strings.HasPrefix(event.Action, "health_status: healthy"):
		h.handleHealthy(ctx, svc, containerName)

	case event.Action == "die":
		h.handleDied(ctx, svc, containerName)
	}
}

func (h *Healer) handleUnhealthy(ctx context.Context, svc *config.Service, containerName, containerID string) {
	// Skip if this container is in a deploy grace period (updater handles rollback).
	if h.updater.IsDeploying(svc.Name) {
		log.Printf("[healer] %s: unhealthy during deploy grace period, skipping (updater handles rollback)", svc.Name)
		return
	}

	// Get the health check failure reason.
	info, err := h.docker.InspectContainer(ctx, containerID)
	reason := ""
	if err == nil {
		reason = info.LastHealthOutput()
	}

	log.Printf("[healer] %s: unhealthy. Reason: %s", svc.Name, reason)
	h.metrics.SetHealthy(svc.Name, false)
	h.setDegraded(svc.Name, true)

	if !svc.AutoHeal {
		h.dispatcher.Send(ctx, notify.Alert{
			Service:   svc.Name,
			Event:     "unhealthy",
			Message:   "Container is unhealthy.",
			Reason:    reason,
			Container: containerName,
			Level:     notify.LevelWarning,
		})
		return
	}

	// Check if max consecutive restarts exceeded.
	h.restartCountsMu.Lock()
	count := h.restartCounts[svc.Name]
	h.restartCountsMu.Unlock()
	if count >= svc.HealMaxRestarts {
		log.Printf("[healer] %s: max restarts (%d) reached, giving up", svc.Name, svc.HealMaxRestarts)
		return
	}

	// Check cooldown.
	if h.inCooldown(containerName) {
		log.Printf("[healer] %s: in cooldown, skipping restart", svc.Name)
		return
	}

	// Restart the container.
	log.Printf("[healer] %s: restarting", svc.Name)
	if err := h.docker.RestartContainer(ctx, containerID, 10); err != nil {
		log.Printf("[healer] %s: restart failed: %v", svc.Name, err)
		h.metrics.IncFailures(svc.Name)
		h.dispatcher.Send(ctx, notify.Alert{
			Service:   svc.Name,
			Event:     "critical",
			Message:   "Failed to restart unhealthy container.",
			Reason:    reason,
			Container: containerName,
			Level:     notify.LevelCritical,
		})
		return
	}

	// Set cooldown.
	cooldown := time.Duration(svc.HealCooldown) * time.Second
	h.cooldownsMu.Lock()
	h.cooldowns[containerName] = time.Now().Add(cooldown)
	h.cooldownsMu.Unlock()

	// Wait briefly, then check if restart fixed it.
	go h.verifyAfterRestart(ctx, svc, containerName, containerID, reason)
}

func (h *Healer) verifyAfterRestart(ctx context.Context, svc *config.Service, containerName, containerID, reason string) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(30 * time.Second):
	}

	info, err := h.docker.InspectContainer(ctx, containerID)
	if err != nil {
		log.Printf("[healer] %s: could not verify after restart: %v", svc.Name, err)
		return
	}

	if info.State.Health != nil && info.State.Health.Status == "unhealthy" {
		log.Printf("[healer] %s: still unhealthy after restart", svc.Name)
		h.metrics.IncFailures(svc.Name)

		// Increment consecutive failure counter.
		h.restartCountsMu.Lock()
		h.restartCounts[svc.Name]++
		count := h.restartCounts[svc.Name]
		h.restartCountsMu.Unlock()

		if count >= svc.HealMaxRestarts {
			log.Printf("[healer] %s: giving up after %d consecutive failed restarts", svc.Name, count)
			h.dispatcher.Send(ctx, notify.Alert{
				Service:   svc.Name,
				Event:     "critical",
				Message:   fmt.Sprintf("Giving up after %d consecutive failed restarts. Manual intervention required.", count),
				Reason:    info.LastHealthOutput(),
				Container: containerName,
				Level:     notify.LevelCritical,
			})
		} else {
			h.dispatcher.Send(ctx, notify.Alert{
				Service:   svc.Name,
				Event:     "critical",
				Message:   fmt.Sprintf("Container still unhealthy after restart (attempt %d/%d).", count, svc.HealMaxRestarts),
				Reason:    info.LastHealthOutput(),
				Container: containerName,
				Level:     notify.LevelCritical,
			})
		}
		return
	}

	h.metrics.IncRestarts(svc.Name)
	h.metrics.SetHealthy(svc.Name, true)
	h.dispatcher.Send(ctx, notify.Alert{
		Service:   svc.Name,
		Event:     "restarted",
		Message:   "Restarted unhealthy container successfully.",
		Reason:    reason,
		Container: containerName,
		Level:     notify.LevelWarning,
	})
}

func (h *Healer) handleHealthy(ctx context.Context, svc *config.Service, containerName string) {
	// Check if the service was in a degraded state (died, unhealthy, or healer-restarted).
	h.cooldownsMu.Lock()
	_, wasCooling := h.cooldowns[containerName]
	h.cooldownsMu.Unlock()

	wasDegraded := h.isDegraded(svc.Name)

	if !wasCooling && !wasDegraded {
		return
	}

	log.Printf("[healer] %s: recovered (healthy)", svc.Name)
	h.metrics.SetHealthy(svc.Name, true)
	h.setDegraded(svc.Name, false)

	// Reset consecutive restart failure counter.
	h.restartCountsMu.Lock()
	delete(h.restartCounts, svc.Name)
	h.restartCountsMu.Unlock()
	h.dispatcher.Send(ctx, notify.Alert{
		Service:   svc.Name,
		Event:     "healthy",
		Message:   "Container recovered and is healthy.",
		Container: containerName,
		Level:     notify.LevelInfo,
	})
}

func (h *Healer) handleDied(ctx context.Context, svc *config.Service, containerName string) {
	// Skip if in deploy grace period (expected during updates).
	if h.updater.IsDeploying(svc.Name) {
		return
	}

	log.Printf("[healer] %s: container died unexpectedly", svc.Name)
	h.metrics.SetHealthy(svc.Name, false)
	h.setDegraded(svc.Name, true)
	h.dispatcher.Send(ctx, notify.Alert{
		Service:   svc.Name,
		Event:     "died",
		Message:   "Container exited unexpectedly.",
		Container: containerName,
		Level:     notify.LevelCritical,
	})
}

// findServiceByEvent matches a Docker event to a configured service
// using the com.docker.compose.project label or container name from event attributes.
func (h *Healer) findServiceByEvent(event docker.Event) *config.Service {
	project := event.Actor.Attributes["com.docker.compose.project"]
	name := event.Actor.Attributes["name"]
	for i := range h.cfg.Services {
		svc := &h.cfg.Services[i]
		if project != "" && svc.ComposeProject == project {
			return svc
		}
		if name != "" && svc.ContainerName == name {
			return svc
		}
	}
	return nil
}

func (h *Healer) inCooldown(containerName string) bool {
	h.cooldownsMu.Lock()
	defer h.cooldownsMu.Unlock()
	deadline, ok := h.cooldowns[containerName]
	if !ok {
		return false
	}
	return time.Now().Before(deadline)
}

func (h *Healer) setDegraded(serviceName string, degraded bool) {
	h.degradedMu.Lock()
	defer h.degradedMu.Unlock()
	if degraded {
		h.degraded[serviceName] = true
	} else {
		delete(h.degraded, serviceName)
	}
}

func (h *Healer) isDegraded(serviceName string) bool {
	h.degradedMu.Lock()
	defer h.degradedMu.Unlock()
	return h.degraded[serviceName]
}
