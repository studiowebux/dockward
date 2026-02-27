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

Dockward sends notifications on significant events — deploys, rollbacks, heal restarts, and critical failures. All channels are optional. Multiple channels can be active simultaneously.

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

| Event | Level | Trigger |
|-------|-------|---------|
| `updated` | `info` | Successful image deploy |
| `rolled_back` | `warning` | Rollback after failed deploy |
| `unhealthy` | `warning` | Container reported unhealthy by Docker |
| `restarted` | `info` | Auto-heal restart succeeded |
| `critical` | `critical` | Restart exceeded `heal_max_restarts` or rollback failed |
| `healthy` | `info` | Container recovered to healthy state |
| `died` | `warning` | Container exited unexpectedly |
| `not_found` | `warning` | Container not found during health poll |

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
