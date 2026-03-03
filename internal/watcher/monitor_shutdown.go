package watcher

import (
	"context"

	"github.com/studiowebux/dockward/internal/logger"
)

// Shutdown implements the GracefulManager interface for Monitor.
func (m *Monitor) Shutdown(ctx context.Context) error {
	logger.Printf("[monitor] starting graceful shutdown")

	// The monitor watches for resource alerts and doesn't have long-running operations.
	// It runs periodic checks that complete quickly.
	// Just need to ensure clean shutdown of monitoring goroutines.

	logger.Printf("[monitor] shutdown completed")
	return nil
}