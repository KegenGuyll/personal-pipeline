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

# start the stack — the agent always runs; the dockerized nginx only starts
# if you opt in with `--profile docker-nginx` (see step 4, Option B)
docker compose -f stack/docker-compose.yml up -d --build
```

Verify the agent is up:

```sh
docker compose -f stack/docker-compose.yml logs -f agent
# or
curl -fsS http://localhost:8080/healthz   # -> ok
```

## 4. TLS + external access

### Option A — you already run host nginx (recommended when you have one)

The agent publishes a loopback-only port (`127.0.0.1:8080`) and the dockerized
nginx service is opt-in, so nothing conflicts with your existing nginx.

1. Start the stack (agent only):

   ```sh
   docker compose -f stack/docker-compose.yml up -d --build
   ```

2. Add DNS records for `deploy.<domain>` and each `<service>.<domain>` you
   plan to serve (same host/IP your other records point at).

3. Add this vhost to your existing nginx (a new file in `sites-available/` or
   `conf.d/`), replacing `<domain>` and the cert paths:

   ```nginx
   server {
       listen 443 ssl;
       server_name deploy.<domain>;

       ssl_certificate     /etc/letsencrypt/live/<domain>/fullchain.pem;
       ssl_certificate_key /etc/letsencrypt/live/<domain>/privkey.pem;

       location / {
           proxy_pass http://127.0.0.1:8080;
           proxy_http_version 1.1;
           proxy_set_header Host $host;
           proxy_set_header X-Real-IP $remote_addr;
           proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
           proxy_set_header X-Forwarded-Proto $scheme;
           proxy_read_timeout 300s;   # compose pull+up can take a while
       }
   }
   ```

4. Make sure the certificate covers `deploy.<domain>` — extend your existing
   certbot cert:

   ```sh
   sudo certbot certonly --nginx -d <domain> -d deploy.<domain>
   ```

5. Validate and reload:

   ```sh
   sudo nginx -t && sudo systemctl reload nginx
   ```

**App services with host nginx:** each service publishes a loopback port
instead of `expose`, e.g. `ports: ["127.0.0.1:8080:8080"]` (unique host port
per service), and its vhost uses `proxy_pass http://127.0.0.1:<host-port>;`.
See [adding-a-service.md](adding-a-service.md).

### Option B — no host nginx: use the dockerized one

```sh
docker compose -f stack/docker-compose.yml --profile docker-nginx up -d --build
```

Mount your certificates into the `nginx-certs` volume and copy
`nginx/conf.d/deploy.example.com.conf.example` → `deploy.<domain>.conf` (the
`.conf.example` files are ignored by nginx until renamed). App services keep
`expose` and vhosts use the compose service name over the `proxy` network
(`proxy_pass http://my-service:8080;`).

## 5. DNS summary

Records your nginx will serve (details in step 4):

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
