# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [1.0.0-alpha.2] - 2026-02-28

### Fixed
- Health status shows "unknown" on boot until a Docker event fires; healer now inspects all configured containers at startup to seed health gauges immediately
- CPU and memory always show "--" when no alert thresholds are configured; stats are now collected for all running containers on every poll cycle regardless of threshold config
- Monitor stats show "--" for the entire first poll interval after startup; monitor now polls immediately on start before entering the ticker loop
- Trigger and Unblock buttons caused an infinite browser spinner by triggering a page reload that reconnected the SSE stream; replaced form submissions with `fetch` calls (no reload, no reconnect)
- Table headers and "unknown" status were near-invisible in dark mode due to low-contrast color choices; fixed with CSS custom properties

### Added
- Dark/light theme toggle in the agent web UI; preference persisted to `localStorage`; respects `prefers-color-scheme` as default
- `--verbose` flag: noisy healer skip, cooldown, and deploy-guard log lines are suppressed by default and only emitted when `--verbose` is set
- Quick start guide (`docs/01-getting-started/00-quick-start.md`)
- Minimaldoc homepage nav entry

### Removed
- Stale `docs/03-guides/07-upgrade-v1-alpha.md` upgrade guide

## [1.0.0-alpha.1] - 2026-02-28

### Added
- `GET /audit` endpoint returning recent audit entries as JSON (`?limit=N`, default 100, max 500); returns empty array when audit is disabled
- Agent web UI SSE stream (`GET /ui/events`): live audit entries pushed to the browser via Server-Sent Events; replays the last 50 entries on connect
- Agent web UI: replaced `<meta http-equiv="refresh">` full-page reload with SSE live event feed and a 15-second `fetch`-based status table refresh
- Shared `internal/hub` package: SSE publish-subscribe hub extracted from `internal/warden`; imported by both watcher and warden
- `audit.Broadcaster` interface and `Logger.WithBroadcast` to fan out new entries to the local SSE hub without an import cycle
- `dockward-warden.service`: systemd unit for running dockward in warden mode
- `linux/arm64` binary added to release pipeline (OVH Ampere, Raspberry Pi)
- Watcher test coverage: `api_test.go`, `updater_test.go`, `healer_test.go`

## [0.5.0] - 2026-02-27

### Added
- Central warden mode: aggregates audit entries from multiple dockward agents via HTTP push
- `--mode agent|warden` flag; agent mode is the default (backward compatible)
- Agent push config block (`push.warden_url`, `push.token`, `push.machine_id`): when `warden_url` is set, every audit entry is forwarded to the warden asynchronously
- `internal/push` package: HTTP client that POSTs audit entries to warden `/ingest`
- `audit.Pusher` interface and `Logger.WithPush` to decouple push client from audit package (avoids circular import)
- Warden HTTP server with four endpoints: `POST /ingest`, `GET /events` (SSE), `GET /` (dashboard), `GET /health`
- SSE hub: fan-out broadcaster; replays last 50 events on new connection
- In-memory ring buffer (200 events) with per-agent connectivity state
- Heartbeat poller: polls each agent `GET /health` every 30 s; emits `agent_online` / `agent_offline` synthetic entries on state transitions
- Multi-machine dashboard: per-agent status cards, real-time SSE event feed, machine and level filters (vanilla JS, no dependencies)
- `warden.sample.json`: sample warden config
- `docs/02-reference/05-warden.md`: warden reference
- `docs/03-guides/06-warden-setup.md`: warden setup guide

## [0.4.0] - 2026-02-27

### Added
- Audit log: structured JSON Lines file (`audit.path` config field) recording deploy, rollback, heal, and resource alert events
- `GET /audit` API endpoint returning the last 100 audit entries as JSON
- Compose file watcher: re-deploys a service when its compose file content changes without pulling a new image (`compose_watch: true`)
- Resource alerts: configurable CPU and memory thresholds per service (`cpu_threshold`, `memory_threshold`); sends notifications when exceeded
- Web UI dashboard: served at `GET /` on the API port; shows service status, last event, and audit log; auto-refreshes every 30 s
- Interactive config wizard: `dockward config [--config <path>]` subcommand to create or edit config files interactively
- `GET /status` endpoint: unified status response with per-service health, deploy state, and resource metrics

### Fixed
- Audit entries now written on rollback failure paths (previously missing)

## [0.3.0] - 2026-02-26

### Added
- `compose_files` config field accepts an ordered list of compose files merged left to right (e.g. base + override). `compose_file` (singular) remains supported for backward compatibility and is promoted to `compose_files` at load time.

### Fixed
- Healer hallucination: repeated "recovered" alerts fired on every Docker health event during the cooldown window. The cooldown entry is now consumed on first use.
- Healer missing recovery alert: degraded state was not cleared when a healthy event arrived during an active deploy cycle, leaving the flag stuck. State is now always cleaned up on healthy, regardless of deploy status.
- Healer double notification: when the healer restarted a container, both `verifyAfterRestart` and the healthy event handler could send a recovery alert. `verifyAfterRestart` now skips the notification if the healthy event already handled it.

## [0.2.2] - 2026-02-20

### Fixed
- Updater poll spam: when local image digest cannot be resolved, the updater no longer calls deploy every poll cycle. A `notFound` suppression map tracks the remote digest at time of failure and silences retries until the registry digest changes.
- Healer noise during deploys: `handleHealthy` now checks `IsDeploying()` to suppress spurious "Container recovered and is healthy" notifications during legitimate deploys.

### Added
- `GET /not-found` API endpoint to list services with unresolvable local digests.

## [0.2.1] - 2026-02-20

### Fixed
- Updater redeploying every poll cycle due to `url.PathEscape` encoding `/` in image names, causing `InspectImage` to fail on Docker API lookups
- Removed `url.PathEscape` from image path parameters (`InspectImage`, `TagImage`, `RemoveImage`) to match Docker SDK behavior
- Added container-based fallback for local digest resolution: if image inspect by reference fails, resolves via running container's image ID
- Swallowed errors from `InspectImage` now logged for debuggability

## [0.2.0] - 2026-02-19

### Added
- Heal-only mode: monitor and auto-restart containers by name without compose or registry
- `container_name` service config field for matching standalone containers
- `heal_max_restarts` config field (default 3) to cap consecutive failed restart attempts
- Healer sends "gave up" notification after max restarts exceeded, resets on healthy recovery

### Changed
- `compose_file` and `compose_project` only required when `auto_update` is true
- `findServiceByEvent` matches both compose project label and container name

### Fixed
- Healer restart loop: previously restarted unhealthy containers indefinitely after each cooldown expiry

## [0.1.0] - 2026-02-17

### Added
- Registry polling with digest comparison (remote vs local)
- Auto-deploy via docker compose pull/up on image change
- Rollback on unhealthy or non-running container after grace period
- Blocked digest tracking to prevent infinite rollback loops
- Auto-clear blocked digest when new registry digest appears
- Atomic deploy guard preventing poll/API race conditions
- Label-based container matching via `com.docker.compose.project`
- Health polling every 5s during grace period (fail fast on unhealthy)
- Auto-heal: Docker event listener restarts unhealthy containers with cooldown
- Discord webhook notifications
- SMTP email notifications
- Custom webhook notifications with Go text/template body
- Prometheus metrics endpoint (`/metrics`)
- Trigger API: `POST /trigger` and `POST /trigger/<service>`
- Blocked digest API: `GET /blocked` and `DELETE /blocked/<service>`
- Health check endpoint (`GET /health`)
- Systemd service unit
- Version flag (`-version`) with build-time injection via ldflags

[Unreleased]: https://github.com/studiowebux/dockward/compare/v0.5.0...HEAD
[0.5.0]: https://github.com/studiowebux/dockward/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/studiowebux/dockward/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/studiowebux/dockward/compare/v0.2.2...v0.3.0
[0.2.2]: https://github.com/studiowebux/dockward/compare/v0.2.1...v0.2.2
[0.2.1]: https://github.com/studiowebux/dockward/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/studiowebux/dockward/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/studiowebux/dockward/releases/tag/v0.1.0
