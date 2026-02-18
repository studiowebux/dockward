package watcher

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// Metrics tracks counters and gauges for Prometheus-compatible /metrics endpoint.
type Metrics struct {
	mu sync.RWMutex

	// Counters (per service)
	updates   map[string]int64
	rollbacks map[string]int64
	restarts  map[string]int64
	failures  map[string]int64

	// Gauges
	serviceHealthy map[string]bool
	serviceBlocked map[string]bool
	lastPollTime   time.Time
	pollCount      int64
	startTime      time.Time
}

// NewMetrics creates an initialized metrics collector.
func NewMetrics() *Metrics {
	return &Metrics{
		updates:        make(map[string]int64),
		rollbacks:      make(map[string]int64),
		restarts:       make(map[string]int64),
		failures:       make(map[string]int64),
		serviceHealthy: make(map[string]bool),
		serviceBlocked: make(map[string]bool),
		startTime:      time.Now(),
	}
}

func (m *Metrics) IncUpdates(service string) {
	m.mu.Lock()
	m.updates[service]++
	m.mu.Unlock()
}

func (m *Metrics) IncRollbacks(service string) {
	m.mu.Lock()
	m.rollbacks[service]++
	m.mu.Unlock()
}

func (m *Metrics) IncRestarts(service string) {
	m.mu.Lock()
	m.restarts[service]++
	m.mu.Unlock()
}

func (m *Metrics) IncFailures(service string) {
	m.mu.Lock()
	m.failures[service]++
	m.mu.Unlock()
}

func (m *Metrics) SetHealthy(service string, healthy bool) {
	m.mu.Lock()
	m.serviceHealthy[service] = healthy
	m.mu.Unlock()
}

func (m *Metrics) SetBlocked(service string, blocked bool) {
	m.mu.Lock()
	m.serviceBlocked[service] = blocked
	m.mu.Unlock()
}

func (m *Metrics) RecordPoll() {
	m.mu.Lock()
	m.pollCount++
	m.lastPollTime = time.Now()
	m.mu.Unlock()
}

// Prometheus returns metrics in Prometheus text exposition format.
func (m *Metrics) Prometheus() string {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var b strings.Builder

	b.WriteString("# HELP watcher_updates_total Total successful image updates\n")
	b.WriteString("# TYPE watcher_updates_total counter\n")
	for svc, count := range m.updates {
		fmt.Fprintf(&b, "watcher_updates_total{service=%q} %d\n", svc, count)
	}

	b.WriteString("# HELP watcher_rollbacks_total Total rollbacks after failed updates\n")
	b.WriteString("# TYPE watcher_rollbacks_total counter\n")
	for svc, count := range m.rollbacks {
		fmt.Fprintf(&b, "watcher_rollbacks_total{service=%q} %d\n", svc, count)
	}

	b.WriteString("# HELP watcher_restarts_total Total auto-heal restarts\n")
	b.WriteString("# TYPE watcher_restarts_total counter\n")
	for svc, count := range m.restarts {
		fmt.Fprintf(&b, "watcher_restarts_total{service=%q} %d\n", svc, count)
	}

	b.WriteString("# HELP watcher_failures_total Total failures (critical events)\n")
	b.WriteString("# TYPE watcher_failures_total counter\n")
	for svc, count := range m.failures {
		fmt.Fprintf(&b, "watcher_failures_total{service=%q} %d\n", svc, count)
	}

	b.WriteString("# HELP watcher_service_healthy Whether each service is healthy (1) or not (0)\n")
	b.WriteString("# TYPE watcher_service_healthy gauge\n")
	for svc, healthy := range m.serviceHealthy {
		v := 0
		if healthy {
			v = 1
		}
		fmt.Fprintf(&b, "watcher_service_healthy{service=%q} %d\n", svc, v)
	}

	b.WriteString("# HELP watcher_service_blocked Whether a service has a blocked digest (1) or not (0)\n")
	b.WriteString("# TYPE watcher_service_blocked gauge\n")
	for svc, blocked := range m.serviceBlocked {
		v := 0
		if blocked {
			v = 1
		}
		fmt.Fprintf(&b, "watcher_service_blocked{service=%q} %d\n", svc, v)
	}

	b.WriteString("# HELP watcher_poll_count_total Total registry poll cycles\n")
	b.WriteString("# TYPE watcher_poll_count_total counter\n")
	fmt.Fprintf(&b, "watcher_poll_count_total %d\n", m.pollCount)

	b.WriteString("# HELP watcher_last_poll_timestamp_seconds Unix timestamp of last poll\n")
	b.WriteString("# TYPE watcher_last_poll_timestamp_seconds gauge\n")
	if !m.lastPollTime.IsZero() {
		fmt.Fprintf(&b, "watcher_last_poll_timestamp_seconds %d\n", m.lastPollTime.Unix())
	}

	b.WriteString("# HELP watcher_uptime_seconds Seconds since watcher started\n")
	b.WriteString("# TYPE watcher_uptime_seconds gauge\n")
	fmt.Fprintf(&b, "watcher_uptime_seconds %.0f\n", time.Since(m.startTime).Seconds())

	return b.String()
}
