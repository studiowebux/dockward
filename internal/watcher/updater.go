// Package watcher contains the core logic for image updates and health monitoring.
package watcher

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/studiowebux/dockward/internal/audit"
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
	audit      *audit.Logger

	// deploying tracks services currently in a deploy cycle.
	// The healer checks this to avoid interfering with rollback.
	deploying   map[string]time.Time
	deployingMu sync.RWMutex

	// blocked maps service name -> digest that caused a rollback.
	// Prevents infinite rollback loops by skipping known-bad digests.
	// Memory-only: cleared on watcher restart.
	blocked   map[string]string
	blockedMu sync.RWMutex

	// notFound maps service name -> remote digest at time of failure.
	// Suppresses repeated deploy attempts when the local image cannot be
	// resolved (e.g. compose file image field mismatch). Cleared when the
	// remote digest changes, allowing a retry.
	notFound   map[string]string
	notFoundMu sync.RWMutex

	// errored maps service name -> last error message.
	// Suppresses repeated notifications and log spam for persistent errors
	// (e.g. registry unreachable, compose network not found).
	// Cleared when the service poll succeeds again.
	errored   map[string]string
	erroredMu sync.RWMutex

	// startAttempted maps service name -> remote digest at time of start.
	// Prevents repeated compose up when a container won't stay running
	// (e.g. missing env vars, bad config). Cleared when the remote digest
	// changes, allowing a retry with a new image.
	startAttempted   map[string]string
	startAttemptedMu sync.RWMutex

	// composeHashes maps service name -> SHA-256 hex of concatenated compose file contents.
	// Used by checkComposeDrift to detect spec changes between poll cycles.
	composeHashes   map[string]string
	composeHashesMu sync.Mutex

	// deployed maps service name -> deployed image reference, digest, and container uptime.
	// Updated after each successful deploy and on each poll when image is up to date.
	deployed   map[string]DeployedInfo
	deployedMu sync.RWMutex
}

// NewUpdater creates an image updater.
func NewUpdater(cfg *config.Config, dc *docker.Client, rc *registry.Client, dispatcher *notify.Dispatcher, metrics *Metrics, al *audit.Logger) *Updater {
	return &Updater{
		cfg:            cfg,
		docker:         dc,
		registry:       rc,
		dispatcher:     dispatcher,
		metrics:        metrics,
		audit:          al,
		deploying:      make(map[string]time.Time),
		blocked:        make(map[string]string),
		notFound:       make(map[string]string),
		errored:        make(map[string]string),
		startAttempted: make(map[string]string),
		composeHashes:  make(map[string]string),
		deployed:       make(map[string]DeployedInfo),
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
		if ctx.Err() != nil {
			return
		}
		if svc.AutoUpdate {
			if err := u.checkAndUpdate(ctx, svc); err != nil {
				u.handlePollError(ctx, svc, err)
			}
		}
		if svc.ComposeWatch {
			if err := u.checkComposeDrift(ctx, svc); err != nil {
				log.Printf("[updater] %s: compose drift check error: %v", svc.Name, err)
			}
		}
	}
}

// composeHash returns the SHA-256 hex digest of the concatenated contents of all files.
func (u *Updater) composeHash(files []string) (string, error) {
	h := sha256.New()
	for _, path := range files {
		f, err := os.Open(path) // #nosec G304 -- path from config, not user input
		if err != nil {
			return "", fmt.Errorf("open %s: %w", path, err)
		}
		_, err = io.Copy(h, f)
		_ = f.Close() // #nosec G104 -- read-only file; close error does not affect hash correctness
		if err != nil {
			return "", fmt.Errorf("read %s: %w", path, err)
		}
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}

// checkComposeDrift detects byte-level changes in compose files and runs
// compose up -d (no pull) when the content hash differs from the last known hash.
// First-run stores the hash without deploying — the service is already running the current spec.
func (u *Updater) checkComposeDrift(ctx context.Context, svc config.Service) error {
	if len(svc.ComposeFiles) == 0 {
		return nil
	}

	hash, err := u.composeHash(svc.ComposeFiles)
	if err != nil {
		return err
	}

	u.composeHashesMu.Lock()
	prev := u.composeHashes[svc.Name]
	u.composeHashes[svc.Name] = hash
	u.composeHashesMu.Unlock()

	if prev == "" || prev == hash {
		return nil // first run or no change
	}

	log.Printf("[updater] %s: compose file changed, redeploying", svc.Name)
	if err := compose.Up(ctx, svc.ComposeFiles, svc.ComposeProject, svc.EnvFile); err != nil {
		return fmt.Errorf("compose up (drift): %w", err)
	}

	if werr := u.audit.Write(audit.Entry{
		Service: svc.Name,
		Event:   "compose_drift",
		Message: "Compose file changed. Redeployed without image pull.",
		Level:   "info",
	}); werr != nil {
		log.Printf("[updater] %s: audit write error: %v", svc.Name, werr)
	}

	u.dispatcher.Send(ctx, notify.Alert{
		Service: svc.Name,
		Event:   "compose_drift",
		Message: "Compose file changed. Redeployed without image pull.",
		Level:   notify.LevelInfo,
	})

	return nil
}

// handlePollError sends a notification on the first occurrence of an error
// for a service. Suppresses repeated log and notification spam if the error
// message is unchanged across poll cycles.
func (u *Updater) handlePollError(ctx context.Context, svc config.Service, err error) {
	msg := err.Error()

	u.erroredMu.RLock()
	prev := u.errored[svc.Name]
	u.erroredMu.RUnlock()

	if prev == msg {
		return
	}

	u.erroredMu.Lock()
	u.errored[svc.Name] = msg
	u.erroredMu.Unlock()

	log.Printf("[updater] %s: %v", svc.Name, err)
	u.metrics.IncFailures(svc.Name)
	u.dispatcher.Send(ctx, notify.Alert{
		Service: svc.Name,
		Event:   "error",
		Message: fmt.Sprintf("Poll error: %s", msg),
		Level:   notify.LevelCritical,
	})
}

// clearPollError logs recovery when a previously errored service succeeds.
func (u *Updater) clearPollError(svc config.Service) {
	u.erroredMu.RLock()
	wasErrored := u.errored[svc.Name] != ""
	u.erroredMu.RUnlock()

	if !wasErrored {
		return
	}

	u.erroredMu.Lock()
	delete(u.errored, svc.Name)
	u.erroredMu.Unlock()

	log.Printf("[updater] %s: recovered from previous error", svc.Name)
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

	// Check if this service is in the notFound suppression map.
	u.notFoundMu.RLock()
	notFoundDigest := u.notFound[svc.Name]
	u.notFoundMu.RUnlock()
	if notFoundDigest != "" {
		if notFoundDigest == remoteDigest {
			return nil // Same unresolvable digest, skip silently.
		}
		// Remote digest changed, clear suppression and retry.
		log.Printf("[updater] %s: registry digest changed since not-found suppression, retrying", svc.Name)
		u.notFoundMu.Lock()
		delete(u.notFound, svc.Name)
		u.notFoundMu.Unlock()
	}

	// Step 2: Get local digest from Docker.
	registryPrefix := registryHost(u.cfg.Registry.URL) + "/" + imageName(svc.Image)

	localDigest := u.resolveLocalDigest(ctx, svc, registryPrefix)
	if localDigest == "" {
		// Suppress future polls until the registry digest changes.
		log.Printf("[updater] %s: no local digest resolved, suppressing until registry digest changes", svc.Name)
		u.notFoundMu.Lock()
		u.notFound[svc.Name] = remoteDigest
		u.notFoundMu.Unlock()
		u.dispatcher.Send(ctx, notify.Alert{
			Service: svc.Name,
			Event:   "not_found",
			Message: "Image not found locally. Verify compose file image field matches registry. Suppressing until registry digest changes.",
			Level:   notify.LevelWarning,
		})
		if err := u.audit.Write(audit.Entry{
			Service: svc.Name,
			Event:   "not_found",
			Message: "Image not found locally. Suppressing until registry digest changes.",
			Level:   "warning",
		}); err != nil {
			log.Printf("[updater] %s: audit write error: %v", svc.Name, err)
		}
		return nil
	}

	// Step 3: Compare.
	if localDigest == remoteDigest {
		// Digests match, but verify at least one container is running.
		container, status := u.findContainerByProject(ctx, svc.ComposeProject)
		if status == containerRunning {
			u.setDeployedInfo(svc.Name, container.Image, localDigest, container.Status)
			u.startAttemptedMu.Lock()
			delete(u.startAttempted, svc.Name)
			u.startAttemptedMu.Unlock()
			u.clearPollError(svc)
			return nil
		}

		// No running container. Only intervene if auto_start is enabled.
		if !svc.AutoStart {
			return nil
		}

		// Check if we already tried this digest.
		u.startAttemptedMu.RLock()
		attemptedDigest := u.startAttempted[svc.Name]
		u.startAttemptedMu.RUnlock()
		if attemptedDigest == remoteDigest {
			return nil
		}

		u.startAttemptedMu.Lock()
		u.startAttempted[svc.Name] = remoteDigest
		u.startAttemptedMu.Unlock()

		switch status {
		case containerStuck:
			// Containers exist but none running (created/restarting).
			// Force a clean restart to recover from the stuck state.
			log.Printf("[updater] %s: containers stuck (created/restarting), forcing down+up", svc.Name)
			if err := compose.Restart(ctx, svc.ComposeFiles, svc.ComposeProject, svc.EnvFile); err != nil {
				return fmt.Errorf("compose restart (stuck containers): %w", err)
			}
			u.dispatcher.Send(ctx, notify.Alert{
				Service: svc.Name,
				Event:   "started",
				Message: "Containers were stuck. Forced restart (down+up).",
				Level:   notify.LevelWarning,
			})
		default:
			// No containers at all. Normal start.
			log.Printf("[updater] %s: image up to date but no containers, starting compose project", svc.Name)
			if err := compose.Up(ctx, svc.ComposeFiles, svc.ComposeProject, svc.EnvFile); err != nil {
				return fmt.Errorf("compose up (no running container): %w", err)
			}
			u.dispatcher.Send(ctx, notify.Alert{
				Service: svc.Name,
				Event:   "started",
				Message: "Image up to date but no containers found. Started compose project.",
				Level:   notify.LevelWarning,
			})
		}
		return nil
	}

	log.Printf("[updater] %s: digest changed %s -> %s", svc.Name, shortDigest(localDigest), shortDigest(remoteDigest))
	u.clearPollError(svc)
	return u.deploy(ctx, svc, localDigest, remoteDigest)
}

// resolveLocalDigest tries two strategies to find the local image digest:
//  1. Inspect image by constructed reference (registryHost/name:tag).
//  2. Fallback: find the running container by compose project label, get its
//     image ID, inspect by ID, and extract digest from RepoDigests.
func (u *Updater) resolveLocalDigest(ctx context.Context, svc config.Service, registryPrefix string) string {
	fullImage := registryPrefix + ":" + imageTag(svc.Image)

	// Strategy 1: direct image inspect by reference.
	localImg, err := u.docker.InspectImage(ctx, fullImage)
	if err == nil {
		if d := localImg.LocalDigest(registryPrefix); d != "" {
			return d
		}
		log.Printf("[updater] %s: image found by reference but no matching digest in RepoDigests", svc.Name)
	} else {
		log.Printf("[updater] %s: inspect image %s failed: %v", svc.Name, fullImage, err)
	}

	// Strategy 2: resolve via running container's image ID.
	container, status := u.findContainerByProject(ctx, svc.ComposeProject)
	if status != containerRunning {
		log.Printf("[updater] %s: no running container for fallback digest resolution", svc.Name)
		return ""
	}

	info, err := u.docker.InspectContainer(ctx, container.ID)
	if err != nil {
		log.Printf("[updater] %s: container inspect failed during fallback: %v", svc.Name, err)
		return ""
	}

	// ContainerInspect.Image is the image ID (sha256:...).
	imgByID, err := u.docker.InspectImage(ctx, info.Image)
	if err != nil {
		log.Printf("[updater] %s: inspect image by ID %s failed: %v", svc.Name, info.Image, err)
		return ""
	}

	if d := imgByID.LocalDigest(registryPrefix); d != "" {
		log.Printf("[updater] %s: resolved digest via container fallback", svc.Name)
		return d
	}

	log.Printf("[updater] %s: fallback image has no matching RepoDigests for %s", svc.Name, registryPrefix)
	return ""
}

func (u *Updater) deploy(ctx context.Context, svc config.Service, oldDigest, newDigest string) error {
	// Atomic deploy guard: prevent concurrent deploys for the same service.
	if !u.tryStartDeploy(svc.Name) {
		log.Printf("[updater] %s: deploy already in progress, skipping", svc.Name)
		return nil
	}

	registryPrefix := registryHost(u.cfg.Registry.URL) + "/" + imageName(svc.Image)
	fullImage := registryPrefix + ":" + imageTag(svc.Image)

	// Step 1: Tag current image as :rollback using the running container's image ID.
	// We resolve by image ID because compose may store the image under a short reference
	// (e.g. "firegen:latest") that does not match the constructed registry prefix form
	// ("localhost:5000/firegen:latest"). TagImage by ID always succeeds.
	// We also capture the compose image reference so rollback can retag to the right name.
	var oldImageRef string
	if oldDigest != "" {
		if container, cStatus := u.findContainerByProject(ctx, svc.ComposeProject); cStatus == containerRunning {
			if info, err := u.docker.InspectContainer(ctx, container.ID); err == nil {
				oldImageRef = info.Config.Image // reference compose used (e.g. "firegen:latest")
				if err := u.docker.TagImage(ctx, info.Image, registryPrefix, "rollback"); err != nil {
					log.Printf("[updater] %s: failed to tag rollback: %v", svc.Name, err)
					// Continue anyway; rollback won't be available.
				}
			}
		}
	}

	// Step 2: Pull new image and recreate via compose.
	log.Printf("[updater] %s: pulling and deploying", svc.Name)
	if err := compose.Pull(ctx, svc.ComposeFiles, svc.ComposeProject, svc.EnvFile); err != nil {
		u.clearDeploying(svc.Name)
		return fmt.Errorf("compose pull: %w", err)
	}
	if err := compose.Up(ctx, svc.ComposeFiles, svc.ComposeProject, svc.EnvFile); err != nil {
		u.clearDeploying(svc.Name)
		return fmt.Errorf("compose up: %w", err)
	}

	// Step 3: Verify health asynchronously. clearDeploying is called via defer in verifyAfterDeploy.
	go u.verifyAfterDeploy(ctx, svc, oldDigest, newDigest, fullImage, registryPrefix, oldImageRef)

	return nil
}

func (u *Updater) verifyAfterDeploy(ctx context.Context, svc config.Service, oldDigest, newDigest, fullImage, registryPrefix, oldImageRef string) {
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

		container, cStatus := u.findContainerByProject(ctx, svc.ComposeProject)
		if cStatus == containerNone {
			if time.Now().After(deadline) {
				log.Printf("[updater] %s: container not found after grace period, rolling back", svc.Name)
				u.rollback(ctx, svc, oldDigest, newDigest, fullImage, registryPrefix, "container not found after deploy", oldImageRef)
				return
			}
			continue // Container may be starting up.
		}

		info, err := u.docker.InspectContainer(ctx, container.ID)
		if err != nil {
			log.Printf("[updater] %s: inspect failed during health poll: %v", svc.Name, err)
			if time.Now().After(deadline) {
				u.rollback(ctx, svc, oldDigest, newDigest, fullImage, registryPrefix, "inspect failed: "+err.Error(), oldImageRef)
				return
			}
			continue
		}

		// No healthcheck configured: success if running.
		if info.State.Health == nil {
			if info.State.Running {
				u.onDeploySuccess(ctx, svc, oldDigest, newDigest, info.ContainerName(), registryPrefix, info.Config.Image)
				return
			}
			if time.Now().After(deadline) {
				u.rollback(ctx, svc, oldDigest, newDigest, fullImage, registryPrefix, "container not running", oldImageRef)
				return
			}
			continue
		}

		switch info.State.Health.Status {
		case "healthy":
			u.onDeploySuccess(ctx, svc, oldDigest, newDigest, info.ContainerName(), registryPrefix, info.Config.Image)
			return
		case "unhealthy":
			reason := info.LastHealthOutput()
			log.Printf("[updater] %s: unhealthy, rolling back immediately", svc.Name)
			u.rollback(ctx, svc, oldDigest, newDigest, fullImage, registryPrefix, reason, oldImageRef)
			return
		default: // "starting" or other transient states
			if time.Now().After(deadline) {
				reason := info.LastHealthOutput()
				log.Printf("[updater] %s: still %s after grace period, rolling back", svc.Name, info.State.Health.Status)
				u.rollback(ctx, svc, oldDigest, newDigest, fullImage, registryPrefix, reason, oldImageRef)
				return
			}
			// Keep polling.
		}
	}
}

func (u *Updater) onDeploySuccess(ctx context.Context, svc config.Service, oldDigest, newDigest, containerName, registryPrefix, imageRef string) {
	log.Printf("[updater] %s: deployed successfully", svc.Name)
	u.metrics.IncUpdates(svc.Name)
	u.metrics.SetHealthy(svc.Name, true)
	u.setDeployedInfo(svc.Name, imageRef, newDigest, "")
	u.dispatcher.Send(ctx, notify.Alert{
		Service:   svc.Name,
		Event:     "updated",
		Message:   "Deployed new image successfully.",
		OldDigest: oldDigest,
		NewDigest: newDigest,
		Container: containerName,
		Level:     notify.LevelInfo,
	})
	if err := u.audit.Write(audit.Entry{
		Service:   svc.Name,
		Event:     "updated",
		Message:   "Deployed new image successfully.",
		Level:     "info",
		OldDigest: oldDigest,
		NewDigest: newDigest,
		Container: containerName,
	}); err != nil {
		log.Printf("[updater] %s: audit write error: %v", svc.Name, err)
	}
	u.cleanupRollback(ctx, registryPrefix)
}

func (u *Updater) rollback(ctx context.Context, svc config.Service, oldDigest, newDigest, fullImage, registryPrefix, reason, oldImageRef string) {
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

	// If compose uses a different image reference than the registry-prefixed form
	// (e.g. "localhost:5000/firegen:latest" vs "firegen:latest"), also retag to
	// that reference so compose up picks up the old image correctly.
	// This handles the case where Docker stores the image without a RepoTag for
	// the registry-prefixed name after a local registry pull.
	composeRef := registryPrefix + ":" + tag
	if oldImageRef != "" && oldImageRef != composeRef {
		if err := u.docker.TagImage(ctx, rollbackImage, imageName(oldImageRef), imageTag(oldImageRef)); err != nil {
			log.Printf("[updater] %s: rollback retag to compose ref %s failed: %v", svc.Name, oldImageRef, err)
		}
	}

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
		if werr := u.audit.Write(audit.Entry{
			Service:   svc.Name,
			Event:     "rolled_back",
			Message:   "Rollback failed: could not retag image.",
			Level:     "critical",
			OldDigest: oldDigest,
			NewDigest: newDigest,
			Reason:    reason,
		}); werr != nil {
			log.Printf("[updater] %s: audit write error: %v", svc.Name, werr)
		}
		return
	}

	if err := compose.Up(ctx, svc.ComposeFiles, svc.ComposeProject, svc.EnvFile); err != nil {
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
		if werr := u.audit.Write(audit.Entry{
			Service:   svc.Name,
			Event:     "rolled_back",
			Message:   "Rollback compose up failed.",
			Level:     "critical",
			OldDigest: oldDigest,
			NewDigest: newDigest,
			Reason:    reason,
		}); werr != nil {
			log.Printf("[updater] %s: audit write error: %v", svc.Name, werr)
		}
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
	if err := u.audit.Write(audit.Entry{
		Service:   svc.Name,
		Event:     "rolled_back",
		Message:   "Rolled back to previous image.",
		Level:     "warning",
		OldDigest: oldDigest,
		NewDigest: newDigest,
		Reason:    reason,
	}); err != nil {
		log.Printf("[updater] %s: audit write error: %v", svc.Name, err)
	}

	u.cleanupRollback(ctx, registryPrefix)
}

func (u *Updater) cleanupRollback(ctx context.Context, registryPrefix string) {
	if err := u.docker.RemoveImage(ctx, registryPrefix+":rollback"); err != nil {
		log.Printf("[updater] failed to remove rollback image %s:rollback: %v", registryPrefix, err)
	}
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

// NotFoundServices returns a copy of the not-found service->digest map.
func (u *Updater) NotFoundServices() map[string]string {
	u.notFoundMu.RLock()
	defer u.notFoundMu.RUnlock()
	result := make(map[string]string, len(u.notFound))
	for k, v := range u.notFound {
		result[k] = v
	}
	return result
}

// ErroredServices returns a copy of the errored service->message map.
func (u *Updater) ErroredServices() map[string]string {
	u.erroredMu.RLock()
	defer u.erroredMu.RUnlock()
	result := make(map[string]string, len(u.errored))
	for k, v := range u.errored {
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

// DeployedInfo holds the deployed image reference, digest, and container uptime for a service.
type DeployedInfo struct {
	Image  string // full image reference from container (e.g. localhost:5000/myapp:latest)
	Digest string // full digest (displayed as shortDigest in the UI)
	Uptime string // Docker status string (e.g. "Up 3 hours")
}

// containerStatus describes the state of containers in a compose project.
type containerStatus int

const (
	containerNone    containerStatus = iota // No containers exist
	containerStuck                          // Containers exist but none running (created/restarting)
	containerRunning                        // At least one container is running or exited
)

// setDeployedInfo stores the deployed image reference, digest, and uptime for a service.
func (u *Updater) setDeployedInfo(service, image, digest, uptime string) {
	u.deployedMu.Lock()
	u.deployed[service] = DeployedInfo{Image: image, Digest: digest, Uptime: uptime}
	u.deployedMu.Unlock()
}

// DeployedInfos returns a copy of the deployed service info map.
func (u *Updater) DeployedInfos() map[string]DeployedInfo {
	u.deployedMu.RLock()
	defer u.deployedMu.RUnlock()
	result := make(map[string]DeployedInfo, len(u.deployed))
	for k, v := range u.deployed {
		result[k] = v
	}
	return result
}

// findContainerByProject finds a container by compose project label.
// Returns the first running container and the project status.
func (u *Updater) findContainerByProject(ctx context.Context, project string) (*docker.Container, containerStatus) {
	containers, err := u.docker.ListContainersByProject(ctx, project)
	if err != nil {
		log.Printf("[updater] failed to list containers for project %s: %v", project, err)
		return nil, containerNone
	}
	if len(containers) == 0 {
		return nil, containerNone
	}
	// Return first running container if any exist.
	for i := range containers {
		if containers[i].State == "running" || containers[i].State == "exited" {
			return &containers[i], containerRunning
		}
	}
	// Containers exist but all are stuck (created/restarting).
	// Return the first so callers can still inspect if needed.
	return &containers[0], containerStuck
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
