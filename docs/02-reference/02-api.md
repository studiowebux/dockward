---
title: API Reference
description: HTTP API endpoints exposed by dockward on localhost, including trigger, status, blocked digest, audit, and metrics routes.
tags:
  - api
  - reference
  - http
  - trigger
---

# API Reference

The dockward API binds exclusively to `127.0.0.1:<port>`. Default port is `9090`. Configure via `api.port` in the config file.

## Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/trigger` | Trigger an immediate poll for all `auto_update` services |
| `POST` | `/trigger/<name>` | Trigger an immediate poll for one service |
| `GET` | `/blocked` | Map of blocked digests keyed by `service/image` |
| `DELETE` | `/blocked/<name>` | Unblock a service (legacy; prefer `POST /unblock/<name>`) |
| `POST` | `/unblock/<name>` | Unblock a service |
| `GET` | `/not-found` | Map of unresolvable local digests keyed by `service/image` |
| `GET` | `/errored` | Map of services with persistent poll errors |
| `GET` | `/status` | Aggregated state for all configured services |
| `GET` | `/status/<name>` | Aggregated state for a single service |
| `GET` | `/audit` | Recent audit log entries as JSON |
| `GET` | `/health` | Liveness check |
| `GET` | `/metrics` | Prometheus text format metrics |
| `GET` | `/ui` | Web dashboard |
| `GET` | `/ui/events` | SSE stream of live audit entries |

---

## POST /trigger

Triggers an immediate poll for all services with `auto_update: true`, bypassing the configured `poll_interval`.

```sh
curl -sf -X POST localhost:9090/trigger
```

Response:

```json
{"status":"triggered","scope":"all"}
```

---

## POST /trigger/`<name>`

Triggers an immediate poll for a single service by name.

```sh
curl -sf -X POST localhost:9090/trigger/myapp
```

Success response:

```json
{"status":"triggered","scope":"myapp"}
```

Skipped — `auto_update: false`:

```json
{"status":"skipped","reason":"auto_update is false"}
```

Skipped — deploy already in progress:

```json
{"status":"skipped","reason":"deploy in progress"}
```

All return `200 OK`. A `404` indicates the service name is not in the config.

---

## GET /blocked

Returns all blocked digests. A digest is blocked after a rollback to prevent the same bad image from being redeployed. The block clears automatically when the remote digest changes.

Keys are in `service/image` format, values are the blocked digest.

```sh
curl -s localhost:9090/blocked
```

Example response:

```json
{"myapp/myapp:latest":"sha256:deadbeef..."}
```

Empty object when nothing is blocked:

```json
{}
```

---

## DELETE /blocked/`<name>` · POST /unblock/`<name>`

Manually unblocks a service, allowing the next poll to attempt a deploy. The `POST /unblock/<name>` form is used by the web UI; both are equivalent.

```sh
curl -sf -X DELETE localhost:9090/blocked/myapp
curl -sf -X POST   localhost:9090/unblock/myapp
```

Response when unblocked:

```json
{"status":"unblocked","service":"myapp"}
```

Response when service was not blocked:

```json
{"status":"not_blocked","service":"myapp"}
```

:::warning
Unblocking before pushing a fixed image triggers another deploy of the same bad image, which will roll back and re-block.
:::

---

## GET /not-found

Returns services whose local image digest could not be resolved. Dockward suppresses deploys until the remote digest changes.

Keys are in `service/image` format, values are the last known remote digest.

```sh
curl -s localhost:9090/not-found
```

Example response:

```json
{"myapp/myapp:latest":"sha256:abc123..."}
```

---

## GET /errored

Returns services with a persistent poll error. Dockward notifies on first occurrence and suppresses repeats until the error changes or resolves.

```sh
curl -s localhost:9090/errored
```

Example response:

```json
{"myapp":"remote digest: HEAD http://localhost:5000/v2/myapp/manifests/latest: dial tcp 127.0.0.1:5000: connect: connection refused"}
```

Empty object when no services are errored:

```json
{}
```

---

## GET /status

Returns the aggregated state for all configured services plus watcher-level metadata.

```sh
curl -s localhost:9090/status
```

Example response:

```json
{
  "uptime_seconds": 86400,
  "last_poll": "2026-02-28T10:30:00Z",
  "poll_count": 288,
  "services": [
    {
      "name": "myapp",
      "status": "ok",
      "auto_update": true,
      "auto_start": false,
      "auto_heal": true,
      "healthy": true,
      "deploying": false,
      "degraded": false,
      "exhausted": false,
      "restart_failures": 0,
      "updates_total": 5,
      "rollbacks_total": 0,
      "restarts_total": 1,
      "failures_total": 0,
      "image": "myapp:latest",
      "image_digest": "sha256:abc123...",
      "containers": [
        {
          "id": "abc123def456",
          "name": "myapp_api_1",
          "state": "running",
          "status": "Up 2 hours (healthy)",
          "image": "myapp:latest",
          "cpu_percent": 1.2,
          "memory_percent": 15.3,
          "memory_usage_mb": 153.2,
          "memory_limit_mb": 1000.0
        }
      ],
      "has_stats": true,
      "cpu_percent": 2.4,
      "memory_percent": 18.7,
      "memory_usage_mb": 187.3,
      "memory_limit_mb": 1000.0
    }
  ]
}
```

**`status` field** — priority-ordered, only the highest applies:

| Value | Meaning |
|-------|---------|
| `exhausted` | Max restart attempts reached; manual intervention required |
| `degraded` | Healer detected a die or unhealthy event; attempting recovery |
| `errored` | Persistent poll error (registry unreachable, compose failure, etc.) |
| `blocked` | Digest blocked after rollback; retries when remote digest changes |
| `not_found` | Local image not found; suppressed until remote digest changes |
| `deploying` | Image update in progress |
| `ok` | Running and healthy |
| `unhealthy` | Health gauge reports unhealthy; no active recovery |
| `unknown` | No health data yet (process just started or no Docker event received) |

**Field notes:**

- `blocked`, `not_found`, `errored` — omitted from JSON when empty
- `healthy` — omitted until the healer receives a Docker health event
- `image`, `image_digest` — omitted until first successful deploy or poll cycle
- `containers` — omitted for services with no `compose_project` or `silent: true`
  - `containers[].id` — Docker container ID (full, not short)
  - `containers[].cpu_percent`, `memory_percent`, `memory_usage_mb`, `memory_limit_mb` — per-container stats, omitted if monitor hasn't polled yet
- `has_stats` — `false` until the first monitor poll cycle (service-level aggregated stats)
- `cpu_percent`, `memory_percent`, `memory_usage_mb`, `memory_limit_mb` — service-level aggregated stats across all containers
- `last_poll` — omitted until the first poll cycle completes

---

## GET /status/`<name>`

Returns the aggregated state for a single service. Same shape as a single entry in the `services` array above (not the wrapper object).

```sh
curl -s localhost:9090/status/myapp
```

Returns `404` if the service name is not in the config.

---

## GET /audit

Returns the last N audit log entries as a JSON array. Returns an empty array when `audit.path` is not set.

**Query parameters:**

| Parameter | Default | Max | Description |
|-----------|---------|-----|-------------|
| `limit` | `100` | `500` | Number of entries to return (most recent first) |

```sh
curl -s "localhost:9090/audit?limit=20"
```

Example response:

```json
[
  {
    "timestamp": "2026-02-28T10:00:00Z",
    "service": "myapp",
    "event": "updated",
    "message": "Deployed new image successfully.",
    "level": "info",
    "old_digest": "sha256:aaa...",
    "new_digest": "sha256:bbb..."
  }
]
```

---

## GET /health

Liveness check. Returns `200 OK` when the process is running.

```sh
curl -s localhost:9090/health
```

```json
{"status":"ok"}
```

---

## GET /metrics

Prometheus text exposition format. See [Metrics Reference](03-metrics.md).

```sh
curl -s localhost:9090/metrics
```

---

## GET /ui

Web dashboard. Open in a browser:

```
http://127.0.0.1:9090/ui
```

See [Web UI Reference](06-web-ui.md).

---

## GET /ui/events

Server-Sent Events stream of live audit entries. Replays the last 50 entries on connect, then streams new entries as they are logged.

Consumed automatically by the web UI. Can also be consumed directly:

```sh
curl -s localhost:9090/ui/events
```
