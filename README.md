# personal-pipeline

A self-hosted, GitHub-connected deployment pipeline for a home Docker server.

`git push` to a project repo → GitHub Actions builds and pushes an image to
GitHub Container Registry (GHCR) → a signed webhook tells a small deploy agent
on your server to pull and run it → apps are served privately via Tailscale
(`https://<service>.<tailnet>.ts.net`) or publicly via nginx.

```
[project repo] --push--> GitHub Actions --build/push--> GHCR
                                  |
                                  | HTTPS POST /hooks/deploy (HMAC-SHA256)
                                  v
[home server]  nginx --> deploy agent --> docker compose pull && up -d --> app
                     (deploy webhook stays public; apps served via Tailscale)
```

## Features

- **One reusable workflow** for every project — each repo adds a ~15-line `deploy.yml`.
- **GitHub-App project onboarding** — onboard a new project from the dashboard
  without touching the repo by hand: it creates the service compose file, sets
  the `SERVICE_ENV` secret, and opens a review PR adding `deploy.yml`
  (see [docs/onboarding.md](docs/onboarding.md)).
- **Webhook deploy agent** (Go, single binary) with HMAC verification, image/project
  allowlists, and per-project concurrency control.
- **Secrets from GitHub** — each service's env is stored as a `SERVICE_ENV` GitHub
  secret and delivered inside the signed webhook; the agent writes it to
  `services/<name>/.env` atomically (0600).
- **Pre/post deploy hooks** (`services/<name>/hooks/pre-deploy`, `post-deploy`) and
  **generic webhook notifications** (Discord/Slack/ntfy/anything via a template).
- **Tailscale access** for apps holding personal data — loopback ports + Tailscale
  Serve, no public exposure.
- **Deployment history** — append-only JSONL + a bearer-authenticated `GET /deployments`
  read API + live structured `docker logs`.
- **Web dashboard** — a small UI served by the agent at `/ui` to view every service
  and the version it's running, and to add services to the deployment allowlist
  from the browser (see [docs/dashboard.md](docs/dashboard.md)).

## Quickstart

1. **Server setup** — see [docs/setup.md](docs/setup.md): clone this repo, create the
   `proxy` network, fill `stack/.env`, `docker compose up -d --build`.
2. **Add a service** — see [docs/adding-a-service.md](docs/adding-a-service.md), or
   [docs/onboarding.md](docs/onboarding.md) to onboard a project repo from the dashboard
   with a GitHub App (compose file + `SERVICE_ENV` secret + review PR, no manual repo edits).
3. **Wire GitHub** — create `DEPLOY_WEBHOOK_URL` / `DEPLOY_WEBHOOK_SECRET` secrets
   (org/user level), then add the `deploy.yml` snippet from
   [docs/adding-a-service.md](docs/adding-a-service.md) to each project repo.

## Repo layout

```
.github/workflows/   reusable deploy-service workflow + agent image build
deploy-agent/        the Go deploy agent (HTTP API, hooks, notify, history, dashboard)
stack/               docker-compose for the agent + nginx, and .env.example
services/            per-service compose files (copy from _template)
nginx/conf.d/        reverse-proxy vhost templates (deploy webhook)
docs/                setup, architecture, adding-a-service, hooks, logs, security, tailscale
```

## Docs

- [Setup](docs/setup.md) — one-time server + GitHub bootstrap
- [Adding a service](docs/adding-a-service.md) — the 4-part recipe
- [Onboarding](docs/onboarding.md) — onboard a project repo via GitHub App (no manual repo edits)
- [Hooks & notifications](docs/hooks-and-notifications.md) — pre/post hooks + Discord/Slack/ntfy
- [Deployment logs](docs/deployment-logs.md) — history API + log schema
- [Dashboard](docs/dashboard.md) — view services/versions + add services
- [dsh (DeepSeek Harness)](docs/dsh.md) — self-hosted coding-agent web UI, accessed from phone + computer over Tailscale
- [Tailscale access](docs/tailscale.md) — private access for personal-data apps
- [Security](docs/security.md) — threat model
- [Architecture](docs/architecture.md) — how it fits together

## Development

```sh
cd deploy-agent
go test ./...
go build .
```
