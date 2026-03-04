# Dockward

Auto-deploy and auto-heal agent for self-hosted Docker containers.

## Why this project exists?

I got tired of SSHing into my server every time I pushed a new image. Pull, restart, watch the logs, roll back if something broke — all manual, all in the middle of the night. Dockward handles that loop automatically. It watches a local Docker registry, deploys through compose, health-checks the result, and rolls back if the container fails to come up healthy. No cloud account, no extra agents in your images, just a small binary running on the same box as Docker.

Bug tracker: https://github.com/studiowebux/dockward/issues<br>
Discord: https://discord.gg/BG5Erm9fNv

## Funding

[Buy Me a Coffee](https://buymeacoffee.com/studiowebux)<br>
[GitHub Sponsors](https://github.com/sponsors/studiowebux)<br>
[Patreon](https://patreon.com/studiowebux)

## Features

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

## Installation

### Binary

Download the latest release for your platform from [Releases](https://github.com/studiowebux/dockward/releases), then install:

```sh
sudo cp dockward-linux-amd64 /usr/local/bin/dockward
sudo chmod +x /usr/local/bin/dockward
sudo mkdir -p /etc/dockward
sudo cp config.sample.json /etc/dockward/config.json
sudo cp dockward.service /etc/systemd/system/dockward.service
sudo systemctl daemon-reload
sudo systemctl enable --now dockward
```

### From source

Requires Go 1.24+.

```sh
git clone https://github.com/studiowebux/dockward.git
cd dockward
GOOS=linux GOARCH=amd64 go build -ldflags "-X main.version=$(git describe --tags --always)" -o dockward-linux-amd64 ./cmd/dockward/
```

## Usage

```sh
dockward -config /etc/dockward/config.json
```

## Getting Started

### Heal-only mode

Watch and restart any container without a registry or compose setup:

```json
{
  "services": [
    {
      "name": "myapp",
      "container_name": "myapp",
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
  "monitor": {
    "stats_interval": 30
  },
  "api": {
    "address": ["127.0.0.1:9090"]
  },
  "services": [
    {
      "name": "myapp",
      "images": ["myapp:latest"],
      "compose_files": ["/srv/myapp/docker-compose.yml"],
      "compose_project": "myapp",
      "auto_update": true,
      "auto_heal": true,
      "health_grace": 60,
      "heal_cooldown": 300,
      "heal_max_restarts": 3
    }
  ]
}
```

## Documentation

https://studiowebux.github.io/dockward

## Contributions

Contributions are welcome via pull request.

1. Fork the repository
2. Create a branch: `git checkout -b feat/your-feature`
3. Commit your changes
4. Open a pull request against `main`

Open an issue before starting significant work.

## License

[AGPL-3.0-or-later](LICENSE)

## Contact

[Studio Webux](https://studiowebux.com)<br>
[Discord](https://discord.gg/BG5Erm9fNv)<br>
tommy@studiowebux.com
