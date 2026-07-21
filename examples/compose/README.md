# Compose deployment (full-featured example)

A copy-and-run, single-node Miabi stack that turns on the optional pieces most
self-hosters want, so you can see the knobs in one place:

- **Reverse-proxy gateway** (Goma) — terminates TLS, auto-issues Let's Encrypt
  certs, and serves the per-app route files Miabi writes.
- **Built-in container registry** — enabled, so git-source app deploys and
  pipeline builds work (a runner builds the image and pushes it here).
- **One-click public app URLs** — a wildcard base domain (`*.apps.example.com`).
- **Externalized log store** — deploy/pipeline/job/backup logs on a shared volume.
- **Scale-out worker** — a commented `miabi-worker` service showing how to move
  the background worker out of the panel process (and the shared-log-volume rule).

> This is the **only** Compose stack Miabi ships — there is no second, minimal copy
> in `deploy/` to drift out of sync with it. Everything lives in one folder you can
> copy anywhere. Turn the optional pieces off by leaving their `.env` keys unset.
>
> For a **managed** install, don't use Compose at all: `deploy/install.sh` (or
> `docker run … install`) has Miabi create and own the stack itself, which is what
> makes `miabi update` able to replace the control plane. See the
> [installation docs](https://miabi.io/docs/getting-started/installation).

## Layout

```
examples/compose/
├── compose.yaml           # default: postgres · redis · Goma gateway · miabi (+ optional worker)
├── compose.traefik.yaml   # variant: same stack with Traefik as the edge proxy instead of Goma
├── goma.yml               # Goma config (TLS/ACME, panel route, per-app providers)
└── .env.example           # domains, secrets, and feature toggles
```

Both files read a single `.env`. Every service gets it via `env_file`, so you
**rename `.env.example` → `.env`, fill in the values, and deploy** — no per-file
edits. (Goma expands `${MIABI_DOMAIN}` & friends from that same env at load time.)

## Run it

```sh
cp .env.example .env
# Edit .env: MIABI_DOMAIN, MIABI_WEB_URL, MIABI_EXTERNAL_BASE_DOMAIN, and secrets.
openssl rand -hex 32          # use for MIABI_JWT_SECRET and MIABI_ENCRYPTION_KEY
echo "DOCKER_GID=$(stat -c '%g' /var/run/docker.sock)" >> .env

# Optional but recommended on a busy multi-tenant host: pre-create the shared
# `miabi` bridge with a roomy CIDR so it isn't capped by Docker's small default
# pool. (Compose here creates it for you otherwise; the production deploy/ stack
# and install.sh always pre-create it as an external network.)
docker network create --driver bridge --subnet 10.63.0.0/16 miabi || true

docker compose up -d
```

### Prefer Traefik as the edge proxy?

```sh
docker compose -f compose.traefik.yaml up -d
```

Traefik terminates TLS and routes by Docker labels. Note the trade-offs: app
routes are exposed via `traefik.*` labels you add per app (App → Settings →
Container labels), rolling/canary deploys and the **built-in registry require
Goma** (keep `MIABI_REGISTRY_ENABLED=false` on this variant). Details are in the
header of [`compose.traefik.yaml`](./compose.traefik.yaml).

**DNS:** point both records at this host so Goma can issue certs and serve
one-click URLs:

| Record | Points to |
|---|---|
| `miabi.example.com` (panel) | this host |
| `*.apps.example.com` (apps) | this host |

Then open `https://miabi.example.com` and log in with `MIABI_ADMIN_EMAIL` /
`MIABI_ADMIN_PASSWORD`.

## Next steps

- **Add a runner** (required for git builds): Settings → Runners shows the
  `docker run … miabi/runner` command. Runners dial out to the panel, so they can
  run on this host or anywhere else.
- **Scale the worker:** uncomment the `miabi-worker` service in `compose.yaml`.
  It must mount the same `miabi-logs` volume so the panel can read the logs it
  externalizes.
- See the full env reference in the repo-root [`.env.example`](../../.env.example).
