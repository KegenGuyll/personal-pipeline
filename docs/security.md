# Security

## Threat model

The agent is an internet-reachable (via nginx) service that can command Docker
on your server. That is a privileged position, so its input surface is kept
minimal and authenticated.

## Controls

| Control | Detail |
|---|---|
| HMAC-SHA256 | Every `POST /hooks/deploy` is signed over the **raw body** (including `env`); verified constant-time. |
| HTTPS only | nginx terminates TLS; the agent binds to the internal `proxy` network and publishes no host port (by default). |
| Image allowlist | `ALLOWED_IMAGE_PREFIXES` (default `ghcr.io/`; tighten to `ghcr.io/<owner>/`). |
| Known-project allowlist | The webhook can only set tag + env for a project with an existing `services/<name>/docker-compose.yml`. |
| No compose over the wire | Compose definitions are repo-only; the webhook cannot create or alter them. |
| Env key validation | Keys must match `^[A-Za-z_][A-Za-z0-9_]*$`, blocking `.env` line injection. |
| Atomic 0600 writes | `services/<name>/.env` is written to a temp file then renamed, mode `0600`. |
| No secret logging | The `env` map is never logged/echoed; the CI notify step never prints the payload. |
| Read-only GHCR token | The server's pull token has `read:packages` only. |
| Bearer read API | `GET /deployments` requires `READ_TOKEN`; disabled if unset. |
| Bearer write API | `POST /services` (dashboard "add service") requires a separate `ADMIN_TOKEN`; disabled if unset. The agent only accepts name/image/port/hostname — never raw compose — and validates the image against the allowlist. |
| Onboarding GitHub App | `POST /onboard` (also `ADMIN_TOKEN`) uses a dedicated GitHub App (`contents:write`, `secrets:write`, `pull-requests:write`); disabled unless `GITHUB_APP_ID` + key are configured. It creates the compose file, sets `SERVICE_ENV`, and **opens a PR that is never auto-merged** — a human reviews the added workflow before it activates. |

## Secrets flow

- `DEPLOY_WEBHOOK_SECRET` (GitHub) == `WEBHOOK_SECRET` (server) — the HMAC key.
- `SERVICE_ENV` (GitHub) — the app's env, sent inside the signed webhook body,
  then written to `services/<name>/.env`. HMAC authenticates it and TLS
  encrypts it in transit; the file itself is plaintext because the app needs it.
- `GHCR_USER` / `GHCR_TOKEN` — read-only registry pull credentials, server only.
- `GITHUB_APP_PRIVATE_KEY` — the onboarding app's signing key, base64 in
  `stack/.env`. With it (plus the App ID) the agent can mint installation
  tokens, which are scoped to the app's permissions (`contents:write`,
  `secrets:write`, `pull-requests:write`) and expire hourly. It sits on the same
  server that mounts the Docker socket, so it is part of the "agent is already
  privileged" trust boundary; restrict the app's install scope to the repos you
  will onboard if you want to limit blast radius.

## Known tradeoffs

- **Docker socket mount.** The agent mounts `/var/run/docker.sock`, which
  effectively grants host-level control to the agent. Mitigations are the HMAC
  + allowlists above, plus a read-only socket mount. Hardening options (not in
  v1): rootless Docker, a socket proxy (`docker-socket-proxy`), or removing the
  socket in favor of a remote Docker context.
- **Secrets in transit.** `SERVICE_ENV` crosses the network and passes through
  GitHub Actions. HMAC + TLS cover authentication and confidentiality in
  transit. Optional future hardening: encrypt the `env` field end-to-end
  (e.g. `age`/`nacl-secretbox`) with a public key held on the server.
- **Hook scripts.** Hooks run in the agent container with the socket mounted;
  treat them as trusted code and never print secrets from them.
