# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

[Unreleased]: https://github.com/studiowebux/dockward/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/studiowebux/dockward/releases/tag/v0.1.0
