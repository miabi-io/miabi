# Pipelines — triggering from Git

A Miabi pipeline turns a **specific commit** into an **image** and deploys
that exact artifact. This guide shows the three ways a `git push` (or a commit)
can start a run. The runner clones the bound app's repo once at the run's commit
into a shared `/workspace`, runs each step over it, builds an image with a
captured digest (`uses: build`), and deploys it by that digest (`uses: deploy`).

See [pipeline.yaml](pipeline.yaml) (minimal) and
[pipeline-multistage.yaml](pipeline-multistage.yaml) (test → build → scan →
deploy + schedule).

```bash
BASE=https://miabi.example.com   # your Miabi URL
WS=acme                          # workspace name (its handle) — numeric id also works
PIPELINE=7                       # pipeline id (or its uid)
APP=42                           # the Git-backed application's id (bound to the pipeline)
TOKEN=mb_xxx                     # API token (Settings → API keys), Developer+ role
```

The `/workspaces/{ws}` path accepts the workspace **name** (handle), its uid, or
the numeric id — the readable name is used throughout below.

## 0. Prerequisite: bind the pipeline to a Git-backed app

The runner needs a repo to clone, so `uses: build`/`uses: deploy` have a source
and a target. Create (or pick) an **application** whose source is a Git repo
(set its repository URL, branch, and — for private repos — a stored Git
credential), then set the pipeline's **Application** to it. Create the pipeline
from [pipeline.yaml](pipeline.yaml).

## Keep the spec in your repo: `.miabi/pipeline.yaml`

Version your pipeline-as-code next to the app it deploys — the home Miabi looks
in is **`.miabi/pipeline.yaml`** in the application's repository:

```
your-app/
├── .miabi/
│   └── pipeline.yaml     # this spec, versioned with the code
├── Dockerfile
└── src/…
```

### The easy way: let Miabi adopt it

When you create an application from a Git repository, Miabi reads the repository
and offers to use the pipeline it finds. Accept, and it creates a **repo-owned**
pipeline bound to the app — no API calls, no copy-pasting.

A repo-owned pipeline is different from one you author in Miabi:

- **The file is the source of truth.** Before every run, Miabi re-reads
  `.miabi/pipeline.yaml` at the ref being built and runs what it finds. Edit the
  file and push; there is nothing to re-apply.
- **It can't be edited in Miabi.** The spec, the name, and the app binding are
  all derived — from the file and from the app it was adopted for — so a `PATCH`
  that changes any of them is rejected with `409`. The UI shows the definition
  read-only behind a *managed by repository* badge. The one exception is
  `enabled`: disabling a repo-owned pipeline is the kill switch that puts its app
  back on direct builds without deleting anything.
- **Deploying the app runs the pipeline.** The app has no separate build of its
  own, so its build-method settings no longer apply and a deploy returns a
  pipeline run instead of a deployment. This is the point: a deploy can't skip
  the test and scan steps the repository declares.

Miabi also accepts `.miabi/pipeline.yml`, `.miabi/pipelines.yaml`, and
`.miabi/pipelines.yml`; the first that exists wins.

An operator can turn adoption off fleet-wide with the `repo_pipelines_enabled`
platform setting — git apps then always build and deploy directly.

### The manual way: register the spec yourself

Use this when you want a pipeline Miabi owns (editable in the UI, unaffected by
what the repo carries). Its contents become the pipeline's stored spec
(`$APP` = the Git-backed app's id):

```bash
curl -X POST "$BASE/api/v1/workspaces/$WS/pipelines" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d "$(jq -Rs --argjson app "$APP" \
        '{name: "shop-web", application_id: $app, spec: ., enabled: true}' \
        < .miabi/pipeline.yaml)"
```

When you edit the file later, re-apply it — from the UI (**Pipelines → Edit →
paste**), or by `PATCH`ing the pipeline. Send the full object, **including
`name`, `application_id`, and `enabled`**: an update applies those fields as
given, so a spec-only body would unbind the app and disable the pipeline.

```bash
curl -X PATCH "$BASE/api/v1/workspaces/$WS/pipelines/$PIPELINE" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d "$(jq -Rs --argjson app "$APP" \
        '{name: "shop-web", application_id: $app, spec: ., enabled: true}' \
        < .miabi/pipeline.yaml)"
```

> **A manually registered pipeline runs the _stored_ spec, not the repo file.**
> The calls above copy `.miabi/pipeline.yaml` into the pipeline record, and that
> record is what executes at run time. A matching `git push` triggers a run
> (section 1) but does **not** re-read the file — re-apply after you change it.
>
> A **repo-owned** pipeline (adopted at app creation, above) does re-read the
> file at every run, which is what makes re-applying unnecessary there.

---

## 1. Native push webhook (recommended)

The pipeline carries a generated webhook secret. Reveal the URL + secret
(Developer+ role), then register it with your Git provider. A push whose branch
matches `on.push.branches` fires a run **pinned to the pushed commit**.

```bash
# Reveal the webhook URL (path) + secret for this pipeline
curl -s "$BASE/api/v1/workspaces/$WS/pipelines/$PIPELINE/webhook-info" \
  -H "Authorization: Bearer $TOKEN" | jq
# → { "path": "/api/v1/workspaces/1/pipelines/7/webhook",
#     "secret": "8f3c…",
#     "signature_header": "X-Hub-Signature-256" }
```

The returned `path` uses the canonical **numeric** ids (that's what the server
emits); register it as-is with your provider. The full webhook URL is
`"$BASE" + path`, e.g.
`https://miabi.example.com/api/v1/workspaces/1/pipelines/7/webhook`.

### GitHub

Repo → **Settings → Webhooks → Add webhook**:

| Field | Value |
|-------|-------|
| Payload URL | `$BASE` + `path` |
| Content type | `application/json` |
| Secret | the `secret` from above |
| Events | **Just the push event** |

GitHub signs the body with HMAC-SHA256 and sends it as
`X-Hub-Signature-256: sha256=…`; Miabi verifies it against the secret.

### GitLab

Repo → **Settings → Webhooks**:

- **URL** = `$BASE` + `path`
- **Secret token** = the `secret` (GitLab sends it as `X-Gitlab-Token`)
- **Trigger** = *Push events* (optionally filter to your branch)

### Test it locally (simulate a GitHub push)

```bash
SECRET=8f3c...   # the secret from webhook-info
BODY='{"ref":"refs/heads/main","after":"a1b2c3d4","head_commit":{"id":"a1b2c3d4","message":"feat: ship it"}}'
SIG="sha256=$(printf '%s' "$BODY" | openssl dgst -sha256 -hmac "$SECRET" | awk '{print $2}')"

curl -X POST "$BASE/api/v1/workspaces/$WS/pipelines/$PIPELINE/webhook" \
  -H "Content-Type: application/json" \
  -H "X-Hub-Signature-256: $SIG" \
  -d "$BODY"
# → 201 with the new run when the branch matches on.push.branches;
#   200 {"message":"ignored: push does not match this pipeline's trigger"} otherwise.
```

The run is created with `trigger: "push"` and `commit` = the pushed SHA, so the
build and deploy reproduce exactly that commit — even if `main` advances while
the run is in flight.

---

## 2. CI-driven trigger (GitHub Actions / GitLab CI)

When you want CI to gate first (lint/tests), or inbound webhooks are blocked,
have the CI job call the authenticated trigger endpoint with the commit SHA.

```bash
curl -X POST "$BASE/api/v1/workspaces/$WS/pipelines/$PIPELINE/trigger" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d '{"commit": "'"$GIT_SHA"'", "commit_message": "'"$GIT_MSG"'"}'
```

**GitHub Actions** — `.github/workflows/deploy.yml`:

```yaml
name: deploy
on:
  push: { branches: [main] }
jobs:
  trigger:
    runs-on: ubuntu-latest
    steps:
      - name: Trigger Miabi pipeline
        env:
          BASE: ${{ secrets.MIABI_URL }}
          TOKEN: ${{ secrets.MIABI_TOKEN }}
        run: |
          curl -fX POST "$BASE/api/v1/workspaces/acme/pipelines/7/trigger" \
            -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
            -d "{\"commit\": \"$GITHUB_SHA\", \"commit_message\": \"${{ github.event.head_commit.message }}\"}"
```

**GitLab CI** — `.gitlab-ci.yml`:

```yaml
deploy:
  stage: deploy
  only: [main]
  script:
    - >
      curl -fX POST "$MIABI_URL/api/v1/workspaces/acme/pipelines/7/trigger"
      -H "Authorization: Bearer $MIABI_TOKEN" -H 'Content-Type: application/json'
      -d "{\"commit\": \"$CI_COMMIT_SHA\", \"commit_message\": \"$CI_COMMIT_TITLE\"}"
```

A plain server-side Git hook works the same way:

```bash
# .git/hooks/post-receive (on a self-hosted remote)
while read _old new ref; do
  [ "$ref" = "refs/heads/main" ] && \
    curl -fsX POST "$BASE/api/v1/workspaces/$WS/pipelines/$PIPELINE/trigger" \
      -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
      -d "{\"commit\": \"$new\"}"
done
```

---

## 3. Schedule (and manual)

`on.schedule` registers a cron entry — no Git event needed. The run checks out
the bound app's branch HEAD:

```yaml
on:
  schedule: "0 3 * * *"   # nightly at 03:00
```

Manual runs come from the UI's **Run now** button or `POST …/trigger` with no
commit (HEAD of the app's ref is resolved and recorded on the run).

## Build cache

Build steps reuse cached layers by default — that is what makes a second run
fast. When a cached layer has gone stale (a moving `apt` or `npm` index, a `FROM`
tag that moved under you), drop the cache from the pipeline instead of committing
an `ARG CACHEBUST` line to the Dockerfile:

```yaml
  - name: build
    uses: build
    cache: false     # rebuild every layer, every run
```

For a one-off, leave the spec alone and ask for a cold run instead — the UI's
**Run without cache** button, or the API:

```bash
curl -X POST "$BASE/api/v1/workspaces/$WS/pipelines/$PIPELINE/trigger"   -H "Authorization: Bearer $TOKEN" -H "Content-Type: application/json"   -d '{"no_cache": true}'
```

The override applies to every build step in that run and to that run only; the
next run caches again. A cold build costs whatever a first build costs — minutes,
usually — so the run is labelled **no cache** in its history, and the build log
says `no cache` on the line that starts the build. `cache:` is only valid on a
`uses: build` step; both backends honour it (`docker build --no-cache`,
`buildctl --no-cache`, and `pack build --clear-cache` for buildpack builds).

> Requires a runner new enough to carry the flag. An older runner ignores what it
> does not understand, so the build would silently reuse its cache — check for the
> `no cache` note in the build log after upgrading a mixed fleet.

### Where the cache lives

The cache belongs to the **application**, not to the pipeline: a pipeline run and
a direct deploy of the same app share it. On a buildkit runner it is a registry
ref alongside the app's images:

```
<registry>/ws_<id>/<app>:cache-<branch>-g<generation>
```

Each branch writes only its own ref and additionally *reads* the tracked ref's —
so a build of `feat/x` starts warm from `main`, but can never push layers into a
cache a `main` build would trust. A docker-backed runner has no ref to rotate and
keeps using its daemon's local cache.

### Invalidating it

**Invalidate build cache** (app → Source, or `miabi apps invalidate-cache web`,
or `POST .../apps/$APP/build-cache/invalidate`) bumps the generation. Every ref
changes name, so the next build is cold and repopulates the cache; the one after
is warm again. Nothing is deleted — the old refs simply stop being read, and a
build already in flight is unaffected. Because a bumped generation also forces
`--no-cache` until something has rebuilt it, the invalidation reaches
docker-backed runners too.

Use it when the cache itself is suspect. For a single run, prefer `no_cache`
above — it costs one cold build instead of changing the app's state.

### Re-running

**Re-run** (run detail, or `miabi pipeline rerun <run-id> [--no-cache]`, or
`POST .../pipeline-runs/$RUN/rerun`) starts a fresh run at the *same* ref and
commit — the same code again, not whatever has landed since. The run is recorded
with `trigger: "rerun"`.

A direct deploy takes the same override: `miabi apps deploy web --no-cache`, or
**Rebuild without cache** in the deploy dialog.

---

## What you get back

Every run records the resolved commit and, after a `uses: build` step, the image
it produced — visible on the run detail (**Built image**: repository, digest,
commit, size) and via `GET /api/v1/workspaces/$WS/images`. The deploy step runs
that local image directly: no rebuild, no registry pull.
