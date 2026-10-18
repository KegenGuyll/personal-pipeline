# ntfy — self-hosted push notifications on the home server

Runs a self-hosted [ntfy](https://ntfy.sh) server so you get push notifications
on your phone at the private tailnet URL:

```
https://ntfy.<tailnet>.ts.net
```

It's the notification delivery endpoint for the **dsh-notify** plugin (pushes
when the DeepSeek Harness agent finishes a task or needs your input) and can
optionally receive deploy-agent webhook notifications. See
[hooks-and-notifications.md](hooks-and-notifications.md) for the deploy-agent
side.

Unlike the per-app project services, ntfy is **static infrastructure**: it uses
the off-the-shelf `binwiederhier/ntfy` image, so no project repo builds it and
no project webhook deploys it. It's brought up manually on the server.

## Service files

```
services/ntfy/docker-compose.yml   the ntfy app + Tailscale sidecar + ts-serve config
services/ntfy/.env.example         template for services/ntfy/.env (manual)
```

The compose is a standard per-app service (copied from `_template`): the
`ntfy-app` container and a `tailscale` sidecar on the shared `proxy` network.
The sidecar registers as `ntfy` in MagicDNS, terminates HTTPS on tailnet `:443`,
and proxies `/` to `http://ntfy-app:80`. No host port, no public route.

> The app service is deliberately named `ntfy-app` (not `ntfy`): the sidecar's
> `hostname: ntfy` is registered by Docker's embedded DNS, and naming the app
> `ntfy` would shadow it — `http://ntfy:80` would resolve to the sidecar itself
> and fail with 502.

## Prerequisites

- A Tailscale account + tailnet with **MagicDNS** and **HTTPS Certificates**
  enabled (see [tailscale.md](tailscale.md)).
- A Tailscale auth key (`tskey-auth-…`, Reusable on, Ephemeral off).
- On the iOS side, the **ntfy** app (or the ntfy web app) — HTTPS is required.

## Deploying (first time)

1. **Copy and edit the env file** on the server:

   ```sh
   git pull
   cd services/ntfy
   cp .env.example .env
   vi .env           # set TS_AUTHKEY; NTFY_BASE_URL=https://ntfy.<tailnet>.ts.net
   ```

   > `TS_AUTHKEY` is only required by the sidecar. `NTFY_BASE_URL` /
   > `NTFY_BEHIND_PROXY` are read by the ntfy app from its `env_file: .env`.

2. **Bring it up:**

   ```sh
   docker compose -f services/ntfy/docker-compose.yml up -d
   ```

3. **Verify** the sidecar registered and the app is reachable:

   ```sh
   docker compose -f services/ntfy/docker-compose.yml ps
   curl -fsS https://ntfy.<tailnet>.ts.net/
   ```

   From a tailnet device, `tailscale status` should show a new `ntfy` node.

> **iOS push requires `NTFY_UPSTREAM_BASE_URL=https://ntfy.sh`.** A self-hosted
> ntfy relays notifications to the phone through an upstream ntfy
> infrastructure. If it's unset, the iOS app stays subscribed but never receives
> an actual push (the topic shows the message in the app's list only when it's
> open). Set this in `services/ntfy/.env` and `up`/restart the ntfy container —
> it needs outbound internet to `ntfy.sh`.

## Using it from DSH

In the DSH web UI → **Settings → Plugins → Notify**:

- **Topic URL**: `https://ntfy.<tailnet>.ts.net/dsh` (the topic is the path).
- **Token**: leave blank — the topic is open.

Then tap **Send test** and confirm the push lands on your phone (the ntfy app
must be subscribed to the `dsh` topic, added by URL
`https://ntfy.<tailnet>.ts.net/dsh`).

## Deploy-agent notifications (optional)

ntfy can also receive the pipeline's deploy notifications. In `stack/.env`:

```bash
NOTIFY_WEBHOOK_URLS=https://ntfy.<tailnet>.ts.net/pipeline
NOTIFY_CONTENT_TYPE=text/plain
NOTIFY_TEMPLATE='Deploy {{.Status}} for {{.Project}} @ {{.Tag}}'
```

## Topic access model

Default ntfy has **no access control** — the topic is open, so the security
boundary is tailnet membership only. Neither DSH's outbound publish nor the
sidecar exposes a public route, so this is consistent with the rest of the
pipeline. To lock it down later, configure ntfy's access control and give DSH a
bearer token (`NTFY_TOKEN`).

## Troubleshooting

- **Sidecar `Auth` / no HTTPS**: confirm `TS_AUTHKEY` in `services/ntfy/.env`
  and that HTTPS Certificates is enabled. See [tailscale.md](tailscale.md).
- **ntfy links/redirects point at the wrong host**: set `NTFY_BASE_URL` to the
  public `https://ntfy.<tailnet>.ts.net` (not `http://ntfy:80`).
- **"unable to open database file"**: ensure the `ntfy-data` volume is mounted
  at `/var/lib/ntfy` (the compose already does this).
- **No push from a long-running task**: the dsh-notify `suppressWhenVisible`
  default quietens "done" pushes while a DSH page is in the foreground.
