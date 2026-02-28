---
title: Quick Start
description: Get dockward running in under five minutes — from binary to monitored containers.
tags:
  - quick-start
  - getting-started
  - install
---

# Quick Start

## Prerequisites

- Linux host (amd64 or arm64)
- Docker daemon running at `/var/run/docker.sock`
- `docker compose` CLI installed (required for full mode only)
- Root or socket access

## 1. Install the Binary

Download from the [latest release](https://github.com/studiowebux/dockward/releases/latest):

```sh
curl -Lo dockward https://github.com/studiowebux/dockward/releases/latest/download/dockward-linux-amd64
chmod +x dockward
sudo mv dockward /usr/local/bin/dockward
```

For arm64:

```sh
curl -Lo dockward https://github.com/studiowebux/dockward/releases/latest/download/dockward-linux-arm64
```

Verify:

```sh
dockward -version
```

## 2. Create the Config

Choose the mode that matches your setup.

**Heal-only** — restart unhealthy or crashed containers, no registry required:

```sh
sudo mkdir -p /etc/dockward
sudo tee /etc/dockward/config.json > /dev/null <<'EOF'
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
EOF
```

**Full mode** — poll a local registry, auto-deploy on image change, auto-heal:

```sh
sudo mkdir -p /etc/dockward
sudo tee /etc/dockward/config.json > /dev/null <<'EOF'
{
  "registry": {
    "url": "http://localhost:5000",
    "poll_interval": 300
  },
  "api": {
    "port": "9090"
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
EOF
```

:::note
Use the interactive wizard instead of editing the file by hand: `dockward config`
:::

## 3. Install and Start the Systemd Service

```sh
sudo curl -Lo /etc/systemd/system/dockward.service \
  https://raw.githubusercontent.com/studiowebux/dockward/main/dockward.service
sudo systemctl daemon-reload
sudo systemctl enable --now dockward
```

If you built from source, copy the unit file from the repo instead:

```sh
sudo cp dockward.service /etc/systemd/system/dockward.service
```

## 4. Verify

```sh
systemctl status dockward
journalctl -u dockward -f
curl -s http://127.0.0.1:9090/health
curl -s http://127.0.0.1:9090/status | jq
```

Open the web UI at `http://127.0.0.1:9090/ui`.

## Next Steps

- [Configuration](02-configuration.md) — all config fields with examples
- [Full Mode Guide](../03-guides/01-full-mode.md) — deploy and rollback mechanics
- [Heal-Only Mode Guide](../03-guides/02-heal-only-mode.md) — healer behavior and tuning
- [API Reference](../02-reference/02-api.md) — HTTP endpoints
