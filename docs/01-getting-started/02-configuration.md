---
title: Configuration
description: Config file structure and field walkthrough for dockward, covering both full mode and heal-only mode setups.
tags:
  - configuration
  - config
  - json
  - services
---

# Configuration

Dockward reads a single JSON config file at startup. The default path is `/etc/dockward/config.json`; override with `-config <path>`.

For the complete field reference including all defaults and validation rules, see [Config Reference](../02-reference/01-config.md).

## Minimal — Heal-Only Mode

Monitor a standalone container by name and restart it when unhealthy:

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

No registry, compose files, or compose project required. See [Heal-Only Mode Guide](../03-guides/02-heal-only-mode.md) for behavior details.

## Full Mode — Registry + Compose

Poll a local registry, auto-deploy on image change, and auto-heal:

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
    "port": "9090"
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
      "heal_cooldown": 300,
      "heal_max_restarts": 3
    }
  ]
}
```

See [Full Mode Guide](../03-guides/01-full-mode.md) for deploy and rollback behavior.

## Multiple Compose Files

Pass multiple files to `compose_files` to simulate `docker compose -f base.yml -f override.yml`:

```json
{
  "services": [
    {
      "name": "myapp",
      "image": "myapp:latest",
      "compose_files": [
        "/srv/myapp/docker-compose.yml",
        "/srv/myapp/docker-compose.override.yml"
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

:::note
The deprecated singular `compose_file` field is still accepted and is promoted to a single-element `compose_files` array at load time. Prefer `compose_files` in new configs.
:::

## Mixed Services

A single config can mix full-mode and heal-only services:

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
      "compose_files": ["/srv/myapp/docker-compose.yml"],
      "compose_project": "myapp",
      "auto_update": true,
      "auto_heal": true,
      "health_grace": 60,
      "heal_cooldown": 300
    },
    {
      "name": "proxy",
      "compose_files": ["/srv/proxy/docker-compose.yml"],
      "compose_project": "proxy",
      "auto_update": false,
      "auto_heal": true,
      "heal_cooldown": 300
    },
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

## Notifications

For notification configuration see [Notifications Reference](../02-reference/04-notifications.md).
