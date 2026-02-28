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

Dockward ships as a single static binary with no external runtime dependencies.

## Requirements

- Docker daemon with unix socket at `/var/run/docker.sock`
- `docker compose` CLI available on the target host (required for full mode only)

## Binary (Recommended)

Download the latest release for your platform from the [Releases page](https://github.com/studiowebux/dockward/releases/latest):

```sh
# amd64
curl -Lo dockward https://github.com/studiowebux/dockward/releases/latest/download/dockward-linux-amd64

# arm64
curl -Lo dockward https://github.com/studiowebux/dockward/releases/latest/download/dockward-linux-arm64
```

Install the binary and service unit:

```sh
sudo install -m 755 dockward /usr/local/bin/dockward
sudo mkdir -p /etc/dockward

# Download config sample and service unit from the repo
curl -Lo /etc/dockward/config.json \
  https://raw.githubusercontent.com/studiowebux/dockward/main/config.sample.json
sudo curl -Lo /etc/systemd/system/dockward.service \
  https://raw.githubusercontent.com/studiowebux/dockward/main/dockward.service

sudo systemctl daemon-reload
sudo systemctl enable --now dockward
```

Edit `/etc/dockward/config.json` before starting the service. See [Configuration](02-configuration.md) for a walkthrough and [Config Reference](../02-reference/01-config.md) for all fields.

## From Source

Requires Go 1.24+ on the build host.

Cross-compile for `linux/amd64`:

```sh
git clone https://github.com/studiowebux/dockward.git
cd dockward
GOOS=linux GOARCH=amd64 go build \
  -ldflags "-X main.version=$(git describe --tags --always)" \
  -o dockward-linux-amd64 \
  ./cmd/dockward/
```

Install on host:

```sh
sudo install -m 755 dockward-linux-amd64 /usr/local/bin/dockward
sudo mkdir -p /etc/dockward
sudo cp config.sample.json /etc/dockward/config.json
sudo cp dockward.service /etc/systemd/system/dockward.service
sudo systemctl daemon-reload
sudo systemctl enable --now dockward
```

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
