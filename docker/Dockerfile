# syntax=docker/dockerfile:1
#
# Single image: builds the web UI (static export), embeds it into the Go binary
# (internal/web), and ships one self-contained executable that serves both the
# API and the UI — no static files at runtime.

# Build the UI on a glibc image, not alpine: Vite 8's rolldown bundler needs a
# native binding, and `npm ci` on musl trips the npm optional-deps bug
# (npm/cli#4828), failing to install @rolldown/binding-linux-x64-musl. glibc
# uses the well-supported -gnu binding. This is a throwaway builder stage (only
# /web/dist is embedded below), so the base has no effect on the final image.
FROM node:24-bookworm-slim AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
# API is same-origin behind the reverse proxy.
ENV VITE_API_URL=/api/v1
RUN npm run build

FROM golang:1.26.3 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# Stage the built UI where the embed directive (internal/web) picks it up, so the
# binary is self-contained.
COPY --from=web /web/dist ./internal/web/dist
ARG VERSION=dev
ARG COMMIT=unknown
# Edition knobs: GO_TAGS="enterprise" + a baked LICENSE_PUBLIC_KEY produce an
# Enterprise binary; both empty (the default) build Community. The
# embeddedPublicKey symbol only exists in the enterprise-tagged build, so the -X
# is a harmless no-op for a Community build.
ARG GO_TAGS=""
ARG LICENSE_PUBLIC_KEY=""
RUN CGO_ENABLED=0 go build -tags "${GO_TAGS}" \
    -ldflags "-s -w -X github.com/miabi-io/miabi/internal/config.Version=${VERSION} -X github.com/miabi-io/miabi/internal/config.CommitID=${COMMIT} -X github.com/miabi-io/miabi/internal/enterprise.embeddedPublicKey=${LICENSE_PUBLIC_KEY}" \
    -o /out/miabi ./cmd/miabi

FROM alpine:3.20
# Re-declare build args in this stage so they are in scope for the labels below.
ARG VERSION=dev
ARG COMMIT=unknown
LABEL org.opencontainers.image.title="Miabi" \
      org.opencontainers.image.description="The open-source, self-hosted Platform-as-a-Service (PaaS) for Docker — API + web UI in a single binary." \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}" \
      org.opencontainers.image.authors="Jonas Kaninda" \
      org.opencontainers.image.vendor="miabi-io" \
      org.opencontainers.image.url="https://github.com/miabi-io/miabi" \
      org.opencontainers.image.source="https://github.com/miabi-io/miabi" \
      org.opencontainers.image.documentation="https://github.com/miabi-io/miabi#readme" \
      org.opencontainers.image.licenses="AGPL-3.0-or-later"
# tzdata: without it Alpine has no zoneinfo database, and TZ is SILENTLY IGNORED —
# the container stays on UTC while the setting says otherwise. Postgres, Redis and
# Goma all ship it, so without this the stack's own logs are the only ones in the
# wrong timezone, which is the most confusing possible outcome. ~3 MB.
RUN apk add --no-cache ca-certificates tzdata \
    && addgroup -g 10001 miabi \
    && adduser -u 10001 -G miabi -s /sbin/nologin -D -H miabi \
    && mkdir -p /var/lib/miabi/logs /etc/goma/providers \
    && chown -R miabi:miabi /var/lib/miabi /etc/goma/providers
COPY --from=build /out/miabi /usr/local/bin/miabi
ENV MIABI_LOG_DIR=/var/lib/miabi/logs
EXPOSE 9000
# Default to root; override with `user: "10001:10001"` in your compose file (plus
# group_add for the host's docker GID) to run unprivileged. See examples/compose.
# For a purpose-built rootless image, use Dockerfile.debian instead.
ENTRYPOINT ["/usr/local/bin/miabi"]
CMD ["server"]