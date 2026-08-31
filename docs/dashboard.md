# Dashboard

A small web dashboard, served by the deploy agent itself, for viewing deployed
services and adding new ones to the deployment allowlist. No separate service,
no build step — the UI is a static page embedded in the Go binary.

Open `https://deploy.<domain>/ui` (or hit `/`, which redirects there).

## What it shows

For every service under `services/` (anything with a `docker-compose.yml`,
except `_template`):

- **Service** — the directory / compose project name.
- **Version** — the `TAG` currently in the service's `.env` (what `compose` will
  run), or "never deployed" before its first deploy.
- **Image** — the image ref from the compose file.
- **Access** — `private` (Tailscale sidecar) or `public`.
- **Last deploy** — the most recent deployment's status and time.

## Adding a service

The "Add service" form creates `services/<name>/docker-compose.yml` from the
same private (Tailscale) template as `services/_template/`, given a name,
image, port, and optional Tailscale hostname. This is the same "known project"
allowlist the webhook enforces, so the service becomes deployable on the next
`git push` (once its project repo has the `deploy.yml` snippet and `SERVICE_ENV`
secret — see [adding-a-service.md](adding-a-service.md)).

The agent deliberately accepts only those fields — never raw compose — and
validates the image against `ALLOWED_IMAGE_PREFIXES` just like a webhook deploy.

## Onboarding a project

The "Onboard project" form (above "Add service") does the whole per-repo wiring
in one action, using a GitHub App (see [onboarding.md](onboarding.md)):

- creates `services/<name>/docker-compose.yml` on the server (same template),
- sets the project repo's `SERVICE_ENV` secret (from the Env JSON field),
- opens a **pull request** adding `.github/workflows/deploy.yml`.

The PR is never auto-merged — the result panel shows the PR link and an
"awaiting your review" notice; merge it to activate deploys. A warning is shown
if the repo has no Dockerfile at the given `context`/`dockerfile` path (the
first build will fail until one is added). Requires `GITHUB_APP_ID` +
`GITHUB_APP_PRIVATE_KEY_B64` on the server; otherwise the endpoint returns 404.

## Auth

| Scope | Endpoints | Token |
|---|---|---|
| Read | `GET /services`, `GET /deployments`, `GET /deployments/{id}` | `READ_TOKEN` |
| Write | `POST /services`, `POST /onboard` | `ADMIN_TOKEN` |

- Both tokens are sent as `Authorization: Bearer <token>` and stored by the
  dashboard in your browser's `localStorage` (entered once via **Settings**).
- Unset `READ_TOKEN` → the read endpoints (and the service list) return `404`.
- Unset `ADMIN_TOKEN` → `POST /services` returns `404` (adding disabled).
- Bad token → `401`.

`READ_TOKEN` and `ADMIN_TOKEN` are independent: you can share a read-only token
without granting the ability to add services.

## Security note

The dashboard itself is static and reveals nothing without a token, but it is
served on the same origin as the public `deploy.<domain>` webhook. Prefer
reaching it over Tailscale or otherwise restricting `/ui` at your reverse proxy
if you do not want it reachable publicly. All data and write endpoints are
token-gated regardless.

## API

```sh
# list services
curl -H "Authorization: Bearer $READ_TOKEN" https://deploy.<domain>/services

# add a service (admin)
curl -X POST https://deploy.<domain>/services \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"my-service","image":"ghcr.io/you/my-service","port":3000,"hostname":"my-service"}'

# onboard a project repo (admin; requires the onboarding GitHub App)
curl -X POST https://deploy.<domain>/onboard \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "repo": "you/my-service",
    "env": {"API_KEY": "..."},
    "overwrite_workflow": false
  }'
# -> 201 {"repo":"you/my-service","service":"my-service","image":"ghcr.io/you/my-service",
#        "base_branch":"main","secret":"set","compose":"created",
#        "pr":{"number":1,"url":"https://github.com/you/my-service/pull/1",
#              "branch":"pipeline/onboard-my-service","state":"open"}}
# -> 409 if deploy.yml already exists (re-run with overwrite_workflow:true)
```
