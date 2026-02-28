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
| Name | Service name. Expands to a per-container list when clicked (state, status string) |
| Status | Synthesised status word — same values as `GET /status`. Color-coded |
| Image | Deployed image reference (e.g. `myapp:latest`). `--` until first deploy |
| Digest | Short form of the deployed image digest. `--` until first deploy |
| CPU / Mem | Current CPU % and memory % across all containers. `--` when `has_stats` is false |
| Updates | Cumulative update counter since process start |
| Rollbacks | Cumulative rollback counter since process start |
| Restarts | Cumulative restart counter since process start |
| Actions | Trigger and Unblock buttons (Unblock only shown when service is blocked) |

The table refreshes every 15 seconds via a background `fetch` to `GET /status`. Open `<details>` rows survive the refresh — their state is preserved across updates.

## Container list

Clicking the `N container(s)` summary inside the Name cell expands a per-container list showing:

- Container name
- State (`running`, `exited`, `created`, etc.)
- Status string from Docker (e.g. `Up 2 hours (healthy)`)

## Recent events

A live feed of the last 50 audit entries, streamed via SSE from `GET /ui/events`. New entries are prepended as they arrive. On page load the last 50 entries are replayed immediately from the audit log.

Columns: time, service, event type, level (color-coded), message.

## Controls

**Trigger** — sends `POST /trigger/<name>` without reloading the page. The service's status updates on the next 15-second refresh.

**Unblock** — sends `POST /unblock/<name>`. Visible only when the service status is `blocked`.

## Theme

A `[light]` / `[dark]` toggle in the header switches themes. The preference is persisted to `localStorage` and restored on page load. Defaults to the OS `prefers-color-scheme` setting.

## Access

The UI binds to `127.0.0.1` alongside the rest of the API. To access it from a browser on another machine, use an SSH tunnel:

```sh
ssh -L 9090:127.0.0.1:9090 user@host
```

Then open `http://127.0.0.1:9090/ui` locally.
