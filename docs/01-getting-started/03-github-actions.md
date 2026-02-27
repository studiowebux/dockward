---
title: GitHub Actions
description: Set up a self-hosted GitHub Actions runner to build, push, and deploy via dockward on a single host.
tags:
  - github-actions
  - ci-cd
  - self-hosted-runner
  - deploy
---

# GitHub Actions

The recommended CI/CD setup uses a self-hosted GitHub Actions runner installed on the same host as dockward. The runner has direct access to the local registry at `localhost:5000` and the dockward API at `localhost:9090`.

## One-Time Host Setup

Install the runner via the GitHub UI: repository Settings → Actions → Runners → New self-hosted runner. Follow the generated instructions to register and start the runner.

Grant the runner user Docker access:

```sh
sudo usermod -aG docker github-runner
```

Place compose files on the host and ensure dockward's config references them:

```sh
mkdir -p /srv/myapp
cp docker-compose.yml /srv/myapp/
```

Add the service entry to `/etc/dockward/config.json` and restart dockward:

```sh
sudo systemctl restart dockward
```

Run the initial deploy once manually — dockward cannot deploy a container that has never existed:

```sh
docker compose -p myapp -f /srv/myapp/docker-compose.yml up -d
```

## Workflow

Add this workflow to your application repository at `.github/workflows/deploy.yml`:

```yaml
name: Deploy

on:
  push:
    branches: [main]

jobs:
  deploy:
    runs-on: self-hosted
    timeout-minutes: 15
    steps:
      - uses: actions/checkout@v4

      - name: Build image
        run: docker build -t localhost:5000/myapp:latest .

      - name: Push to local registry
        run: docker push localhost:5000/myapp:latest

      - name: Trigger dockward
        run: curl -sf -X POST localhost:9090/trigger/myapp
```

The `curl` call triggers an immediate poll, bypassing the configured `poll_interval`. Dockward handles the pull, `compose up -d`, health grace period, and rollback if needed. The workflow has no further work to do after the push.

:::tip
`curl -sf` suppresses progress output and fails with a non-zero exit code on HTTP errors, which causes the workflow step to fail if the trigger endpoint is unreachable.
:::

## First Deploy Handling

On the first push, dockward will detect no local image to compare against and suppress the deploy. The `docker compose up -d` run during host setup covers this case. For fully automated first deploys, add a conditional step before the trigger:

```yaml
      - name: Initial deploy if container absent
        run: |
          if ! docker ps -a --format '{{.Names}}' | grep -qx myapp; then
            docker compose -p myapp -f /srv/myapp/docker-compose.yml up -d
          fi

      - name: Trigger dockward
        run: curl -sf -X POST localhost:9090/trigger/myapp
```

## Secrets

Store any values the workflow needs (registry credentials, tokens) in the repository's GitHub Actions secrets. Reference them as `${{ secrets.MY_SECRET }}` in the workflow. Never hardcode credentials in workflow files.

:::warning
The local registry at `localhost:5000` is accessible only from the host. The self-hosted runner must be on the same host as dockward and the registry for this setup to work.
:::
