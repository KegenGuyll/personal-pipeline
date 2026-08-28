# Architecture

## Flow

```
┌──────────────┐   push    ┌───────────────────────────────────────────────┐
│ project repo │ ────────▶ │ GitHub Actions (reusable workflow)           │
└──────────────┘           │  checkout → buildx → login GHCR → push image │
                           │  → sign payload → POST /hooks/deploy         │
                           └──────────────┬────────────────────────────────┘
                                          │ HTTPS (HMAC-SHA256)
                                          ▼
                        ┌─────────────────────────────────────────┐
                        │ home server                             │
                        │  nginx:443 ──▶ deploy agent (Docker)    │
                        │                  ├─ notify "started"    │
                        │                  ├─ pre-deploy hook     │
                        │                  ├─ write services/<n>/.env
                        │                  ├─ compose pull && up  │
                        │                  ├─ post-deploy hook    │
                        │                  ├─ notify success/fail │
                        │                  └─ append history      │
                        │  GET /deployments (Bearer) ──▶ JSONL    │
                        └─────────────────────────────────────────┘
```

## Components

### 1. Reusable workflow (`.github/workflows/deploy-service.yml`)

Runs on GitHub's hosted runners. Builds the image (Buildx, optional multi-arch),
pushes to GHCR, then notifies the agent. It embeds the service's env (from the
`SERVICE_ENV` secret) into the signed payload. It never prints the payload —
the merged JSON contains secrets and is not masked by GitHub.

### 2. Deploy agent (`deploy-agent/`)

A small Go binary running in a container on the server. It:

- verifies every webhook with HMAC-SHA256 over the raw body,
- enforces an image allowlist (`ALLOWED_IMAGE_PREFIXES`) and a known-project
  allowlist (a `services/<project>/docker-compose.yml` must exist),
- writes `TAG` + the service's `env` into `services/<project>/.env` (atomic, 0600),
- runs `docker compose pull && up -d` against the host Docker socket,
- runs pre/post hooks, sends notifications, and appends history.

It deliberately accepts no compose content over the webhook — only an image
tag + env for a versioned service.

### 3. Server state

- `stack/` — the bootstrap compose for the agent + nginx, and `stack/.env`
  (secrets that must exist before any webhook can arrive: `WEBHOOK_SECRET`,
  GHCR credentials, `READ_TOKEN`, notification URLs).
- `services/<name>/` — one compose project per app, deployed independently.
- `nginx/conf.d/` — vhost templates for the deploy webhook (apps use Tailscale
  or NPM for public ones).

### 4. Networks

All containers join an external `proxy` network. nginx reaches the deploy agent
by its compose service name. Private apps publish a loopback-only port and are
fronted by Tailscale Serve; public apps publish a port for NPM.

### 5. App access (Tailscale)

Services with personal data (e.g. `personal-finance`) are **not** exposed
publicly. They bind `127.0.0.1:<port>` and are reached only via
`https://<machine>.<tailnet>.ts.net` (Tailscale Serve + MagicDNS). See
[tailscale.md](tailscale.md). The deploy webhook stays public — it serves no
personal content.

## Why not a self-hosted runner?

A hosted-runner + webhook split keeps the build in GitHub's cloud (no arbitrary
workflow code executing on your server) while still letting the server do the
final pull/run. The only inbound surface is the single authenticated webhook
endpoint.
