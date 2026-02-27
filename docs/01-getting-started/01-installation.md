---
title: Installation
description: Build dockward from source, install the binary, and configure the systemd service.
tags:
  - installation
  - build
  - systemd
  - go
---

# Installation

Dockward ships as a single static binary with no external runtime dependencies. It must be built from source and installed on the target host.

## Requirements

- Go 1.24+ (build host only — not required on the target)
- Docker daemon with unix socket at `/var/run/docker.sock`
- `docker compose` CLI available on the target host (required for full mode only)

## Build

Cross-compile for `linux/amd64` from any host:

```sh
git clone https://github.com/studiowebux/dockward.git
cd dockward
GOOS=linux GOARCH=amd64 go build \
  -ldflags "-X main.version=$(git describe --tags --always)" \
  -o dockward-linux-amd64 \
  ./cmd/dockward/
```

## Install on Host

```sh
sudo cp dockward-linux-amd64 /usr/local/bin/dockward
sudo chmod +x /usr/local/bin/dockward
sudo mkdir -p /etc/dockward
sudo cp config.sample.json /etc/dockward/config.json
sudo cp dockward.service /etc/systemd/system/dockward.service
sudo systemctl daemon-reload
sudo systemctl enable --now dockward
```

Edit `/etc/dockward/config.json` before starting the service. See [Configuration](02-configuration.md) for a walkthrough and [Config Reference](../02-reference/01-config.md) for all fields.

## Verify

```sh
dockward -version
systemctl status dockward
journalctl -u dockward -f
```

## Systemd Unit

The provided `dockward.service` unit runs dockward as root (required for Docker socket access), restarts on failure with a 10-second delay, and logs to the systemd journal.

```ini
[Unit]
Description=Dockward - Docker deploy guard
After=docker.service
Requires=docker.service

[Service]
ExecStart=/usr/local/bin/dockward -config /etc/dockward/config.json
Restart=always
RestartSec=10
User=root
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
```

Config path is fixed at `/etc/dockward/config.json` by the unit. To use a different path, edit `ExecStart` and run `systemctl daemon-reload`.

:::note
Dockward talks to the Docker daemon via the unix socket `/var/run/docker.sock`. Running as root is the simplest way to ensure socket access. If your setup grants socket access to a non-root user, adjust `User=` accordingly.
:::
