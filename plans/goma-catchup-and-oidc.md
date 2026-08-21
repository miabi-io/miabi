# Goma Gateway catch-up and the OIDC middleware

Bring Miabi up to date with the current Goma Gateway, and expose its new OIDC
middleware as a first-class security policy.

## Where Miabi stands today

Miabi drives Goma through its file provider: `internal/proxy/goma.go` renders
routes and middlewares as YAML into Goma's watched directory, which Goma
hot-reloads. Which middleware types Miabi *curates* is decided by
`internal/mwcatalog` — one `Descriptor` per type drives validation, secret
encryption at rest, and the UI form. An uncatalogued type passes straight
through to Goma unchecked.

That last point matters for scoping: `type: oidc` already reaches Goma today if
someone types the rule by hand. What it does not get is a form, validation, or
encryption of the client secret. Everything below is about closing that, and
about absorbing the changes in Goma that alter existing behaviour.

Upstream changes this plan responds to:

- A new `oidc` middleware (discovery, PKCE, per-request state and nonce, session
  stores, logout, `claimsExpression`). `oauth`/`oauth2` remain as deprecated
  aliases.
- Forwarded headers (`X-Forwarded-For`, `X-Forwarded-Proto`) are now only
  believed when the request arrives from a configured trusted proxy.
- The HTTP cache keys on the caller, host and encoding, and no longer caches
  credentialed requests unless asked to.
- `jwtAuth` gained a `forward:` block; flat `forwardHeaders` is deprecated.
- Path patterns are matched case-insensitively; `claimsExpression` accepts
  single and double quotes as well as backticks.

---

## Phase 0 — absorb the breaking changes

These ship with the gateway bump or before it. Nothing else in this plan is
urgent; this is.

### 0.1 `redirectScheme` will redirect in a loop behind a TLS terminator

The highest-risk item. `internal/proxy/goma.go` gives the registry route
`mb-registry-https` (`scheme: https`, `permanent: true`), and
`internal/mwcatalog/presets.go` offers the same preset to users. Goma now only
honours `X-Forwarded-Proto` from a trusted proxy, so an install behind
Cloudflare, nginx or a cloud load balancer that has not enabled the gateway's
`proxy:` block will see the plaintext hop, decide the request is not HTTPS, and
redirect it — forever.

The shipped `pkg/stack/assets/goma.yml` has that block commented out. Its prose
already describes the new semantics ("a forwarded header is trusted only when
the connection comes from a trustedProxies source") — that was aspirational
before and is now literally true.

- [ ] Decide whether Miabi renders the `proxy:` block from its own settings
      instead of leaving it to hand-edited YAML. Miabi already knows its
      topology (edge → node gateway → app), and a `MIABI_TRUSTED_PROXIES`
      setting would make this one place rather than two. **Decision needed.**
- [ ] Uncomment a working default for the standard compose topology in
      `pkg/stack/assets/goma.yml` and `examples/compose/goma.yml`.
- [ ] Update the comment: an empty `trustedProxies` with `enabled: true` is now
      rejected at load, not silently permissive.
- [ ] Upgrade note, prominently placed.

### 0.2 `httpCache` stops caching authenticated responses

Requests carrying `Authorization`, `Proxy-Authorization` or `Cookie` now bypass
the cache unless `cachePrivateResponses` is set, in which case the key includes a
fingerprint of those credentials. Anyone whose hit rate drops to zero needs to be
able to find that switch.

- [ ] Add `cachePrivateResponses` to the `httpCache` descriptor in
      `internal/mwcatalog/catalog.go`, with help text explaining the trade-off.

### 0.3 Stored `paths` using the `/*` wildcard

Miabi's own internal middlewares already emit `/.*`. User rows in the database
may still hold `/admin/*`. Those keep working through Goma's wildcard fallback
and stay case-insensitive, so nothing breaks — but each logs a warning the first
time it is seen.

- [ ] Optional one-shot migration rewriting stored `paths` from `/x/*` to
      `/x/.*`.
- [ ] Mirror Goma's pattern guidance (unanchored, case-insensitive, wildcard
      limits) into the catalog help text for `paths`.

---

## Phase 1 — the `oidc` descriptor

A new entry in `internal/mwcatalog/catalog.go` under `CategoryAccess`.

| Group | Fields |
|---|---|
| Provider | `issuer`, `clientId`, `clientSecret` *(secret)*, `provider` *(enum: custom, google, github, gitlab, amazon, facebook)*, `endpoint` *(object: authUrl, tokenUrl, jwksUrl, userInfoUrl)*, `scopes`, `audience` |
| Flow | `callbackPath`, `logoutPath`, `postLoginRedirect`, `postLogoutRedirect`, `pkce` *(bool, default true)* |
| Session | `session` *(object: `store` enum cookie/memory/redis, `secret` **secret**, `ttl`, `idleTimeout`, `cookie` object: name, path, domain, `sameSite` enum, `secure`)* |
| Authorization | `claimsExpression`, `claimsSource` |
| Forwarding | `forward` *(object: `headers`/`query`/`cookies` maps, `stripInbound`, `arraySeparator`, `encoding` enum, `maxValueBytes`, `accessTokenHeader`, `idTokenHeader`)* |

The `Validate` hook carries what the field loop cannot express, mirroring Goma's
own load-time rules:

- `issuer`, or both `endpoint.authUrl` and `endpoint.tokenUrl`.
- `issuer`, or `endpoint.jwksUrl`, or `endpoint.userInfoUrl` — without one of
  them Goma cannot verify a token and refuses to guard the route at all.
- `ttl` and `idleTimeout` parse as durations.
- `claimsSource` entries are one of `id_token`, `userinfo`, `access_token`.
- `session.cookie.sameSite` is one of `lax`, `strict`, `none`.

- [ ] Descriptor and field help text.
- [ ] `Validate` hook.
- [ ] Decide whether to offer `session.store: redis` at all — Goma fails the
      middleware at load when Redis is not configured on that gateway, and Miabi
      would need to know the workspace gateway's Redis state to validate it
      honestly. **Decision needed.**

---

## Phase 2 — nested secrets

`internal/mwcatalog/secrets.go` walks top-level fields plus the `basicAuth`
users special case. `session.secret` sits one level down, so as things stand it
would be **stored in plaintext, returned unredacted by the API, and wiped** by
any edit that round-trips the redaction sentinel.

`clientSecret` is top-level and works with the existing machinery. It is only the
session key that needs this.

- [ ] Teach `transformSecrets` to reach nested fields — either dotted keys on
      `Field`, or recursion into `FieldObject` children marked `Secret`.
- [ ] Same for `Redact` and `MergeKeptSecrets`.
- [ ] Tests for all three paths: encrypt/decrypt round trip, redaction in API
      responses, and an edit that keeps the stored value.

**This lands before Phase 1 ships.** A descriptor without it writes client
secrets to the database in the clear.

---

## Phase 3 — catch up the other descriptors

- [ ] Add the `forward` object to the `jwtAuth` descriptor (headers, query,
      cookies, stripInbound, arraySeparator, encoding, maxValueBytes). Keep the
      flat `forwardHeaders` for existing rows, marked deprecated in its `Help`;
      migrate on write if convenient.
- [ ] Update `claimsExpression` help: single and double quotes now parse, so the
      documented `Equals('email_verified', true)` form finally works.

---

## Phase 4 — presets and UI

`web/src/components/MiddlewareField.vue` renders itself recursively for object
and list sub-fields, so `session.cookie` two levels deep comes for free. Two
things do not.

- [ ] An SSO preset in `internal/mwcatalog/presets.go`, alongside the basicAuth
      and rateLimit ones.
- [ ] On `web/src/views/networking/MiddlewareDetail.vue`, show the exact redirect
      URI to register with the identity provider, computed from the route's host
      and `callbackPath`. This is the most common OIDC setup failure, and Miabi
      is the only thing that knows both halves.

---

## Phase 5 — version pin and compatibility

A route referencing `type: oidc` fails to load on a gateway that predates it.
Miabi pins Goma in five places, three of which default to `:latest`:

| Location | Current |
|---|---|
| `internal/config/config.go` (`MIABI_NODE_GATEWAY_IMAGE`) | `jkaninda/goma-gateway:latest` |
| `pkg/stack/stack.go` (`DefaultGatewayImage`) | `jkaninda/goma-gateway:latest` |
| `internal/services/platformimage/platformimage.go` | `jkaninda/goma-gateway:latest` |
| `examples/compose/compose.yaml` | `jkaninda/goma-gateway:latest` |
| `.github/workflows/release.yml` (`GOMA_VERSION`) | `v0.11.0` |

- [ ] Bump all five together.
- [ ] Add a minimum-Goma-version constant so the catalog can hide `oidc`, or warn
      on it, when the pinned gateway is older.

---

## Phase 6 — tests

- [ ] `internal/proxy/goma_test.go`: golden render of an `oidc` middleware.
- [ ] `internal/mwcatalog/catalog_test.go`: the `Validate` cases above, each
      asserting the rule Goma itself would enforce.
- [ ] `internal/mwcatalog` secrets tests for the nested path (Phase 2).
- [ ] A regression on the rendered registry config — the thing Phase 0.1 could
      quietly break.

---

## Sequencing

1. **Phase 2** — independent, and a prerequisite for shipping Phase 1 safely.
2. **Phase 1 + 5** together — the descriptor is useless against a gateway that
   does not have the type.
3. **Phase 0** with the gateway bump.
4. **Phases 3, 4, 6** in any order.

## Open decisions

1. Does Miabi own the gateway's `proxy:`/`trustedProxies` block, rendered from
   its own settings, or does it stay hand-edited YAML? (Phase 0.1)
2. Is `session.store: redis` offered in the UI, given Miabi cannot currently
   verify the workspace gateway has Redis? (Phase 1)
