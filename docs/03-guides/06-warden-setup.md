---
title: Warden Setup
description: Deploy a central warden to aggregate audit events from multiple dockward agents and view them on a multi-machine dashboard.
tags:
  - warden
  - setup
  - multi-machine
---

# Warden Setup

Deploy one warden instance to aggregate audit events from multiple agents.

## Prerequisites

- Dockward v0.5.0+ installed on each agent host and the warden host
- Each agent already running in `agent` mode (default)
- A reverse proxy (nginx-proxy or equivalent) providing HTTPS on both sides

## 1. Generate tokens

Generate a unique bearer token for each agent and one for the warden API:

```bash
openssl rand -hex 32   # run once per agent, once for warden API
```

Store them in environment files or a secrets manager. Never hardcode them in
config files.

## 2. Configure each agent

Add a `push` block to each agent's `config.json`:

```json
"push": {
  "warden_url": "https://warden.example.com",
  "token": "$DOCKWARD_PUSH_TOKEN",
  "machine_id": "ovh-01"
}
```

Set `DOCKWARD_PUSH_TOKEN` to the token you generated for that agent. Each
agent should use its own token. Restart dockward on each agent host.

## 3. Create the warden config

Use the interactive wizard:

```bash
dockward warden-config --config /etc/dockward/warden.json
```

Or use `warden.sample.json` as a starting point:

```json
{
  "api": {
    "port": "8080",
    "token": "$DOCKWARD_WARDEN_TOKEN",
    "state_path": "/var/lib/dockward/warden-state.json"
  },
  "agents": [
    {
      "id": "ovh-01",
      "url": "http://ovh-01.internal:9090",
      "token": "$DOCKWARD_AGENT_TOKEN_OVH01"
    },
    {
      "id": "ovh-02",
      "url": "http://ovh-02.internal:9090",
      "token": "$DOCKWARD_AGENT_TOKEN_OVH02"
    }
  ]
}
```

- `agents[].url` is the agent's dockward API base URL (used for heartbeat polling)
- `agents[].token` must match the `push.token` configured on that agent
- `api.token` is the warden dashboard password
- `api.state_path` persists the event ring buffer to disk on shutdown and restores it on start; leave empty to disable

## 4. Start the warden

```bash
dockward --mode warden --config /etc/dockward/warden.json
```

As a systemd service, add a second unit or override `ExecStart`:

```ini
[Service]
ExecStart=/usr/local/bin/dockward --mode warden --config /etc/dockward/warden.json
```

## 5. Access the dashboard

Navigate to `https://warden.example.com/?token=<api.token>`. The token is
stored in a `HttpOnly` cookie after first login so subsequent page loads
require no token in the URL.

The SSE feed connects automatically and replays the last 50 events on load.
Use the machine and level filters to narrow the view.

## Troubleshooting

**Agent entries not appearing in warden**
Check agent logs for `push to warden failed`. Verify `warden_url` is reachable
from the agent host and the `token` matches `agents[].token` in warden config.

**Agent shows offline in warden**
Warden polls `GET <agents[].url>/health` every 30 seconds. Verify the URL is
reachable from the warden host. The agent API binds to `127.0.0.1` by default
— use an internal network address if the warden is on a different host.

**SSE stream disconnects**
nginx-proxy buffers SSE by default. Ensure `X-Accel-Buffering: no` passes
through your proxy config, or add `proxy_buffering off` for the warden location.
