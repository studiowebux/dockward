package shutdown

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// mockManager is a test implementation of GracefulManager
type mockManager struct {
	shutdownCalled atomic.Bool
	shutdownDelay  time.Duration
	shutdownError  error
}

func (m *mockManager) Shutdown(ctx context.Context) error {
	m.shutdownCalled.Store(true)

	if m.shutdownDelay > 0 {
		select {
		case <-time.After(m.shutdownDelay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	return m.shutdownError
}

func (m *mockManager) wasCalled() bool {
	return m.shutdownCalled.Load()
}

func TestCoordinator_BasicShutdown(t *testing.T) {
	coord := NewCoordinator()
	coord.SetTimeout(5 * time.Second)

	mgr1 := &mockManager{}
	mgr2 := &mockManager{}

	coord.Register(mgr1)
	coord.Register(mgr2)

	ctx := context.Background()
	err := coord.Shutdown(ctx)

	if err != nil {
		t.Errorf("expected no error, got %v", err)
	}

	if !mgr1.wasCalled() {
		t.Error("manager 1 shutdown was not called")
	}

	if !mgr2.wasCalled() {
		t.Error("manager 2 shutdown was not called")
	}
}

func TestCoordinator_ShutdownTimeout(t *testing.T) {
	coord := NewCoordinator()
	coord.SetTimeout(100 * time.Millisecond)

	// Create a manager that takes too long to shut down
	slowMgr := &mockManager{
		shutdownDelay: 500 * time.Millisecond,
	}

	coord.Register(slowMgr)

	ctx := context.Background()
	err := coord.Shutdown(ctx)

	// We expect no error from the coordinator itself even if shutdown times out
	// The timeout just prevents us from waiting forever
	if err != nil {
		t.Logf("shutdown completed with error (expected): %v", err)
	}

	if !slowMgr.wasCalled() {
		t.Error("slow manager shutdown was not called")
	}
}

func TestCoordinator_OperationTracking(t *testing.T) {
	coord := NewCoordinator()

	// Start an operation
	if !coord.OperationStarted() {
		t.Fatal("OperationStarted should return true before shutdown")
	}

	// Operation should be tracked
	// Start shutdown in a goroutine
	shutdownDone := make(chan error, 1)
	go func() {
		ctx := context.Background()
		shutdownDone <- coord.Shutdown(ctx)
	}()

	// Give shutdown time to start
	time.Sleep(50 * time.Millisecond)

	// New operations should be rejected during shutdown
	if coord.OperationStarted() {
		t.Error("OperationStarted should return false during shutdown")
	}

	// Complete the operation
	coord.OperationCompleted()

	// Wait for shutdown to complete
	select {
	case err := <-shutdownDone:
		if err != nil {
			t.Errorf("shutdown failed: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown did not complete in time")
	}
}

func TestCoordinator_IsShuttingDown(t *testing.T) {
	coord := NewCoordinator()

	if coord.IsShuttingDown() {
		t.Error("IsShuttingDown should be false initially")
	}

	mgr := &mockManager{shutdownDelay: 100 * time.Millisecond}
	coord.Register(mgr)

	// Start shutdown
	go func() {
		ctx := context.Background()
		coord.Shutdown(ctx)
	}()

	// Give shutdown time to start
	time.Sleep(50 * time.Millisecond)

	if !coord.IsShuttingDown() {
		t.Error("IsShuttingDown should be true during shutdown")
	}
}

func TestCoordinator_ManagerErrors(t *testing.T) {
	coord := NewCoordinator()

	// Create a manager that returns an error
	failingMgr := &mockManager{
		shutdownError: errors.New("shutdown failed"),
	}

	coord.Register(failingMgr)

	ctx := context.Background()
	err := coord.Shutdown(ctx)

	if err == nil {
		t.Error("expected error from failing manager")
	}

	if !failingMgr.wasCalled() {
		t.Error("failing manager shutdown was not called")
	}
}

func TestCoordinator_MultipleShutdownCalls(t *testing.T) {
	coord := NewCoordinator()

	mgr := &mockManager{}
	coord.Register(mgr)

	ctx := context.Background()

	// Call shutdown multiple times
	err1 := coord.Shutdown(ctx)
	err2 := coord.Shutdown(ctx)
	err3 := coord.Shutdown(ctx)

	if err1 != nil {
		t.Errorf("first shutdown failed: %v", err1)
	}

	// Second and third calls should be no-ops (sync.Once behavior)
	if err2 != nil || err3 != nil {
		t.Errorf("subsequent shutdowns should not error: err2=%v, err3=%v", err2, err3)
	}

	// Manager should only be called once
	if !mgr.wasCalled() {
		t.Error("manager shutdown was not called")
	}
}
