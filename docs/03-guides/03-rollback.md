---
title: Rollback Guide
description: How dockward performs automatic rollback after a failed deploy, how digest blocking works, and how to manage blocked services.
tags:
  - rollback
  - blocked-digest
  - guide
  - deploy
---

# Rollback Guide

Dockward performs automatic rollback when a deployed container fails to reach a healthy state within the configured grace period. A digest block is set after rollback to prevent the same bad image from triggering another deploy loop.

For the full deploy sequence that precedes rollback, see [Full Mode Guide](01-full-mode.md).

## Rollback Trigger Conditions

Rollback is triggered when the health grace period expires or a definitive unhealthy state is detected:

| Container state during grace period | Outcome |
|-------------------------------------|---------|
| `healthy` | Deploy succeeds, no rollback |
| `unhealthy` | Rollback immediately |
| `starting` (grace period expires) | Rollback |
| Not found (grace period expires) | Rollback |

## Rollback Sequence

**1. Retag `:rollback` as `:latest`**

Before each deploy, dockward tags the current image as `<image>:rollback`. On rollback, this tag is promoted back to `<image>:latest`.

**2. Redeploy**

```sh
docker compose -p myapp -f /srv/myapp/docker-compose.yml up -d
```

This brings up the container using the restored `:latest` (which is the previously working image).

**3. Block the bad digest**

The digest that triggered the failed deploy is stored in memory. Subsequent poll cycles that resolve the same remote digest are silently skipped — no pull, no deploy attempt.

**4. Send notification**

A `rolled_back` notification is sent at `warning` level including `.OldDigest` (the bad digest) and `.NewDigest` if available.

:::danger
If the rollback itself fails — for example because the `:rollback` tag is missing or `compose up` returns an error — dockward sends a `critical` notification and increments `watcher_failures_total`. The service is left in whatever state Docker put it in. Manual intervention is required.
:::

## Blocked Digest Behaviour

After rollback, `watcher_service_blocked{service="myapp"}` is set to `1`. The service appears in `GET /blocked`.

The block clears automatically when the remote registry returns a different digest on the next poll. This is the normal path: push a fixed image, wait for the next poll (or trigger manually), and dockward deploys the new digest.

The block does not clear on process restart — it is held in memory only. A dockward restart effectively unblocks all services. If the same bad digest is still in the registry after a restart, the next poll will attempt a deploy and roll back again, re-establishing the block.

## Manual Unblock

To unblock a service without waiting for a new image or a process restart:

```sh
curl -sf -X DELETE localhost:9090/blocked/myapp
```

:::warning
Only unblock after confirming a fixed image is in the registry. Unblocking while the same bad digest is present will trigger another failed deploy and immediate rollback.
:::

Check which services are currently blocked:

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

## Tuning the Grace Period

`health_grace` controls how long dockward waits for the container to reach a healthy state before rolling back. The default is 60 seconds.

Set `health_grace` high enough to accommodate your container's startup time, including any `start_period` defined in its healthcheck. A container still in `starting` state when the grace period expires will be rolled back even if it would have become healthy shortly after.

```json
{
  "name": "myapp",
  "health_grace": 120
}
```

If the container has no healthcheck configured, dockward considers it healthy as soon as it is running, and the grace period is not used for timing — the deploy succeeds immediately on first detection of a running state.
