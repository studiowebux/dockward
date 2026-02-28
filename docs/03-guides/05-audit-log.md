---
title: Audit Log
description: Record every dockward action to a structured JSON Lines file for auditing, debugging, and the web UI event feed.
tags:
  - audit
  - logging
  - observability
---

# Audit Log

The audit log writes one JSON object per line to a file every time dockward takes a significant action — deploy, rollback, restart, container death, recovery. It is opt-in and has no performance impact when disabled.

## Enable

Add `audit.path` to your config:

```json
"audit": {
  "path": "/var/log/dockward/audit.jsonl"
}
```

The directory must exist. The file is created if it does not exist and is opened in append mode, so existing entries are preserved across restarts.

## Entry format

Each line is a self-contained JSON object:

```json
{
  "timestamp": "2026-02-27T10:30:00Z",
  "service": "myapp",
  "event": "updated",
  "message": "Deployed new image successfully.",
  "level": "info",
  "old_digest": "sha256:abc123...",
  "new_digest": "sha256:def456..."
}
```

Optional fields (`old_digest`, `new_digest`, `container`, `reason`, `machine`) are omitted when empty.

## Event types

| Event | Level | Source | Description |
|-------|-------|--------|-------------|
| `updated` | info | updater | New image deployed successfully |
| `rolled_back` | warning | updater | Deploy failed; rolled back to previous image |
| `not_found` | warning | updater | Local image not found; suppressed until remote digest changes |
| `restarting` | warning | healer | Unhealthy container being restarted |
| `restarted` | info | healer | Container restarted and recovered |
| `critical` | critical | healer | Max restarts reached; manual intervention required |
| `died` | critical | healer | Container exited unexpectedly |
| `healthy` | info | healer | Container recovered and is healthy |

## Reading the log

Standard Unix tools work against JSON Lines:

```sh
# Tail in real time
tail -f /var/log/dockward/audit.jsonl

# All critical events
grep '"level":"critical"' /var/log/dockward/audit.jsonl

# All events for a specific service
grep '"service":"myapp"' /var/log/dockward/audit.jsonl | jq .

# Last 20 entries formatted
tail -20 /var/log/dockward/audit.jsonl | jq .
```

## Rotation

The audit logger itself does not rotate files. Use `logrotate` with `copytruncate`:

```
/var/log/dockward/audit.jsonl {
    daily
    rotate 30
    compress
    missingok
    copytruncate
}
```

`copytruncate` is required because dockward holds the file open. Do not use `create` (default) as it would leave dockward writing to the old inode.
