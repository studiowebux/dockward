# Dockward

Docker deploy guard: polls a local registry for image changes, auto-deploys via docker compose with rollback on failure, blocks bad digests, and auto-heals unhealthy containers.

Bug tracker: https://github.com/studiowebux/dockward/issues
Discord: https://discord.gg/BG5Erm9fNv

Funding: [Buy Me a Coffee](https://buymeacoffee.com/studiowebux) | [GitHub Sponsors](https://github.com/sponsors/studiowebux) | [Patreon](https://patreon.com/studiowebux)

## About

Single static Go binary (~12 MB), zero external dependencies. Talks to the Docker daemon and a local registry via their HTTP APIs (unix socket and REST).

Two operational modes:

1. **Full mode**: registry polling + auto-deploy with rollback + auto-heal. Requires a local Docker registry and docker compose.
2. **Heal-only mode**: healthcheck monitoring + auto-restart. Works with any container (compose-managed or standalone `docker run`). No registry or compose files needed.

Features:

- Registry polling: compares remote vs local image digests, pulls and redeploys on change
- Rollback: tags current image as `:rollback` before deploy, reverts if container is unhealthy or not running after grace period. Blocks the bad digest to prevent infinite rollback loops.
- Atomic deploys: concurrent deploy guard prevents poll/API race conditions
- Container matching: `com.docker.compose.project` label for compose containers, `container_name` for standalone containers
- Health polling: polls container health every 5s during grace period (fail fast on unhealthy, keep waiting on starting)
- Auto-heal: listens for Docker health events, restarts unhealthy containers with cooldown and max retry protection
- Notifications: Discord webhook, SMTP email, custom webhooks with Go `text/template` body
- Prometheus metrics: `/metrics` endpoint with update/rollback/restart/failure/blocked counters per service
- Trigger API: `POST /trigger` and `POST /trigger/<service>` for manual deploys
- Blocked digest API: `GET /blocked` and `DELETE /blocked/<service>` for managing blocked digests
- Systemd integration: runs as a system service, logs to journal

## Installation

Build from source (requires Go 1.24+):

```sh
git clone https://github.com/studiowebux/dockward.git
cd dockward
GOOS=linux GOARCH=amd64 go build -ldflags "-X main.version=$(git describe --tags --always)" -o dockward-linux-amd64 ./cmd/dockward/
```

Install on the target host:

```sh
sudo cp dockward-linux-amd64 /usr/local/bin/dockward
sudo chmod +x /usr/local/bin/dockward
sudo mkdir -p /etc/dockward
sudo cp config.sample.json /etc/dockward/config.json
sudo cp dockward.service /etc/systemd/system/dockward.service
sudo systemctl daemon-reload
sudo systemctl enable --now dockward
```

Verify:

```sh
dockward -version
systemctl status dockward
journalctl -u dockward -f
```

## Usage

```sh
dockward -config /etc/dockward/config.json
```

## Getting Started

### Heal-only mode (standalone container)

Monitor a standalone container by name and auto-restart on unhealthy:

```json
{
  "services": [
    {
      "name": "my-api",
      "container_name": "my-api",
      "auto_heal": true,
      "heal_cooldown": 120,
      "heal_max_restarts": 5
    }
  ]
}
```

No registry, compose file, or compose project required. Dockward matches Docker events by container name and restarts the container when it becomes unhealthy. After 5 consecutive failed restarts, it stops retrying and sends a critical notification.

### Full mode (registry + compose)

Poll a local registry for image updates, auto-deploy, and auto-heal:

```json
{
  "registry": {
    "url": "http://localhost:5000",
    "poll_interval": 300
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
      "compose_file": "/srv/myapp/docker-compose.yml",
      "compose_project": "myapp",
      "auto_update": true,
      "auto_heal": true,
      "health_grace": 60,
      "heal_cooldown": 300
    }
  ]
}
```

## Configuration

| Field | Description | Default |
|-------|-------------|---------|
| `registry.url` | Local registry address | `http://localhost:5000` |
| `registry.poll_interval` | Seconds between polls | `300` |
| `api.port` | API listen port (localhost only) | `9090` |
| `services[].name` | Service identifier | required |
| `services[].image` | Registry image reference | required if `auto_update` |
| `services[].compose_file` | Absolute path to docker-compose.yml | required if `auto_update` |
| `services[].compose_project` | Docker Compose project name | required if `auto_update` |
| `services[].container_name` | Container name for event matching (standalone containers) | optional |
| `services[].auto_update` | Enable registry polling for this service | `false` |
| `services[].auto_heal` | Enable auto-restart on unhealthy | `false` |
| `services[].health_grace` | Seconds to wait after deploy before health check | `60` |
| `services[].heal_cooldown` | Minimum seconds between auto-restarts | `300` |
| `services[].heal_max_restarts` | Max consecutive failed restarts before giving up | `3` |

When `auto_heal` is true, at least one of `compose_project` or `container_name` is required for event matching.

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

## Contributions

https://github.com/studiowebux/dockward

## License

[AGPL-3.0-or-later](LICENSE)

## Contact

[Studio Webux](https://studiowebux.com) | [Discord](https://discord.gg/BG5Erm9fNv) | tommy@studiowebux.com
