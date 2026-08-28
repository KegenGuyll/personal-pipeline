# Tailscale access (per-app sidecars)

Each service gets its own `tailscale` **sidecar** container that registers as its
own device, giving it its own MagicDNS name and private HTTPS:

```
laptop/phone ── Tailscale ──> https://<hostname>.<tailnet>.ts.net
                                 │  MagicDNS → tailnet IP
                                 ▼
                     tailscale sidecar :443 ──> http://app:3000 (over the proxy network)
```

No host port, no public route. The deploy webhook (`deploy.guyll.live`) stays
public and unchanged — only the apps go private.

## Prerequisites

- A Tailscale account + tailnet.
- **MagicDNS** and **HTTPS Certificates** enabled: admin console → tailnet → DNS.
- Docker can mount `/dev/net/tun` and grant `net_admin` (normal on Linux hosts).

## 1. Create an auth key

Admin console → **Settings → Keys → Generate auth key**:

- **Reusable:** on
- **Ephemeral:** off (so the node identity persists)
- **Tags:** optional, e.g. `tag:container`

One reusable key can power every sidecar. Put it in each service's
`SERVICE_ENV` GitHub secret:

```json
{"TS_AUTHKEY":"tskey-auth-...", "MONGODB_URI":"...", "...":"..."}
```

Optionally add `TS_HOSTNAME` to override the MagicDNS name (defaults to what's
set in the compose file, e.g. `finance`).

## 2. Deploy

The compose template (`services/_template/docker-compose.yml`) already contains
the `app` + `tailscale` sidecar + `ts-serve` config. On the next deploy the
agent brings up both. The sidecar:

- registers as `<hostname>.<tailnet>.ts.net`,
- terminates HTTPS on tailnet `:443` using `TS_SERVE_CONFIG`,
- proxies to `http://<service>:<port>` over the shared `proxy` network.

## 3. Access

From any device on the tailnet:

```sh
curl -fsS https://finance.<tailnet>.ts.net/
```

## Serve config (what it does)

```json
{"TCP":{"443":{"HTTPS":true}},
 "Web":{"${TS_CERT_DOMAIN}:443":{"Handlers":{"/":{"Proxy":"http://app:3000"}}}},
 "AllowFunnel":{"${TS_CERT_DOMAIN}:443":false}}
```

- `TCP.443.HTTPS` — serve the tailnet's port 443 with HTTPS (Tailscale cert).
- `Web["<fqdn>:443"]` — proxy `/` to the app.
- `AllowFunnel: false` — Funnel (public exposure) stays off.

`${TS_CERT_DOMAIN}` is substituted by the container at runtime (the node's
MagicDNS FQDN).

## Networking notes

- The app keeps its own network namespace (on `proxy`), so it reaches the
  internet (e.g. MongoDB Atlas) and sibling containers normally — the sidecar
  does **not** take over the app's networking.
- `TS_AUTHKEY` / `TS_HOSTNAME` are read from the service's `.env` (which the
  deploy agent writes from `SERVICE_ENV`); they also land in the app container's
  env (harmless for a trusted app).
- The sidecar needs `net_admin` + `/dev/net/tun` (TUN mode). If your Docker host
  blocks that, fall back to `TS_USERSPACE=true` and a `--set-path`/port-based
  serve (see the single-node notes at the bottom).

## Multiple apps

Copy `services/_template` to `services/<name>` and set a unique service name,
image, port, and `hostname`. Each service is its own compose project, so its
`ts-state` volume and serve config are automatically namespaced. Reuse one
auth key or mint per-app keys.

## Remove the public route (once it works)

- NPM: delete the app's proxy host (keep `deploy.*`).
- Cloudflare: remove the tunnel public-hostname / proxied DNS for the app.
- Any port-forward that existed only for the app.

## Troubleshooting

```sh
docker compose -f services/<name>/docker-compose.yml logs -f tailscale
tailscale status          # from a device: see the new <hostname> node
```

- Sidecar `Auth`: check `TS_AUTHKEY` in the service's `SERVICE_ENV`.
- `402`/no HTTPS: confirm **HTTPS Certificates** is enabled in the tailnet.
- App not resolving: confirm the sidecar and app are on the same `proxy` network
  and the `Proxy` URL uses the right service name + port.

## Single-node alternative (not recommended for multiple root-path apps)

If you don't want per-app sidecars, one host node can serve multiple apps by
path or port:

```sh
tailscale serve --bg 443 --set-path /app1 http://127.0.0.1:3001
tailscale serve --bg 443 --set-path /app2 http://127.0.0.1:3002
# or distinct ports:  tailscale serve --bg 8443 http://127.0.0.1:3002
```

Path routing breaks root-path apps (Next.js), so sidecars are the default.
