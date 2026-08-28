# Deployment logs

Three surfaces:

## 1. Live stream

The agent writes structured JSON lines to stdout:

```sh
docker compose -f stack/docker-compose.yml logs -f agent
```

Each line is one `{"ts":…,"event":…,…}` record.

## 2. Durable history

Append-only JSONL at `deploy-agent/data/deployments.jsonl` (a named volume,
so it survives restarts). One record per deploy attempt:

```json
{"id":"…","ts":"…","project":"my-service","image":"ghcr.io/you/my-service",
 "tag":"sha-abc1234","sha":"…","repo":"you/my-service","status":"success",
 "duration_ms":8123,"pre_hook":{"exit_code":0,"output_tail":"…","duration_ms":12},
 "post_hook":{…},"compose":{"pull_output_tail":"…","up_output_tail":"…"},
 "notifications":["https://…:ok"],"error":""}
```

`LOG_RETENTION` (default 100) keeps the last N records. The `env` map is never
written — secrets stay out of history.

## 3. Read API

Bearer-authenticated endpoints on `deploy.<domain>`:

```sh
# list recent deployments (filters: ?project= &status= &limit=)
curl -H "Authorization: Bearer $READ_TOKEN" \
  "https://deploy.<domain>/deployments?limit=20&status=failed"

# single record
curl -H "Authorization: Bearer $READ_TOKEN" \
  "https://deploy.<domain>/deployments/<id>"
```

- No `Authorization` header → `401`.
- `READ_TOKEN` unset in `stack/.env` → endpoint disabled (`404`).

## Failure surfacing

When a deploy fails, the agent returns the sanitized error + output tail in the
webhook HTTP response, so the GitHub Actions run shows why it failed without
SSHing into the server.
