package watcher

import (
	"context"
	"time"

	"github.com/studiowebux/dockward/internal/logger"
	"github.com/studiowebux/dockward/internal/manager"
)

// DeploymentManager handles deployment tracking for graceful shutdown.
type DeploymentManager struct {
	tracker *manager.DeploymentTracker
}

// NewDeploymentManager creates a new deployment manager.
func NewDeploymentManager() *DeploymentManager {
	return &DeploymentManager{
		tracker: manager.NewDeploymentTracker(),
	}
}

// Shutdown implements the GracefulManager interface for Updater.
func (u *Updater) Shutdown(ctx context.Context) error {
	logger.Printf("[updater] starting graceful shutdown")

	// Get list of active deployments
	u.deployingMu.RLock()
	activeCount := len(u.deploying)
	services := make([]string, 0, activeCount)
	for service := range u.deploying {
		services = append(services, service)
	}
	u.deployingMu.RUnlock()

	if activeCount > 0 {
		logger.Printf("[updater] waiting for %d active deployment(s): %v", activeCount, services)

		// Wait for deployments to complete
		ticker := time.NewTicker(500 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				logger.Printf("[updater] shutdown timeout, cancelling remaining deployments")
				return ctx.Err()

			case <-ticker.C:
				u.deployingMu.RLock()
				remaining := len(u.deploying)
				u.deployingMu.RUnlock()

				if remaining == 0 {
					logger.Printf("[updater] all deployments completed")
					break
				}

				// Check if deployments are taking too long
				u.deployingMu.RLock()
				var stuck []string
				now := time.Now()
				for service, startTime := range u.deploying {
					if now.Sub(startTime) > 5*time.Minute {
						stuck = append(stuck, service)
					}
				}
				u.deployingMu.RUnlock()

				if len(stuck) > 0 {
					logger.Printf("[updater] warning: deployments running >5min: %v", stuck)
				}
			}

			// Check if all deployments are done
			u.deployingMu.RLock()
			if len(u.deploying) == 0 {
				u.deployingMu.RUnlock()
				break
			}
			u.deployingMu.RUnlock()
		}
	}

	logger.Printf("[updater] shutdown completed")
	return nil
}

// StartDeploymentWithContext starts a deployment with proper context tracking.
// Returns a context that should be used for the deployment operations.
// NOTE: This function is currently unused but kept for future enhancement.
// The existing deploy() function already handles context correctly via tryStartDeploy/clearDeploying.
func (u *Updater) StartDeploymentWithContext(ctx context.Context, service string) (context.Context, bool) {
	// Check if we're shutting down
	select {
	case <-ctx.Done():
		return nil, false
	default:
	}

	// Use existing tryStartDeploy for atomic check
	if !u.tryStartDeploy(service) {
		return nil, false
	}

	// Return the parent context directly since deployment tracking is already
	// handled via the deploying map in tryStartDeploy/clearDeploying
	return ctx, true
}