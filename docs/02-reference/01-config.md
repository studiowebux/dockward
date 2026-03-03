---
title: Config Reference
description: Complete field reference for the dockward JSON configuration file, including defaults and validation rules.
tags:
  - config
  - reference
  - json
---

# Config Reference

All configuration is loaded from a single JSON file. Default path: `/etc/dockward/config.json`. Override with `-config <path>`.

For a walkthrough with annotated examples, see [Configuration](../01-getting-started/02-configuration.md).

## Top-Level Structure

```json
{
  "runtime": "docker",
  "registry": { ... },
  "api": { ... },
  "audit": { ... },
  "monitor": { ... },
  "notifications": { ... },
  "push": { ... },
  "services": [ ... ]
}
```

## `runtime`

Specifies the container runtime to use for all compose operations.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `runtime` | string | `"docker"` | Container runtime executable: `"docker"` or `"podman"`. Both support the same `compose` subcommand syntax |

```json
"runtime": "podman"
```

:::tip
Both Docker and Podman use the same compose command syntax (`docker compose` / `podman compose`), making them interchangeable. This setting determines which executable is called.
:::

## `registry`

Controls the registry polling behaviour used by full-mode services.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `url` | string | `"http://localhost:5000"` | Base URL of the local Docker registry |
| `poll_interval` | integer | `300` | Seconds between registry poll cycles (image digest comparison) |

```json
"registry": {
  "url": "http://localhost:5000",
  "poll_interval": 300
}
```

## `monitor`

Controls container resource stat collection (CPU, memory). Independent of registry polling.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `stats_interval` | integer | `registry.poll_interval` | Seconds between container stat collections. Set lower than `poll_interval` (e.g. `30`) to get fresher CPU/memory data in the UI and `/status` endpoint without polling the registry more often |

```json
"monitor": {
  "stats_interval": 30
}
```

## `api`

Controls the HTTP API server.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `address` | string | `"127.0.0.1"` | Bind address for the API server. Use `"0.0.0.0"` to expose externally (not recommended without authentication) |
| `port` | string | `"9090"` | Port for the API and metrics server |

```json
"api": {
  "address": "127.0.0.1",
  "port": "9090"
}
```

:::warning
Setting `address: "0.0.0.0"` exposes the API to your network. The API has **no authentication** - only do this on trusted networks or behind a reverse proxy with auth.
:::

## `audit`

Audit logging is opt-in. Omit the section or leave `path` empty to disable it.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `path` | string | `""` | Absolute path to the audit log file. Created if it does not exist. Empty disables audit logging |

```json
"audit": {
  "path": "/var/log/dockward/audit.jsonl"
}
```

The file is written in [JSON Lines](https://jsonlines.org) format — one JSON object per line. Each entry contains: `timestamp`, `service`, `event`, `message`, `level`, and optional fields (`old_digest`, `new_digest`, `container`, `reason`). See [Audit Log Guide](../03-guides/05-audit-log.md) for event types and usage.

## `notifications`

All notification channels are optional. Omit any channel to disable it. See [Notifications Reference](04-notifications.md) for template fields and event types.

### `notifications.discord`

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `webhook_url` | string | yes | Discord channel webhook URL |

### `notifications.smtp`

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `host` | string | yes | SMTP server hostname |
| `port` | integer | yes | SMTP server port |
| `from` | string | yes | Sender address |
| `to` | string | yes | Recipient address |
| `username` | string | no | SMTP auth username |
| `password` | string | no | SMTP auth password |

### `notifications.webhooks`

Array of custom webhook definitions.

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `name` | string | yes | Identifier for this webhook |
| `url` | string | yes | Endpoint URL |
| `method` | string | yes | HTTP method (e.g. `"POST"`) |
| `headers` | object | no | Key-value HTTP headers; values support `$ENV_VAR` expansion |
| `body` | string | no | Request body; Go `text/template` with notification fields |

## `push`

Optional. When `warden_url` is set, every audit entry is forwarded to the warden asynchronously. Push is fire-and-forget — agent operation is not affected by warden availability.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `warden_url` | string | `""` | Warden base URL (e.g. `https://warden.example.com`). Empty disables push |
| `token` | string | `""` | Bearer token matching the warden's `agents[].token`. `$ENV_VAR` expansion supported |
| `machine_id` | string | `""` | Identifier shown in the warden dashboard (e.g. `ovh-01`) |

```json
"push": {
  "warden_url": "https://warden.example.com",
  "token": "$DOCKWARD_PUSH_TOKEN",
  "machine_id": "ovh-01"
}
```

## `services`

Array of service definitions. Each service is independent — fields used depend on which modes are enabled.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `name` | string | required | Unique service identifier used in API paths, metrics labels, and notifications |
| `images` | []string | — | Registry image references (e.g. `["api:latest", "worker:latest"]`). Required when `auto_update: true`. One deploy per compose project when any image changes |
| `silent` | boolean | `false` | Skip validation and monitoring for this service. Use for internal or externally-managed services referenced for healer-only purposes |
| `compose_files` | []string | — | Absolute paths to compose files, applied in order. Required when `auto_update: true` |
| `compose_project` | string | — | Docker Compose project name (`-p` flag). Required when `auto_update: true` |
| `container_name` | string | — | Container name for event matching. Used for standalone containers or as fallback |
| `env_file` | string | — | Path to a `.env` file. Variables are loaded into the process environment before running compose, making them available for `${VAR}` interpolation in compose files |
| `auto_update` | boolean | `false` | Enable registry polling and auto-deploy for this service |
| `auto_start` | boolean | `false` | When `true` and digests match, start the compose project if no containers are running. Forces `down`+`up` if containers are stuck (created/restarting) |
| `auto_heal` | boolean | `false` | Enable auto-restart on unhealthy health status |
| `compose_watch` | boolean | `false` | Re-deploy on compose file content change (no image pull). Computes SHA-256 of all `compose_files` each poll cycle; runs `compose up -d` when the hash changes. First run stores the hash without deploying |
| `cpu_threshold` | float | `0` | Alert when CPU usage exceeds this percentage. `0` disables. Uses same cooldown as `heal_cooldown` |
| `memory_threshold` | float | `0` | Alert when memory usage exceeds this percentage. `0` disables. Uses same cooldown as `heal_cooldown` |
| `health_grace` | integer | `60` | Seconds to wait after deploy before evaluating container health |
| `heal_cooldown` | integer | `300` | Minimum seconds between consecutive auto-restarts |
| `heal_max_restarts` | integer | `3` | Maximum consecutive failed restarts before giving up |

## Validation Rules

### Global Validation (Fatal)

These validation errors cause dockward to exit immediately:

- `runtime` must be `"docker"` or `"podman"`
- `api.port` must be a valid port number (1-65535)
- `registry.poll_interval` must be 10-86400 seconds
- `docker_health.check_interval` must be 5-3600 seconds
- `docker_health.timeout` must be 1-30 seconds and less than `check_interval`

### Service Validation (Non-Fatal)

**As of v1.0.0-alpha.9:** Service-level validation errors are **non-fatal**. Invalid services are logged as warnings and excluded from monitoring, while valid services continue operating normally.

Invalid services trigger warnings like:
```
[config] WARNING: 1 service(s) failed validation and will be skipped:
  - service[18] "otel-collector": compose_file[0] not found: "/srv/observability/docker-compose.yml"
```

Service validation rules:
- `auto_update: true` requires at least one entry in `images`, at least one entry in `compose_files`, and `compose_project`
- `auto_heal: true` requires at least one of `compose_project` or `container_name` for Docker event matching
- `compose_files` paths must be absolute, must exist, and must be regular files (no directories or symlinks)
- `compose_project` must match pattern `^[a-zA-Z0-9_-]{1,64}$` (security: prevents command injection)
- `env_file` path must be absolute and must exist if specified
- Path traversal attempts (`..`) are forbidden in all file paths (security)
- `silent: true` skips all validation rules for the service

**Monitoring invalid services:** Use `GET /health` to see `config_warnings` array with reasons for each skipped service.

## Full Example

```json
{
  "runtime": "docker",
  "registry": {
    "url": "http://localhost:5000",
    "poll_interval": 300
  },
  "monitor": {
    "stats_interval": 30
  },
  "api": {
    "port": "9090"
  },
  "audit": {
    "path": "/var/log/dockward/audit.jsonl"
  },
  "notifications": {
    "discord": {
      "webhook_url": "https://discord.com/api/webhooks/ID/TOKEN"
    },
    "smtp": {
      "host": "smtp.example.com",
      "port": 587,
      "from": "alerts@example.com",
      "to": "ops@example.com",
      "username": "",
      "password": ""
    },
    "webhooks": [
      {
        "name": "my-webhook",
        "url": "https://example.com/hook",
        "method": "POST",
        "headers": {
          "Authorization": "Bearer $MY_TOKEN"
        },
        "body": "{\"service\":\"{{ .Service }}\",\"event\":\"{{ .Event }}\"}"
      }
    ]
  },
  "push": {
    "warden_url": "https://warden.example.com",
    "token": "$DOCKWARD_PUSH_TOKEN",
    "machine_id": "ovh-01"
  },
  "services": [
    {
      "name": "myapp",
      "images": ["myapp:latest"],
      "compose_files": [
        "/srv/myapp/docker-compose.yml",
        "/srv/myapp/docker-compose.override.yml"
      ],
      "compose_project": "myapp",
      "env_file": "/srv/myapp/.env",
      "auto_update": true,
      "auto_start": true,
      "auto_heal": true,
      "health_grace": 60,
      "heal_cooldown": 300,
      "heal_max_restarts": 3
    },
    {
      "name": "standalone-api",
      "container_name": "standalone-api",
      "auto_heal": true,
      "heal_cooldown": 120,
      "heal_max_restarts": 5
    }
  ]
}
```
