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
	u.blocked["svc"] = "sha256:abc"
	u.blockedMu.Unlock()

	blocked := u.BlockedDigests()
	if blocked["svc"] != "sha256:abc" {
		t.Errorf("want sha256:abc, got %q", blocked["svc"])
	}

	if !u.UnblockService("svc") {
		t.Error("UnblockService should return true for a blocked service")
	}
	if u.BlockedDigests()["svc"] != "" {
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
	u.notFound["svc"] = "sha256:deadbeef"
	u.notFoundMu.Unlock()

	nf := u.NotFoundServices()
	if nf["svc"] != "sha256:deadbeef" {
		t.Errorf("want sha256:deadbeef, got %q", nf["svc"])
	}
}
