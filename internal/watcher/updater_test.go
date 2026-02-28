package watcher

import (
	"sync"
	"testing"
	"time"
)

func TestTryStartDeploy_GuardsConcurrentDeploys(t *testing.T) {
	u := &Updater{
		deploying: make(map[string]time.Time),
	}

	if !u.tryStartDeploy("svc") {
		t.Fatal("first tryStartDeploy should succeed")
	}
	if u.tryStartDeploy("svc") {
		t.Fatal("second concurrent tryStartDeploy should fail")
	}

	u.clearDeploying("svc")

	if !u.tryStartDeploy("svc") {
		t.Fatal("tryStartDeploy should succeed after clearDeploying")
	}
}

func TestIsDeploying_PerServiceIsolation(t *testing.T) {
	u := &Updater{
		deploying: make(map[string]time.Time),
	}

	u.tryStartDeploy("svc-a")

	if u.IsDeploying("svc-b") {
		t.Error("svc-b should not be deploying")
	}
	if !u.IsDeploying("svc-a") {
		t.Error("svc-a should be deploying")
	}
}

func TestBlockedDigests_UnblockService(t *testing.T) {
	metrics := NewMetrics()
	u := &Updater{
		blocked:   make(map[string]string),
		blockedMu: sync.RWMutex{},
		metrics:   metrics,
	}

	u.blockedMu.Lock()
	u.blocked["svc/api:latest"] = "sha256:abc"
	u.blockedMu.Unlock()

	blocked := u.BlockedDigests()
	if blocked["svc/api:latest"] != "sha256:abc" {
		t.Errorf("want sha256:abc, got %q", blocked["svc/api:latest"])
	}

	if !u.UnblockService("svc") {
		t.Error("UnblockService should return true for a blocked service")
	}
	if len(u.BlockedDigests()) != 0 {
		t.Error("service should be unblocked")
	}

	if u.UnblockService("svc") {
		t.Error("UnblockService should return false for an already-unblocked service")
	}
}

func TestBlockedDigests_NotFoundSuppression(t *testing.T) {
	u := &Updater{
		notFound:   make(map[string]string),
		notFoundMu: sync.RWMutex{},
	}

	u.notFoundMu.Lock()
	u.notFound["svc/api:latest"] = "sha256:deadbeef"
	u.notFoundMu.Unlock()

	nf := u.NotFoundServices()
	if nf["svc/api:latest"] != "sha256:deadbeef" {
		t.Errorf("want sha256:deadbeef, got %q", nf["svc/api:latest"])
	}
}

func TestUnblockService_ClearsAllImagesForService(t *testing.T) {
	metrics := NewMetrics()
	u := &Updater{
		blocked:   make(map[string]string),
		blockedMu: sync.RWMutex{},
		metrics:   metrics,
	}

	u.blockedMu.Lock()
	u.blocked["svc/api:latest"] = "sha256:aaa"
	u.blocked["svc/worker:latest"] = "sha256:bbb"
	u.blockedMu.Unlock()

	if !u.UnblockService("svc") {
		t.Error("UnblockService should return true when images are blocked")
	}
	if len(u.BlockedDigests()) != 0 {
		t.Errorf("all images should be unblocked, got %v", u.BlockedDigests())
	}
}

func TestUnblockService_DoesNotClearOtherService(t *testing.T) {
	metrics := NewMetrics()
	u := &Updater{
		blocked:   make(map[string]string),
		blockedMu: sync.RWMutex{},
		metrics:   metrics,
	}

	u.blockedMu.Lock()
	u.blocked["svc-a/api:latest"] = "sha256:aaa"
	u.blockedMu.Unlock()

	if u.UnblockService("svc-b") {
		t.Error("UnblockService should return false when service is not blocked")
	}
	if u.BlockedDigests()["svc-a/api:latest"] != "sha256:aaa" {
		t.Error("svc-a should remain blocked")
	}
}

func TestBlockedDigests_MultiImage(t *testing.T) {
	metrics := NewMetrics()
	u := &Updater{
		blocked:   make(map[string]string),
		blockedMu: sync.RWMutex{},
		metrics:   metrics,
	}

	u.blockedMu.Lock()
	u.blocked["svc/api:latest"] = "sha256:aaa"
	u.blocked["svc/worker:latest"] = "sha256:bbb"
	u.blockedMu.Unlock()

	blocked := u.BlockedDigests()
	if len(blocked) != 2 {
		t.Errorf("want 2 blocked entries, got %d", len(blocked))
	}

	u.UnblockService("svc")
	if len(u.BlockedDigests()) != 0 {
		t.Error("all blocked entries should be cleared after UnblockService")
	}
}
