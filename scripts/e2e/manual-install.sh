#!/usr/bin/env bash
# A manual install: Postgres, Redis and the control plane by hand, 9000 published and
# no gateway, so the gateway is added afterwards the way the Admin Dashboard does it.
source "$(dirname "$0")/lib.sh"

: >"$E2E_STATE"
DB_PW=$(openssl rand -hex 16)
REDIS_PW=$(openssl rand -hex 16)
ADMIN_EMAIL=admin@e2e.test
ADMIN_PW="Mb!$(openssl rand -hex 12)"
# nip.io resolves every name under it to the address it spells, so app hostnames reach this runner
# without any DNS of our own.
APPS_DOMAIN=apps.127.0.0.1.nip.io
save ADMIN_EMAIL "$ADMIN_EMAIL"
save ADMIN_PW "$ADMIN_PW"
save APPS_DOMAIN "$APPS_DOMAIN"
save REDIS_PW "$REDIS_PW"

log "Network and data stores"
docker network create miabi-proxy >/dev/null
docker run -d --name miabi-postgres --network miabi-proxy \
  -e POSTGRES_USER=miabi -e POSTGRES_PASSWORD="$DB_PW" -e POSTGRES_DB=miabi \
  --health-cmd 'pg_isready -U miabi' --health-interval 2s \
  -v mb-platform-pgdata:/var/lib/postgresql/data postgres:17-alpine >/dev/null
docker run -d --name miabi-redis --network miabi-proxy \
  --health-cmd "redis-cli -a $REDIS_PW ping" --health-interval 2s \
  -v mb-platform-redisdata:/data redis:7-alpine redis-server --requirepass "$REDIS_PW" >/dev/null
wait_until "postgres healthy" 90 healthy miabi-postgres
wait_until "redis healthy" 60 healthy miabi-redis

log "Control plane ($IMAGE)"
docker run -d --name miabi --network miabi-proxy -p 9000:9000 \
  -e MIABI_ENV=production -e MIABI_PORT=9000 \
  -e MIABI_DB_HOST=miabi-postgres -e MIABI_DB_USER=miabi \
  -e MIABI_DB_PASSWORD="$DB_PW" -e MIABI_DB_NAME=miabi \
  -e MIABI_REDIS_ADDR=miabi-redis:6379 -e MIABI_REDIS_PASSWORD="$REDIS_PW" \
  -e MIABI_JWT_SECRET="$(openssl rand -hex 32)" -e MIABI_ENCRYPTION_KEY="$(openssl rand -hex 32)" \
  -e MIABI_ADMIN_EMAIL="$ADMIN_EMAIL" -e MIABI_ADMIN_PASSWORD="$ADMIN_PW" \
  -e MIABI_WEB_URL="$MIABI_URL" -e MIABI_CORS_ORIGINS="$MIABI_URL" \
  -e MIABI_CONTROL_URL="$MIABI_URL" \
  -e MIABI_PROXY_NETWORK=miabi-proxy \
  -e MIABI_EXTERNAL_BASE_DOMAIN="$APPS_DOMAIN" \
  -e MIABI_GOMA_PROVIDER_DIR=/etc/goma/providers \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -v mb-platform-gateway-providers:/etc/goma/providers \
  "$IMAGE" >/dev/null
wait_until "control plane answers /healthz" 180 curl -fsS "$MIABI_URL/healthz"
wait_until "control plane ready (database reachable)" 120 curl -fsS "$MIABI_URL/readyz"
