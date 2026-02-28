---
title: Troubleshooting
description: Common dockward problems, their causes, and how to fix them.
tags:
  - troubleshooting
  - debug
  - operations
---

# Troubleshooting

## Service stuck in `not_found`

**Symptom:** `GET /status` shows `"status":"not_found"` and no deploys are attempted.

**Cause:** Dockward compared the remote digest against the local image and could not resolve the local image. This happens when:

- The image has never been pulled on this host
- The `images` entry in config does not match the image name in the compose file
- The local registry has the image but Docker's image store does not

**Fix:** Verify the image name in `images[]` matches exactly what `docker images` shows. If the image is absent locally, push a new digest to the registry — dockward will detect the change and deploy.

---

## Service stuck in `blocked`

**Symptom:** `GET /status` shows `"status":"blocked"`. No deploys occur even after pushing a new image.

**Cause:** A rollback was triggered and dockward blocked the digest that caused it. It will unblock automatically when the remote digest changes (i.e. a new image is pushed).

**Fix:** Either push a fixed image to the registry, or manually unblock:

```sh
curl -sf -X POST localhost:9090/unblock/myapp
```

Only unblock manually if you have confirmed the current image in the registry is healthy.

---

## Rollback immediately re-blocks

**Symptom:** Push a new image, deploy starts, rolls back, blocked again.

**Cause:** The container fails the health check within `health_grace` seconds.

**Fix:**
- Check container logs: `docker logs <container>`
- Increase `health_grace` to give the container more startup time
- Verify the Docker `HEALTHCHECK` in the image is correct

---

## Updater redeploys on every poll

**Symptom:** `updated` events fire every poll cycle even though the image has not changed.

**Cause:** The image reference in `images[]` does not match the locally stored image name, causing the digest lookup to always return a mismatch or fail.

**Fix:** Run `docker images` and confirm the name and tag exactly match the value in `images[]`.

---

## Healer restarts a container in a loop

**Symptom:** Repeated `restarted` entries in the audit log; service never stabilises.

**Cause:** The container crashes or stays unhealthy faster than `heal_cooldown` allows it to be declared exhausted, or `heal_max_restarts` is set too high.

**Fix:**
- Lower `heal_max_restarts` (default is `3`)
- Investigate the container crash: `docker logs <container>`
- Fix the underlying application issue

---

## CPU / memory always shows `--` in the web UI

**Symptom:** The CPU / Mem column shows `--` for all services.

**Cause 1:** `monitor.stats_interval` has not elapsed since startup — stats are collected on a separate interval.

**Cause 2:** The service has no running containers matching the `compose_project` label.

**Cause 3:** The service has `silent: true`.

**Fix:** Wait one `stats_interval` cycle, then check `GET /status` for `has_stats: true`. If it stays false, verify the compose project name matches `docker ps --format '{{.Labels}}'` for `com.docker.compose.project`.

---

## Systemd service will not start

**Symptom:** `systemctl status dockward` shows `failed` immediately.

**Cause:** Config file parse error or validation failure.

**Fix:** Run dockward directly to see the error:

```sh
/usr/local/bin/dockward -config /etc/dockward/config.json
```

Common causes:
- JSON syntax error (trailing comma, missing brace)
- `auto_update: true` without `images`, `compose_files`, or `compose_project`
- `auto_heal: true` without `compose_project` or `container_name`

---

## Audit log not written

**Symptom:** `GET /audit` returns `[]`. No file appears at the configured path.

**Cause:** `audit.path` is empty or the directory does not exist.

**Fix:**

```sh
sudo mkdir -p /var/log/dockward
```

Verify `audit.path` in the config file points to a writable location.

---

## Warden: agent entries not appearing

**Symptom:** Events from one or more agents are absent from the warden dashboard.

**Cause:** Push is misconfigured or the warden is unreachable from the agent.

**Fix:**

Check agent logs for `push to warden failed`. Then verify:
- `push.warden_url` is reachable from the agent host
- `push.token` matches the corresponding `agents[].token` in warden config
- The warden is running: `curl -s https://warden.example.com/health`

---

## Warden: agent shown as offline

**Symptom:** The warden dashboard shows an agent as offline.

**Cause:** The warden heartbeat polls `GET <agents[].url>/health` every 30 seconds. The agent API binds to `127.0.0.1` — it is not reachable from another host on its default address.

**Fix:** Set `agents[].url` to an internal network address that the warden host can reach, not `127.0.0.1`.

---

## SSE stream disconnects (warden or agent UI)

**Symptom:** The event feed in the web UI stops updating. Browser console shows connection errors.

**Cause:** A reverse proxy (nginx) is buffering the SSE response.

**Fix:** Add `proxy_buffering off` to the nginx location block, or ensure the `X-Accel-Buffering: no` header passes through from dockward to the browser.
