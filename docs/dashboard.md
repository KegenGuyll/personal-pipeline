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

Each service row has a vertical **⋮ (overflow) menu** on the right. Sign in with
the admin token (see [Auth](#auth)) to reveal the actions:

- **Start** — `docker compose up -d --remove-orphans` (bring the service up,
  creating containers if it has never been run, or restarting stopped ones).
  This is the same bring-up command a webhook deploy uses.
- **Stop** — `docker compose stop` (gracefully stop the running containers,
  keeping them so **Start** can bring them back).
- **Restart** — `docker compose restart` (restart the running containers in
  place).
- **Delete** — `docker compose down` then remove `services/<name>/` (see the
  [API](#api) below).

The menu is hidden until an admin token is set, because every action mutates the
server. Without an admin token the actions are not shown at all (read-only
view), and even with the UI shown the agent enforces `ADMIN_TOKEN` on each
request.

## Running and stopping a service

Use the ⋮ menu on a service row to `start`, `stop`, or `restart` it. This is
handy for static infra services that a project webhook never deploys (for
example the `ntfy` service) — you can bring them up without a `git push`, or
stop them to take them offline temporarily.

Actions are applied with `docker compose` against the service's own compose
project, so they respect the project's `restart` policy, networks, and volumes.
The result is echoed in the dashboard banner and the service list is refreshed
after each action.

## Adding a service (manual, advanced)

The "Add service (manual — no GitHub workflow)" form is collapsed under the
**Advanced** toggle on the dashboard. It creates `services/<name>/docker-compose.yml`
from the same private (Tailscale) template as `services/_template/`, given a
name, image, port, and optional Tailscale hostname. This is the same "known
project" allowlist the webhook enforces, so the service becomes deployable on
the next `git push` (once its project repo has the `deploy.yml` snippet and
`SERVICE_ENV` secret — see [adding-a-service.md](adding-a-service.md)).

Use it for services that don't fit onboarding: third-party/prebuilt images,
manual webhook deploys, or repos the onboarding GitHub App can't reach. The
agent deliberately accepts only those fields — never raw compose — and
validates the image against `ALLOWED_IMAGE_PREFIXES` just like a webhook deploy.

## Onboarding a project

The "Onboard project" form (above "Add service") does the whole per-repo wiring
in one action, using a GitHub App (see [onboarding.md](onboarding.md)):

- lists every repository the app can see in a **searchable picker**
  (`GET /onboard/repos`) — type to filter, click or arrow-keys + Enter to pick,
  or type any `owner/repo` manually; selecting auto-fills the service name,
  Tailscale hostname, and default image (`ghcr.io/<owner>/<service>`),
- creates `services/<name>/docker-compose.yml` on the server (same template),
- sets the project repo's `SERVICE_ENV` secret (from the key-value Env rows —
  empty keys are skipped, and the rows are serialized to JSON for you),
- opens a **pull request** adding `.github/workflows/deploy.yml`.

The PR is never auto-merged — the result panel shows the PR link and an
"awaiting your review" notice; merge it to activate deploys. A warning is shown
if the repo has no Dockerfile at the given `context`/`dockerfile` path (the
first build will fail until one is added). Requires `GITHUB_APP_ID` +
`GITHUB_APP_PRIVATE_KEY_B64` on the server; otherwise the endpoints return 404.

## Auth

| Scope | Endpoints | Token |
|---|---|---|
| Read | `GET /services`, `GET /deployments`, `GET /deployments/{id}`, `GET /onboard/repos` | `READ_TOKEN` |
| Write | `POST /services`, `DELETE /services/{name}`, `POST /services/{name}/start`, `POST /services/{name}/stop`, `POST /services/{name}/restart`, `POST /onboard` | `ADMIN_TOKEN` |

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

# list repos the onboarding GitHub App can see (read)
curl -H "Authorization: Bearer $READ_TOKEN" https://deploy.<domain>/onboard/repos

# add a service (admin)
curl -X POST https://deploy.<domain>/services \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"name":"my-service","image":"ghcr.io/you/my-service","port":3000,"hostname":"my-service"}'

# delete a service (admin) — stops/removes containers, deletes services/<name>/
# on the server; volumes are kept unless ?purge=true (adds `-v` to compose down)
curl -X DELETE https://deploy.<domain>/services/my-service \
  -H "Authorization: Bearer $ADMIN_TOKEN"

# start / stop / restart a service (admin) — run the service's own compose
# project (up -d / stop / restart)

# start (up -d --remove-orphans — creates containers if never run)
curl -X POST https://deploy.<domain>/services/my-service/start \
  -H "Authorization: Bearer $ADMIN_TOKEN"

# stop (graceful stop, containers kept so Start can bring them back)
curl -X POST https://deploy.<domain>/services/my-service/stop \
  -H "Authorization: Bearer $ADMIN_TOKEN"

# restart (restart running containers in place)
curl -X POST https://deploy.<domain>/services/my-service/restart \
  -H "Authorization: Bearer $ADMIN_TOKEN"

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
