# Graceful Shutdown

Dockward implements graceful shutdown to ensure in-flight deployments complete properly when the process is stopped.

## Overview

When dockward receives a termination signal (SIGTERM or SIGINT), it:

1. **Stops accepting new deployments** - New deployment requests are rejected
2. **Waits for active deployments** - Allows in-flight deployments to complete
3. **Shuts down components** - Gracefully stops all managers in parallel
4. **Flushes audit logs** - Ensures all log entries are persisted
5. **Closes connections** - Gracefully terminates HTTP and SSE connections

## Shutdown Timeout

Default timeout: **30 seconds**

If deployments don't complete within the timeout:
- Remaining operations are cancelled
- Resources are cleaned up
- Process exits with error log

## Shutdown Sequence

### 1. Signal Reception
```
SIGTERM/SIGINT → Shutdown Coordinator → Cancel Context
```

### 2. Component Shutdown (Parallel)

**Updater:**
- Waits for active deployments to complete
- Tracks stuck deployments (>5min warning)
- Cancels new deployment requests

**Healer:**
- Completes in-progress health checks
- 5-second grace period

**Monitor:**
- Stops resource monitoring
- Immediate shutdown

**API:**
- Closes HTTP server gracefully (10s timeout)
- Disconnects SSE clients
- Completes in-flight requests

**Audit Logger:**
- Flushes pending log entries
- Syncs file to disk (5s timeout)

### 3. Process Exit
- Clean exit code 0 on success
- Exit code 1 if shutdown fails

## Operational Notes

### Systemd Integration

Recommended systemd unit configuration:

```ini
[Service]
TimeoutStopSec=45
Restart=on-failure
RestartSec=10
```

The 45-second timeout provides:
- 30s for graceful shutdown
- 15s margin for cleanup

### Monitoring Shutdown

Watch logs for shutdown progress:

```bash
journalctl -u dockward -f | grep shutdown
```

Expected log messages:
```
[INFO] received SIGTERM, starting graceful shutdown
[INFO] [shutdown] starting graceful shutdown (timeout: 30s)
[INFO] [updater] waiting for 2 active deployment(s): [service1, service2]
[INFO] [updater] all deployments completed
[INFO] [api] shutting down HTTP server
[INFO] [audit] audit logs flushed successfully
[INFO] [shutdown] graceful shutdown completed
```

### Deployment Duration

Typical deployment times:
- Pull + Up: 10-30 seconds
- Health verification: 60-120 seconds (HealthGrace setting)

**Important:** Services with long HealthGrace periods may not complete within the 30-second shutdown timeout. Consider:
- Reducing HealthGrace for faster shutdown
- Accepting that some deployments may be cancelled during shutdown
- Allowing the deployment to retry on next startup

### Stuck Deployments

If a deployment runs >5 minutes, a warning is logged:
```
[updater] warning: deployments running >5min: [service-name]
```

These deployments will be cancelled at the 30-second shutdown timeout.

## Testing Graceful Shutdown

### Manual Test

1. Start a long-running deployment:
```bash
# Trigger update for service with long pull time
curl -X POST http://localhost:8080/trigger/myservice
```

2. Send termination signal:
```bash
sudo systemctl stop dockward
# OR
kill -TERM $(pgrep dockward)
```

3. Check logs:
```bash
journalctl -u dockward -n 50
```

Expected: Deployment completes before shutdown.

### Automated Test

The shutdown coordinator includes comprehensive unit tests:

```bash
go test ./internal/shutdown/... -v
```

Tests cover:
- Basic shutdown
- Timeout enforcement
- Operation tracking
- Multiple shutdown calls
- Manager errors

## Architecture

### Shutdown Coordinator

File: `internal/shutdown/coordinator.go`

Responsibilities:
- Manages shutdown lifecycle
- Tracks active operations
- Enforces timeout
- Coordinates component shutdown

### GracefulManager Interface

```go
type GracefulManager interface {
    Shutdown(ctx context.Context) error
}
```

All components implement this interface:
- Updater
- Healer
- Monitor
- API
- Audit Logger

### Operation Tracking

The coordinator provides operation tracking:

```go
// Start an operation
if !coordinator.OperationStarted() {
    // Shutdown in progress, reject operation
    return errors.New("shutting down")
}
defer coordinator.OperationCompleted()

// Perform operation...
```

## Troubleshooting

### Shutdown Timeout

**Problem:** Shutdown exceeds 30 seconds

**Causes:**
- Long-running deployment (large images, slow network)
- Stuck health check
- External service dependency

**Solutions:**
1. Check for stuck deployments in logs
2. Review HealthGrace settings
3. Investigate network/registry issues
4. Consider increasing timeout (future enhancement)

### Incomplete Deployments

**Problem:** Deployment interrupted during shutdown

**Expected behavior:** This is normal if deployment exceeds timeout

**Recovery:**
- Dockward will retry on next startup
- Check service health after restart
- Review audit logs for deployment status

### Resource Leaks

**Problem:** Goroutines or connections not cleaned up

**Diagnosis:**
```bash
# Check for orphaned processes
ps aux | grep dockward

# Check for open ports
sudo lsof -i :8080
```

**Expected:** No residual processes or open ports after shutdown

## Future Enhancements

Potential improvements:
1. **Configurable timeout** - Allow users to set custom shutdown timeout
2. **Priority shutdown** - Critical services shut down last
3. **Metrics export** - Publish shutdown metrics before exit
4. **Health check bypass** - Skip health verification during shutdown
5. **Progressive timeout** - Different timeouts per component

## See Also

- [Deployment Guide](../03-guides/02-deployment.md)
- [Configuration Reference](./01-configuration.md)
- [API Reference](./02-api.md)