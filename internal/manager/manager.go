// Package manager provides a base implementation for graceful shutdown.
package manager

import (
	"context"
	"sync"
	"time"

	"github.com/studiowebux/dockward/internal/logger"
)

// DeploymentTracker tracks active deployments for graceful shutdown.
type DeploymentTracker struct {
	mu          sync.RWMutex
	deployments map[string]*DeploymentInfo
}

// DeploymentInfo contains information about an active deployment.
type DeploymentInfo struct {
	Service   string
	StartedAt time.Time
	Context   context.Context
	Cancel    context.CancelFunc
}

// NewDeploymentTracker creates a new deployment tracker.
func NewDeploymentTracker() *DeploymentTracker {
	return &DeploymentTracker{
		deployments: make(map[string]*DeploymentInfo),
	}
}

// StartDeployment registers a new deployment and returns a context for it.
// Returns nil if a deployment is already in progress for the service.
func (dt *DeploymentTracker) StartDeployment(service string, parentCtx context.Context) context.Context {
	dt.mu.Lock()
	defer dt.mu.Unlock()

	if _, exists := dt.deployments[service]; exists {
		return nil // Deployment already in progress
	}

	ctx, cancel := context.WithCancel(parentCtx)
	dt.deployments[service] = &DeploymentInfo{
		Service:   service,
		StartedAt: time.Now(),
		Context:   ctx,
		Cancel:    cancel,
	}

	logger.Printf("[manager] deployment started: %s", service)
	return ctx
}

// EndDeployment removes a deployment from tracking.
func (dt *DeploymentTracker) EndDeployment(service string) {
	dt.mu.Lock()
	defer dt.mu.Unlock()

	if info, exists := dt.deployments[service]; exists {
		duration := time.Since(info.StartedAt)
		delete(dt.deployments, service)
		logger.Printf("[manager] deployment completed: %s (duration: %s)", service, duration)
	}
}

// IsDeploying returns true if a deployment is in progress for the service.
func (dt *DeploymentTracker) IsDeploying(service string) bool {
	dt.mu.RLock()
	defer dt.mu.RUnlock()
	_, exists := dt.deployments[service]
	return exists
}

// ActiveDeployments returns the list of services with active deployments.
func (dt *DeploymentTracker) ActiveDeployments() []string {
	dt.mu.RLock()
	defer dt.mu.RUnlock()

	services := make([]string, 0, len(dt.deployments))
	for service := range dt.deployments {
		services = append(services, service)
	}
	return services
}

// WaitForDeployments waits for all active deployments to complete or until context is cancelled.
func (dt *DeploymentTracker) WaitForDeployments(ctx context.Context) error {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Cancel remaining deployments
			dt.mu.Lock()
			for service, info := range dt.deployments {
				logger.Printf("[manager] cancelling deployment: %s", service)
				info.Cancel()
			}
			dt.mu.Unlock()
			return ctx.Err()

		case <-ticker.C:
			dt.mu.RLock()
			count := len(dt.deployments)
			dt.mu.RUnlock()

			if count == 0 {
				return nil
			}

			dt.mu.RLock()
			services := make([]string, 0, count)
			for service := range dt.deployments {
				services = append(services, service)
			}
			dt.mu.RUnlock()

			logger.Printf("[manager] waiting for %d deployment(s): %v", count, services)
		}
	}
}

// CancelNewDeployments prevents new deployments from starting.
// Used during shutdown to reject new deployment requests.
func (dt *DeploymentTracker) CancelNewDeployments() {
	dt.mu.Lock()
	defer dt.mu.Unlock()
	// This is handled by the shutdown coordinator's IsShuttingDown check
	logger.Printf("[manager] new deployments disabled for shutdown")
}