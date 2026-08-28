# Tailscale access

Route services that hold personal data (e.g. `personal-finance`) through
Tailscale so they're reachable only on your private tailnet — never the public
internet. The deploy webhook and nginx stay public; only the apps go private.

## Prerequisites

- A Tailscale account and a tailnet ([tailscale.com](https://tailscale.com)).
- Tailscale installed on the server and on every device you'll use to access
  the app (laptop, phone, etc.).

## 1. Install on the server

```sh
curl -fsSL https://tailscale.com/install.sh | sh
sudo tailscale up
```

Log in and pick your tailnet when prompted.

## 2. Enable MagicDNS + HTTPS certificates

Tailscale admin console → your tailnet → **DNS**:

- Enable **MagicDNS** (gives every device a `*.ts.net` name).
- Enable **HTTPS Certificates** (Tailscale provisions Let's Encrypt certs for
  `*.ts.net` names automatically).

Rename the server to something friendly (e.g. `finance`) so its MagicDNS name
is `finance.<tailnet>.ts.net`.

## 3. Serve the app

The app publishes a **loopback-only** port (`127.0.0.1:3001`). Front it with
Tailscale Serve so the tailnet reaches it over HTTPS:

```sh
sudo tailscale serve --bg 443 http://127.0.0.1:3001
```

Now `https://finance.<tailnet>.ts.net` serves the app (to tailnet devices only).

- One `:443` per machine — for multiple apps later, use distinct ports
  (`tailscale serve --bg 8443 http://127.0.0.1:3002`), path routing
  (`tailscale serve --set-path /app …`), or a tailnet-only reverse proxy.
- `tailscale serve status` shows active listeners; `tailscale serve --help`
  for options.

## 4. Install on your devices

Install Tailscale on your laptop/phone, `tailscale up`, and log in to the same
tailnet. Then browse to `https://finance.<tailnet>.ts.net`.

## 5. (Optional) Tighten ACLs

By default every device in the tailnet can reach every other. The admin console
**Access Controls** page lets you restrict which devices/users can reach the
server (or specific ports), e.g. allow only your own devices to reach
`finance.<tailnet>.ts.net:443`.

## 6. Remove the public route

Once Tailscale access works, remove the app's public path:

- NPM: delete the proxy host for the app (keep the `deploy.*` one).
- Cloudflare: remove the tunnel public-hostname / proxied DNS record for the
  app (keep `deploy.*`).
- Any router port-forward that existed only for the app.

## Verify

```sh
# from the server (loopback still works)
curl -fsS http://127.0.0.1:3001/

# from a tailnet device
curl -fsS https://finance.<tailnet>.ts.net/

# from OUTSIDE the tailnet — should fail (timeout / no route)
curl https://finance.<tailnet>.ts.net/
```
