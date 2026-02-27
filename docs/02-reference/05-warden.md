# Warden Reference

The warden aggregates audit entries from multiple dockward agents, stores them
in a ring buffer, fans them out to SSE clients, and serves a multi-machine
dashboard.

## Mode flag

```
dockward --mode agent  --config /etc/dockward/config.json   # default
dockward --mode warden --config /etc/dockward/warden.json
```

## Warden config

`warden.sample.json` is the reference. Fields:

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `api.port` | string | no | HTTP listen port. Default: `8080` |
| `api.token` | string | yes | Bearer token for browser and SSE auth. `$ENV_VAR` expanded |
| `agents[].id` | string | yes | Display name shown in the UI |
| `agents[].url` | string | yes | Agent base URL for heartbeat polling (e.g. `http://host:9090`) |
| `agents[].token` | string | yes | Token agents use when POSTing to `/ingest`. `$ENV_VAR` expanded |

## HTTP endpoints

| Method | Path | Auth | Description |
|--------|------|------|-------------|
| `POST` | `/ingest` | Bearer token (agent token) | Receive audit entry from agent |
| `GET` | `/events` | `?token=` query param | SSE stream of all events |
| `GET` | `/` | Cookie or `?token=` query param | Dashboard UI |
| `GET` | `/health` | None | Returns 200 OK |

## Agent push config

To enable push from an agent, add to its `config.json`:

```json
"push": {
  "warden_url": "https://warden.example.com",
  "token": "$DOCKWARD_PUSH_TOKEN",
  "machine_id": "ovh-01"
}
```

`warden_url` empty disables push. Push is fire-and-forget: agent operation
is not affected by warden availability.

## Ring buffer

The warden stores the last 200 events in memory. On SSE connect, the browser
receives the last 50 events as replay. Events are not persisted to disk on
the warden side; each agent retains its own audit log.

## Heartbeat

The warden polls each agent's `GET /health` every 30 seconds. State
transitions (online → offline, offline → online) produce synthetic
`agent_online` / `agent_offline` audit entries which are stored in the ring
buffer and broadcast to SSE clients.

## Auth model

| Flow | Method |
|------|--------|
| Agent → Warden `/ingest` | `Authorization: Bearer <agents[].token>` |
| Browser → Warden `/events` | `?token=<api.token>` query param |
| Browser → Warden `GET /` | `?token=` query param or `token` cookie |
| Warden → Agent `/health` | None (health is public) |

TLS is handled by a reverse proxy (e.g. nginx-proxy with Let's Encrypt).
Dockward does not terminate TLS.
