#!/usr/bin/env bash
# Each of these once failed silently, which is why it is asserted directly.
source "$(dirname "$0")/lib.sh"
load

redis() { docker exec miabi-redis redis-cli --no-auth-warning -a "$REDIS_PW" "$@"; }

log "The build under test is the one running"
# Without this the suite passes just as happily against the last released image.
version=$(curl -fsS "$MIABI_URL/api/v1/info" | jq -r '.data.version')
[[ $version == "$EXPECTED_VERSION" ]] || fail "/api/v1/info reports '$version', want '$EXPECTED_VERSION'"
pass "/api/v1/info reports $version"

log "Gateway on the manager"
healthy mb-node-gateway || fail "mb-node-gateway is not healthy"
pass "gateway running and healthy"
config=$(docker exec mb-node-gateway cat /etc/goma/goma.yml)
grep -q 'addr: miabi-redis:6379' <<<"$config" || fail "the manager's gateway does not use the platform Redis"
pass "gateway shares the platform Redis"
[[ -z $(docker ps -aq --filter name=mb-node-gateway-redis) ]] || fail "a per-node Redis was started on the manager"
pass "no per-node Redis"
grep -A4 'analytics:' <<<"$config" | grep -q 'stream: goma:analytics' || fail "analytics is not configured"
pass "analytics configured"
wait_until "Goma reports analytics enabled" 60 sh -c "docker logs mb-node-gateway 2>&1 | grep -qi 'analytics enabled'"
miabi_vol=$(docker inspect miabi --format '{{range .Mounts}}{{if eq .Destination "/etc/goma/providers"}}{{.Name}}{{end}}{{end}}')
gateway_vols=$(docker inspect mb-node-gateway --format '{{range .Mounts}}{{.Name}} {{end}}')
[[ -n $miabi_vol && " $gateway_vols " == *" $miabi_vol "* ]] ||
  fail "the gateway mounts [$gateway_vols], not Miabi's route volume $miabi_vol"
pass "gateway reads Miabi's own route volume ($miabi_vol)"

log "Traffic"
status() {
  curl -sk -o /dev/null -w '%{http_code}' -L --resolve "$APP_HOST:80:127.0.0.1" --resolve "$APP_HOST:443:127.0.0.1" "http://$APP_HOST/"
}
served() { [[ $(status) == 200 ]]; }
wait_until "gateway loaded the app's route" 120 sh -c "docker logs mb-node-gateway 2>&1 | grep -Eq 'routes(_count)?=[1-9]'"
wait_until "$APP_HOST answers 200 through the gateway" 120 served

log "Analytics"
# Goma publishes in 250 ms batches, so wait for the stream length to stop moving.
settled() {
  local last now
  last=$(redis XLEN goma:analytics)
  while sleep 1; do
    now=$(redis XLEN goma:analytics)
    [[ $now == "$last" ]] && break
    last=$now
  done
  echo "$now"
}
# An equality, not > 0: it catches sampling and double-publish regressions alike.
before=$(settled)
sent=10
for _ in $(seq "$sent"); do served || fail "a request to $APP_HOST failed"; done
after=$(settled)
((after - before == sent)) || fail "$((after - before)) events published for $sent requests"
pass "exactly $sent events published for $sent requests"
drained() {
  redis XINFO GROUPS goma:analytics | tr -d '\r' | awk '
    $1 == "name" { getline; name = $1 }
    $1 == "pending" { getline; if (name == "miabi-analytics" && $1 == 0) ok = 1 }
    END { exit !ok }'
}
wait_until "the miabi-analytics consumer drained the stream" 60 drained
