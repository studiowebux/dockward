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
| `watcher_poll_count_total` | counter | — | Total poll cycles executed across all services |
| `watcher_last_poll_timestamp_seconds` | gauge | — | Unix timestamp of the most recent poll cycle |
| `watcher_uptime_seconds` | gauge | — | Seconds elapsed since dockward started |

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
