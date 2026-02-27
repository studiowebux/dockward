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

When adding or editing a service, the wizard covers every field: name, image, compose files, compose project, container name, env file, and all behaviour flags (`auto_update`, `auto_start`, `auto_heal`, `health_grace`, `heal_cooldown`, `heal_max_restarts`).

## Notes

- Compose files are entered as comma-separated paths: `/srv/app/docker-compose.yml, /srv/app/docker-compose.prod.yml`
- Boolean fields prompt with `Y/n` (default true) or `y/N` (default false)
- The wizard does not validate the config — run `dockward --config <path>` to confirm it loads correctly before restarting the service
- To update a single field, run the wizard, navigate to that field, change it, then save
