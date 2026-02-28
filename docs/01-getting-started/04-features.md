---
title: Features
description: Complete feature list for dockward — auto-deploy, rollback, healing, metrics, notifications, and warden mode.
tags:
  - features
  - overview
---

# Features

| Feature | Description |
|---|---|
| **Auto-update** | Polls a local Docker registry; redeploys a compose service the moment a new image digest lands |
| **Rollback** | Tags the previous image before every deploy; reverts automatically if the new container fails to become healthy |
| **Auto-heal** | Listens to Docker health events; restarts unhealthy containers with per-service cooldown to prevent restart storms |
| **Multi-image services** | One service entry can track multiple images; a single compose redeploy fires when any image changes |
| **Heal-only mode** | Watch and recover a service without ever touching its image — useful for third-party or pinned containers |
| **Blocked digest protection** | Blocks a known-bad digest in memory; dockward skips it until a new digest is pushed to the registry, the service is manually unblocked via API, or dockward restarts |
| **Resource alerts** | Per-container CPU and memory thresholds with independent cooldowns; fires notifications before a service falls over |
| **Audit log** | Append-only JSONL file of every deploy, rollback, restart, and alert — full trail for post-mortems |
| **Live web UI** | Real-time dashboard over SSE; shows service health, deployed image, container list, and recent events without page reloads |
| **Prometheus metrics** | Counters and gauges for updates, rollbacks, restarts, and failures; drop into any existing Grafana setup |
| **Notifications** | Discord webhook, SMTP email, and arbitrary HTTP webhooks; each event type is independently configurable |
| **HTTP API** | `/status`, `/trigger`, `/unblock`, `/audit`, `/metrics` — automate or integrate with any toolchain |
| **Warden mode** | Central aggregator for multi-machine fleets; agents push audit entries over HTTP, warden exposes a unified SSE dashboard |
| **Single binary** | Zero runtime dependencies, ~12 MB; ships as `linux/amd64`, `linux/arm64`, and `darwin/arm64` |
| **Compose-native** | Delegates all deploys to `docker compose up -d` — no custom orchestration, no vendor lock-in |
| **Systemd-ready** | Ships with service units for both agent and warden modes; `systemctl enable dockward` and done |
