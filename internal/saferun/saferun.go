// Package saferun provides safe goroutine execution with panic recovery.
package saferun

import (
	"context"
	"fmt"
	"runtime/debug"

	"github.com/studiowebux/dockward/internal/logger"
)

// Go executes a function in a goroutine with panic recovery.
func Go(name string, fn func()) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Critical("[%s] panic recovered: %v\n%s", name, r, debug.Stack())
			}
		}()
		fn()
	}()
}

// GoContext executes a context-aware function in a goroutine with panic recovery.
func GoContext(name string, ctx context.Context, fn func(context.Context)) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Critical("[%s] panic recovered: %v\n%s", name, r, debug.Stack())
			}
		}()
		fn(ctx)
	}()
}

// RunWithRecover wraps a function with panic recovery for inline use.
func RunWithRecover(name string, fn func()) {
	defer func() {
		if r := recover(); r != nil {
			logger.Critical("[%s] panic recovered: %v\n%s", name, r, debug.Stack())
		}
	}()
	fn()
}

// RunnerFunc is a function that takes a context and blocks until context is done.
type RunnerFunc func(context.Context)

// RunWithRecovery executes a runner function with panic recovery.
func RunWithRecovery(name string, ctx context.Context, runner RunnerFunc) {
	go func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Critical("[%s] panic recovered: %v\n%s", name, r, debug.Stack())
			}
		}()
		logger.Debug("[%s] starting", name)
		runner(ctx)
		logger.Debug("[%s] stopped", name)
	}()
}

// Wrap returns a function that will execute fn with panic recovery.
func Wrap(name string, fn func()) func() {
	return func() {
		defer func() {
			if r := recover(); r != nil {
				logger.Critical("[%s] panic recovered: %v\n%s", name, r, debug.Stack())
			}
		}()
		fn()
	}
}

// WrapError returns a function that executes fn with panic recovery and returns an error.
func WrapError(name string, fn func() error) func() error {
	return func() error {
		defer func() {
			if r := recover(); r != nil {
				logger.Critical("[%s] panic recovered: %v\n%s", name, r, debug.Stack())
			}
		}()
		return fn()
	}
}

// MustRecover is a defer helper for manual panic recovery.
// Usage: defer saferun.MustRecover("component-name")
func MustRecover(name string) {
	if r := recover(); r != nil {
		logger.Critical("[%s] panic recovered: %v\n%s", name, r, fmt.Sprintf("%s", debug.Stack()))
	}
}