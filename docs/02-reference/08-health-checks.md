# Docker Daemon Health Checks

Dockward continuously monitors Docker daemon connectivity to ensure reliable operations and provide visibility when Docker becomes unavailable.

## Overview

The Docker health checker:

1. **Pings the Docker daemon** - Uses the `/_ping` endpoint for lightweight connectivity checks
2. **Tracks health status** - Maintains connection state and failure counts
3. **Exposes metrics** - Reports health status via Prometheus metrics
4. **Updates health endpoint** - Reflects Docker status in `/health` API response

## Configuration

Configure health check intervals in your `config.json`:

```json
{
  "docker_health": {
    "check_interval": 30,
    "timeout": 5
  }
}
```

### Parameters

| Parameter | Type | Default | Description |
|-----------|------|---------|-------------|
| `check_interval` | int | 30 | Seconds between health checks (min: 5, max: 3600) |
| `timeout` | int | 5 | Timeout for each ping request (min: 1, max: 30) |

**Validation:** `timeout` must be less than `check_interval`

## Health Check Behavior

### Startup

- First check runs immediately on startup
- Subsequent checks run at configured intervals
- Initial state is **unhealthy** until first successful check

### Success

When a ping succeeds:
- Health status set to `healthy`
- Consecutive failure count reset to 0
- Docker version and API version recorded
- Metrics updated

### Failure

When a ping fails:
- Health status set to `unhealthy`
- Consecutive failure count incremented
- Error message recorded
- Metrics updated

### Common Failure Scenarios

1. **Docker daemon not running**
   ```
   Error: dial unix /var/run/docker.sock: connect: no such file or directory
   ```

2. **Permission denied**
   ```
   Error: dial unix /var/run/docker.sock: connect: permission denied
   ```

3. **Timeout**
   ```
   Error: context deadline exceeded
   ```

## Health Endpoint Integration

The `/health` endpoint returns Docker daemon status:

### Healthy Response (200 OK)

```json
{
  "status": "healthy",
  "components": {
    "http": {
      "healthy": true
    },
    "docker": {
      "healthy": true,
      "last_check": "2026-03-03T10:30:00Z",
      "last_healthy_check": "2026-03-03T10:30:00Z",
      "consecutive_fails": 0,
      "docker_version": "Docker/24.0.7 (linux)",
      "api_version": "1.45"
    }
  }
}
```

### Unhealthy Response (503 Service Unavailable)

```json
{
  "status": "unhealthy",
  "components": {
    "http": {
      "healthy": true
    },
    "docker": {
      "healthy": false,
      "last_check": "2026-03-03T10:30:15Z",
      "last_healthy_check": "2026-03-03T10:29:45Z",
      "consecutive_fails": 3,
      "last_error": "dial unix /var/run/docker.sock: connect: no such file or directory"
    }
  }
}
```

## Prometheus Metrics

The health checker exposes three metrics via `/metrics`:

### `docker_daemon_healthy`

**Type:** Gauge
**Values:** 1 (healthy) or 0 (unhealthy)

Indicates current Docker daemon connectivity status.

```
docker_daemon_healthy 1
```

### `docker_daemon_consecutive_failures`

**Type:** Gauge
**Values:** 0-N

Number of consecutive failed health checks. Resets to 0 on successful check.

```
docker_daemon_consecutive_failures 0
```

### `docker_daemon_checks_total`

**Type:** Counter
**Values:** Cumulative count

Total number of health checks performed since startup.

```
docker_daemon_checks_total 120
```

## Monitoring and Alerting

### Prometheus Alert Rules

Example alert when Docker becomes unavailable:

```yaml
groups:
  - name: dockward
    rules:
      - alert: DockerDaemonDown
        expr: docker_daemon_healthy == 0
        for: 1m
        labels:
          severity: critical
        annotations:
          summary: "Docker daemon is unavailable"
          description: "Docker daemon health check has failed for 1 minute ({{ $value }} consecutive failures)"

      - alert: DockerDaemonFlapping
        expr: rate(docker_daemon_checks_total[5m]) - rate(docker_daemon_checks_total[5m] offset 5m) > 0.5
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "Docker daemon is flapping"
          description: "Docker daemon health is unstable"
```

### Monitoring Health Endpoint

Use `/health` for external monitoring (Kubernetes, load balancers):

```bash
# Simple check
curl http://localhost:9090/health

# Check exit code
curl -f http://localhost:9090/health || echo "Unhealthy"

# Parse JSON status
curl -s http://localhost:9090/health | jq -r '.status'
```

## Operational Notes

### When Docker is Unavailable

While Docker is unhealthy:
- **Updater:** Cannot list containers, inspect images, or check for updates
- **Healer:** Cannot restart containers or receive health events
- **Monitor:** Cannot collect container resource stats
- **API:** Continues serving HTTP (health endpoint returns 503)

**Recommendation:** Monitor `docker_daemon_healthy` and alert operators when Docker is down.

### Recovery

When Docker recovers:
- Health status automatically updates on next successful check
- All components resume normal operation
- No manual intervention required

### Systemd Health Check

Integrate with systemd watchdog:

```ini
[Service]
WatchdogSec=60
ExecStartPost=/bin/sh -c 'while ! curl -sf http://localhost:9090/health > /dev/null; do sleep 1; done'
```

### Docker Socket Permissions

Ensure dockward has access to Docker socket:

```bash
# Add user to docker group
sudo usermod -aG docker dockward

# Or adjust socket permissions (not recommended)
sudo chmod 666 /var/run/docker.sock
```

## Troubleshooting

### Health Checks Always Fail

**Symptoms:** `docker_daemon_healthy` always 0, `consecutive_fails` increasing

**Causes:**
1. Docker daemon not running
   ```bash
   sudo systemctl status docker
   sudo systemctl start docker
   ```

2. Socket path mismatch (default: `/var/run/docker.sock`)
   ```bash
   ls -l /var/run/docker.sock
   ```

3. Permission denied
   ```bash
   groups $(whoami)  # Should include 'docker'
   ```

### High Check Interval

**Symptoms:** Long delay before detecting Docker failure

**Solution:** Reduce `check_interval` in config:

```json
{
  "docker_health": {
    "check_interval": 10,
    "timeout": 3
  }
}
```

**Trade-off:** More frequent checks increase CPU overhead (minimal impact)

### Timeouts

**Symptoms:** Health checks timeout even though Docker is running

**Causes:**
- Docker daemon overloaded
- Slow disk I/O
- System resource exhaustion

**Solution:** Increase timeout or investigate Docker performance:

```bash
# Check Docker daemon logs
journalctl -u docker -n 100

# Monitor system resources
top
iostat -x 1
```

## Best Practices

1. **Set appropriate intervals**
   - Production: 30s check interval, 5s timeout
   - Development: 10s check interval, 3s timeout

2. **Monitor the metrics**
   - Alert on `docker_daemon_healthy == 0` for >1 minute
   - Track `consecutive_failures` for flapping detection

3. **Use /health for readiness**
   - Return 503 when Docker is unavailable
   - Prevents routing traffic to unhealthy instances

4. **Log review**
   - Check logs when health degrades
   - Look for permission or connectivity issues

5. **Test recovery**
   - Stop Docker daemon: `sudo systemctl stop docker`
   - Verify health status changes to unhealthy
   - Start Docker: `sudo systemctl start docker`
   - Verify automatic recovery
