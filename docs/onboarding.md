# Project onboarding (GitHub App)

Onboarding wires a new project repo into the pipeline **without touching the
repo by hand**. A GitHub App, driven from the deploy dashboard, does three
things in one action:

1. Creates the service's compose file on the server (`services/<name>/`) — same
   private Tailscale template as the dashboard's "Add service".
2. Sets the project repo's `SERVICE_ENV` secret (the app's runtime env).
3. Opens a **pull request** that adds `.github/workflows/deploy.yml`.

The PR is **never auto-merged** — you review the workflow snippet, then merge.
Once merged, the next `git push` builds and deploys exactly like a manually
wired project.

```
[dashboard "Onboard project"] ──POST /onboard (ADMIN_TOKEN)──▶ deploy agent
        │
        ├─ writes services/<name>/docker-compose.yml      (server)
        ├─ sets SERVICE_ENV secret in the project repo    (GitHub API)
        └─ opens PR adding .github/workflows/deploy.yml   (GitHub API)
                                   │ human review + merge
                                   ▼
        [project repo] --push--> Actions --build/push--> GHCR --> agent deploys
```

## One-time setup

### 1. Create the GitHub App

- GitHub → Settings → Developer settings → **GitHub Apps** → **New GitHub App**.
- **GitHub App name:** anything unique, e.g. `personal-pipeline-onboarder`.
- **Homepage URL:** any URL (e.g. your pipeline repo).
- **Webhook:** you can leave it disabled — onboarding is driven from the
  dashboard/API, the agent calls GitHub outbound. (A webhook is only needed for
  future auto-onboarding.)
- **Permissions** (Repository permissions):

  | Permission | Access |
  |---|---|
  | Contents | **Read and write** (create the workflow file) |
  | **Actions** | **Read and write** (required to write files under `.github/workflows/`) |
  | Secrets | **Read and write** (set `SERVICE_ENV`) |
  | Pull requests | **Read and write** (open the onboarding PR) |
  | Metadata | Read (required, granted automatically) |

  > **Gotcha:** Contents: write alone is *not* enough to create `deploy.yml` —
  > GitHub requires the **Actions** permission for workflow files. And any
  > permission change only takes effect after the app is **re-installed**
  > (or the "granted new permissions" prompt is approved). If onboarding fails
  > with `commit workflow file ... Resource not accessible by integration
  > (status 403)`, this is why — grant Actions, re-install, retry.

- **Where can this app be installed?** "Only on this account" (or the org).
- Create the app, then on its page generate a **private key** and download the
  `.pem` file. Note the **App ID** shown at the top.

### 2. Install it on your repos

From the app page → **Install App** → install on your account, choosing
**All repositories** (simplest) or only the repos you will onboard.

### 3. Configure the server

Add to `stack/.env` (see `.env.example`):

```sh
GITHUB_APP_ID=123456
GITHUB_APP_PRIVATE_KEY_B64=$(base64 -i github-app-key.pem | tr -d '\n')
# optional, if the agent can't auto-resolve the installation:
GITHUB_APP_INSTALLATION_ID=987654
# optional; defaults to GHCR_OWNER / main:
PIPELINE_OWNER=kegenguyll
PIPELINE_REF=main
```

Then apply:

```sh
git pull
docker compose -f stack/docker-compose.yml up -d --build
```

The agent logs `onboarding disabled: ...` if the key fails to parse; otherwise
`POST /onboard` is live.

## Onboarding a project

From the dashboard (`https://deploy.<domain>/ui`), **Onboard project**:

| Field | Meaning | Default |
|---|---|---|
| Repo | searchable picker of every repo the app can see (type to filter, click or Enter to pick; type any `owner/repo`) | — |
| Service | compose project / directory name | repo name, lowercased (`My_App` → `my-app`) |
| Image | image the workflow will build/push (auto-filled from repo) | `ghcr.io/<owner-lower>/<service>` |
| Port | container port | `3000` |
| Hostname | Tailscale MagicDNS hostname | service name |
| Context | build context for the workflow | `.` |
| Dockerfile | Dockerfile path relative to context | `Dockerfile` |
| Env | key-value rows → serialized to JSON as the `SERVICE_ENV` secret | `{}` |
| Replace existing deploy.yml | allow updating an existing workflow | off |

Or via API:

```sh
curl -X POST https://deploy.<domain>/onboard \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "repo": "you/my-service",
    "env": {"API_KEY": "...", "DATABASE_URL": "..."}
  }'
```

On success you get the PR link; **review and merge it**. The response also
reports `compose: created|existing` and any warnings.

## Behavior notes

- **The PR must be reviewed.** The agent performs no merge operations. The PR
  contains exactly the code being added: the ~15-line `deploy.yml`, plus the
  service/image/branch in the description. Deploys only start after merge.
- **No Dockerfile → warning only.** If the effective `Dockerfile` path doesn't
  exist on the default branch, onboarding still completes and the response
  warns that the first build will fail until one is added. This supports the
  "onboard early, add the app later" flow.
- **Idempotent.** Re-running onboarding is safe: an existing compose file is
  left as-is (`compose: existing`), the secret is upserted, and a conflicting
  `deploy.yml` returns `409` unless `overwrite_workflow` is set.
- **Existing services are untouched.** `personal-finance` and anything already
  deployed keep working; onboarding is only for new projects.
- **`DEPLOY_WEBHOOK_URL` / `DEPLOY_WEBHOOK_SECRET`** stay at org/user level, so
  the generated workflow resolves them automatically in every repo.

## Troubleshooting

| Symptom | Fix |
|---|---|
| `POST /onboard` returns 404 | `GITHUB_APP_ID` / key not set, or the key failed to parse — check agent logs. |
| `fetch repo ... (status 404/403)` | The app isn't installed on that repo (or the repo is private without install access). |
| `repo has no commits on main` | Make an initial commit before onboarding — there's no HEAD to branch from. |
| `409 ... deploy.yml already exists` | Re-run with `overwrite_workflow: true`, or delete the file first. |
| PR exists but deploy doesn't run | Merge the PR first — workflows only run from the default branch. |
| First workflow run fails at build | Missing Dockerfile (the onboarding warning told you), or the `context`/`dockerfile` inputs are wrong. |
