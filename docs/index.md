---
title: Dockward
description: Auto-deploy and auto-heal agent for self-hosted Docker containers.
tags:
  - dockward
  - docker
  - deploy
  - registry
  - auto-heal
---

# Dockward

Single static Go binary (~12 MB), zero external dependencies. Monitors a local Docker registry, auto-deploys via docker compose with rollback on failure, blocks bad digests, and auto-heals unhealthy containers. Designed for single-host deployment on bare metal or a VPS.

## Two Operational Modes

**Full mode** — registry polling + auto-deploy with rollback + auto-heal. Requires a local Docker registry and docker compose files on the host.

**Heal-only mode** — Docker event monitoring + auto-restart. Works with any container, compose-managed or standalone. No registry or compose files needed.

## Getting Started

- [Quick Start](01-getting-started/00-quick-start.md) — up and running in 5 minutes
- [Installation](01-getting-started/01-installation.md) — binary, build from source, systemd
- [Configuration](01-getting-started/02-configuration.md) — config file walkthrough
- [GitHub Actions](01-getting-started/03-github-actions.md) — self-hosted runner CI/CD

## Guides

- [Full Mode](03-guides/01-full-mode.md) — deploy flow, health grace, concurrent guards
- [Heal-Only Mode](03-guides/02-heal-only-mode.md) — event matching, cooldown, max restarts
- [Rollback](03-guides/03-rollback.md) — rollback triggers, digest blocking, manual unblock
- [Config Wizard](03-guides/04-config-wizard.md) — interactive CLI setup
- [Audit Log](03-guides/05-audit-log.md) — enable, format, event types, rotation
- [Troubleshooting](03-guides/07-troubleshooting.md) — common problems and fixes

## Reference

- [Config Reference](02-reference/01-config.md) — all fields, defaults, validation rules
- [API Reference](02-reference/02-api.md) — HTTP endpoints and response shapes
- [Web UI](02-reference/06-web-ui.md) — dashboard layout, controls, SSE feed
- [Metrics](02-reference/03-metrics.md) — Prometheus metrics
- [Notifications](02-reference/04-notifications.md) — Discord, SMTP, webhooks, event types

## Warden

- [Warden Reference](02-reference/05-warden.md) — config, endpoints, auth model
- [Warden Setup](03-guides/06-warden-setup.md) — multi-machine deployment guide
