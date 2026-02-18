# Dockward

Docker deploy guard: polls a local registry for image changes, auto-deploys via docker compose with rollback on failure, blocks bad digests to prevent infinite loops, and auto-heals unhealthy containers.

Bug tracker: https://github.com/studiowebux/dockward/issues
Discord: https://discord.gg/BG5Erm9fNv

Funding: [Buy Me a Coffee](https://buymeacoffee.com/studiowebux) | [GitHub Sponsors](https://github.com/sponsors/studiowebux) | [Patreon](https://patreon.com/studiowebux)

## About

Single static Go binary (~12 MB), zero external dependencies. Talks to the Docker daemon and a local registry via their HTTP APIs (unix socket and REST).

Features:

- Registry polling: compares remote vs local image digests, pulls and redeploys on change
- Rollback: tags current image as `:rollback` before deploy, reverts if container is unhealthy or not running after grace period. Blocks the bad digest to prevent infinite rollback loops.
- Atomic deploys: concurrent deploy guard prevents poll/API race conditions
- Label-based matching: uses `com.docker.compose.project` labels for container identification (no substring ambiguity)
- Health polling: polls container health every 5s during grace period (fail fast on unhealthy, keep waiting on starting)
- Auto-heal: listens for Docker health events, restarts unhealthy containers with cooldown protection
- Notifications: Discord webhook, SMTP email, custom webhooks with Go `text/template` body
- Prometheus metrics: `/metrics` endpoint with update/rollback/restart/failure/blocked counters per service
- Trigger API: `POST /trigger` and `POST /trigger/<service>` for manual deploys
- Blocked digest API: `GET /blocked` and `DELETE /blocked/<service>` for managing blocked digests
- Systemd integration: runs as a system service, logs to journal

## Architecture

```
dockward/
  cmd/dockward/main.go          Entry point, signal handling, component wiring
  internal/
    config/config.go            JSON config loader with defaults and validation
    docker/
      client.go                 Unix socket HTTP client for Docker daemon
      containers.go             List, inspect, restart, label-based lookup
      images.go                 Inspect, tag, remove images
      events.go                 Real-time Docker event stream (SSE)
    registry/registry.go        Registry v2 API (digest comparison)
    compose/compose.go          docker compose pull/up via exec (with -p project)
    notify/
      notify.go                 Dispatcher + Alert types
      discord.go                Discord webhook sender
      smtp.go                   SMTP email sender
      webhook.go                Custom webhook with template rendering
    watcher/
      updater.go                Poll loop, digest comparison, deploy, rollback, blocked digests
      healer.go                 Docker event listener, restart, cooldown
      metrics.go                Prometheus counters and gauges
      api.go                    HTTP API (trigger, health, metrics, blocked)
```

## Installation

Build from source (requires Go 1.24+):

```sh
GOOS=linux GOARCH=amd64 go build -ldflags "-X main.version=$(git describe --tags --always)" -o dockward-linux-amd64 ./cmd/dockward/
```

Deploy via Ansible:

```sh
ansible-playbook -l production playbooks/dockward.yml
```

The playbook copies the binary to `/usr/local/bin/dockward`, the config to `/etc/dockward/config.json`, and installs a systemd unit.

## Configuration

Copy `config.sample.json` to `config.json` and edit:

```json
{
  "registry": {
    "url": "http://localhost:5000",
    "poll_interval": 300
  },
  "api": {
    "port": "9090"
  },
  "notifications": {
    "discord": {
      "webhook_url": "https://discord.com/api/webhooks/ID/TOKEN"
    }
  },
  "services": [
    {
      "name": "myapp",
      "image": "myapp:latest",
      "compose_file": "/srv/myapp.com/docker-compose.yml",
      "compose_project": "myapp",
      "auto_update": true,
      "auto_heal": true,
      "health_grace": 60,
      "heal_cooldown": 300
    }
  ]
}
```

| Field | Description | Default |
|-------|-------------|---------|
| `registry.url` | Local registry address | `http://localhost:5000` |
| `registry.poll_interval` | Seconds between polls | `300` |
| `api.port` | API listen port (localhost only) | `9090` |
| `services[].name` | Service identifier | required |
| `services[].image` | Registry image reference | required if `auto_update` |
| `services[].compose_file` | Absolute path to docker-compose.yml | required |
| `services[].compose_project` | Docker Compose project name (used for `-p` flag and label matching) | required |
| `services[].auto_update` | Enable registry polling for this service | `false` |
| `services[].auto_heal` | Enable auto-restart on unhealthy | `false` |
| `services[].health_grace` | Seconds to wait after deploy before health check | `60` |
| `services[].heal_cooldown` | Minimum seconds between auto-restarts | `300` |

### Notifications

Discord: set `webhook_url` to a Discord channel webhook URL.

SMTP: set `host`, `port`, `from`, `to`, and optionally `username`/`password`.

Custom webhooks: define a `name`, `url`, `method`, `headers`, and `body` (Go template with `.Service`, `.Event`, `.Message`, `.Reason`, `.Level` fields). Headers support `$ENV_VAR` expansion.

## API

All endpoints listen on `127.0.0.1:<port>` only.

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/trigger` | Trigger poll for all services |
| `POST` | `/trigger/<name>` | Trigger poll for a specific service |
| `GET` | `/blocked` | List blocked service digests |
| `DELETE` | `/blocked/<name>` | Unblock a service (allows next deploy attempt) |
| `GET` | `/health` | Health check (`{"status":"ok"}`) |
| `GET` | `/metrics` | Prometheus text format metrics |

## Metrics

Exposed at `/metrics` in Prometheus text exposition format:

- `watcher_updates_total{service}` - successful image updates
- `watcher_rollbacks_total{service}` - rollbacks after failed updates
- `watcher_restarts_total{service}` - auto-heal restarts
- `watcher_failures_total{service}` - critical failures
- `watcher_service_healthy{service}` - 1 if healthy, 0 if not
- `watcher_service_blocked{service}` - 1 if digest blocked after rollback, 0 if not
- `watcher_poll_count_total` - total poll cycles
- `watcher_last_poll_timestamp_seconds` - unix timestamp of last poll
- `watcher_uptime_seconds` - seconds since start

## Event Flow

Update cycle:
1. Poll registry for each service with `auto_update: true`
2. Skip if digest is blocked (previous rollback) or deploy is in progress
3. Compare remote digest with local digest
4. If changed: tag current as `:rollback`, `docker compose -p <project> pull`, `docker compose -p <project> up -d`
5. Poll container health every 5s until `health_grace` deadline
6. If healthy: success. If unhealthy: rollback immediately (fail fast). If still starting at deadline: rollback.
7. On rollback: block the bad digest, retag `:rollback` as `:latest`, redeploy
8. Blocked digest auto-clears when a new digest appears in the registry (fix pushed). Manual unblock via `DELETE /blocked/<name>`.

Heal cycle:
1. Listen for Docker `health_status` and `die` events
2. Match events to services via `com.docker.compose.project` label
3. On unhealthy: check cooldown, restart container if `auto_heal` is true
4. Verify health 30 seconds after restart
5. If still unhealthy: send critical notification
6. On die (outside deploy): send critical notification

## Systemd

Dockward runs as `/etc/systemd/system/dockward.service`:

```sh
systemctl start dockward
systemctl status dockward
journalctl -u dockward -f
```

## License

[AGPL-3.0-or-later](LICENSE)

## Contact

[Studio Webux](https://studiowebux.com) | [Discord](https://discord.gg/BG5Erm9fNv) | tommy@studiowebux.com
