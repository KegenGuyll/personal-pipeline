# Adding a service

Four steps. Use a unique lowercase name (`my-service`) throughout.

## 1. Service compose file (in this repo)

```sh
cp -R services/_template services/my-service
```

Edit `services/my-service/docker-compose.yml`:

```yaml
services:
  my-service:                                  # unique name = network alias
    image: ghcr.io/YOUR_OWNER/my-service:${TAG}   # YOUR_OWNER in lowercase
    restart: unless-stopped
    env_file: .env
    networks: [proxy]
    expose: ["8080"]
```

Optional: drop a `services/my-service/hooks/pre-deploy` and/or `post-deploy`
script (see [hooks-and-notifications.md](hooks-and-notifications.md)).

Commit and push this repo.

## 2. nginx vhost (in this repo)

```sh
cp nginx/conf.d/app.example.com.conf.example nginx/conf.d/my-service.<domain>.conf
```

Edit it: set `server_name my-service.<domain>` and
`proxy_pass http://my-service:8080`. Reload nginx:

```sh
docker compose -f stack/docker-compose.yml exec nginx nginx -s reload
# or, for host nginx: sudo nginx -s reload
```

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
curl -fsS https://my-service.<domain>/
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
