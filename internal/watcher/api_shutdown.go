package watcher

import (
	"context"
	"net/http"
	"time"

	"github.com/studiowebux/dockward/internal/logger"
)

// httpServer interface to avoid circular dependency
type httpServer interface {
	Shutdown(ctx context.Context) error
}

// SetHTTPServer sets the HTTP server for graceful shutdown.
// This must be called after the server is created in Run().
func (a *API) SetHTTPServer(srv *http.Server) {
	a.httpServer = srv
}

// Shutdown implements the GracefulManager interface for API.
func (a *API) Shutdown(ctx context.Context) error {
	logger.Printf("[api] starting graceful shutdown")

	// If we have a reference to the HTTP server, shut it down gracefully
	if a.httpServer != nil {
		// Give the server some time to close active connections
		shutdownCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()

		logger.Printf("[api] shutting down HTTP server")
		if err := a.httpServer.Shutdown(shutdownCtx); err != nil {
			logger.Printf("[api] HTTP server shutdown error: %v", err)
			return err
		}
	}

	// Close SSE connections gracefully if hub exists
	// The hub will notify all connected clients that the server is shutting down
	// This is handled by context cancellation in the SSE handlers

	logger.Printf("[api] shutdown completed")
	return nil
}