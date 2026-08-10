# Miabi examples

Runnable examples: a full-featured **Docker Compose deployment**, plus the
**GitOps & CI/CD** feature — the declarative resource model (`miabi.io/v1`), the
apply API, GitOps reconciliation, and CI/CD pipelines.

```
examples/
├── compose/                      # deploy Miabi with docker compose (gateway + registry + logs)
│   ├── compose.yaml              #   postgres · redis · Goma gateway · miabi (+ optional worker)
│   ├── compose.traefik.yaml      #   same stack, Traefik as the edge proxy instead of Goma
│   ├── goma.yml                  #   reverse-proxy config (TLS/ACME, panel route, app providers)
│   └── .env.example              #   domains, secrets, and feature toggles (rename to .env)
├── apply/
│   ├── project.yaml              # a Project bundle (db + volume + secret + app + route)
│   ├── domain.yaml               # owned Domains (FQDN + TLS policy) + Routes exposing an app
│   ├── app-ports.yaml            # per-port exposure: externalAccess (URL) + publish/hostPort
│   └── config.yaml               # Configs mounted as files (directory + pinned key, delimiters)
├── gitops/
│   ├── README.md
│   ├── envs/
│   │   ├── dev/stack.yaml        # dev desired state
│   │   └── prod/stack.yaml       # prod desired state (digest-pinned)
│   └── okapi-example/stack.yaml  # single-app example (the okapi-example template as GitOps)
└── pipeline/
    ├── README.md                # triggering a pipeline from Git (webhook / CI / schedule)
    ├── pipeline.yaml            # minimal test → build → deploy
    └── pipeline-multistage.yaml # test → build → scan → deploy + schedule
```

## Resource kinds (`miabi.io/v1`)

`Application` · `Stack` · `Database` · `Volume` · `Route` · `Domain` · `Secret` ·
`Config` · `Project` (a bundle of the others). One schema, four consumers: the
apply API, GitOps, the Terraform/OpenTofu provider, and marketplace templates.

> **Secrets vs Configs** — a **`Secret`** is one opaque value consumed as an
> environment variable; a **`Config`** is a set of named files projected into the
> container as read-only files (`nginx.conf`, `prometheus.yml`, a provisioning
> directory). Both are workspace-scoped and encrypted at rest. A config's file
> contents interpolate like env, and a mount either projects the whole set under
> a directory or pins one `key` to an exact path. See
> [apply/config.yaml](apply/config.yaml).

> **Domains vs Routes** — a **`Domain`** is an owned hostname/zone (its
> `metadata.name` is the FQDN) carrying the default TLS policy and optional
> wildcard coverage; a **`Route`** is an HTTP routing rule that exposes an app on
> a host/path. See [apply/domain.yaml](apply/domain.yaml). TLS is `acme` (Let's
> Encrypt, default) or `custom` for a Domain; Routes also allow `off`.
> DNS-ownership verification is a runtime action, not a declarable field, so an
> applied Domain starts unverified.

Env values interpolate against the workspace's resolvable databases:

```yaml
env:
  DATABASE_URL: "{{ .databases.<name>.uri }}"   # or .url; also .host .port .user .password .name
```

## Setup

The API calls below assume these shell variables. Address the workspace by its
**name** (its handle) — the `/workspaces/{ws}` path accepts the name, its uid, or
the numeric id:

```bash
BASE=https://miabi.example.com   # your Miabi URL
WS=acme                          # workspace name (handle) — numeric id also works
TOKEN=mb_xxx                     # API token (Settings → API keys)
ID=7                             # a pipeline id (section 3; name is not accepted here)
```

## 1. Apply (imperative, one-shot)

Preview the plan, then converge:

```bash
# dry run — returns an ordered plan of create/update/delete/noop
curl -X POST "$BASE/api/v1/workspaces/$WS/apply" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d "$(jq -Rs '{manifests: ., dry_run: true}' < apply/project.yaml)"

# apply for real (add prune:true to delete managed resources removed from the bundle)
curl -X POST "$BASE/api/v1/workspaces/$WS/apply" \
  -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
  -d "$(jq -Rs '{manifests: .}' < apply/project.yaml)"
```

## 2. GitOps (declarative, pull-based)

Commit the `gitops/` tree to a repo, then create a GitSource per environment —
see [gitops/README.md](gitops/README.md).

## 3. Pipelines (CI/CD)

Create a pipeline from `pipeline/pipeline.yaml` and **bind it to a Git-backed
application** — the runner clones that repo at the run's commit into a shared
`/workspace`, runs each step over it, builds an image with a captured digest
(`uses: build`), and deploys that exact image by digest (`uses: deploy`). Steps
run in isolated containers on the internal runner; logs stream live.

**Triggering from Git** — a `git push` can start a run three ways: a native
push webhook, a CI job calling the trigger API, or `on.schedule`. Full,
copy-pasteable setup (GitHub/GitLab webhook config, signature test, Actions /
GitLab CI snippets): **[pipeline/README.md](pipeline/README.md)**.

```bash
# Reveal this pipeline's webhook URL + secret (Developer+), then register it with your provider
curl -s "$BASE/api/v1/workspaces/$WS/pipelines/$ID/webhook-info" \
  -H "Authorization: Bearer $TOKEN" | jq
```

> Notes: `digest` values here are illustrative. Apply v1 converges
> Application/Volume/Database/Stack/Secret/Config/Route; `custom` TLS needs a stored
> certificate. A `Route`'s host must fall under a **domain registered in the
> workspace** (Networking → Domains) — register `example.com` before applying
> these, or the route create is rejected.
