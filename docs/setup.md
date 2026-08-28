# Setup

One-time bootstrap for the server and GitHub.

## Prerequisites

- A home server with Docker + Docker Compose v2 and an externally reachable
  nginx (port-forward 80/443 or Cloudflare Tunnel) already working.
- A GitHub account (your `GHCR_OWNER` below).
- A domain with DNS you can add subdomains to (e.g. `deploy.example.com`).

## 1. Push this repo to GitHub

```sh
git init -b main            # already done if you cloned it here
git add .
git commit -m "initial pipeline"
git remote add origin git@github.com:<you>/personal-pipeline.git
git push -u origin main
```

## 2. Generate secrets

```sh
# webhook HMAC secret (shared GitHub <-> server)
openssl rand -hex 32
# read-API token (server only)
openssl rand -hex 16
```

Create a **read-only** GHCR token on GitHub:
- Classic PAT with `read:packages` scope, **or**
- a fine-grained token with read access to your packages.

## 3. Server bootstrap

```sh
# on the server, clone this repo
git clone git@github.com:<you>/personal-pipeline.git
cd personal-pipeline

# create the shared proxy network (once)
docker network create proxy

# configure secrets
cp stack/.env.example stack/.env
# edit stack/.env: WEBHOOK_SECRET, GHCR_OWNER, GHCR_USER, GHCR_TOKEN, READ_TOKEN, ...

# start the agent + nginx (builds the agent image locally)
docker compose -f stack/docker-compose.yml up -d --build
```

Verify the agent is up:

```sh
docker compose -f stack/docker-compose.yml logs -f agent
# or
curl -fsS http://localhost:8080/healthz   # -> ok
```

## 4. TLS + external access

Two supported layouts:

### Option A — dockerized nginx (default)

The stack runs its own nginx on ports 80/443. Point your existing external
entry (port-forward or tunnel) at this nginx, and mount your certificates into
the `nginx-certs` volume (or bind-mount your `/etc/letsencrypt` tree). Copy
`nginx/conf.d/deploy.example.com.conf.example` to `deploy.<domain>.conf` and
edit the `server_name` + cert paths (the `.conf.example` files are ignored by
nginx, so they're safe to leave in place).

### Option B — keep your existing host nginx

Add a loopback port to the agent in `stack/docker-compose.yml`:

```yaml
    ports:
      - "127.0.0.1:8080:8080"
```

Don't run the dockerized `nginx` service. Instead, add the two vhosts from
`nginx/conf.d/` to your host nginx, changing `proxy_pass http://agent:8080`
to `proxy_pass http://127.0.0.1:8080`, and app vhosts to
`http://127.0.0.1:<port>` (app services then need `ports` instead of `expose`).

## 5. DNS

Create records for the domains your nginx will serve:

| Domain | Purpose |
|---|---|
| `deploy.<domain>` | the deploy agent (webhook + history API) |
| `<service>.<domain>` | one per app |

Ensure your TLS certificate covers each name (or use a wildcard cert).

## 6. GitHub secrets

Create these secrets (at the **organization** or **user** level so every repo
gets them automatically):

| Secret | Value |
|---|---|
| `DEPLOY_WEBHOOK_URL` | `https://deploy.<domain>/hooks/deploy` |
| `DEPLOY_WEBHOOK_SECRET` | the `WEBHOOK_SECRET` from step 2 |

## 7. Smoke-test the webhook

From any machine that can reach the agent:

```sh
SECRET="<WEBHOOK_SECRET>"
BODY='{"project":"demo","image":"ghcr.io/you/demo","tag":"sha-0000000","repo":"you/demo","sha":"0000000","env":{}}'
SIG="sha256=$(printf '%s' "$BODY" | openssl dgst -sha256 -hmac "$SECRET" | awk '{print $2}')"
curl -i https://deploy.<domain>/hooks/deploy \
  -H "Content-Type: application/json" \
  -H "X-Hub-Signature-256: $SIG" \
  -d "$BODY"
```

A `404 unknown project` response means auth + routing work (there is no `demo`
service yet). A `401 invalid signature` means the secret doesn't match.

## 8. Updating the infra

Infra changes (agent, stack, nginx) are published to GHCR automatically by
`.github/workflows/deploy-infra.yml`. To apply them on the server:

```sh
git pull
docker compose -f stack/docker-compose.yml up -d --build
```

## Next

- [Add your first service](adding-a-service.md)
- [Configure hooks & notifications](hooks-and-notifications.md)
