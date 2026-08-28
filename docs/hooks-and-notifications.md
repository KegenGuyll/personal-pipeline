# Hooks & notifications

Two mechanisms:

1. **Notifications** — the agent POSTs a JSON payload to your webhook URL(s) on
   `started`, `succeeded`, and `failed`. Zero scripts.
2. **Script hooks** — per-service `pre-deploy` / `post-deploy` shell scripts run
   around the deploy.

## Notifications

Configure in `stack/.env`:

```bash
NOTIFY_WEBHOOK_URLS=https://discord.com/api/webhooks/...,https://ntfy.sh/mytopic
NOTIFY_CONTENT_TYPE=application/json
```

- **Default body** (no template set) is a JSON envelope:
  ```json
  {"event":"deploy.succeeded","project":"my-service","image":"ghcr.io/you/my-service",
   "tag":"sha-abc1234","sha":"abc1234…","repo":"you/my-service","status":"success",
   "duration_ms":8123,"error":""}
  ```
- **Custom body** with `NOTIFY_TEMPLATE` (a Go `text/template`). Available
  fields: `{{.Event}} {{.Project}} {{.Image}} {{.Tag}} {{.Sha}} {{.Repo}}
  {{.Status}} {{.DurationMs}} {{.Error}}`.

### Discord example

```bash
NOTIFY_TEMPLATE='{"content":"Deploy **{{.Status}}** — `{{.Project}}` @ `{{.Tag}}` ({{.DurationMs}}ms){{if .Error}} — {{.Error}}{{end}}"}'
```

### Slack example

```bash
NOTIFY_TEMPLATE='{"text":"Deploy *{{.Status}}* for `{{.Project}}` @ `{{.Tag}}`{{if .Error}} — {{.Error}}{{end}}"}'
```

### ntfy example (plain text)

```bash
NOTIFY_WEBHOOK_URLS=https://ntfy.sh/mytopic
NOTIFY_CONTENT_TYPE=text/plain
NOTIFY_TEMPLATE='Deploy {{.Status}} for {{.Project}} @ {{.Tag}}'
```

> In `.env`, a literal `$` must be escaped as `$$` (compose interpolation).
> Notification send failures are non-fatal and logged as warnings.

## Script hooks

Drop an executable file in a service directory:

```
services/my-service/hooks/pre-deploy     # runs BEFORE pull/up
services/my-service/hooks/post-deploy    # runs AFTER up
```

Scripts receive these env vars:

| Var | Meaning |
|---|---|
| `DEPLOY_PROJECT` | service name |
| `DEPLOY_IMAGE` | image ref (no tag) |
| `DEPLOY_TAG` | image tag (e.g. `sha-abc1234`) |
| `DEPLOY_SHA` | full commit SHA |
| `DEPLOY_REPO` | `owner/repo` |
| `DEPLOY_STATUS` | `started` (pre) or `success`/`failed` (post) |
| `DEPLOY_ERROR` | error message when failed |
| `DEPLOY_DURATION_MS` | elapsed ms |

Hooks run inside the agent container, which is on the `proxy` network and has
the Docker socket mounted — so a hook can `docker run` a one-off container or
`curl` another service by its network alias.

Semantics:

- **`pre-deploy` non-zero exit** → deploy aborts (status `failed`, failure
  notification sent).
- **`post-deploy` non-zero exit** → logged as a warning; does **not** flip a
  successful deploy to `failed`.
- Timeout is `HOOK_TIMEOUT` seconds (default 60).

See `services/_template/hooks/` for copy-paste examples (rename to the exact
name and `chmod +x`).
