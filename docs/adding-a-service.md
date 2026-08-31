# Adding a service

> **Fastest path:** if you have the onboarding GitHub App wired, use the
> dashboard's **Onboard project** form — it creates the service compose file,
> sets the repo's `SERVICE_ENV` secret, and opens a review PR adding
> `deploy.yml`, all without touching the project repo by hand
> (see [onboarding.md](onboarding.md)). The manual recipe below is the
> equivalent, step by step.

Four steps. Use a unique lowercase name (`my-service`) throughout.

> **Alternative:** the [dashboard](dashboard.md) can create the service compose
> file for you (its "Add service" form). That replaces step 1 below; you still
> need steps 2–4 (the project repo's `deploy.yml` + `SERVICE_ENV` secret, then a
> push).

## 1. Service compose file (in this repo)

```sh
cp -R services/_template services/my-service
```

Edit `services/my-service/docker-compose.yml`:

1. Rename the `app` service key to `my-service`, and update the `Proxy` URL in
   the `ts-serve` config at the bottom to `http://my-service:<port>`.
2. Set `image: ghcr.io/YOUR_OWNER/my-service:${TAG}` (owner lowercase).
3. Set the container port in `expose` (default `3000`).
4. Set the Tailscale `hostname` — this is the MagicDNS name
   `https://<hostname>.<tailnet>.ts.net`.

The template already includes a per-app **Tailscale sidecar**, so the service is
private by default (no public route). See [tailscale.md](tailscale.md). To make
it public instead, remove the `tailscale` service + `ts-serve` config and publish
a port for NPM.

Optional: drop a `services/my-service/hooks/pre-deploy` and/or `post-deploy`
script (see [hooks-and-notifications.md](hooks-and-notifications.md)).

Commit and push this repo.

## 2. Expose the service

- **Private (default):** nothing else — the sidecar serves
  `https://<hostname>.<tailnet>.ts.net` to your tailnet. Set `TS_AUTHKEY` in
  this service's `SERVICE_ENV` (see [tailscale.md](tailscale.md)).
- **Public (optional):** remove the sidecar + `ts-serve` config, publish a port,
  and add an NPM **Proxy Host** (Domain `my-service.<domain>`, Scheme `http`,
  Forward `<server LAN IP>:<port>`).

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
{"TS_AUTHKEY":"tskey-auth-...","API_KEY":"...","DATABASE_URL":"..."}
```

## 4. First deploy

Push to `main`. The workflow builds, pushes to GHCR, and notifies the agent,
which writes `services/my-service/.env` (with `TAG` + your env) and runs
`compose pull/up`. Check:

```sh
docker compose -f stack/docker-compose.yml logs -f agent
# private (Tailscale):
curl -fsS https://<hostname>.<tailnet>.ts.net/
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
