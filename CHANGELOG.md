# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[Unreleased]: https://github.com/studiowebux/dockward/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/studiowebux/dockward/compare/v0.2.2...v0.3.0
[0.2.2]: https://github.com/studiowebux/dockward/compare/v0.2.1...v0.2.2
[0.2.1]: https://github.com/studiowebux/dockward/compare/v0.2.0...v0.2.1
[0.2.0]: https://github.com/studiowebux/dockward/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/studiowebux/dockward/releases/tag/v0.1.0
