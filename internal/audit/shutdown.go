package audit

import (
	"context"
	"time"

	"github.com/studiowebux/dockward/internal/logger"
)

// Shutdown implements graceful shutdown for the audit logger.
// It ensures all pending audit log entries are flushed to disk.
func (l *Logger) Shutdown(ctx context.Context) error {
	logger.Printf("[audit] starting graceful shutdown")

	// If no file is configured, nothing to flush
	if l.file == nil {
		logger.Printf("[audit] no audit file configured, shutdown complete")
		return nil
	}

	// Set a deadline for flushing
	flushTimeout := 5 * time.Second
	flushCtx, cancel := context.WithTimeout(ctx, flushTimeout)
	defer cancel()

	// Channel to signal flush completion
	done := make(chan error, 1)

	go func() {
		// Ensure file is synced to disk
		if err := l.file.Sync(); err != nil {
			done <- err
			return
		}
		done <- nil
	}()

	select {
	case err := <-done:
		if err != nil {
			logger.Printf("[audit] failed to flush logs: %v", err)
			return err
		}
		logger.Printf("[audit] audit logs flushed successfully")
		return nil

	case <-flushCtx.Done():
		logger.Printf("[audit] timeout flushing audit logs")
		return flushCtx.Err()
	}
}