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

### 2b. Onboarding GitHub App (optional)

An outbound-only GitHub App client (`deploy-agent/github.go`) powers
`POST /onboard` (admin-gated, same as `POST /services`). Given
`owner/repo` + service/image/port/hostname/env, it:

1. writes `services/<name>/docker-compose.yml` (same fixed template),
2. sets the repo's `SERVICE_ENV` secret (encrypted with the repo's
   Actions-secrets public key, libsodium sealed box),
3. opens a **review PR** adding `.github/workflows/deploy.yml`
   (`pipeline/onboard-<service>` branch; the agent never merges).

No inbound webhook is needed — the agent calls GitHub's REST API with
installation tokens minted from the app's private key. Missing Dockerfile at
the configured `context`/`dockerfile` path is a warning, not a block.

### 3. Server state

- `stack/` — the bootstrap compose for the agent + nginx, and `stack/.env`
  (secrets that must exist before any webhook can arrive: `WEBHOOK_SECRET`,
  GHCR credentials, `READ_TOKEN`, notification URLs).
- `services/<name>/` — one compose project per app, deployed independently.
- `nginx/conf.d/` — vhost templates for the deploy webhook (apps use Tailscale
  or NPM for public ones).

### 4. Networks

All containers join an external `proxy` network. nginx reaches the deploy agent
by its compose service name. Each private app has a `tailscale` sidecar on the
same network that proxies tailnet HTTPS to the app; public apps publish a port
for NPM.

### 5. App access (Tailscale)

Services with personal data (e.g. `personal-finance`) are **not** exposed
publicly. Each service runs a per-app `tailscale` **sidecar** that registers as
its own device and serves `https://<hostname>.<tailnet>.ts.net`, proxying to the
app over the `proxy` network (no host port). See [tailscale.md](tailscale.md).
The deploy webhook stays public — it serves no personal content.

## Why not a self-hosted runner?

A hosted-runner + webhook split keeps the build in GitHub's cloud (no arbitrary
workflow code executing on your server) while still letting the server do the
final pull/run. The only inbound surface is the single authenticated webhook
endpoint.
