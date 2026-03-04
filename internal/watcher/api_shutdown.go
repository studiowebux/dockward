package watcher

import (
	"context"
	"time"

	"github.com/studiowebux/dockward/internal/logger"
)

// Shutdown implements the GracefulManager interface for API.
// Gracefully shuts down all HTTP servers.
func (a *API) Shutdown(ctx context.Context) error {
	logger.Printf("[api] starting graceful shutdown")

	for i, srv := range a.servers {
		shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		logger.Printf("[api] shutting down HTTP server %d/%d (%s)", i+1, len(a.servers), srv.Addr)
		if err := srv.Shutdown(shutdownCtx); err != nil {
			logger.Printf("[api] HTTP server shutdown error (%s): %v", srv.Addr, err)
			cancel()
			return err
		}
		cancel()
	}

	logger.Printf("[api] shutdown completed")
	return nil
}