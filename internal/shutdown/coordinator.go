// Package shutdown provides graceful shutdown coordination for dockward.
package shutdown

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/studiowebux/dockward/internal/logger"
)

const (
	// DefaultTimeout is the default timeout for graceful shutdown.
	DefaultTimeout = 30 * time.Second
)

// GracefulManager represents a component that can shut down gracefully.
type GracefulManager interface {
	// Shutdown performs graceful shutdown of the component.
	// It should wait for in-flight operations to complete or until ctx is cancelled.
	Shutdown(ctx context.Context) error
}

// Coordinator manages graceful shutdown of multiple components.
type Coordinator struct {
	mu              sync.Mutex
	managers        []GracefulManager
	shutdownTimeout time.Duration
	activeOps       sync.WaitGroup
	shutdownOnce    sync.Once
	isShuttingDown  bool
}

// NewCoordinator creates a new shutdown coordinator with default timeout.
func NewCoordinator() *Coordinator {
	return &Coordinator{
		managers:        make([]GracefulManager, 0),
		shutdownTimeout: DefaultTimeout,
	}
}

// SetTimeout configures the shutdown timeout.
func (c *Coordinator) SetTimeout(timeout time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.shutdownTimeout = timeout
}

// Register adds a manager to be shut down gracefully.
func (c *Coordinator) Register(mgr GracefulManager) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.managers = append(c.managers, mgr)
}

// IsShuttingDown returns true if shutdown is in progress.
func (c *Coordinator) IsShuttingDown() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.isShuttingDown
}

// OperationStarted marks the beginning of an operation that should complete before shutdown.
// Returns false if shutdown has already started and the operation should not proceed.
func (c *Coordinator) OperationStarted() bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.isShuttingDown {
		return false
	}

	c.activeOps.Add(1)
	return true
}

// OperationCompleted marks the completion of an operation.
func (c *Coordinator) OperationCompleted() {
	c.activeOps.Done()
}

// Shutdown performs graceful shutdown of all registered managers.
func (c *Coordinator) Shutdown(ctx context.Context) error {
	var shutdownErr error

	c.shutdownOnce.Do(func() {
		c.mu.Lock()
		c.isShuttingDown = true
		c.mu.Unlock()

		logger.Printf("[shutdown] starting graceful shutdown (timeout: %s)", c.shutdownTimeout)

		// Create context with timeout for shutdown
		shutdownCtx, cancel := context.WithTimeout(ctx, c.shutdownTimeout)
		defer cancel()

		// Channel to signal when active operations complete
		opsDone := make(chan struct{})
		go func() {
			c.activeOps.Wait()
			close(opsDone)
		}()

		// Wait for active operations to complete or timeout
		select {
		case <-opsDone:
			logger.Printf("[shutdown] all active operations completed")
		case <-shutdownCtx.Done():
			logger.Printf("[shutdown] timeout waiting for active operations")
		}

		// Shutdown all managers concurrently
		var wg sync.WaitGroup
		errCh := make(chan error, len(c.managers))

		for _, mgr := range c.managers {
			wg.Add(1)
			go func(m GracefulManager) {
				defer wg.Done()

				// Give each manager remaining time in the shutdown context
				if err := m.Shutdown(shutdownCtx); err != nil {
					errCh <- err
				}
			}(mgr)
		}

		// Wait for all managers to complete
		wg.Wait()
		close(errCh)

		// Collect errors
		var errs []error
		for err := range errCh {
			errs = append(errs, err)
		}

		if len(errs) > 0 {
			shutdownErr = fmt.Errorf("shutdown errors: %v", errs)
		}

		logger.Printf("[shutdown] graceful shutdown completed")
	})

	return shutdownErr
}