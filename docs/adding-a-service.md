# Adding a service

Four steps. Use a unique lowercase name (`my-service`) throughout.

## 1. Service compose file (in this repo)

```sh
cp -R services/_template services/my-service
```

Edit `services/my-service/docker-compose.yml` and pick the **access mode**:

```yaml
services:
  my-service:                                  # unique name = network alias
    image: ghcr.io/YOUR_OWNER/my-service:${TAG}   # YOUR_OWNER in lowercase
    restart: unless-stopped
    env_file: .env
    networks: [proxy]
    ports:
      - "127.0.0.1:3001:3000"                  # private (Tailscale)
    # public (NPM/nginx)? use:  - "3001:3000"
```

- **Private (Tailscale)** — for anything with personal data: keep the loopback
  port and add a `tailscale serve` entry (see [tailscale.md](tailscale.md)).
  Reachable only on your tailnet.
- **Public** — publish a port NPM can reach and add a proxy host (step 2).

Optional: drop a `services/my-service/hooks/pre-deploy` and/or `post-deploy`
script (see [hooks-and-notifications.md](hooks-and-notifications.md)).

Commit and push this repo.

## 2. Expose the service

### Private (Tailscale) — recommended for personal data

On the server, front the loopback port with Tailscale Serve:

```sh
tailscale serve --bg 443 http://127.0.0.1:3001
```

Now `https://<machine>.<tailnet>.ts.net` serves the app to your tailnet devices.
See [tailscale.md](tailscale.md) for the full setup (MagicDNS, TLS certs, clients).

### Public (NPM/nginx)

Add an NPM **Proxy Host**: Domain `my-service.<domain>`, Scheme `http`, Forward
`<server LAN IP>:3001`. (Or use the hand-written vhost in
`nginx/conf.d/app.example.com.conf.example`.) No cert needed if Cloudflare
terminates TLS.

## 3. Project repo: `deploy.yml` + secrets

In the **project repo** (not this one), add `.github/workflows/deploy.yml`:

```yaml
name: Deploy
on:
  push:
    branches: [main]
permissions:
  contents: read
  packages: write
jobs:
  deploy:
    uses: <owner>/personal-pipeline/.github/workflows/deploy-service.yml@main
    with:
      service: my-service
    secrets:
      deploy_webhook_url: ${{ secrets.DEPLOY_WEBHOOK_URL }}
      deploy_webhook_secret: ${{ secrets.DEPLOY_WEBHOOK_SECRET }}
      service_env: ${{ secrets.SERVICE_ENV }}
```

In that repo, create a `SERVICE_ENV` secret holding the app's environment as a
JSON object:

```json
{"PORT":"8080","API_KEY":"...","DATABASE_URL":"..."}
```

## 4. First deploy

Push to `main`. The workflow builds, pushes to GHCR, and notifies the agent,
which writes `services/my-service/.env` (with `TAG` + your env) and runs
`compose pull/up`. Check:

```sh
docker compose -f stack/docker-compose.yml logs -f agent
# private (Tailscale):
curl -fsS https://<machine>.<tailnet>.ts.net/
# public (NPM):
# curl -fsS https://my-service.<domain>/
```

## Notes

- **`SERVICE_ENV` is the single source of truth** for the app's runtime env.
  Removing a key from it removes it from `services/my-service/.env` on the next
  deploy.
- **Project name vs image name** — the `service` input is the directory name on
  the server (`services/<service>/`) and the compose project name. The image
  defaults to `ghcr.io/<owner>/<service>`; override with the `image` input if
  they differ.
- **Custom Dockerfile/context** — pass `context: ./app` and
  `dockerfile: Dockerfile` (the `dockerfile` is relative to the context).
