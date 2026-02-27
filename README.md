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

Full installation guide: [docs/01-getting-started/01-installation.md](docs/01-getting-started/01-installation.md)

## Usage

```sh
dockward -config /etc/dockward/config.json
```

## Getting Started

### Heal-only mode (standalone container)

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
      "auto_heal": true
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
