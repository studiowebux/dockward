# Dockward

Auto-deploy and auto-heal agent for self-hosted Docker containers — polls your local registry, deploys via compose when a new image lands, rolls back on health failure, and restarts unhealthy containers. Zero cloud dependencies.

Bug tracker: https://github.com/studiowebux/dockward/issues
Discord: https://discord.gg/BG5Erm9fNv

Funding: [Buy Me a Coffee](https://buymeacoffee.com/studiowebux) | [GitHub Sponsors](https://github.com/sponsors/studiowebux) | [Patreon](https://patreon.com/studiowebux)

## About

Single static Go binary (~12 MB), zero external dependencies. Talks to the Docker daemon via unix socket and a local registry via its HTTP API.

**What it does:**

- **Registry polling** — compares local vs remote image digest on a configurable interval; deploys automatically when a new image is pushed
- **Safe deploy pipeline** — tags the current image as `:rollback` before pulling, then runs `docker compose pull` + `docker compose up -d`
- **Health-gated rollback** — monitors container health during a configurable grace period; rolls back immediately on unhealthy status and blocks the bad digest to prevent loops
- **Auto-start** — detects when a compose project has the right image but no running containers and starts it
- **Compose drift detection** — hashes compose file contents each cycle; reapplies with `compose up` (no pull) when the spec changes
- **Auto-heal** — listens to Docker events (`health_status`, `die`), restarts failed containers with configurable cooldown and per-service restart limits
- **Resource monitoring** — collects CPU and memory usage per container; alerts when thresholds are exceeded
- **Web UI** — live dashboard at `/ui` with service status, deployed image name, digest, container uptime, CPU/memory, event stream (SSE), and trigger/unblock actions
- **Audit log** — structured history of all deploy, rollback, heal, and error events; queryable via `GET /audit`
- **Prometheus metrics** — counters and gauges at `GET /metrics` for updates, rollbacks, restarts, failures, and health state
- **REST API** — `POST /trigger`, `GET /status`, `GET /blocked`, `GET /not-found`, `GET /errored`, `GET /health`
- **Notifications** — Discord webhook, SMTP email, and custom HTTP webhooks for deploy, rollback, heal, and alert events
- **Central warden** — optional aggregator that collects status from multiple agents via HTTP push and exposes a unified SSE stream and web UI
- **Heal-only mode** — healthcheck monitoring and auto-restart for any container; no registry or compose files required
- **systemd-ready** — ships with a `dockward.service` unit file

Two operational modes:

1. **Full mode**: registry polling + auto-deploy with rollback + auto-heal. Requires a local Docker registry and docker compose.
2. **Heal-only mode**: healthcheck monitoring + auto-restart. Works with any container (compose-managed or standalone `docker run`). No registry or compose files needed.

## Installation

Download the latest binary from [Releases](https://github.com/studiowebux/dockward/releases) or build from source (requires Go 1.24+):

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

Full installation guide: [docs/01-getting-started/01-installation.md](docs/01-getting-started/01-installation.md)

## Usage

```sh
dockward -config /etc/dockward/config.json
dockward -config /etc/dockward/config.json --verbose
```

## Getting Started

### Heal-only mode (standalone container)

```json
{
  "services": [
    {
      "name": "standalone-api",
      "container_name": "standalone-api",
      "auto_heal": true,
      "heal_cooldown": 120,
      "heal_max_restarts": 5
    }
  ]
}
```

### Full mode (registry + compose)

```json
{
  "registry": {
    "url": "http://localhost:5000",
    "poll_interval": 300
  },
  "services": [
    {
      "name": "myapp",
      "image": "myapp:latest",
      "compose_files": [
        "/srv/myapp/docker-compose.yml"
      ],
      "compose_project": "myapp",
      "auto_update": true,
      "auto_heal": true,
      "health_grace": 60,
      "heal_cooldown": 300
    }
  ]
}
```

Full documentation: [docs/](docs/)

## Contributions

https://github.com/studiowebux/dockward

## License

[AGPL-3.0-or-later](LICENSE)

## Contact

[Studio Webux](https://studiowebux.com) | [Discord](https://discord.gg/BG5Erm9fNv) | tommy@studiowebux.com
