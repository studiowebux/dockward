---
title: Dockward
description: Docker deploy guard — polls a local registry for image changes, auto-deploys via docker compose with rollback, blocks bad digests, and auto-heals unhealthy containers.
tags:
  - dockward
  - docker
  - deploy
  - registry
  - auto-heal
---

# Dockward

Single static Go binary (~12 MB), zero external dependencies. Monitors a local Docker registry, auto-deploys via docker compose with rollback on failure, blocks bad digests, and auto-heals unhealthy containers. Designed for single-host deployment (OVH baremetal or similar).

## Two Operational Modes

**Full mode** — registry polling + auto-deploy with rollback + auto-heal. Requires a local Docker registry and docker compose files on the host.

**Heal-only mode** — Docker event monitoring + auto-restart. Works with any container, compose-managed or standalone. No registry or compose files needed.

## Key Behaviors

- Compares remote vs local image digests on each poll cycle; pulls and redeploys on change
- Tags current image as `:rollback` before each deploy; reverts automatically if the container is unhealthy or absent after the grace period
- Blocks the triggering digest after a rollback to prevent infinite redeploy loops; clears automatically when the remote digest changes
- Concurrent deploy guard prevents poll and API trigger races
- Listens to Docker event stream for `health_status` and `die` events; restarts unhealthy containers with cooldown and max-restart protection
- Prometheus metrics and trigger API exposed on localhost

## Navigation

- [Installation](01-getting-started/01-installation.md) — build, install, systemd setup
- [Configuration](01-getting-started/02-configuration.md) — config file walkthrough
- [GitHub Actions](01-getting-started/03-github-actions.md) — self-hosted runner CI/CD setup
- [Config Reference](02-reference/01-config.md) — all fields and validation rules
- [API Reference](02-reference/02-api.md) — HTTP endpoints
- [Metrics Reference](02-reference/03-metrics.md) — Prometheus metrics
- [Notifications Reference](02-reference/04-notifications.md) — Discord, SMTP, custom webhooks
- [Full Mode Guide](03-guides/01-full-mode.md) — deploy flow walkthrough
- [Heal-Only Mode Guide](03-guides/02-heal-only-mode.md) — healer setup and behavior
- [Rollback Guide](03-guides/03-rollback.md) — rollback mechanics and blocked digest management
