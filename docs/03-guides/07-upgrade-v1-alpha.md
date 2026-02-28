---
title: Upgrade to v1.0.0-alpha.1
description: Step-by-step production rollout from v0.5.0 to v1.0.0-alpha.1.
tags:
  - upgrade
  - production
  - deployment
---

# Upgrade to v1.0.0-alpha.1

Upgrades the running dockward agent from v0.5.0. No config changes required — all new features activate with an existing config.

---

## 1. Build the binary on your dev machine

```sh
cd /path/to/dockward
git checkout feat/v1          # or main after merge
GOOS=linux GOARCH=amd64 go build -ldflags "-X main.version=v1.0.0-alpha.1" \
  -o dockward-linux-amd64 ./cmd/dockward/
```

For ARM64 (OVH Ampere):

```sh
GOOS=linux GOARCH=arm64 go build -ldflags "-X main.version=v1.0.0-alpha.1" \
  -o dockward-linux-arm64 ./cmd/dockward/
```

---

## 2. Upload to production server

```sh
scp dockward-linux-amd64 user@your-server:/tmp/dockward-new
```

---

## 3. On the production server: swap binary

```sh
# Verify the new binary runs
/tmp/dockward-new --version   # should print v1.0.0-alpha.1

# Back up the running binary before touching anything
sudo cp /usr/local/bin/dockward /usr/local/bin/dockward.bak

# Stop the running agent
sudo systemctl stop dockward

# Replace binary
sudo cp /tmp/dockward-new /usr/local/bin/dockward
sudo chmod +x /usr/local/bin/dockward

# Start the agent
sudo systemctl start dockward
sudo systemctl status dockward   # confirm active (running)
```

---

## 4. Verify new endpoints

Replace `9090` with your configured `api.port`.

```sh
# Audit log — returns [] if audit.path is not set, JSON array otherwise
curl -s localhost:9090/audit | head -c 200

# Limit to last 5 entries
curl -s "localhost:9090/audit?limit=5"

# Health still works
curl -s localhost:9090/health
```

Open the web UI in a browser:

```
http://localhost:9090/ui
```

The page no longer auto-refreshes every 10 seconds. Events stream live via SSE. If your browser shows a live connection indicator (spinning favicon) the SSE is active. New events appear at the top of the events table without a page reload.

---

## 5. (Optional) Verify SSE stream directly

```sh
curl -N http://localhost:9090/ui/events
```

Each new audit entry appears as a `data:` line. Trigger one:

```sh
curl -sf -X POST localhost:9090/trigger
```

---

## 6. (Optional) Deploy the warden systemd unit

Only if you run a warden instance on this or another machine.

```sh
# Copy the unit file
sudo cp dockward-warden.service /etc/systemd/system/

# Ensure warden config exists
sudo ls /etc/dockward/warden.json

# Enable and start
sudo systemctl daemon-reload
sudo systemctl enable dockward-warden
sudo systemctl start dockward-warden
sudo systemctl status dockward-warden
```

---

## 7. Smoke test checklist

Run through these after the upgrade. All should pass before you consider the rollout stable.

```sh
# Agent running
sudo systemctl is-active dockward

# Health
curl -sf localhost:9090/health

# Audit endpoint exists and returns JSON
curl -sf localhost:9090/audit | python3 -m json.tool > /dev/null && echo OK

# Status endpoint unchanged
curl -sf localhost:9090/status | python3 -m json.tool > /dev/null && echo OK

# Metrics endpoint unchanged
curl -sf localhost:9090/metrics | grep -c dockward_

# Web UI loads without errors
curl -sf localhost:9090/ui | grep -q 'EventSource' && echo "SSE present"
```

---

## 8. Rollback

If anything is wrong:

```sh
sudo systemctl stop dockward
sudo cp /usr/local/bin/dockward.bak /usr/local/bin/dockward  # if you kept a backup
sudo systemctl start dockward
```

Or re-deploy the v0.5.0 binary. The config format is unchanged — rollback requires only a binary swap.

---

## Known limitations (alpha)

- The SSE events table in the agent web UI inserts rows via `innerHTML` without HTML escaping. Service names and messages are written by dockward itself (from config), so this is low risk for a localhost admin UI. Will be addressed before v1.0.0.
- The remaining gosec findings (G704 SSRF false positives in heartbeat/push, G117 SMTP password field, G104 unhandled Close errors) are pre-existing and do not affect this release.
- Monitor for 2 weeks. If clean, tag `v1.0.0` and remove the alpha designation.
