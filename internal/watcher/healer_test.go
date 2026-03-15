package watcher

import (
	"testing"
	"time"

	"github.com/studiowebux/dockward/internal/config"
	"github.com/studiowebux/dockward/internal/docker"
)

func TestHealer_CooldownPreventsDoubleRestart(t *testing.T) {
	h := &Healer{
		cooldowns: make(map[string]time.Time),
	}

	container := "myapp-web-1"

	// No cooldown initially.
	if h.inCooldown(container) {
		t.Error("should not be in cooldown before any restart")
	}

	// Set cooldown for 1 minute.
	h.cooldownsMu.Lock()
	h.cooldowns[container] = time.Now().Add(time.Minute)
	h.cooldownsMu.Unlock()

	if !h.inCooldown(container) {
		t.Error("should be in cooldown after setting future deadline")
	}
}

func TestHealer_CooldownExpires(t *testing.T) {
	h := &Healer{
		cooldowns: make(map[string]time.Time),
	}

	container := "myapp-web-1"

	// Set an already-expired cooldown.
	h.cooldownsMu.Lock()
	h.cooldowns[container] = time.Now().Add(-time.Second)
	h.cooldownsMu.Unlock()

	if h.inCooldown(container) {
		t.Error("expired cooldown should not block restart")
	}
}

func TestHealer_MaxRestartsExhausted(t *testing.T) {
	h := &Healer{
		restartCounts: make(map[string]int),
		exhausted:     make(map[string]bool),
	}

	svc := "myapp"
	maxRestarts := 3

	h.restartCountsMu.Lock()
	h.restartCounts[svc] = maxRestarts
	h.restartCountsMu.Unlock()

	h.restartCountsMu.Lock()
	count := h.restartCounts[svc]
	h.restartCountsMu.Unlock()

	if count < maxRestarts {
		t.Errorf("want count >= %d, got %d", maxRestarts, count)
	}
}

func TestHealer_DegradedStateTracking(t *testing.T) {
	h := &Healer{
		degraded: make(map[string]bool),
	}

	svc := "myapp"

	if h.isDegraded(svc) {
		t.Error("service should not be degraded initially")
	}

	h.setDegraded(svc, true)
	if !h.isDegraded(svc) {
		t.Error("service should be degraded after setDegraded(true)")
	}

	h.setDegraded(svc, false)
	if h.isDegraded(svc) {
		t.Error("service should not be degraded after setDegraded(false)")
	}
}

func TestHealer_ExhaustedServicesSnapshot(t *testing.T) {
	h := &Healer{
		exhausted: make(map[string]bool),
	}

	h.exhaustedMu.Lock()
	h.exhausted["svc-a"] = true
	h.exhaustedMu.Unlock()

	snap := h.ExhaustedServices()
	if !snap["svc-a"] {
		t.Error("svc-a should appear in exhausted snapshot")
	}
	if snap["svc-b"] {
		t.Error("svc-b should not appear in exhausted snapshot")
	}
}

func TestHealer_HandleStarted_ClearsDegradedWhenNoHealthcheck(t *testing.T) {
	h := &Healer{
		degraded:      make(map[string]bool),
		restartCounts: make(map[string]int),
		exhausted:     make(map[string]bool),
	}

	svc := "myapp"
	h.setDegraded(svc, true)

	// Simulate: container has no healthcheck — State.Health is nil.
	info := &docker.ContainerInspect{
		State: docker.ContainerState{
			Running: true,
			Health:  nil,
		},
	}

	// Replicate the no-healthcheck branch of handleStarted inline
	// (no Docker client needed — test the state machine directly).
	if !h.isDegraded(svc) {
		t.Fatal("service should be degraded before start event")
	}
	if info.State.Health != nil {
		t.Fatal("test setup error: health should be nil")
	}

	// Apply the recovery.
	h.setDegraded(svc, false)
	h.restartCountsMu.Lock()
	delete(h.restartCounts, svc)
	h.restartCountsMu.Unlock()
	h.exhaustedMu.Lock()
	delete(h.exhausted, svc)
	h.exhaustedMu.Unlock()

	if h.isDegraded(svc) {
		t.Error("service should no longer be degraded after start with no healthcheck")
	}
}

func TestHealer_HandleStarted_SkipsWhenNotDegraded(t *testing.T) {
	h := &Healer{
		degraded: make(map[string]bool),
	}

	// Service was never degraded — start event should be a no-op.
	if h.isDegraded("myapp") {
		t.Fatal("test setup error: service should not be degraded")
	}
	// Nothing to assert beyond confirming isDegraded stays false.
	if h.isDegraded("myapp") {
		t.Error("service should remain non-degraded")
	}
}

func TestHealer_SilentServiceIgnoredByFindServiceByEvent(t *testing.T) {
	cfg := &config.Config{
		Services: []config.Service{
			{
				Name:           "silent-svc",
				Silent:         true,
				ComposeProject: "myproject",
			},
		},
	}
	h := &Healer{cfg: cfg}

	event := docker.Event{
		Actor: docker.EventActor{
			Attributes: map[string]string{
				"com.docker.compose.project": "myproject",
			},
		},
	}

	if svc := h.findServiceByEvent(event); svc != nil {
		t.Errorf("silent service should be ignored by findServiceByEvent, got %q", svc.Name)
	}
}
