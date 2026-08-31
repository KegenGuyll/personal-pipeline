# dsh — DeepSeek Harness web UI on the home server

Runs [DeepSeek Harness](https://github.com/deepseek-ai/deepseek-harness) (DSH)
as an always-on web server so you can drive a coding agent from any device —
phone at work, computer at home — at the private tailnet URL:

```
https://dsh.<tailnet>.ts.net
```

No upstream fork: the image ([`dsh-server`](https://github.com/KegenGuyll/dsh-server))
is a thin, version-pinned wrapper around the published `@deepseek-ai/dsh` npm
package.

## Why sessions "sync" across devices

All DSH state lives **server-side** under `$DSH_HOME` (`/data` in the
container), in persistent volumes:

| Volume | Container path | Contents |
|---|---|---|
| `dsh-data` | `/data` | sessions (event-sourced JSONL), `settings.yaml`, `.credentials.yaml`, the auto-initialized `profiles/web`, `storages/` |
| `dsh-workspaces` | `/workspaces` | the agent's working directory (project checkouts) |

The web UI is a thin client, so phone and computer see the same session list,
settings, and files. Start a session on the phone → it's saved server-side →
open the same session from the computer later: same history, same `cwd`, the
agent keeps going. The code also lives on the server — from the phone you
direct the agent via chat; nothing moves between devices.

Two caveats:
- **One device per session at a time.** A session has a single writer; opening
  it in two browsers concurrently is unsupported. Parallel sessions are fine.
- **No auth layer.** DSH deliberately ships without TLS/auth; the tailnet is
  the access boundary, and a session is effectively remote code execution on
  the server. Keep `AllowFunnel` off and don't publish host ports.

## Why the compose file deviates from `_template`

`dsh web` **refuses `--host 0.0.0.0`** (upstream safety guard) and binds
`127.0.0.1` by design. So:

1. The `dsh` container uses `network_mode: service:tailscale` — it shares the
   sidecar's network namespace, making `127.0.0.1:3080` local to the sidecar
   (the `_template`'s `expose` + proxy-network hop could not reach a
   loopback-bound process in another container).
2. `ts-serve` proxies to `http://127.0.0.1:3080` on that shared loopback.

The app also runs with `--trusted-host dsh.<tailnet>.ts.net`
(`DSH_TRUSTED_HOST`): DSH's `/api` **browser-trust fence** accepts requests
whose Host is loopback *or* a declared trusted host, plus a same-origin check.
Behind the sidecar the Host header is the tailnet FQDN, so the FQDN must be
declared or every `/api` call 403s. Tailscale Serve preserves the Host header,
and `/api` uses fetch/SSE (no WebSockets), so it proxies cleanly.

## Deploying (first time)

1. **Image repo** (`dsh-server`): create the `SERVICE_ENV` secret (JSON) with
   `TS_AUTHKEY` (reuse the existing key), `DEEPSEEK_API_KEY`, and
   `DSH_TRUSTED_HOST` set to `dsh.<tailnet>.ts.net`; also ensure the
   `DEPLOY_WEBHOOK_URL` / `DEPLOY_WEBHOOK_SECRET` secrets exist.
2. **This repo**: commit `services/dsh/docker-compose.yml` and push; on the
   server `git pull` so the deploy agent sees the new service (compose
   definitions are repo-only by design).
3. **Push `dsh-server` to `main`** → the workflow builds
   `ghcr.io/kegenguyll/dsh:<sha>` (amd64 + arm64), notifies the agent, which
   writes `services/dsh/.env` and runs `compose pull && up -d`.
4. First boot auto-initializes the web profile under `/data/profiles/web`
   (shipped template) — nothing else to configure.

## Updating

Bump the pinned `@deepseek-ai/dsh` version in the `dsh-server` Dockerfile →
push. The pipeline rebuilds and redeploys; the volumes keep every session,
setting, and file. Session logs are forward-compatible (versioned headers +
read-compat path), and the web profile resolves bundles from the installed dsh
first, so an old profile boots against a new install. If a future release ever
requires a regenerated profile: `docker compose -f services/dsh/docker-compose.yml run --rm dsh rm -rf /data/profiles/web`
(it rebuilds from the shipped template; sessions/settings live elsewhere).

Rollback: every build leaves its `sha-…` tag in GHCR; put a previous
`TAG=sha-xxxxxxx` in `services/dsh/.env` and run
`docker compose -f services/dsh/docker-compose.yml up -d`.

## Checking it works

```sh
curl -fsS https://dsh.<tailnet>.ts.net/                      # 200 HTML
docker compose -f stack/docker-compose.yml logs -f agent     # deploy log
docker compose -f services/dsh/docker-compose.yml ps        # both healthy
```

Then the acceptance checklist from the plan: chat round-trip from a laptop
(model configured via the Models page if needed), start a session, open it from
the phone (Tailscale app + mobile browser), continue it from the computer,
`docker compose restart dsh` + a redeploy and confirm sessions are still there.

## Troubleshooting

- **Everything 403s on /api**: the browser URL must exactly equal
  `DSH_TRUSTED_HOST`; verify the Host header passes through the sidecar
  (`curl -v`). Never loosen the fence.
- **Volume permissions**: fresh named volumes inherit the image's `node`
  ownership; if a volume predates this image, run
  `docker compose -f services/dsh/docker-compose.yml run --rm dsh chown -R node:node /data /workspaces`.
- **Tailscale sidecar restarts**: compose restarts the netns-sharing app with
  it; if not, `docker compose -f services/dsh/docker-compose.yml restart dsh`.
- **Agent needs git push access**: configure git identity/credentials inside
  the container's session env, or mount an SSH key into `dsh-workspaces`.
- **Telemetry**: disabled via `DSH_TELEMETRY_DISABLED=1` in the image.

## Security

Tailnet-only: no funnel, no public nginx route, no host ports. DSH has no auth
layer — tailnet membership is the access boundary. Keys travel only inside the
signed webhook and land in `services/dsh/.env` (0600), same as every service.
If the tailnet ever grows beyond personal devices: Tailscale ACLs first, then
a Basic Auth hop in front (transparent to the trust fence since the Host
header is unchanged).
