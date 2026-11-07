#!/usr/bin/env bash
source "$(dirname "$0")/lib.sh"
load
login "$ADMIN_EMAIL" "$ADMIN_PW"

volume() {
  printf 'apiVersion: miabi.io/v1\nkind: Volume\nmetadata:\n  name: %s\nspec:\n  size: %s\n' "$1" "$2"
}
app() {
  printf 'apiVersion: miabi.io/v1\nkind: Application\nmetadata:\n  name: %s\nspec:\n  image: nginx:alpine\n%s  mounts:\n    - volume: %s\n      path: /data\n' "$1" "$2" "$3"
}
apply_body() { jq -nc --arg m "$1" --argjson dry "$2" '{manifests: $m, dry_run: $dry}'; }

# A plan refusal answers 400; a change refused while applying is listed under the result's failures.
expect_refused() {
  local out code
  out=$(mktemp)
  code=$(curl -sS -o "$out" -w '%{http_code}' -X POST -H "Authorization: Bearer $TOKEN" -H 'Content-Type: application/json' \
    -d "$(apply_body "$2" "${4:-true}")" "$MIABI_URL/api/v1/workspaces/$WS_ID/apply")
  if ! { [[ $code == 400 ]] && grep -q "$3" "$out"; } &&
    ! jq -e --arg n "$3" '[.data.failures[]?.error | select(contains($n))] | length > 0' "$out" >/dev/null; then
    fail "$1: got $code $(cat "$out")"
  fi
  rm -f "$out"
  pass "$1"
}

log "A bundle converges"
bundle="$(volume git-data 64Mi)
---
$(app git-web '' git-data)"
api POST "/workspaces/$WS_ID/apply" "$(apply_body "$bundle" false)" >/dev/null
api GET "/workspaces/$WS_ID/volumes" | jq -e '.data[] | select(.name == "git-data")' >/dev/null || fail "git-data was not created"
pass "volume and app created from the bundle"

log "The plan refuses what cannot converge"
# Capacity is checked where it is converged, so this one is a real apply.
expect_refused "shrinking a volume" "$(volume git-data 32Mi)" "can only be expanded" false
expect_refused "a replicated service on node-local storage" "$(volume git-cache 64Mi)
---
$(app git-svc "  deployment:
    runtime: service
    replicas: 3
" git-cache)" "node-local volume"
api GET "/workspaces/$WS_ID/volumes" | jq -e '[.data[] | select(.name == "git-cache")] | length == 0' >/dev/null ||
  fail "a refused plan still created git-cache"
pass "a refused plan created nothing"
