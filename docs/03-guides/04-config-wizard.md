---
title: Config Wizard
description: Use the interactive dockward config wizard to create or update your config file without editing JSON by hand.
tags:
  - config
  - wizard
  - cli
---

# Config Wizard

`dockward config` is an interactive CLI that creates or edits the dockward config file. It walks through every section and shows current values as defaults — press Enter to keep a value unchanged.

## Usage

```sh
dockward config --config /etc/dockward/config.json
```

`--config` defaults to `/etc/dockward/config.json`. If the file exists it is loaded first. If it does not exist a new one is created.

## Session flow

```
Loaded existing config: /etc/dockward/config.json

[Registry]
  Registry URL [http://localhost:5000]:
  Poll interval in seconds [300]: 60

[API]
  Port [9090]:

[Notifications]
  Discord webhook URL (leave empty to disable) [https://discord.com/...]:
  Configure SMTP? (N):

[Services] (2 configured)
  [1] myapp  (auto_update, auto_heal)
  [2] proxy  (auto_heal)

  a) Add service
  e) Edit service
  r) Remove service
  s) Save and exit
  q) Quit without saving

Choice:
```

Each prompt shows the current value in brackets. Pressing Enter accepts it without retyping.

## Services menu

| Option | Action |
|--------|--------|
| `a` | Add a new service |
| `e` | Select and edit an existing service by number |
| `r` | Remove a service by number |
| `s` | Save the config file and exit |
| `q` | Quit without saving |

When adding or editing a service, the wizard covers every field: name, image, compose files, compose project, container name, env file, behaviour flags (`auto_update`, `auto_start`, `auto_heal`, `compose_watch`, `health_grace`, `heal_cooldown`, `heal_max_restarts`), and resource alert thresholds (`cpu_threshold`, `memory_threshold`).

## Warden config wizard

`dockward warden-config` is the equivalent wizard for the warden config file.

```sh
dockward warden-config --config /etc/dockward/warden.json
```

`--config` defaults to `/etc/dockward/warden.json`.

### Session flow

```
Creating new warden config: /etc/dockward/warden.json

[API]
  Listen port [8080]:
  Warden token ($ENV_VAR supported) []: $DOCKWARD_WARDEN_TOKEN
  State file path (persists ring buffer across restarts, leave empty to disable) []: /var/lib/dockward/warden-state.json

[Agents] (0 configured)

  a) Add agent
  s) Save and exit
  q) Quit without saving

Choice: a

[Agent]
  ID (display name) []: ovh-01
  URL (agent base URL for heartbeat, e.g. http://host:9090) []: http://ovh-01.internal:9090
  Token (must match agent push.token, $ENV_VAR supported) []: $DOCKWARD_AGENT_TOKEN_OVH01
```

### Agents menu

| Option | Action |
|--------|--------|
| `a` | Add a new agent |
| `e` | Select and edit an existing agent by number |
| `r` | Remove an agent by number |
| `s` | Save the config file and exit |
| `q` | Quit without saving |

## Notes

- Compose files are entered as comma-separated paths: `/srv/app/docker-compose.yml, /srv/app/docker-compose.prod.yml`
- Boolean fields prompt with `Y/n` (default true) or `y/N` (default false)
- Neither wizard validates the config — run the binary to confirm it loads correctly before restarting the service
- To update a single field, run the wizard, navigate to that field, change it, then save
