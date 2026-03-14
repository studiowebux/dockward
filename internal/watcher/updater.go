// Package watcher contains the core logic for image updates and health monitoring.
package watcher

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"github.com/studiowebux/dockward/internal/logger"
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

	// blocked maps "service/image" -> digest that caused a rollback.
	// Prevents infinite rollback loops by skipping known-bad digests.
	// Memory-only: cleared on watcher restart.
	blocked   map[string]string
	blockedMu sync.RWMutex

	// notFound maps "service/image" -> remote digest at time of failure.
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

	// deployed maps "service/image" -> deployed image reference and digest.
	// Updated after each successful deploy and on each poll when image is up to date.
	deployed   map[string]DeployedInfo
	deployedMu sync.RWMutex

	// lastChecked maps service name -> time of last check
	lastChecked   map[string]time.Time
	lastCheckedMu sync.RWMutex

	// checkStatus maps service name -> current check status
	checkStatus   map[string]string
	checkStatusMu sync.RWMutex
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
		lastChecked:    make(map[string]time.Time),
		checkStatus:    make(map[string]string),
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

// DeployingCount returns the number of services currently in a deploy cycle.
func (u *Updater) DeployingCount() int {
	u.deployingMu.RLock()
	defer u.deployingMu.RUnlock()
	return len(u.deploying)
}

// Run starts the polling loop. Blocks until ctx is cancelled.
func (u *Updater) Run(ctx context.Context) {
	interval := time.Duration(u.cfg.Registry.PollInterval) * time.Second
	logger.Printf("[updater] polling every %s", interval)

	// Run once immediately on startup.
	u.pollAll(ctx)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	// Cleanup ticker - every hour, clean old entries from maps
	cleanupTicker := time.NewTicker(1 * time.Hour)
	defer cleanupTicker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			u.pollAll(ctx)
		case <-cleanupTicker.C:
			u.cleanupOldEntries()
		}
	}
}

// cleanupOldEntries removes entries for services that no longer exist in config
func (u *Updater) cleanupOldEntries() {
	// Build set of current service names
	currentServices := make(map[string]bool)
	for _, svc := range u.cfg.Services {
		currentServices[svc.Name] = true
		for _, img := range svc.Images {
			currentServices[svc.Name+"/"+img] = true
		}
	}

	// Clean lastChecked
	u.lastCheckedMu.Lock()
	for k := range u.lastChecked {
		if !currentServices[k] {
			delete(u.lastChecked, k)
		}
	}
	u.lastCheckedMu.Unlock()

	// Clean checkStatus
	u.checkStatusMu.Lock()
	for k := range u.checkStatus {
		if !currentServices[k] {
			delete(u.checkStatus, k)
		}
	}
	u.checkStatusMu.Unlock()

	// Clean deployed
	u.deployedMu.Lock()
	for k := range u.deployed {
		// Check if service/image key still exists
		found := false
		for svc := range currentServices {
			if strings.HasPrefix(k, svc) {
				found = true
				break
			}
		}
		if !found {
			delete(u.deployed, k)
		}
	}
	u.deployedMu.Unlock()

	// Clean errored
	u.erroredMu.Lock()
	for k := range u.errored {
		if !currentServices[k] {
			delete(u.errored, k)
		}
	}
	u.erroredMu.Unlock()

	// Clean startAttempted
	u.startAttemptedMu.Lock()
	for k := range u.startAttempted {
		if !currentServices[k] {
			delete(u.startAttempted, k)
		}
	}
	u.startAttemptedMu.Unlock()

	// Clean composeHashes
	u.composeHashesMu.Lock()
	for k := range u.composeHashes {
		if !currentServices[k] {
			delete(u.composeHashes, k)
		}
	}
	u.composeHashesMu.Unlock()

	logger.Printf("[updater] cleaned old entries from state maps")
}

func (u *Updater) pollAll(ctx context.Context) {
	u.metrics.RecordPoll()
	for _, svc := range u.cfg.Services {
		if ctx.Err() != nil {
			return
		}
		if svc.AutoUpdate {
			if err := u.checkAndUpdate(ctx, svc, false); err != nil {
				u.handlePollError(ctx, svc, err)
			}
		}
		if svc.ComposeWatch {
			if err := u.checkComposeDrift(ctx, svc); err != nil {
				logger.Printf("[updater] %s: compose drift check error: %v", svc.Name, err)
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

	logger.Printf("[updater] %s: compose file changed, redeploying", svc.Name)
	u.tryStartDeploy(svc.Name)
	composeOut, err := compose.Up(ctx, u.cfg.Runtime, svc.ComposeFiles, svc.ComposeProject, svc.EnvFile)
	if err != nil {
		u.clearDeploying(svc.Name)
		return fmt.Errorf("compose up (drift): %w", err)
	}

	if werr := u.audit.Write(audit.Entry{
		Service: svc.Name,
		Event:   "compose_drift",
		Message: "Compose file changed. Redeployed without image pull.",
		Level:   "info",
		Output:  composeOut,
	}); werr != nil {
		logger.Printf("[updater] %s: audit write error: %v", svc.Name, werr)
	}

	u.dispatcher.Send(ctx, notify.Alert{
		Service: svc.Name,
		Event:   "compose_drift",
		Message: "Compose file changed. Redeployed without image pull.",
		Level:   notify.LevelInfo,
	})

	go u.verifyHealthAfterCompose(ctx, svc) // clears deploying when done
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

	logger.Printf("[updater] ERROR: %s: %v", svc.Name, err)
	u.metrics.IncFailures(svc.Name)
	u.dispatcher.Send(ctx, notify.Alert{
		Service: svc.Name,
		Event:   "error",
		Message: fmt.Sprintf("Poll error: %s", msg),
		Level:   notify.LevelCritical,
	})
	if werr := u.audit.Write(audit.Entry{
		Service: svc.Name,
		Event:   "error",
		Message: fmt.Sprintf("Poll error: %s", msg),
		Level:   "critical",
	}); werr != nil {
		logger.Printf("[updater] %s: audit write error: %v", svc.Name, werr)
	}
}

// handlePollErrorAlways logs error without suppression. Used for manual triggers.
func (u *Updater) handlePollErrorAlways(ctx context.Context, svc config.Service, err error) {
	msg := err.Error()
	logger.Printf("[updater] ERROR: %s: %v", svc.Name, err)
	u.metrics.IncFailures(svc.Name)

	// Write to audit log
	if werr := u.audit.Write(audit.Entry{
		Service: svc.Name,
		Event:   "trigger_failed",
		Message: fmt.Sprintf("Manual trigger failed: %v", err),
		Level:   "error",
	}); werr != nil {
		logger.Printf("[updater] ERROR: %s: audit write error: %v", svc.Name, werr)
	}

	u.dispatcher.Send(ctx, notify.Alert{
		Service: svc.Name,
		Event:   "error",
		Message: fmt.Sprintf("Manual trigger error: %s", msg),
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

	logger.Printf("[updater] %s: recovered from previous error", svc.Name)
	if werr := u.audit.Write(audit.Entry{
		Service: svc.Name,
		Event:   "recovered",
		Message: "Service recovered from previous poll error",
		Level:   "info",
	}); werr != nil {
		logger.Printf("[updater] %s: audit write error: %v", svc.Name, werr)
	}
}

// GetNextCheck returns the next scheduled check time for a service
func (u *Updater) GetNextCheck(serviceName string) time.Time {
	u.lastCheckedMu.RLock()
	lastCheck, exists := u.lastChecked[serviceName]
	u.lastCheckedMu.RUnlock()

	if !exists {
		return time.Now().Add(time.Duration(u.cfg.Registry.PollInterval) * time.Second)
	}
	return lastCheck.Add(time.Duration(u.cfg.Registry.PollInterval) * time.Second)
}

// GetLastCheck returns the last check time for a service
func (u *Updater) GetLastCheck(serviceName string) time.Time {
	u.lastCheckedMu.RLock()
	defer u.lastCheckedMu.RUnlock()
	return u.lastChecked[serviceName]
}

// GetCheckStatus returns the current check status for a service
func (u *Updater) GetCheckStatus(serviceName string) string {
	u.checkStatusMu.RLock()
	defer u.checkStatusMu.RUnlock()

	status := u.checkStatus[serviceName]
	if status == "" {
		return "idle"
	}
	return status
}

// setCheckStatus updates the check status for a service
func (u *Updater) setCheckStatus(serviceName string, status string) {
	u.checkStatusMu.Lock()
	u.checkStatus[serviceName] = status
	u.checkStatusMu.Unlock()
}

// updateLastChecked updates the last check time for a service
func (u *Updater) updateLastChecked(serviceName string) {
	u.lastCheckedMu.Lock()
	u.lastChecked[serviceName] = time.Now()
	u.lastCheckedMu.Unlock()
}

func (u *Updater) checkAndUpdate(ctx context.Context, svc config.Service, manual bool) error {
	// Update check tracking
	u.setCheckStatus(svc.Name, "checking")
	defer func() {
		u.setCheckStatus(svc.Name, "idle")
		u.updateLastChecked(svc.Name)
	}()

	var changed []imageChange
	representativeDigest := "" // first matched image's digest, used for startAttempted guard

	// Per-image loop: check each image for digest changes.
	for _, img := range svc.Images {
		key := svc.Name + "/" + img
		registryPrefix := registryHost(u.cfg.Registry.URL) + "/" + imageName(img)

		// Step 1: Get remote digest from registry.
		remoteDigest, err := u.registry.RemoteDigest(ctx, img)
		if err != nil {
			return fmt.Errorf("remote digest %s: %w", img, err)
		}

		// Check if this digest is blocked (caused a previous rollback).
		u.blockedMu.RLock()
		blockedDigest := u.blocked[key]
		u.blockedMu.RUnlock()
		if blockedDigest != "" {
			if blockedDigest == remoteDigest {
				continue // Still the same bad digest, skip silently.
			}
			// Remote digest changed (fix pushed), clear the block.
			logger.Printf("[updater] %s/%s: blocked digest changed, unblocking", svc.Name, img)
			u.blockedMu.Lock()
			delete(u.blocked, key)
			u.blockedMu.Unlock()
			u.metrics.SetBlocked(svc.Name, false)
		}

		// Check if this image is in the notFound suppression map.
		u.notFoundMu.RLock()
		notFoundDigest := u.notFound[key]
		u.notFoundMu.RUnlock()
		if notFoundDigest != "" {
			if notFoundDigest == remoteDigest {
				continue // Same unresolvable digest, skip silently.
			}
			logger.Printf("[updater] %s/%s: registry digest changed since not-found suppression, retrying", svc.Name, img)
			u.notFoundMu.Lock()
			delete(u.notFound, key)
			u.notFoundMu.Unlock()
		}

		// Step 2: Get local digest from Docker.
		localDigest, localSize := u.resolveLocalDigestForImage(ctx, svc, registryPrefix, img)
		if localDigest == "" {
			logger.Printf("[updater] %s/%s: no local digest resolved, suppressing until registry digest changes", svc.Name, img)
			u.notFoundMu.Lock()
			u.notFound[key] = remoteDigest
			u.notFoundMu.Unlock()
			u.dispatcher.Send(ctx, notify.Alert{
				Service: svc.Name,
				Event:   "not_found",
				Message: fmt.Sprintf("Image %s not found locally. Verify compose file image field matches registry. Suppressing until registry digest changes.", img),
				Level:   notify.LevelWarning,
			})
			if err := u.audit.Write(audit.Entry{
				Service: svc.Name,
				Event:   "not_found",
				Message: fmt.Sprintf("Image %s not found locally. Suppressing until registry digest changes.", img),
				Level:   "warning",
			}); err != nil {
				logger.Printf("[updater] %s: audit write error: %v", svc.Name, err)
			}
			continue
		}

		// Step 3: Compare.
		if localDigest == remoteDigest {
			u.setDeployedInfo(key, registryPrefix+":"+imageTag(img), localDigest, localSize)
			if representativeDigest == "" {
				representativeDigest = remoteDigest
			}
			continue
		}

		logger.Printf("[updater] %s/%s: digest changed %s -> %s", svc.Name, img, shortDigest(localDigest), shortDigest(remoteDigest))
		changed = append(changed, imageChange{
			Image:     img,
			OldDigest: localDigest,
			NewDigest: remoteDigest,
		})
	}

	// No image changes: verify containers are running, handle auto_start.
	if len(changed) == 0 {
		_, status := u.findContainerByProject(ctx, svc.ComposeProject)
		if status == containerRunning {
			u.startAttemptedMu.Lock()
			delete(u.startAttempted, svc.Name)
			u.startAttemptedMu.Unlock()
			u.clearPollError(svc)
			if manual {
				if werr := u.audit.Write(audit.Entry{
					Service: svc.Name,
					Event:   "checked",
					Message: "All images up to date",
					Level:   "info",
				}); werr != nil {
					logger.Printf("[updater] %s: audit write error: %v", svc.Name, werr)
				}
			}
			return nil
		}

		if !svc.AutoStart {
			u.clearPollError(svc)
			if manual {
				if werr := u.audit.Write(audit.Entry{
					Service: svc.Name,
					Event:   "checked",
					Message: "All images up to date (containers not running, auto_start disabled)",
					Level:   "info",
				}); werr != nil {
					logger.Printf("[updater] %s: audit write error: %v", svc.Name, werr)
				}
			}
			return nil
		}

		// Guard against repeated start attempts at the same image version.
		u.startAttemptedMu.RLock()
		attemptedDigest := u.startAttempted[svc.Name]
		u.startAttemptedMu.RUnlock()
		if attemptedDigest != "" && attemptedDigest == representativeDigest {
			return nil
		}
		u.startAttemptedMu.Lock()
		u.startAttempted[svc.Name] = representativeDigest
		u.startAttemptedMu.Unlock()

		u.tryStartDeploy(svc.Name)
		switch status {
		case containerStuck:
			logger.Printf("[updater] %s: containers stuck (created/restarting), forcing down+up", svc.Name)
			composeOut, err := compose.Restart(ctx, u.cfg.Runtime, svc.ComposeFiles, svc.ComposeProject, svc.EnvFile)
			if err != nil {
				u.clearDeploying(svc.Name)
				return fmt.Errorf("compose restart (stuck containers): %w", err)
			}
			u.dispatcher.Send(ctx, notify.Alert{
				Service: svc.Name,
				Event:   "started",
				Message: "Containers were stuck. Forced restart (down+up).",
				Level:   notify.LevelWarning,
			})
			if werr := u.audit.Write(audit.Entry{
				Service: svc.Name,
				Event:   "started",
				Message: "Containers were stuck. Forced restart (down+up).",
				Level:   "warning",
				Output:  composeOut,
			}); werr != nil {
				logger.Printf("[updater] %s: audit write error: %v", svc.Name, werr)
			}
		default:
			logger.Printf("[updater] %s: images up to date but no containers, starting compose project", svc.Name)
			composeOut, err := compose.Up(ctx, u.cfg.Runtime, svc.ComposeFiles, svc.ComposeProject, svc.EnvFile)
			if err != nil {
				u.clearDeploying(svc.Name)
				return fmt.Errorf("compose up (no running container): %w", err)
			}
			u.dispatcher.Send(ctx, notify.Alert{
				Service: svc.Name,
				Event:   "started",
				Message: "Images up to date but no containers found. Started compose project.",
				Level:   notify.LevelWarning,
			})
			if werr := u.audit.Write(audit.Entry{
				Service: svc.Name,
				Event:   "started",
				Message: "Images up to date but no containers found. Started compose project.",
				Level:   "warning",
				Output:  composeOut,
			}); werr != nil {
				logger.Printf("[updater] %s: audit write error: %v", svc.Name, werr)
			}
		}
		u.clearPollError(svc)
		go u.verifyHealthAfterCompose(ctx, svc) // clears deploying when done
		return nil
	}

	u.clearPollError(svc)
	return u.deploy(ctx, svc, changed)
}

// resolveLocalDigestForImage tries two strategies to find the local image digest:
//  1. Resolve via running container's actual image ID (what is actually deployed).
//  2. Fallback: inspect image by constructed reference (registryHost/name:tag)
//     when no running container exists.
//
// Strategy 1 is preferred because the image tag reference can be updated (e.g.
// by a docker pull) before the container is recreated, which would make the
// local digest match the remote and skip the deploy even though the container
// is still running the old image.
func (u *Updater) resolveLocalDigestForImage(ctx context.Context, svc config.Service, registryPrefix, img string) (string, int64) {
	fullImage := registryPrefix + ":" + imageTag(img)

	// Strategy 1: resolve via running container's image ID (authoritative).
	container, status := u.findContainerByProject(ctx, svc.ComposeProject)
	if status == containerRunning {
		info, err := u.docker.InspectContainer(ctx, container.ID)
		if err == nil {
			// ContainerInspect.Image is the image ID (sha256:...).
			imgByID, err := u.docker.InspectImage(ctx, info.Image)
			if err == nil {
				if d := imgByID.LocalDigest(registryPrefix); d != "" {
					return d, imgByID.Size
				}
				logger.Printf("[updater] %s/%s: container image has no matching RepoDigests for %s", svc.Name, img, registryPrefix)
			} else {
				logger.Printf("[updater] %s/%s: inspect image by ID %s failed: %v", svc.Name, img, info.Image, err)
			}
		} else {
			logger.Printf("[updater] %s/%s: container inspect failed: %v", svc.Name, img, err)
		}
	}

	// Strategy 2: direct image inspect by reference (fallback when no container is running).
	localImg, err := u.docker.InspectImage(ctx, fullImage)
	if err == nil {
		if d := localImg.LocalDigest(registryPrefix); d != "" {
			return d, localImg.Size
		}
		logger.Printf("[updater] %s/%s: image found by reference but no matching digest in RepoDigests", svc.Name, img)
	} else {
		logger.Printf("[updater] %s/%s: inspect image %s failed: %v", svc.Name, img, fullImage, err)
	}

	logger.Printf("[updater] %s/%s: no local digest resolved", svc.Name, img)
	return "", 0
}

func (u *Updater) deploy(ctx context.Context, svc config.Service, changed []imageChange) error {
	// Check if we're shutting down before starting a new deployment
	select {
	case <-ctx.Done():
		logger.Printf("[updater] %s: context cancelled, skipping deploy", svc.Name)
		return ctx.Err()
	default:
	}

	// Atomic deploy guard: prevent concurrent deploys for the same service.
	if !u.tryStartDeploy(svc.Name) {
		logger.Printf("[updater] %s: deploy already in progress, skipping", svc.Name)
		return nil
	}

	// Step 1: For each changed image, tag the currently running container's image as :rollback.
	// We tag by image ID so it works regardless of how compose references the image name.
	// We also capture the compose image reference (OldRef) for rollback retag.
	allContainers, _ := u.docker.ListContainersByProject(ctx, svc.ComposeProject)
	for i, ch := range changed {
		registryPrefix := registryHost(u.cfg.Registry.URL) + "/" + imageName(ch.Image)
		imgName := imageName(ch.Image)
		for _, c := range allContainers {
			if c.State != "running" {
				continue
			}
			// Match container to image by name (handles both short and registry-prefixed forms).
			cName := imageName(c.Image)
			if cName == imgName || strings.HasSuffix(cName, "/"+imgName) {
				if info, err := u.docker.InspectContainer(ctx, c.ID); err == nil {
					changed[i].OldRef = info.Config.Image
					if err := u.docker.TagImage(ctx, info.Image, registryPrefix, "rollback"); err != nil {
						logger.Printf("[updater] %s/%s: failed to tag rollback: %v", svc.Name, ch.Image, err)
					}
				}
				break
			}
		}
	}

	// Step 2: Pull new images and recreate via compose.
	logger.Printf("[updater] %s: pulling and deploying", svc.Name)
	pullOut, err := compose.Pull(ctx, u.cfg.Runtime, svc.ComposeFiles, svc.ComposeProject, svc.EnvFile)
	if err != nil {
		u.clearDeploying(svc.Name)
		return fmt.Errorf("compose pull: %w", err)
	}
	upOut, err := compose.Up(ctx, u.cfg.Runtime, svc.ComposeFiles, svc.ComposeProject, svc.EnvFile)
	if err != nil {
		u.clearDeploying(svc.Name)
		return fmt.Errorf("compose up: %w", err)
	}
	composeOut := strings.TrimSpace(pullOut + "\n" + upOut)

	// Step 3: Verify health asynchronously. clearDeploying is called via defer in verifyAfterDeploy.
	go u.verifyAfterDeploy(ctx, svc, changed, composeOut)

	return nil
}

func (u *Updater) verifyAfterDeploy(ctx context.Context, svc config.Service, changed []imageChange, composeOut string) {
	defer u.clearDeploying(svc.Name)

	grace := time.Duration(svc.HealthGrace) * time.Second
	deadline := time.Now().Add(grace)
	logger.Printf("[updater] %s: health polling for %s", svc.Name, grace)

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
				logger.Printf("[updater] %s: container not found after grace period, rolling back", svc.Name)
				u.rollback(ctx, svc, changed, "container not found after deploy", composeOut)
				return
			}
			continue // Container may be starting up.
		}

		info, err := u.docker.InspectContainer(ctx, container.ID)
		if err != nil {
			logger.Printf("[updater] %s: inspect failed during health poll: %v", svc.Name, err)
			if time.Now().After(deadline) {
				u.rollback(ctx, svc, changed, "inspect failed: "+err.Error(), composeOut)
				return
			}
			continue
		}

		// No healthcheck configured: success if running.
		if info.State.Health == nil {
			if info.State.Running {
				u.onDeploySuccess(ctx, svc, changed, info.ContainerName(), info.Config.Image, composeOut)
				return
			}
			if time.Now().After(deadline) {
				u.rollback(ctx, svc, changed, "container not running", composeOut)
				return
			}
			continue
		}

		switch info.State.Health.Status {
		case "healthy":
			u.onDeploySuccess(ctx, svc, changed, info.ContainerName(), info.Config.Image, composeOut)
			return
		case "unhealthy":
			reason := info.LastHealthOutput()
			logger.Printf("[updater] %s: unhealthy, rolling back immediately", svc.Name)
			u.rollback(ctx, svc, changed, reason, composeOut)
			return
		default: // "starting" or other transient states
			if time.Now().After(deadline) {
				reason := info.LastHealthOutput()
				logger.Printf("[updater] %s: still %s after grace period, rolling back", svc.Name, info.State.Health.Status)
				u.rollback(ctx, svc, changed, reason, composeOut)
				return
			}
			// Keep polling.
		}
	}
}

// verifyHealthAfterCompose polls container state after a non-image-update
// compose operation (drift, auto-start, stuck restart) and sets the health
// gauge based on actual container health.  No rollback — there are no old
// images to restore.
func (u *Updater) verifyHealthAfterCompose(ctx context.Context, svc config.Service) {
	defer u.clearDeploying(svc.Name)

	grace := time.Duration(svc.HealthGrace) * time.Second
	deadline := time.Now().Add(grace)
	logger.Printf("[updater] %s: verifying health for %s", svc.Name, grace)

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
				logger.Printf("[updater] %s: no container found after grace period", svc.Name)
				u.metrics.SetHealthy(svc.Name, false)
				return
			}
			continue
		}

		info, err := u.docker.InspectContainer(ctx, container.ID)
		if err != nil {
			if time.Now().After(deadline) {
				logger.Printf("[updater] %s: inspect failed after grace: %v", svc.Name, err)
				u.metrics.SetHealthy(svc.Name, false)
				return
			}
			continue
		}

		// No healthcheck: running = healthy.
		if info.State.Health == nil {
			if info.State.Running {
				logger.Printf("[updater] %s: running (no healthcheck), marking healthy", svc.Name)
				u.metrics.SetHealthy(svc.Name, true)
				return
			}
			if time.Now().After(deadline) {
				logger.Printf("[updater] %s: not running after grace period", svc.Name)
				u.metrics.SetHealthy(svc.Name, false)
				return
			}
			continue
		}

		// Has healthcheck: respect Docker's health status.
		switch info.State.Health.Status {
		case "healthy":
			logger.Printf("[updater] %s: healthy after compose operation", svc.Name)
			u.metrics.SetHealthy(svc.Name, true)
			return
		case "unhealthy":
			logger.Printf("[updater] %s: unhealthy after compose operation", svc.Name)
			u.metrics.SetHealthy(svc.Name, false)
			return
		default: // "starting" etc.
			if time.Now().After(deadline) {
				logger.Printf("[updater] %s: still %s after grace period", svc.Name, info.State.Health.Status)
				u.metrics.SetHealthy(svc.Name, false)
				return
			}
		}
	}
}

func (u *Updater) onDeploySuccess(ctx context.Context, svc config.Service, changed []imageChange, containerName, imageRef, composeOut string) {
	logger.Printf("[updater] %s: deployed successfully", svc.Name)
	u.metrics.IncUpdates(svc.Name)
	u.metrics.SetHealthy(svc.Name, true)
	for _, ch := range changed {
		u.setDeployedInfo(svc.Name+"/"+ch.Image, imageRef, ch.NewDigest, 0) // size resolved next poll
	}
	u.dispatcher.Send(ctx, notify.Alert{
		Service:   svc.Name,
		Event:     "updated",
		Message:   "Deployed new image successfully.",
		OldDigest: changed[0].OldDigest,
		NewDigest: changed[0].NewDigest,
		Container: containerName,
		Level:     notify.LevelInfo,
	})
	if err := u.audit.Write(audit.Entry{
		Service:   svc.Name,
		Event:     "updated",
		Message:   "Deployed new image successfully.",
		Level:     "info",
		OldDigest: changed[0].OldDigest,
		NewDigest: changed[0].NewDigest,
		Container: containerName,
		Output:    composeOut,
	}); err != nil {
		logger.Printf("[updater] %s: audit write error: %v", svc.Name, err)
	}
	u.cleanupRollbacks(ctx, changed)
}

func (u *Updater) rollback(ctx context.Context, svc config.Service, changed []imageChange, reason string, composeOut string) {
	logger.Printf("[updater] %s: rolling back. Reason: %s", svc.Name, reason)
	u.metrics.IncRollbacks(svc.Name)
	u.metrics.SetHealthy(svc.Name, false)

	// Block all new digests to prevent infinite rollback loops.
	u.blockedMu.Lock()
	for _, ch := range changed {
		key := svc.Name + "/" + ch.Image
		u.blocked[key] = ch.NewDigest
		logger.Printf("[updater] %s/%s: blocked digest %s", svc.Name, ch.Image, shortDigest(ch.NewDigest))
	}
	u.blockedMu.Unlock()
	u.metrics.SetBlocked(svc.Name, true)

	// Retag each :rollback back to its versioned tag and (if needed) to the compose ref.
	// If compose uses a different image reference than the registry-prefixed form
	// (e.g. "localhost:5000/firegen:latest" vs "firegen:latest"), also retag to
	// that reference so compose up picks up the old image correctly.
	tagFailed := false
	for _, ch := range changed {
		registryPrefix := registryHost(u.cfg.Registry.URL) + "/" + imageName(ch.Image)
		tag := imageTag(ch.Image)
		rollbackImage := registryPrefix + ":rollback"
		composeRef := registryPrefix + ":" + tag

		if ch.OldRef != "" && ch.OldRef != composeRef {
			if err := u.docker.TagImage(ctx, rollbackImage, imageName(ch.OldRef), imageTag(ch.OldRef)); err != nil {
				logger.Printf("[updater] %s/%s: rollback retag to compose ref %s failed: %v", svc.Name, ch.Image, ch.OldRef, err)
			}
		}

		if err := u.docker.TagImage(ctx, rollbackImage, registryPrefix, tag); err != nil {
			logger.Printf("[updater] %s/%s: rollback tag failed: %v", svc.Name, ch.Image, err)
			tagFailed = true
		}
	}

	if tagFailed {
		u.metrics.IncFailures(svc.Name)
		u.dispatcher.Send(ctx, notify.Alert{
			Service:   svc.Name,
			Event:     "rolled_back",
			Message:   "Rollback failed: could not retag image.",
			Reason:    reason,
			OldDigest: changed[0].OldDigest,
			NewDigest: changed[0].NewDigest,
			Level:     notify.LevelCritical,
		})
		if werr := u.audit.Write(audit.Entry{
			Service:   svc.Name,
			Event:     "rolled_back",
			Message:   "Rollback failed: could not retag image.",
			Level:     "critical",
			OldDigest: changed[0].OldDigest,
			NewDigest: changed[0].NewDigest,
			Reason:    reason,
			Output:    composeOut,
		}); werr != nil {
			logger.Printf("[updater] %s: audit write error: %v", svc.Name, werr)
		}
		return
	}

	rollbackOut, err := compose.Up(ctx, u.cfg.Runtime, svc.ComposeFiles, svc.ComposeProject, svc.EnvFile)
	allOut := strings.TrimSpace(composeOut + "\n" + rollbackOut)
	if err != nil {
		logger.Printf("[updater] %s: rollback compose up failed: %v", svc.Name, err)
		u.metrics.IncFailures(svc.Name)
		u.dispatcher.Send(ctx, notify.Alert{
			Service:   svc.Name,
			Event:     "rolled_back",
			Message:   "Rollback compose up failed.",
			Reason:    reason,
			OldDigest: changed[0].OldDigest,
			NewDigest: changed[0].NewDigest,
			Level:     notify.LevelCritical,
		})
		if werr := u.audit.Write(audit.Entry{
			Service:   svc.Name,
			Event:     "rolled_back",
			Message:   "Rollback compose up failed.",
			Level:     "critical",
			OldDigest: changed[0].OldDigest,
			NewDigest: changed[0].NewDigest,
			Reason:    reason,
			Output:    allOut,
		}); werr != nil {
			logger.Printf("[updater] %s: audit write error: %v", svc.Name, werr)
		}
		return
	}

	u.dispatcher.Send(ctx, notify.Alert{
		Service:   svc.Name,
		Event:     "rolled_back",
		Message:   "Rolled back to previous image.",
		Reason:    reason,
		OldDigest: changed[0].OldDigest,
		NewDigest: changed[0].NewDigest,
		Level:     notify.LevelWarning,
	})
	if err := u.audit.Write(audit.Entry{
		Service:   svc.Name,
		Event:     "rolled_back",
		Message:   "Rolled back to previous image.",
		Level:     "warning",
		OldDigest: changed[0].OldDigest,
		NewDigest: changed[0].NewDigest,
		Reason:    reason,
		Output:    allOut,
	}); err != nil {
		logger.Printf("[updater] %s: audit write error: %v", svc.Name, err)
	}

	u.cleanupRollbacks(ctx, changed)
}

func (u *Updater) cleanupRollbacks(ctx context.Context, changed []imageChange) {
	for _, ch := range changed {
		registryPrefix := registryHost(u.cfg.Registry.URL) + "/" + imageName(ch.Image)
		if err := u.docker.RemoveImage(ctx, registryPrefix+":rollback"); err != nil {
			logger.Printf("[updater] failed to remove rollback image %s:rollback: %v", registryPrefix, err)
		}
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

// UnblockService clears all blocked digests for a service (prefix scan over "service/image" keys).
// Returns true if at least one image was unblocked.
func (u *Updater) UnblockService(service string) bool {
	prefix := service + "/"
	u.blockedMu.Lock()
	found := false
	for k := range u.blocked {
		if strings.HasPrefix(k, prefix) {
			delete(u.blocked, k)
			found = true
		}
	}
	u.blockedMu.Unlock()
	if found {
		u.metrics.SetBlocked(service, false)
		logger.Printf("[updater] %s: manually unblocked", service)
	}
	return found
}

// ContainersByProject returns all containers for a compose project.
func (u *Updater) ContainersByProject(ctx context.Context, project string) ([]docker.Container, error) {
	return u.docker.ListContainersByProject(ctx, project)
}

// serviceContainerInfos fetches containers for a project and converts them to ContainerInfo.
// ContainerInfo is defined in api.go (same package).
func (u *Updater) serviceContainerInfos(ctx context.Context, project string) []ContainerInfo {
	list, err := u.docker.ListContainersByProject(ctx, project)
	if err != nil {
		return nil
	}
	result := make([]ContainerInfo, 0, len(list))
	for _, c := range list {
		name := ""
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		var mounts []MountInfo
		for _, m := range c.Mounts {
			mounts = append(mounts, MountInfo{
				Type:        m.Type,
				Name:        m.Name,
				Source:      m.Source,
				Destination: m.Destination,
				ReadOnly:    !m.RW,
			})
		}
		result = append(result, ContainerInfo{
			ID:     c.ID,
			Name:   name,
			State:  c.State,
			Status: c.Status,
			Image:  c.Image,
			Mounts: mounts,
		})
	}
	return result
}

// imageChange records a digest transition for a single image within a deploy cycle.
type imageChange struct {
	Image     string // short form from config (e.g. "api:latest")
	OldDigest string
	NewDigest string
	OldRef    string // compose image reference captured from container inspect (for rollback retag)
}

// DeployedInfo holds the deployed image reference and digest for a service image.
type DeployedInfo struct {
	Image  string // full image reference from container (e.g. localhost:5000/myapp:latest)
	Digest string // full digest (displayed as shortDigest in the UI)
	Size   int64  // uncompressed image size in bytes (from docker image inspect)
}

// containerStatus describes the state of containers in a compose project.
type containerStatus int

const (
	containerNone    containerStatus = iota // No containers exist
	containerStuck                          // Containers exist but none running (created/restarting)
	containerRunning                        // At least one container is running or exited
)

// setDeployedInfo stores the deployed image reference, digest, and size for a service image key.
// key format: "service/image" (e.g. "myapp/api:latest")
func (u *Updater) setDeployedInfo(key, image, digest string, size int64) {
	u.deployedMu.Lock()
	u.deployed[key] = DeployedInfo{Image: image, Digest: digest, Size: size}
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
		logger.Printf("[updater] failed to list containers for project %s: %v", project, err)
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
