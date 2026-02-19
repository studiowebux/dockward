# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[Unreleased]: https://github.com/studiowebux/dockward/compare/v0.2.0...HEAD
[0.2.0]: https://github.com/studiowebux/dockward/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/studiowebux/dockward/releases/tag/v0.1.0
