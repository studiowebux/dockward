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
  "registry": { ... },
  "api": { ... },
  "audit": { ... },
  "notifications": { ... },
  "services": [ ... ]
}
```

## `registry`

Controls the registry polling behaviour used by full-mode services.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `url` | string | `"http://localhost:5000"` | Base URL of the local Docker registry |
| `poll_interval` | integer | `300` | Seconds between poll cycles |

```json
"registry": {
  "url": "http://localhost:5000",
  "poll_interval": 300
}
```

## `api`

Controls the HTTP API server. The API binds to `127.0.0.1` only — it is never exposed externally.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `port` | string | `"9090"` | Port for the API and metrics server |

```json
"api": {
  "port": "9090"
}
```

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

## `services`

Array of service definitions. Each service is independent — fields used depend on which modes are enabled.

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `name` | string | required | Unique service identifier used in API paths, metrics labels, and notifications |
| `image` | string | — | Registry image reference (e.g. `myapp:latest`). Required when `auto_update: true` |
| `compose_files` | []string | — | Absolute paths to compose files, applied in order. Required when `auto_update: true` |
| `compose_file` | string | — | Deprecated singular form. Promoted to `compose_files` at load time. Still accepted |
| `compose_project` | string | — | Docker Compose project name (`-p` flag). Required when `auto_update: true` |
| `container_name` | string | — | Container name for event matching. Used for standalone containers or as fallback |
| `env_file` | string | — | Path to a `.env` file. Variables are loaded into the process environment before running compose, making them available for `${VAR}` interpolation in compose files |
| `auto_update` | boolean | `false` | Enable registry polling and auto-deploy for this service |
| `auto_start` | boolean | `false` | When `true` and digests match, start the compose project if no containers are running. Forces `down`+`up` if containers are stuck (created/restarting) |
| `auto_heal` | boolean | `false` | Enable auto-restart on unhealthy health status |
| `compose_watch` | boolean | `false` | Re-deploy on compose file content change (no image pull). Computes SHA-256 of all `compose_files` each poll cycle; runs `compose up -d` when the hash changes. First run stores the hash without deploying |
| `health_grace` | integer | `60` | Seconds to wait after deploy before evaluating container health |
| `heal_cooldown` | integer | `300` | Minimum seconds between consecutive auto-restarts |
| `heal_max_restarts` | integer | `3` | Maximum consecutive failed restarts before giving up |

## Validation Rules

:::danger
Validation failures at startup cause dockward to exit with a non-zero status. Check `journalctl -u dockward` for the error message.
:::

- `auto_update: true` requires `image`, at least one entry in `compose_files` (or the deprecated `compose_file`), and `compose_project`
- `auto_heal: true` requires at least one of `compose_project` or `container_name` for Docker event matching
- `name` must be unique across all service definitions
- `compose_file` (singular) is accepted but deprecated; prefer `compose_files`

## Full Example

```json
{
  "registry": {
    "url": "http://localhost:5000",
    "poll_interval": 300
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
  "services": [
    {
      "name": "myapp",
      "image": "myapp:latest",
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
