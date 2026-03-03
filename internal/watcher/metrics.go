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

	// Docker daemon health
	dockerHealthy          bool
	dockerConsecutiveFails int
	dockerCheckCount       int64

	// Config validation
	invalidServicesCount int
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

// SeedServices pre-populates zero-valued counter and gauge entries for all
// configured services so every label combination appears from the first
// Prometheus scrape — not only after the first event fires.
// serviceHealthy is intentionally excluded: containers without a HEALTHCHECK
// should not emit a health gauge at all.
func (m *Metrics) SeedServices(names []string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, name := range names {
		if _, ok := m.updates[name]; !ok {
			m.updates[name] = 0
		}
		if _, ok := m.rollbacks[name]; !ok {
			m.rollbacks[name] = 0
		}
		if _, ok := m.restarts[name]; !ok {
			m.restarts[name] = 0
		}
		if _, ok := m.failures[name]; !ok {
			m.failures[name] = 0
		}
		if _, ok := m.serviceBlocked[name]; !ok {
			m.serviceBlocked[name] = false
		}
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

func (m *Metrics) SetDockerHealth(healthy bool, consecutiveFails int) {
	m.mu.Lock()
	m.dockerHealthy = healthy
	m.dockerConsecutiveFails = consecutiveFails
	m.dockerCheckCount++
	m.mu.Unlock()
}

// SetInvalidServicesCount sets the count of services that failed config validation.
func (m *Metrics) SetInvalidServicesCount(count int) {
	m.mu.Lock()
	m.invalidServicesCount = count
	m.mu.Unlock()
}

// HealthSnapshot returns a copy of the service healthy/unhealthy gauges.
func (m *Metrics) HealthSnapshot() map[string]bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	result := make(map[string]bool, len(m.serviceHealthy))
	for k, v := range m.serviceHealthy {
		result[k] = v
	}
	return result
}

// ServiceCounters holds cumulative event counts for a single service.
type ServiceCounters struct {
	Updates   int64
	Rollbacks int64
	Restarts  int64
	Failures  int64
}

// CountersSnapshot returns a copy of per-service cumulative counters.
func (m *Metrics) CountersSnapshot() map[string]ServiceCounters {
	m.mu.RLock()
	defer m.mu.RUnlock()
	services := make(map[string]struct{})
	for k := range m.updates {
		services[k] = struct{}{}
	}
	for k := range m.rollbacks {
		services[k] = struct{}{}
	}
	for k := range m.restarts {
		services[k] = struct{}{}
	}
	for k := range m.failures {
		services[k] = struct{}{}
	}
	result := make(map[string]ServiceCounters, len(services))
	for svc := range services {
		result[svc] = ServiceCounters{
			Updates:   m.updates[svc],
			Rollbacks: m.rollbacks[svc],
			Restarts:  m.restarts[svc],
			Failures:  m.failures[svc],
		}
	}
	return result
}

// Meta holds global watcher metadata for the status endpoint.
type Meta struct {
	UptimeSeconds int64
	LastPoll      time.Time
	PollCount     int64
}

// Meta returns a point-in-time snapshot of watcher uptime and poll counters.
func (m *Metrics) Meta() Meta {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return Meta{
		UptimeSeconds: int64(time.Since(m.startTime).Seconds()),
		LastPoll:      m.lastPollTime,
		PollCount:     m.pollCount,
	}
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

	// Docker daemon health metrics
	b.WriteString("# HELP docker_daemon_healthy Whether Docker daemon is healthy (1) or not (0)\n")
	b.WriteString("# TYPE docker_daemon_healthy gauge\n")
	v := 0
	if m.dockerHealthy {
		v = 1
	}
	fmt.Fprintf(&b, "docker_daemon_healthy %d\n", v)

	b.WriteString("# HELP docker_daemon_consecutive_failures Consecutive Docker daemon health check failures\n")
	b.WriteString("# TYPE docker_daemon_consecutive_failures gauge\n")
	fmt.Fprintf(&b, "docker_daemon_consecutive_failures %d\n", m.dockerConsecutiveFails)

	b.WriteString("# HELP docker_daemon_checks_total Total Docker daemon health checks performed\n")
	b.WriteString("# TYPE docker_daemon_checks_total counter\n")
	fmt.Fprintf(&b, "docker_daemon_checks_total %d\n", m.dockerCheckCount)

	// Config validation metrics
	b.WriteString("# HELP watcher_invalid_services_total Number of services that failed config validation\n")
	b.WriteString("# TYPE watcher_invalid_services_total gauge\n")
	fmt.Fprintf(&b, "watcher_invalid_services_total %d\n", m.invalidServicesCount)

	return b.String()
}
