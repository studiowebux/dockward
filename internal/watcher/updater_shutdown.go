package watcher

import (
	"context"
	"time"

	"github.com/studiowebux/dockward/internal/logger"
)

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

