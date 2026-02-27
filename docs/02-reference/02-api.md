---
title: API Reference
description: HTTP API endpoints exposed by dockward on localhost, including trigger, blocked digest, health, and metrics routes.
tags:
  - api
  - reference
  - http
  - trigger
---

# API Reference

The dockward API binds exclusively to `127.0.0.1:<port>`. The default port is `9090`. Configure the port via `api.port` in the config file. The API is never exposed on external interfaces.

## Endpoints

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/trigger` | Trigger an immediate poll for all `auto_update` services |
| `POST` | `/trigger/<name>` | Trigger an immediate poll for one service |
| `GET` | `/blocked` | List services with a blocked digest |
| `DELETE` | `/blocked/<name>` | Unblock a service, allowing the next poll to attempt a deploy |
| `GET` | `/not-found` | List services with an unresolvable local digest |
| `GET` | `/errored` | List services with a persistent poll error |
| `GET` | `/status` | Aggregated state for all configured services |
| `GET` | `/status/<name>` | Aggregated state for a single service |
| `GET` | `/health` | Liveness check |
| `GET` | `/metrics` | Prometheus text format metrics |

---

## POST /trigger

Triggers an immediate poll for all services with `auto_update: true`, bypassing the configured `poll_interval`.

```sh
curl -sf -X POST localhost:9090/trigger
```

Response: `200 OK` with no body.

---

## POST /trigger/`<name>`

Triggers an immediate poll for a single service by name.

```sh
curl -sf -X POST localhost:9090/trigger/myapp
```

**Skipped responses:**

If the service has `auto_update: false`:
```json
{"status":"skipped","reason":"auto_update is false"}
```

If a deploy is already in progress for this service:
```json
{"status":"skipped","reason":"deploy in progress"}
```

Both return `200 OK`. A non-2xx status indicates a request error (e.g. unknown service name).

---

## GET /blocked

Returns the list of services that have a blocked digest. A digest is blocked after a rollback to prevent the same bad image from being redeployed. The block clears automatically when the remote digest changes.

```sh
curl -s localhost:9090/blocked
```

Example response:

```json
[
  {
    "service": "myapp",
    "digest": "sha256:abc123..."
  }
]
```

Empty list when no services are blocked:

```json
[]
```

---

## DELETE /blocked/`<name>`

Manually unblocks a service, allowing the next poll to attempt a deploy regardless of the blocked digest. Use this after confirming a fixed image has been pushed to the registry.

```sh
curl -sf -X DELETE localhost:9090/blocked/myapp
```

Response: `200 OK` with no body.

:::warning
Unblocking a service before pushing a fixed image will trigger another deploy of the same bad image, which will roll back again and re-block the digest.
:::

---

## GET /not-found

Returns the list of services whose local image digest could not be resolved. Dockward suppresses deploys for these services until the remote digest changes.

```sh
curl -s localhost:9090/not-found
```

Example response:

```json
["myapp"]
```

---

## GET /errored

Returns the list of services with a persistent poll error (e.g. registry unreachable, compose network not found). Dockward sends a notification on the first occurrence and suppresses repeats until the error changes or resolves.

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

Returns the aggregated state for all configured services. This is the single endpoint to check what dockward sees across updater and healer.

```sh
curl -s localhost:9090/status
```

Example response:

```json
[
  {
    "name": "myapp",
    "auto_update": true,
    "auto_start": false,
    "auto_heal": true,
    "healthy": true,
    "deploying": false,
    "degraded": false,
    "exhausted": false,
    "restart_failures": 0
  }
]
```

Fields with error details (`blocked`, `not_found`, `errored`) are omitted when empty.

---

## GET /status/`<name>`

Returns the aggregated state for a single service.

```sh
curl -s localhost:9090/status/myapp
```

Returns `404 Not Found` if the service name is not in the config.

---

## GET /health

Liveness check. Returns `200 OK` when the process is running.

```sh
curl -s localhost:9090/health
```

Response:

```json
{"status":"ok"}
```

---

## GET /metrics

Prometheus text exposition format. See [Metrics Reference](03-metrics.md) for the full metric list.

```sh
curl -s localhost:9090/metrics
```
