---
title: Web UI Reference
description: Overview of the dockward agent web dashboard — layout, data displayed, controls, and SSE live updates.
tags:
  - web-ui
  - reference
  - dashboard
---

# Web UI Reference

The agent web UI is a single-page dashboard served at `GET /ui`. It requires no external assets — the page is embedded in the binary.

```
http://127.0.0.1:9090/ui
```

## Services table

One row per configured service. Columns:

| Column | Description |
|--------|-------------|
| Name | Service name with container details, volume mounts, and deployed image info |
| Status | Synthesised status word — same values as `GET /status`. Color-coded |
| Config | Auto Update / Auto Heal / Auto Start flags (U/H/S badges) |
| Next Check | Time until next update check |
| Resources | CPU and memory usage bars (service aggregate) |
| Stats | U=Updates, R=Rollbacks, H=Heals, F=Failures (hover for full labels) |
| Actions | Trigger, Redeploy, and Unblock buttons |

The table updates in real-time via SSE from `GET /ui/stream`. Status pushes immediately on state changes, with a 30-second fallback poll.

## Service name cell

The Name column contains three collapsible sections:

**Containers** — Per-container details:
- Container name, state, CPU %, and memory usage
- Volume/bind mounts listed below each container (type, name/source, destination, read-only flag)

**Images** — All tracked images for the service:
- Full image reference (e.g. `localhost:5000/myapp:latest`)
- Uncompressed local size in MB
- Short digest (`sha256:abc123def45`)

## Recent events

A live feed of the last 50 audit entries, streamed via SSE from `GET /ui/stream`. New entries appear instantly as they arrive. On page load (including F5 refresh), the last 50 entries are replayed from the audit log so the table is never empty.

Columns: time, service, event type, level (color-coded badge), message.

## Controls

**Trigger** — sends `POST /trigger/<name>`. Status updates in real-time via SSE.

**Redeploy** — sends `POST /redeploy/<name>`. Forces a full redeployment regardless of digest state.

**Unblock** — sends `POST /unblock/<name>`. Visible only when the service status is `blocked`.

## Theme

A `[light]` / `[dark]` toggle in the header switches themes. The preference is persisted to `localStorage` and restored on page load. Defaults to the OS `prefers-color-scheme` setting.

## Access

The UI binds to `127.0.0.1` alongside the rest of the API. To access it from a browser on another machine, use an SSH tunnel:

```sh
ssh -L 9090:127.0.0.1:9090 user@host
```

Then open `http://127.0.0.1:9090/ui` locally.
