---
title: Notifications Reference
description: Notification channels, event types, levels, and webhook template fields for dockward alerts.
tags:
  - notifications
  - discord
  - smtp
  - webhooks
  - reference
---

# Notifications Reference

Dockward sends notifications on significant events — deploys, rollbacks, heal restarts, resource alerts, and critical failures. All channels are optional. Multiple channels can be active simultaneously.

For configuration syntax see [Config Reference](01-config.md).

## Channels

### Discord

Sends a message to a Discord channel via an incoming webhook URL.

```json
"notifications": {
  "discord": {
    "webhook_url": "https://discord.com/api/webhooks/ID/TOKEN"
  }
}
```

### SMTP

Sends an email. `username` and `password` are optional for unauthenticated relays.

```json
"notifications": {
  "smtp": {
    "host": "smtp.example.com",
    "port": 587,
    "from": "alerts@example.com",
    "to": "ops@example.com",
    "username": "",
    "password": ""
  }
}
```

### Custom Webhooks

Sends an HTTP request with a templated body. Multiple webhooks can be defined. Header values support `$ENV_VAR` expansion at runtime — the variable must be present in the environment of the dockward process.

```json
"notifications": {
  "webhooks": [
    {
      "name": "my-webhook",
      "url": "https://example.com/hook",
      "method": "POST",
      "headers": {
        "Authorization": "Bearer $MY_TOKEN",
        "Content-Type": "application/json"
      },
      "body": "{\"service\":\"{{ .Service }}\",\"event\":\"{{ .Event }}\",\"message\":\"{{ .Message }}\"}"
    }
  ]
}
```

## Webhook Template Fields

The `body` field is rendered as a Go `text/template`. All fields below are available in every notification:

| Field | Type | Description |
|-------|------|-------------|
| `.Service` | string | Service name from config |
| `.Event` | string | Event type (see Events table below) |
| `.Message` | string | Human-readable notification message |
| `.Reason` | string | Additional context for the event, if applicable |
| `.Level` | string | Severity level (`info`, `warning`, `critical`) |
| `.OldDigest` | string | Previous image digest, populated on deploy and rollback events |
| `.NewDigest` | string | New image digest, populated on deploy events |
| `.Container` | string | Container name or ID, populated on heal events |

## Events

| Event | Level | Source | Trigger |
|-------|-------|--------|---------|
| `updated` | `info` | updater | Successful image deploy |
| `rolled_back` | `warning` | updater | Rollback succeeded — previous image restored |
| `rolled_back` | `critical` | updater | Rollback failed — could not retag or compose up failed |
| `compose_drift` | `info` | updater | Compose file changed; redeployed without image pull |
| `started` | `warning` | updater | Containers not found with correct image; compose project started |
| `not_found` | `warning` | updater | Local image not found; suppressed until registry digest changes |
| `error` | `critical` | updater | Persistent poll error (registry unreachable, compose failure) |
| `unhealthy` | `warning` | healer | Container reported unhealthy by Docker |
| `restarting` | `warning` | healer | Auto-heal restart attempt in progress |
| `restarted` | `warning` | healer | Auto-heal restart completed successfully |
| `critical` | `critical` | healer | Restart failed, max restarts exceeded, or container still unhealthy after restart |
| `healthy` | `info` | healer | Container recovered to healthy state |
| `died` | `critical` | healer | Container exited unexpectedly |
| `resource_alert` | `warning` | monitor | CPU or memory threshold exceeded |

## Webhook Example — GitHub Actions Dispatch

```json
{
  "name": "github-deploy-pipeline",
  "url": "https://api.github.com/repos/myorg/myrepo/actions/workflows/deploy.yml/dispatches",
  "method": "POST",
  "headers": {
    "Authorization": "Bearer $GH_TOKEN",
    "Accept": "application/vnd.github.v3+json"
  },
  "body": "{\"ref\":\"main\",\"inputs\":{\"service\":\"{{ .Service }}\",\"event\":\"{{ .Event }}\"}}"
}
```

:::note
`$GH_TOKEN` is read from the environment of the dockward process at runtime. When running under systemd, set it via an `EnvironmentFile=` directive in the unit or an `Environment=` line — never hardcode it in the config file.
:::
