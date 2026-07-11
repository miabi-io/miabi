<h1 align="center">Miabi</h1>

<p align="center">
  <img src="https://raw.githubusercontent.com/miabi-io/miabi/main/logo.png" alt="Miabi" width="150" />
</p>

<p align="center">
  <strong>The open-source, self-hosted Platform-as-a-Service (PaaS)</strong><br/>
  Multi-tenancy · GitOps · built-in registry · monitoring · backups · multi-node deployments.
</p>

<p align="center">
  <a href="#live-demo">Live Demo</a> ·
  <a href="#quick-start">Quick Start</a> ·
  <a href="#core-features">Features</a> ·
  <a href="#feature-comparison">Comparison</a> ·
  <a href="#architecture">Architecture</a> ·
  <a href="https://github.com/miabi-io/miabi-cli">CLI</a> ·
  <a href="https://docs.miabi.io">Docs</a>
</p>

<p align="center">
  <a href="https://github.com/miabi-io/miabi/actions/workflows/ci.yml"><img src="https://github.com/miabi-io/miabi/actions/workflows/ci.yml/badge.svg" alt="CI" /></a>
  <a href="https://goreportcard.com/report/github.com/miabi-io/miabi"><img src="https://goreportcard.com/badge/github.com/miabi-io/miabi" alt="Go Report Card" /></a>
  <a href="https://go.dev/"><img src="https://img.shields.io/github/go-mod/go-version/miabi-io/miabi" alt="Go" /></a>
  <a href="https://pkg.go.dev/github.com/miabi-io/miabi"><img src="https://pkg.go.dev/badge/github.com/miabi-io/miabi.svg" alt="Go Reference" /></a>
  <a href="https://github.com/miabi-io/miabi/releases"><img src="https://img.shields.io/github/v/release/miabi-io/miabi" alt="GitHub Release" /></a>
  <a href="./LICENSE"><img src="https://img.shields.io/github/license/miabi-io/miabi" alt="License" /></a>
  <img src="https://img.shields.io/docker/pulls/miabi/miabi?style=flat-square" alt="Docker Pulls" />
</p>

<p align="center">
  <img src="https://raw.githubusercontent.com/miabi-io/miabi/main/docs/screenshots/dashboard.png" alt="Miabi dashboard" width="900"/>
</p>

---

## Table of Contents

- [Overview](#overview)
- [Live Demo](#live-demo)
- [Why Miabi](#why-miabi)
- [Core Features](#core-features)
- [Feature Comparison](#feature-comparison)
- [Architecture](#architecture)
- [Requirements](#requirements)
- [Quick Start](#quick-start)
- [API Documentation](#api-documentation)
- [Screenshots](#screenshots)
- [Ecosystem](#ecosystem)
- [Documentation](#documentation)
- [Contributing](#contributing)
- [License](#license)

---

## Overview

**Miabi** is a self-hosted, developer-first Platform-as-a-Service for containerized
apps. Push an app — from a **Git repo**, a **Docker image**, or a **marketplace
template** — and Miabi handles the rest: build, deploy, domains, **automatic
SSL**, databases, scaling, backups, and monitoring. All from one web interface,
in minutes, without touching a single Docker command.

It is designed as a fully self-hostable alternative to platforms like Heroku,
Render, and Railway — giving you complete ownership of your infrastructure,
data, and runtime, on a VPS, dedicated box, homelab, or cloud VM.

> **The name.** _Miabi_ is Tshiluba (Kasai, DR Congo 🇨🇩) for the **muabi trees** —
> traditionally associated with **blessing and growth**. It joins the same family
> as its sibling projects [Goma Gateway](https://github.com/jkaninda/goma-gateway)
> and [Posta](https://github.com/goposta/posta).

---

## Why Miabi

Miabi is built for developers, teams, hosting providers, and organizations that
want the simplicity of a modern Platform-as-a-Service without giving up control
of their infrastructure.

Whether you're deploying a single application on a VPS, running a shared hosting
platform for hundreds of customers, or building an internal developer platform,
Miabi provides everything you need in one integrated platform.

### Developer-first experience

- Deploy applications from **Git repositories**, **Docker images**, or **Marketplace templates**
- No Kubernetes knowledge required
- Modern web interface, REST API, and official CLI
- Buildpacks for projects without Dockerfiles
- One-click deployments, rollbacks, and zero-downtime updates

### Built for multi-tenancy

Unlike most self-hosted PaaS platforms, Miabi was designed around **workspaces**
from day one.

Every application, database, domain, volume, registry image, secret, backup, and
deployment belongs to a workspace, making Miabi ideal for:

- Shared hosting providers
- Agencies managing client applications
- SaaS platforms
- Internal developer platforms
- Universities and organizations

### Docker without Kubernetes complexity

Miabi delivers a cloud platform experience while staying Docker-first.

- Single-node and multi-node deployments
- Optional Docker Swarm clustering
- Rolling and canary deployments
- Built-in load balancing
- Docker import for existing applications
- No Kubernetes cluster to operate

### Production-ready networking

Powered by [Goma Gateway](https://github.com/jkaninda/goma-gateway), Miabi includes:

- Automatic HTTPS with Let's Encrypt and wildcard certificates
- DNS provider integrations
- Built-in load balancing and canary traffic splitting
- Gateway middlewares
- Custom domains with workspace-aware routing

### Secure by design

Security is built into the platform — not added later.

- Workspace isolation
- Role-based access control (RBAC)
- Encrypted secrets and audit logs
- Two-factor authentication
- OAuth / OpenID Connect
- Enterprise SAML, LDAP, and Active Directory support

### Automate everything

Miabi is API-first. Everything available in the web interface is also available
through:

- REST API with OpenAPI documentation
- Official CLI
- GitOps and CI/CD pipelines
- Terraform / OpenTofu provider

### Own your platform

Run Miabi on a VPS, dedicated server, bare metal, homelab, or private/public
cloud. No vendor lock-in. No managed control plane. Your infrastructure, your
data, your rules.

---

## Core Features

### Applications & deployments

- Deploy from a **Git repo** (build), a **Docker image** (pull), or a **marketplace template**
- Buildpack builds (no Dockerfile required) with configurable memory/time limits
- **Releases** with one-click **rollback** and full deployment history
- **Zero-downtime** updates with canary aliases and weighted traffic splitting
- Env vars, a workspace **secret vault**, and per-app **resource limits**
- **Jobs** — run one-off commands in an app's runtime context
- **Stacks** — group related apps (compose-style); **Environments** — dev → staging → prod
- Per-app **timeline** of lifecycle events
- **Built-in container registry** — push & pull your own images with `docker login <registry> -u <workspace-name> -p <api-token>` (or your username); multi-tenant and namespaced per workspace, with local or S3/MinIO storage and an optional garbage-collector

### Domains, networking & TLS

- **Domains** with DNS-verified ownership
- Routing via [Goma Gateway](https://github.com/jkaninda/goma-gateway) (pluggable proxy) with workspace-owned **middlewares**
- **Automatic TLS** — default HTTP-01 ACME (Let's Encrypt), managed **wildcard / DNS-01** certs via a connected DNS provider (auto-renewed), and uploaded **custom certs** (encrypted)
- **Workspace-isolated Docker networks** carved from a managed address pool (so a busy multi-tenant host never exhausts Docker's small default pool), a configurable roomy CIDR for the shared proxy network, per-node **edge gateways**, and on-demand **port forwarding** to managed databases

### Data, storage & backups

- **Databases** — provision PostgreSQL, MySQL, MariaDB, Redis, libSQL, and MongoDB with managed credentials and in-place **version upgrades**
- **Volumes** — persistent Docker volumes owned by workspaces: node-local by default, or **shared (RWX)** storage a replicated cluster app can mount across nodes — **NFS**, **CIFS/SMB**, or a **host-path bind** to operator-managed storage under `/mnt/*` (a NAS mounted on every node; privileged workspaces)
- **Backups** — scheduled + manual database backup/restore and volume archives, to **local, MinIO, or S3**

### Multi-node & clustering

- **Nodes** — add remote Docker hosts; the [node agent](https://github.com/miabi-io/agent) dials the control plane over an **outbound** WebSocket tunnel (NAT/firewall friendly)
- **Cluster mode** — optional, auto-detected **Docker Swarm** with encrypted overlay networks
- **Replicated service apps** — when cluster mode is on, apps deploy as replicated **Swarm services** by default (opt out per app); stateful apps with node-local storage stay pinned to a container automatically
- **Cluster ingress** — public traffic reaches a clustered app's tasks **wherever the scheduler placed them**, through the central gateway on a shared ingress overlay that survives gateway restarts; the app detail shows the **real nodes** replicas run on
- **Image distribution** — built images are pushed to the internal registry so any node can pull them (credentials are distributed to worker tasks), making multi-node deploys and rollbacks of **Git-built** apps work across the cluster
- **Housekeeping** — reconcile drift and reclaim disk; **Docker import** — adopt pre-existing containers/volumes/networks

### CI/CD & GitOps

- **Pipelines** — pipeline-as-code CI/CD
- **Build runners** — dedicated build/pipeline machines that keep build load off app-hosting nodes; a co-located built-in runner ships for single-node/homelab, and an optional "builds require a runner" guarantee keeps builds off production nodes entirely
- **GitOps** — declarative, pull-based reconciliation from `miabi.io/v1` manifests, plus an imperative one-shot **apply**
- **Git push deploy**, stored Git + container-registry credentials, signed **webhooks**, and **notifications**
- **Automation** — everything is REST + OpenAPI, plus a **CLI** and an official [Terraform / OpenTofu provider](https://github.com/miabi-io/terraform-provider-miabi)

### Identity, teams & access

- **Auth** — registration, **login with email or username**, password reset, JWT sessions with Redis-backed revocation, **API tokens**, and **2FA (TOTP)**
- **SSO & directory** — OAuth 2.0 / OpenID Connect (GitHub, Google, generic OIDC); Enterprise adds **SAML 2.0**, **SCIM** provisioning, and **LDAP / Active Directory** sign-in (users log in with their directory credentials on the normal login form) with directory **groups mapped onto platform-admin and per-workspace roles**
- **Workspaces & teams** — members, invitations, and organizations; each workspace has a unique **name** handle (its URL and `docker login` namespace) plus a free-text display name, and each user a unique **username**
- **RBAC** — built-in roles **Owner · Admin · Developer · Viewer**, enforced in middleware _and_ by `workspace_id` scoping; Enterprise adds **custom roles** (named permission sets) and **per-resource policies** (grant a role on a single app/domain/database)
- **Container security profiles** — optional non-root ("restricted") profile runs app and job containers as a platform UID with `no-new-privileges`; outbound webhooks are SSRF-guarded
- **Plans & quotas**, per-workspace **encryption keys** (keyring/DEK), key rotation, and crypto-shred on delete

### Marketplace

Official, versioned templates: WordPress, Ghost, Nextcloud, n8n, Gitea, Forgejo,
Umami, NGINX, pgAdmin, phpMyAdmin, mongo-express, libSQL, Posta, PostgreSQL,
MySQL, Redis, and MongoDB.

### Monitoring & operations

- Container CPU/memory/disk metrics and workspace health with **retained history**
- **Prometheus** integration and health endpoints
- **Log storage** — deployment, pipeline, job, and backup logs are externalized from Postgres to a shared filesystem store with a bounded DB tail, retention, size caps, and full-log download (live tailing unchanged)
- Append-only **audit log** of every mutating action, with optional **SIEM streaming** to an external pipeline (syslog / webhook, Enterprise)
- **Admin platform** — nodes/cluster, users, plans, settings, OAuth providers, SSO (SAML / LDAP / Active Directory), license, and SIEM

---

## Feature Comparison

| Feature                          |    Miabi    |  Coolify  | Dokploy  | CapRover |
| -------------------------------- | :---------: | :-------: | :------: | :------: |
| Self-hosted                      |     ✅      |    ✅     |    ✅    |    ✅    |
| Open Source                      | ✅  |    ✅     |    ✅    |    ✅    |
| Web UI                           |     ✅      |    ✅     |    ✅    |    ✅    |
| Shared hosting                   |     ✅      |    ❌     |    ❌    |    ❌    |
| CLI                              |     ✅      |    ❌     |    ❌    |    ❌    |
| REST API                         |     ✅      |    ✅     | Partial  | Limited  |
| OpenAPI Documentation            |     ✅      |    ❌     |    ❌    |    ❌    |
| Multi-tenancy                    |     ✅      |    ❌     |    ❌    |    ❌    |
| Workspace Isolation              |     ✅      |    ❌     |    ❌    |    ❌    |
| Organizations & Teams            |     ✅      |  Limited  |    ❌    |    ❌    |
| RBAC                             |     ✅      |  Limited  |    ❌    |    ❌    |
| Deploy from Git                  |     ✅      |    ✅     |    ✅    |    ✅    |
| Deploy Docker Images             |     ✅      |    ✅     |    ✅    |    ✅    |
| Marketplace / Templates          |     ✅      |    ✅     |    ✅    | Limited  |
| Buildpacks (No Dockerfile)       |     ✅      |    ✅     |    ❌    |    ❌    |
| Built-in Container Registry      |     ✅      |    ❌     |    ❌    |    ❌    |
| Managed Databases                |     ✅      |    ✅     |    ✅    | Limited  |
| Automatic HTTPS (Let's Encrypt)  |     ✅      |    ✅     |    ✅    |    ✅    |
| Canary Deployments               |     ✅      |    ❌     |    ❌    |    ❌    |
| Zero-downtime Deployments        |     ✅      |  Limited  | Limited  | Limited  |
| Rollbacks                        |     ✅      |    ✅     | Limited  | Limited  |
| CI/CD Pipelines                  |     ✅      |    ❌     |    ❌    |    ❌    |
| GitOps                           |     ✅      |    ❌     |    ❌    |    ❌    |
| Multi-node Deployments           |     ✅      |  Partial  | Partial  | Partial  |
| Docker Swarm Support             |     ✅      |    ✅     |    ✅    |    ❌    |
| Docker Import                    |     ✅      |    ❌     |    ❌    |    ❌    |
| Secrets Management               |     ✅      |  Partial  | Partial  | Limited  |
| Monitoring                       | ✅ Built-in |   Basic   |  Basic   |  Basic   |
| Scheduled Backups                |     ✅      |  Partial  | Partial  |    ❌    |
| Audit Logs                       |     ✅      |    ❌     |    ❌    |    ❌    |
| API Tokens                       |     ✅      |    ✅     |    ✅    |    ❌    |
| OAuth / OIDC                     |     ✅      |  Partial  |    ❌    |    ❌    |
| SAML / LDAP (Enterprise)         |     ✅      |    ❌     |    ❌    |    ❌    |
| Terraform Provider               |     ✅      |    ❌     |    ❌    |    ❌    |

---

## Architecture

```
Browser / CLI / API clients
        │
        ▼
Goma Gateway (routing, TLS/ACME) ─▶ Miabi (Go/Okapi) ─▶ Docker Engine (local + remote via agent)
                                          │  serves API + web UI (single binary)
                                          │  └─ asynq worker (deploys, provisioning, backups)
                                          └─ PostgreSQL (GORM) · Redis (cache/queue)
```

| Layer                     | Technology                                                                 |
| ------------------------- | -------------------------------------------------------------------------- |
| **Backend**               | Go 1.25+ ([Okapi](https://github.com/jkaninda/okapi) framework, REST + OpenAPI) |
| **Frontend**              | Vue 3 + Pinia + Vite (built and statically served by the binary)           |
| **Database**              | PostgreSQL (GORM)                                                          |
| **Queue / cache**         | Redis + Asynq                                                             |
| **Runtime**               | Docker Engine via the Docker SDK for Go (optional Swarm)                    |
| **Reverse proxy / TLS**   | [Goma Gateway](https://github.com/jkaninda/goma-gateway)                    |
| **Metrics**               | Prometheus                                                                 |

The web console source lives in [`web/`](./web/) and is embedded into the Go
binary, so a deployment is a single image. The node agent is a separate module,
[`github.com/miabi-io/agent`](https://github.com/miabi-io/agent) — a thin Docker
proxy that needs only an outbound connection and the local Docker socket.

---

## Requirements

- Go 1.25+ (to build)
- PostgreSQL
- Redis
- A reachable Docker socket

---

## Quick Start

### One-line install (production)

Installs Docker if needed, fetches the production compose + config into
`/opt/miabi`, generates secrets, and brings the stack up:

```bash
curl -fsSL https://github.com/miabi-io/miabi/releases/latest/download/install.sh | sudo bash
```

Then edit `/opt/miabi/.env` (set your domain) and `/opt/miabi/goma/goma.yml`
(domain + ACME email), and re-run the installer. Open your domain and register —
**the first account becomes the platform admin**.

### Docker Compose

```bash
git clone https://github.com/miabi-io/miabi.git && cd miabi/deploy
cp .env.example .env   # fill in secrets + domain (openssl rand -hex 32)
# Shared app network — created once with a roomy CIDR so it isn't capped by
# Docker's small default pool. (The one-line install.sh does this for you.)
docker network create --driver bridge --subnet 10.63.0.0/16 miabi
docker compose up -d   # uses compose.yaml
```

Want the optional pieces (built-in registry, one-click wildcard app URLs,
externalized log volume, scale-out worker) wired up in one place? See the
full-featured [`examples/compose/`](./examples/compose/) stack.

Open the dashboard at your configured domain (`https://$MIABI_DOMAIN`) once DNS
resolves and Goma has issued a certificate. A platform admin is seeded from your
env — default credentials:

```
Email:    admin@example.com
Password: admin@1234   # change it after first login
```

### Local development

```bash
git clone https://github.com/miabi-io/miabi.git
cd miabi

make run        # API server on :9000 (worker embedded)
make worker     # standalone background worker (optional)
make build-ui   # build the Vue console into the embedded assets
make test       # unit + integration tests
```

---

## API Documentation

- OpenAPI spec and interactive docs at `/docs` and `/openapi.json` on your instance
- The spec is generated from code annotations — see [`internal/routes/`](./internal/routes/)

---
## Live Demo

Try Miabi without installing anything — at **<https://demo.miabi.io>**.

The demo is seeded with **two independent customers across three workspaces**, so
you can see Miabi's core ideas first-hand: **shared hosting on Docker with true
workspace isolation and role-based access** — every app, database, domain, volume,
and secret belongs to a workspace, one tenant can never see or reach another's
resources, and a member only sees the workspaces and permissions their role grants.

Sign in as any of these (**password: `MiabiDemo2026`**):

| Sign in as | Workspaces | Role | Represents |
|------------|------------|------|------------|
| `admin@acme.demo.miabi.io` | **Acme Inc Prod** · **Acme Inc Dev** | Owner | one org running prod + dev in separate, isolated workspaces |
| `dev@acme.demo.miabi.io` | **Acme Inc Dev** | Developer | a teammate scoped to a single workspace — can't see Acme Inc Prod, and has only Developer permissions |
| `admin@startup.demo.miabi.io` | **Startup Prod** | Owner | a different tenant — its resources are invisible to Acme |

Switch workspaces from the workspace picker to watch the entire console re-scope;
sign in as the other customer to confirm the isolation boundary, or as
`dev@acme.demo.miabi.io` to see a single-workspace, Developer-scoped view.

> [!IMPORTANT]
> These are **workspace accounts, not the platform admin.** They can't see the
> admin platform (nodes/cluster, users, plans, settings, licensing). To explore
> the **platform-admin** features, [install Miabi](#quick-start) on your own
> server or local Docker — the first account you seed is the platform admin.

> [!NOTE]
> Apps on the demo run under a **restricted (non-root) security profile**: each
> container runs as a dedicated, unprivileged user, not root. If you deploy a
> new app, make sure its image can run as a **non-root user** — for security,
> every new app on the demo is restricted from running as root, so images that
> require root will fail to start.
---

## Screenshots

Miabi's web console manages every resource — deployments, domains, databases,
backups, monitoring, marketplace, teams, and the admin platform.

<table>
  <tr>
    <td width="50%" align="center"><strong>Workspace dashboard</strong><br/><img src="https://raw.githubusercontent.com/miabi-io/miabi/main/docs/screenshots/dashboard.png" alt="Workspace dashboard" width="420"/></td>
    <td width="50%" align="center"><strong>Deploy an application</strong><br/><img src="https://raw.githubusercontent.com/miabi-io/miabi/main/docs/screenshots/deploy-new-app.png" alt="Deploy a new application from Git, image, or template" width="420"/></td>
  </tr>
  <tr>
    <td width="50%" align="center"><strong>Login screen</strong><br/><img src="https://raw.githubusercontent.com/miabi-io/miabi/main/docs/screenshots/login-screen.png" alt="Miabi login screen" width="420"/></td>
    <td width="50%" align="center"><strong>Application overview &amp; deployments</strong><br/><img src="https://raw.githubusercontent.com/miabi-io/miabi/main/docs/screenshots/application-overview.png" alt="Application overview and deployment history" width="420"/></td>
  </tr>
  <tr>
    <td width="50%" align="center"><strong>Canary deployment</strong><br/><img src="https://raw.githubusercontent.com/miabi-io/miabi/main/docs/screenshots/canary-deployment.png" alt="Canary strategy — weighted traffic split between the stable and canary releases" width="420"/></td>
    <td width="50%" align="center"><strong>GitOps deployment</strong><br/><img src="https://raw.githubusercontent.com/miabi-io/miabi/main/docs/screenshots/gitops-deployment.png" alt="GitOps — declarative, pull-based reconciliation from miabi.io/v1 manifests" width="420"/></td>
  </tr>
  <tr>
    <td width="50%" align="center"><strong>CI/CD pipelines</strong><br/><img src="https://raw.githubusercontent.com/miabi-io/miabi/main/docs/screenshots/pipelines.png" alt="CI/CD pipelines with live per-step logs" width="420"/></td>
    <td width="50%" align="center"><strong>Marketplace</strong><br/><img src="https://raw.githubusercontent.com/miabi-io/miabi/main/docs/screenshots/marketplace.png" alt="Marketplace templates" width="420"/></td>
  </tr>
  <tr>
    <td width="50%" align="center"><strong>Domains, routes &amp; TLS</strong><br/><img src="https://raw.githubusercontent.com/miabi-io/miabi/main/docs/screenshots/domains-routes.png" alt="Domains, routes, and automatic TLS" width="420"/></td>
    <td width="50%" align="center"><strong>Managed databases</strong><br/><img src="https://raw.githubusercontent.com/miabi-io/miabi/main/docs/screenshots/databases.png" alt="Managed databases" width="420"/></td>
  </tr>
  <tr>
    <td width="50%" align="center"><strong>Backups</strong><br/><img src="https://raw.githubusercontent.com/miabi-io/miabi/main/docs/screenshots/backups.png" alt="Scheduled and manual backups" width="420"/></td>
    <td width="50%" align="center"><strong>Monitoring</strong><br/><img src="https://raw.githubusercontent.com/miabi-io/miabi/main/docs/screenshots/monitoring.png" alt="Container and workspace monitoring" width="420"/></td>
  </tr>
  <tr>
    <td width="50%" align="center"><strong>Nodes &amp; cluster</strong><br/><img src="https://raw.githubusercontent.com/miabi-io/miabi/main/docs/screenshots/nodes-cluster.png" alt="Multi-node and cluster management" width="420"/></td>
    <td width="50%" align="center"><strong>Platform admin</strong><br/><img src="https://raw.githubusercontent.com/miabi-io/miabi/main/docs/screenshots/admin-platform.png" alt="Platform admin" width="420"/></td>
  </tr>
</table>

---

## Ecosystem

Miabi is part of a family of self-hosting tools by the same author:

- [Okapi](https://github.com/jkaninda/okapi) — the Go web framework Miabi is built on
- [Goma Gateway](https://github.com/jkaninda/goma-gateway) — reverse proxy + TLS/ACME
- [miabi-cli](https://github.com/miabi-io/miabi-cli) — the official CLI
- [terraform-provider-miabi](https://github.com/miabi-io/terraform-provider-miabi) — official Terraform / OpenTofu provider for managing Miabi resources as code
- [agent](https://github.com/miabi-io/agent) — the outbound node agent for multi-node deployments
- [runner](https://github.com/miabi-io/runner) — dedicated build/pipeline runner
- [marketplace](https://github.com/miabi-io/marketplace) — official app templates catalog
- [Posta](https://github.com/goposta/posta) — self-hosted email delivery & inbound platform
- [pg-bkup](https://github.com/jkaninda/pg-bkup) / [mysql-bkup](https://github.com/jkaninda/mysql-bkup) — database backup tools

---

## Documentation

- API docs — `/docs` and `/openapi.json` on a running instance

---

## Contributing

Contributions are welcome. Please open an issue before submitting a pull request.

---

## License

Miabi core is free and open source under the **GNU Affero General Public License
v3.0 or later (AGPL-3.0-or-later)** — see [LICENSE](./LICENSE) and [NOTICE](./NOTICE).
A **commercial license** is available for uses that don't fit the AGPL (e.g.
offering a modified Miabi as a hosted service without publishing your changes).

Enterprise features (everything under [`internal/enterprise/`](./internal/enterprise),
built with the `enterprise` tag) are **not** AGPL: they are licensed under the
**Miabi Enterprise License** — see [`internal/enterprise/LICENSE.md`](./internal/enterprise/LICENSE.md)
— and require a valid commercial license to use. See [LICENSING.md](./LICENSING.md)
for the full breakdown, and [CONTRIBUTING.md](./CONTRIBUTING.md) for the
contributor terms.

## Copyright

Copyright (c) 2026 Jonas Kaninda