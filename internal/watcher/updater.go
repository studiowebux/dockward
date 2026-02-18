// Package watcher contains the core logic for image updates and health monitoring.
package watcher

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/studiowebux/dockward/internal/compose"
	"github.com/studiowebux/dockward/internal/config"
	"github.com/studiowebux/dockward/internal/docker"
	"github.com/studiowebux/dockward/internal/notify"
	"github.com/studiowebux/dockward/internal/registry"
)

// Updater polls the registry for image changes and triggers deploys with rollback.
type Updater struct {
	cfg        *config.Config
	docker     *docker.Client
	registry   *registry.Client
	dispatcher *notify.Dispatcher
	metrics    *Metrics

	// deploying tracks services currently in a deploy cycle.
	// The healer checks this to avoid interfering with rollback.
	deploying   map[string]time.Time
	deployingMu sync.RWMutex

	// blocked maps service name -> digest that caused a rollback.
	// Prevents infinite rollback loops by skipping known-bad digests.
	// Memory-only: cleared on watcher restart.
	blocked   map[string]string
	blockedMu sync.RWMutex
}

// NewUpdater creates an image updater.
func NewUpdater(cfg *config.Config, dc *docker.Client, rc *registry.Client, dispatcher *notify.Dispatcher, metrics *Metrics) *Updater {
	return &Updater{
		cfg:        cfg,
		docker:     dc,
		registry:   rc,
		dispatcher: dispatcher,
		metrics:    metrics,
		deploying:  make(map[string]time.Time),
		blocked:    make(map[string]string),
	}
}

// IsDeploying returns true if a service is currently in a deploy cycle.
// Used by the healer to avoid interfering with rollback.
func (u *Updater) IsDeploying(service string) bool {
	u.deployingMu.RLock()
	defer u.deployingMu.RUnlock()
	_, ok := u.deploying[service]
	return ok
}

// tryStartDeploy atomically sets the deploying flag for a service.
// Returns false if a deploy is already in progress (poll/API race guard).
func (u *Updater) tryStartDeploy(service string) bool {
	u.deployingMu.Lock()
	defer u.deployingMu.Unlock()
	if _, ok := u.deploying[service]; ok {
		return false
	}
	u.deploying[service] = time.Now()
	return true
}

// clearDeploying removes the deploying flag for a service.
func (u *Updater) clearDeploying(service string) {
	u.deployingMu.Lock()
	delete(u.deploying, service)
	u.deployingMu.Unlock()
}

// Run starts the polling loop. Blocks until ctx is cancelled.
func (u *Updater) Run(ctx context.Context) {
	interval := time.Duration(u.cfg.Registry.PollInterval) * time.Second
	log.Printf("[updater] polling every %s", interval)

	// Run once immediately on startup.
	u.pollAll(ctx)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			u.pollAll(ctx)
		}
	}
}

func (u *Updater) pollAll(ctx context.Context) {
	u.metrics.RecordPoll()
	for _, svc := range u.cfg.Services {
		if !svc.AutoUpdate {
			continue
		}
		if ctx.Err() != nil {
			return
		}
		if err := u.checkAndUpdate(ctx, svc); err != nil {
			log.Printf("[updater] %s: %v", svc.Name, err)
		}
	}
}

func (u *Updater) checkAndUpdate(ctx context.Context, svc config.Service) error {
	// Step 1: Get remote digest from registry.
	remoteDigest, err := u.registry.RemoteDigest(ctx, svc.Image)
	if err != nil {
		return fmt.Errorf("remote digest: %w", err)
	}

	// Check if this digest is blocked (caused a previous rollback).
	u.blockedMu.RLock()
	blockedDigest := u.blocked[svc.Name]
	u.blockedMu.RUnlock()
	if blockedDigest != "" {
		if blockedDigest == remoteDigest {
			return nil // Still the same bad digest, skip silently.
		}
		// Remote digest changed (fix pushed), clear the block.
		log.Printf("[updater] %s: blocked digest changed, unblocking", svc.Name)
		u.blockedMu.Lock()
		delete(u.blocked, svc.Name)
		u.blockedMu.Unlock()
		u.metrics.SetBlocked(svc.Name, false)
	}

	// Step 2: Get local digest from Docker.
	registryPrefix := registryHost(u.cfg.Registry.URL) + "/" + imageName(svc.Image)
	fullImage := registryPrefix + ":" + imageTag(svc.Image)

	localImg, err := u.docker.InspectImage(ctx, fullImage)
	if err != nil {
		// Image not pulled yet locally, treat as needing update.
		log.Printf("[updater] %s: local image not found, pulling", svc.Name)
		return u.deploy(ctx, svc, "", remoteDigest)
	}

	localDigest := localImg.LocalDigest(registryPrefix)
	if localDigest == "" {
		log.Printf("[updater] %s: no local digest found, pulling", svc.Name)
		return u.deploy(ctx, svc, "", remoteDigest)
	}

	// Step 3: Compare.
	if localDigest == remoteDigest {
		return nil
	}

	log.Printf("[updater] %s: digest changed %s -> %s", svc.Name, shortDigest(localDigest), shortDigest(remoteDigest))
	return u.deploy(ctx, svc, localDigest, remoteDigest)
}

func (u *Updater) deploy(ctx context.Context, svc config.Service, oldDigest, newDigest string) error {
	// Atomic deploy guard: prevent concurrent deploys for the same service.
	if !u.tryStartDeploy(svc.Name) {
		log.Printf("[updater] %s: deploy already in progress, skipping", svc.Name)
		return nil
	}

	registryPrefix := registryHost(u.cfg.Registry.URL) + "/" + imageName(svc.Image)
	fullImage := registryPrefix + ":" + imageTag(svc.Image)

	// Step 1: Tag current image as :rollback (if it exists).
	if oldDigest != "" {
		if err := u.docker.TagImage(ctx, fullImage, registryPrefix, "rollback"); err != nil {
			log.Printf("[updater] %s: failed to tag rollback: %v", svc.Name, err)
			// Continue anyway; rollback won't be available.
		}
	}

	// Step 2: Pull new image and recreate via compose.
	log.Printf("[updater] %s: pulling and deploying", svc.Name)
	if err := compose.Pull(ctx, svc.ComposeFile, svc.ComposeProject); err != nil {
		u.clearDeploying(svc.Name)
		return fmt.Errorf("compose pull: %w", err)
	}
	if err := compose.Up(ctx, svc.ComposeFile, svc.ComposeProject); err != nil {
		u.clearDeploying(svc.Name)
		return fmt.Errorf("compose up: %w", err)
	}

	// Step 3: Verify health asynchronously. clearDeploying is called via defer in verifyAfterDeploy.
	go u.verifyAfterDeploy(ctx, svc, oldDigest, newDigest, fullImage, registryPrefix)

	return nil
}

func (u *Updater) verifyAfterDeploy(ctx context.Context, svc config.Service, oldDigest, newDigest, fullImage, registryPrefix string) {
	defer u.clearDeploying(svc.Name)

	grace := time.Duration(svc.HealthGrace) * time.Second
	deadline := time.Now().Add(grace)
	log.Printf("[updater] %s: health polling for %s", svc.Name, grace)

	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		containerID := u.findContainerByProject(ctx, svc.ComposeProject)
		if containerID == "" {
			if time.Now().After(deadline) {
				log.Printf("[updater] %s: container not found after grace period, rolling back", svc.Name)
				u.rollback(ctx, svc, oldDigest, newDigest, fullImage, registryPrefix, "container not found after deploy")
				return
			}
			continue // Container may be starting up.
		}

		info, err := u.docker.InspectContainer(ctx, containerID)
		if err != nil {
			log.Printf("[updater] %s: inspect failed during health poll: %v", svc.Name, err)
			if time.Now().After(deadline) {
				u.rollback(ctx, svc, oldDigest, newDigest, fullImage, registryPrefix, "inspect failed: "+err.Error())
				return
			}
			continue
		}

		// No healthcheck configured: success if running.
		if info.State.Health == nil {
			if info.State.Running {
				u.onDeploySuccess(ctx, svc, oldDigest, newDigest, info.ContainerName(), registryPrefix)
				return
			}
			if time.Now().After(deadline) {
				u.rollback(ctx, svc, oldDigest, newDigest, fullImage, registryPrefix, "container not running")
				return
			}
			continue
		}

		switch info.State.Health.Status {
		case "healthy":
			u.onDeploySuccess(ctx, svc, oldDigest, newDigest, info.ContainerName(), registryPrefix)
			return
		case "unhealthy":
			reason := info.LastHealthOutput()
			log.Printf("[updater] %s: unhealthy, rolling back immediately", svc.Name)
			u.rollback(ctx, svc, oldDigest, newDigest, fullImage, registryPrefix, reason)
			return
		default: // "starting" or other transient states
			if time.Now().After(deadline) {
				reason := info.LastHealthOutput()
				log.Printf("[updater] %s: still %s after grace period, rolling back", svc.Name, info.State.Health.Status)
				u.rollback(ctx, svc, oldDigest, newDigest, fullImage, registryPrefix, reason)
				return
			}
			// Keep polling.
		}
	}
}

func (u *Updater) onDeploySuccess(ctx context.Context, svc config.Service, oldDigest, newDigest, containerName, registryPrefix string) {
	log.Printf("[updater] %s: deployed successfully", svc.Name)
	u.metrics.IncUpdates(svc.Name)
	u.metrics.SetHealthy(svc.Name, true)
	u.dispatcher.Send(ctx, notify.Alert{
		Service:   svc.Name,
		Event:     "updated",
		Message:   "Deployed new image successfully.",
		OldDigest: oldDigest,
		NewDigest: newDigest,
		Container: containerName,
		Level:     notify.LevelInfo,
	})
	u.cleanupRollback(ctx, registryPrefix)
}

func (u *Updater) rollback(ctx context.Context, svc config.Service, oldDigest, newDigest, fullImage, registryPrefix, reason string) {
	log.Printf("[updater] %s: rolling back. Reason: %s", svc.Name, reason)
	u.metrics.IncRollbacks(svc.Name)
	u.metrics.SetHealthy(svc.Name, false)

	// Block this digest to prevent infinite rollback loops.
	u.blockedMu.Lock()
	u.blocked[svc.Name] = newDigest
	u.blockedMu.Unlock()
	u.metrics.SetBlocked(svc.Name, true)
	log.Printf("[updater] %s: blocked digest %s", svc.Name, shortDigest(newDigest))

	// Retag :rollback as :latest and redeploy.
	rollbackImage := registryPrefix + ":rollback"
	tag := imageTag(svc.Image)

	if err := u.docker.TagImage(ctx, rollbackImage, registryPrefix, tag); err != nil {
		log.Printf("[updater] %s: rollback tag failed: %v", svc.Name, err)
		u.metrics.IncFailures(svc.Name)
		u.dispatcher.Send(ctx, notify.Alert{
			Service:   svc.Name,
			Event:     "rolled_back",
			Message:   "Rollback failed: could not retag image.",
			Reason:    reason,
			OldDigest: oldDigest,
			NewDigest: newDigest,
			Level:     notify.LevelCritical,
		})
		return
	}

	if err := compose.Up(ctx, svc.ComposeFile, svc.ComposeProject); err != nil {
		log.Printf("[updater] %s: rollback compose up failed: %v", svc.Name, err)
		u.metrics.IncFailures(svc.Name)
		u.dispatcher.Send(ctx, notify.Alert{
			Service:   svc.Name,
			Event:     "rolled_back",
			Message:   "Rollback compose up failed.",
			Reason:    reason,
			OldDigest: oldDigest,
			NewDigest: newDigest,
			Level:     notify.LevelCritical,
		})
		return
	}

	u.dispatcher.Send(ctx, notify.Alert{
		Service:   svc.Name,
		Event:     "rolled_back",
		Message:   "Rolled back to previous image.",
		Reason:    reason,
		OldDigest: oldDigest,
		NewDigest: newDigest,
		Level:     notify.LevelWarning,
	})

	u.cleanupRollback(ctx, registryPrefix)
}

func (u *Updater) cleanupRollback(ctx context.Context, registryPrefix string) {
	_ = u.docker.RemoveImage(ctx, registryPrefix+":rollback")
}

// BlockedDigests returns a copy of the blocked service->digest map.
func (u *Updater) BlockedDigests() map[string]string {
	u.blockedMu.RLock()
	defer u.blockedMu.RUnlock()
	result := make(map[string]string, len(u.blocked))
	for k, v := range u.blocked {
		result[k] = v
	}
	return result
}

// UnblockService clears the blocked digest for a service.
// Returns true if the service was blocked.
func (u *Updater) UnblockService(service string) bool {
	u.blockedMu.Lock()
	_, ok := u.blocked[service]
	delete(u.blocked, service)
	u.blockedMu.Unlock()
	if ok {
		u.metrics.SetBlocked(service, false)
		log.Printf("[updater] %s: manually unblocked", service)
	}
	return ok
}

// findContainerByProject finds a container by compose project label.
// Returns the first container ID (single-container services).
func (u *Updater) findContainerByProject(ctx context.Context, project string) string {
	containers, err := u.docker.ListContainersByProject(ctx, project)
	if err != nil {
		log.Printf("[updater] failed to list containers for project %s: %v", project, err)
		return ""
	}
	if len(containers) == 0 {
		return ""
	}
	return containers[0].ID
}

// Helper functions for parsing image references.

func registryHost(url string) string {
	s := strings.TrimPrefix(url, "http://")
	s = strings.TrimPrefix(s, "https://")
	return strings.TrimRight(s, "/")
}

func imageName(image string) string {
	if idx := strings.LastIndex(image, ":"); idx >= 0 {
		return image[:idx]
	}
	return image
}

func imageTag(image string) string {
	if idx := strings.LastIndex(image, ":"); idx >= 0 {
		return image[idx+1:]
	}
	return "latest"
}

func shortDigest(d string) string {
	if len(d) > 19 {
		return d[:19]
	}
	return d
}
