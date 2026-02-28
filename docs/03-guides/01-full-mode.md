---
title: Full Mode Guide
description: How dockward's registry polling, deploy, health grace period, and rollback work together in full mode.
tags:
  - full-mode
  - deploy
  - registry
  - rollback
  - guide
---

# Full Mode Guide

Full mode combines registry polling, auto-deploy via docker compose, health verification, and automatic rollback. It requires a local Docker registry, compose files on the host, and a running dockward service.

For configuration fields see [Config Reference](../02-reference/01-config.md). For rollback mechanics see [Rollback Guide](03-rollback.md).

## Prerequisites

- Local Docker registry running at `localhost:5000` (or configured `registry.url`)
- `docker compose` CLI available on the host
- Compose files present at paths listed in `compose_files`
- Container previously started at least once (dockward cannot deploy a container that has never existed)

## Service Configuration

```json
{
  "registry": {
    "url": "http://localhost:5000",
    "poll_interval": 300
  },
  "services": [
    {
      "name": "myapp",
      "images": ["myapp:latest"],
      "compose_files": [
        "/srv/myapp/docker-compose.yml"
      ],
      "compose_project": "myapp",
      "auto_update": true,
      "auto_heal": true,
      "health_grace": 60,
      "heal_cooldown": 300,
      "heal_max_restarts": 3
    }
  ]
}
```

## Deploy Flow

Each poll cycle executes the following sequence per service:

**1. Fetch remote digest**

```
HEAD /v2/myapp/manifests/latest
```

Extracts the `Docker-Content-Digest` response header.

**2. Resolve local digest**

Inspect the local image by reference (`myapp:latest`). If not found, fall back to the running container's image ID. If still not found, suppress the deploy and record the service as `not-found` until the remote digest changes.

**3. Compare digests**

If equal, nothing to do. Poll cycle ends for this service.

**4. Tag current image as `:rollback`**

Preserves the running image for rollback before any changes are made.

**5. Pull and redeploy**

```sh
docker compose -p myapp -f /srv/myapp/docker-compose.yml pull
docker compose -p myapp -f /srv/myapp/docker-compose.yml up -d
```

**6. Health grace period**

Poll container health every 5 seconds for up to `health_grace` seconds:

| Container state | Action |
|-----------------|--------|
| No healthcheck configured | Treat as healthy immediately if running |
| `healthy` | Deploy succeeded — clean up `:rollback` tag |
| `unhealthy` | Rollback immediately |
| `starting` | Continue polling until grace period expires, then rollback |
| Not found | Continue polling until grace period expires, then rollback |

**7. On success**

Remove the `:rollback` tag. Send `updated` notification.

**8. On failure**

Rollback: retag `:rollback` as `:latest`, run `compose up -d`, block the bad digest. See [Rollback Guide](03-rollback.md).

## Concurrent Deploy Guard

Only one deploy per service runs at a time. If a poll cycle or `/trigger` call arrives while a deploy is in progress, it is skipped with reason `"deploy in progress"`. The healer also skips events for services with an active deploy.

## Manual Trigger

Bypass the poll interval and deploy immediately:

```sh
curl -sf -X POST localhost:9090/trigger/myapp
```

This is the same deploy sequence as a poll-triggered deploy. Used by CI/CD workflows immediately after pushing a new image. See [GitHub Actions](../01-getting-started/03-github-actions.md).

## First Deploy

On first run, if the container has never been started, dockward finds no local image and no running container to inspect. It records the service as `not-found` and suppresses the deploy until the remote digest changes.

Run the initial deploy manually before relying on dockward:

```sh
docker compose -p myapp -f /srv/myapp/docker-compose.yml up -d
```

:::warning
If `auto_heal` is also enabled and the container starts unhealthy on the first manual deploy, dockward will attempt restarts according to `heal_cooldown` and `heal_max_restarts`. Ensure the container is healthy before enabling `auto_heal` on a new service.
:::

## Compose Watch

`compose_watch: true` makes dockward apply changes to compose files without pushing a new image. Enable it for services where compose file edits (environment variables, resource limits, healthcheck parameters, bind mounts) should be applied automatically.

It runs independently of `auto_update`. A service can have one, both, or neither enabled.

**How it works:**

Each poll cycle dockward computes the SHA-256 of the concatenated contents of all files in `compose_files`. On the first run the hash is stored and no action is taken — the service is assumed to be running the current spec already. When a subsequent cycle detects a changed hash, dockward runs:

```sh
docker compose -p myapp -f /srv/myapp/docker-compose.yml up -d
```

No image pull occurs. The deploy uses whatever images are already present locally.

A `compose_drift` event is written to the audit log and sent to notification channels.

```json
{
  "name": "myapp",
  "compose_files": ["/srv/myapp/docker-compose.yml"],
  "compose_project": "myapp",
  "compose_watch": true
}
```

The poll interval is still controlled by `registry.poll_interval`. For compose-watch-only services (no `auto_update`), set `poll_interval` to a value that matches how quickly you need changes applied.

## Auto-Start

`auto_start: true` restarts the compose project when images are up to date but no containers are running. It acts as a recovery mechanism for services that stopped for reasons unrelated to a bad deploy (e.g. host reboot, manual `docker compose down`).

`auto_start` only activates when `auto_update: true` and all image digests match the registry. It does not start services with outdated images.

**Behavior by container state:**

| Container state | Action |
|-----------------|--------|
| Running | Nothing — normal state |
| None (never started or stopped) | `compose up -d` |
| Stuck (created/restarting but not running) | `compose down` + `compose up -d` |

A guard prevents repeated start attempts at the same image version. If `compose up` succeeds but the container immediately exits or never becomes healthy, dockward will not keep cycling `up`. The guard clears when the remote digest changes (new image pushed) or when a container comes up running.

A `started` notification (warning level) is sent on each start or forced restart.

```json
{
  "name": "myapp",
  "images": ["myapp:latest"],
  "compose_files": ["/srv/myapp/docker-compose.yml"],
  "compose_project": "myapp",
  "auto_update": true,
  "auto_start": true
}
```
