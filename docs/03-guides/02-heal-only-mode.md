---
title: Heal-Only Mode Guide
description: How dockward's auto-heal works for compose-managed and standalone containers, including event matching, cooldown, and max restart behaviour.
tags:
  - heal-only
  - auto-heal
  - healthcheck
  - guide
---

# Heal-Only Mode Guide

Heal-only mode monitors Docker events and restarts containers that become unhealthy. It requires no local registry, no compose files, and no `auto_update` configuration. It works with any container — compose-managed or started with `docker run`.

For full-mode services, `auto_heal` can be enabled alongside `auto_update`. The healer skips all events for a service while a deploy is in progress.

## Service Configuration

### Standalone container

```json
{
  "services": [
    {
      "name": "my-api",
      "container_name": "my-api",
      "auto_heal": true,
      "heal_cooldown": 120,
      "heal_max_restarts": 5
    }
  ]
}
```

### Compose-managed container

```json
{
  "services": [
    {
      "name": "proxy",
      "compose_project": "proxy",
      "auto_heal": true,
      "heal_cooldown": 300,
      "heal_max_restarts": 3
    }
  ]
}
```

When `compose_project` is set, dockward matches Docker events using the `com.docker.compose.project` label. When `container_name` is set, it matches by container name. Both can be set; compose project takes precedence for label-based matching.

:::note
`auto_heal: true` requires at least one of `compose_project` or `container_name`. A service with neither will fail validation at startup.
:::

## Event Handling

Dockward subscribes to the Docker event stream and processes three event types:

### `health_status: unhealthy`

1. Check cooldown — if within the cooldown window since the last restart, skip.
2. Check max restarts — if the consecutive failed restart count has reached `heal_max_restarts`, stop retrying and send a `critical` notification.
3. Issue `docker restart <container>` with a 10-second timeout.
4. Set cooldown timer.
5. After 30 seconds, verify the container is healthy. If not, the next `unhealthy` event will trigger another attempt (subject to cooldown and max restarts).

### `die`

Mark the service as degraded and send a `died` notification. Does not restart — dockward does not manage container restart policies. If the container has a `restart: unless-stopped` policy in compose or `--restart` flag in `docker run`, Docker will restart it automatically.

### `health_status: healthy`

Clear the degraded state and reset the consecutive restart counter. Send a `healthy` recovery notification.

:::note
Recovery notifications are suppressed during an active deploy for the same service. The updater sends its own `updated` notification on successful deploy.
:::

## Cooldown and Max Restarts

`heal_cooldown` prevents restart storms. After each restart attempt, further restarts for that service are blocked for `heal_cooldown` seconds regardless of how many `unhealthy` events arrive.

`heal_max_restarts` is the consecutive failed restart ceiling. The counter increments on each restart attempt and resets when the container reaches `healthy`. When the ceiling is reached, dockward stops restarting and sends a `critical` notification. The counter does not reset automatically after a `critical` — a manual intervention (fixing the container) must produce a `healthy` event to reset it.

## Healthcheck Requirement

Auto-heal responds to Docker's `health_status` events. These events are only emitted for containers that have a `HEALTHCHECK` instruction in their image or a `healthcheck:` block in their compose service definition.

Containers without a healthcheck produce only `die` events, which dockward records but does not restart.

Example compose healthcheck:

```yaml
services:
  my-api:
    image: my-api:latest
    healthcheck:
      test: ["CMD", "wget", "-qO", "/dev/null", "http://127.0.0.1:8080/health"]
      interval: 30s
      timeout: 5s
      start_period: 10s
      retries: 3
```

:::warning
In Alpine-based images, `localhost` resolves to `::1` (IPv6). If the service listens on IPv4 only, use `127.0.0.1` in the healthcheck URL, not `localhost`.
:::
