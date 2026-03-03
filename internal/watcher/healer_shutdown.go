package watcher

import (
	"context"
	"time"

	"github.com/studiowebux/dockward/internal/logger"
)

// Shutdown implements the GracefulManager interface for Healer.
func (h *Healer) Shutdown(ctx context.Context) error {
	logger.Printf("[healer] starting graceful shutdown")

	// The healer doesn't have long-running operations like the updater does.
	// It performs quick health checks and triggers compose operations.
	// We just need to ensure we don't start new healing operations during shutdown.

	// Wait a short period to allow any in-progress health checks to complete
	gracePeriod := 5 * time.Second
	logger.Printf("[healer] waiting %s for health checks to complete", gracePeriod)

	select {
	case <-time.After(gracePeriod):
		logger.Printf("[healer] shutdown completed")
	case <-ctx.Done():
		logger.Printf("[healer] shutdown timeout")
		return ctx.Err()
	}

	return nil
}