---
title: Metrics Reference
description: Prometheus metrics exposed by dockward at /metrics, with type, labels, and description for each metric.
tags:
  - metrics
  - prometheus
  - reference
  - observability
---

# Metrics Reference

Dockward exposes Prometheus metrics at `GET /metrics` in Prometheus text exposition format. The endpoint is implemented without external dependencies — the format is hand-written to the stdlib `net/http` response writer.

See [API Reference](02-api.md) for endpoint details.

## Metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `watcher_updates_total` | counter | `service` | Successful image deploys |
| `watcher_rollbacks_total` | counter | `service` | Rollbacks triggered after a failed deploy |
| `watcher_restarts_total` | counter | `service` | Auto-heal container restarts |
| `watcher_failures_total` | counter | `service` | Critical failures — restart exceeded max retries or rollback failed |
| `watcher_service_healthy` | gauge | `service` | `1` if the service is healthy, `0` if not |
| `watcher_service_blocked` | gauge | `service` | `1` if the service digest is blocked, `0` if not |
| `watcher_invalid_services_total` | gauge | — | Number of services that failed config validation and were skipped |
| `watcher_poll_count_total` | counter | — | Total poll cycles executed across all services |
| `watcher_last_poll_timestamp_seconds` | gauge | — | Unix timestamp of the most recent poll cycle |
| `watcher_uptime_seconds` | gauge | — | Seconds elapsed since dockward started |
| `docker_daemon_healthy` | gauge | — | `1` if Docker daemon is healthy, `0` if not |
| `docker_daemon_consecutive_failures` | gauge | — | Consecutive Docker daemon health check failures |
| `docker_daemon_checks_total` | counter | — | Total Docker daemon health checks performed |

## Example Output

```text
# HELP watcher_updates_total Successful image updates
# TYPE watcher_updates_total counter
watcher_updates_total{service="myapp"} 4

# HELP watcher_rollbacks_total Rollbacks after failed deploy
# TYPE watcher_rollbacks_total counter
watcher_rollbacks_total{service="myapp"} 1

# HELP watcher_restarts_total Auto-heal restarts
# TYPE watcher_restarts_total counter
watcher_restarts_total{service="myapp"} 2

# HELP watcher_failures_total Critical failures
# TYPE watcher_failures_total counter
watcher_failures_total{service="myapp"} 0

# HELP watcher_service_healthy Service health state
# TYPE watcher_service_healthy gauge
watcher_service_healthy{service="myapp"} 1

# HELP watcher_service_blocked Digest blocked after rollback
# TYPE watcher_service_blocked gauge
watcher_service_blocked{service="myapp"} 0

# HELP watcher_poll_count_total Total poll cycles
# TYPE watcher_poll_count_total counter
watcher_poll_count_total 48

# HELP watcher_last_poll_timestamp_seconds Unix timestamp of last poll
# TYPE watcher_last_poll_timestamp_seconds gauge
watcher_last_poll_timestamp_seconds 1.740567321e+09

# HELP watcher_uptime_seconds Seconds since start
# TYPE watcher_uptime_seconds gauge
watcher_uptime_seconds 14412

# HELP watcher_invalid_services_total Number of services that failed config validation
# TYPE watcher_invalid_services_total gauge
watcher_invalid_services_total 0

# HELP docker_daemon_healthy Whether Docker daemon is healthy (1) or not (0)
# TYPE docker_daemon_healthy gauge
docker_daemon_healthy 1

# HELP docker_daemon_consecutive_failures Consecutive Docker daemon health check failures
# TYPE docker_daemon_consecutive_failures gauge
docker_daemon_consecutive_failures 0

# HELP docker_daemon_checks_total Total Docker daemon health checks performed
# TYPE docker_daemon_checks_total counter
docker_daemon_checks_total 120
```

## Scrape Configuration

Add the following to your Prometheus `scrape_configs`:

```yaml
scrape_configs:
  - job_name: dockward
    static_configs:
      - targets: ["localhost:9090"]
```

:::note
The metrics endpoint is on localhost only. If Prometheus runs on a separate host, expose the endpoint via a secure tunnel or SSH port forward rather than binding dockward to an external interface.
:::
