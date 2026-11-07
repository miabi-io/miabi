#!/usr/bin/env bash
source "$(dirname "$0")/lib.sh"
load
login "$ADMIN_EMAIL" "$ADMIN_PW"

log "Workspace"
WS_ID=$(api POST /workspaces '{"name":"e2e","display_name":"E2E"}' | jq -r '.data.id')
save WS_ID "$WS_ID"
# Unverified-domain routes render disabled; a privileged workspace skips that check.
api PATCH "/admin/workspaces/$WS_ID" '{"privileged":true}' >/dev/null
pass "workspace $WS_ID created and privileged"

log "Placement"
nodes=$(api GET "/workspaces/$WS_ID/location-nodes" | jq -r '[.data[].id] | join(",")')
[[ $nodes == "$MANAGER_ID" ]] || fail "location-nodes = [$nodes], want only the manager [$MANAGER_ID]"
pass "the default location lists only its own node"
[[ $(api_status GET "/workspaces/$WS_ID/location-nodes?location=nowhere") == 404 ]] || fail "an unknown location was not refused"
pass "an unknown location reads as not found"
[[ $(api_status GET /nodes) == 404 ]] || fail "the platform-wide node list is still served"
pass "no platform-wide node list"

log "Volume"
VOL_ID=$(api POST "/workspaces/$WS_ID/volumes" '{"name":"site","size_mb":128}' | jq -r '.data.id')
api POST "/workspaces/$WS_ID/volumes/$VOL_ID/expand" '{"size_mb":256}' >/dev/null
size=$(api GET "/workspaces/$WS_ID/volumes/$VOL_ID" | jq -r '.data.size_bytes')
[[ $size == $((256 * 1024 * 1024)) ]] || fail "expanded volume is $size bytes, want 256 MiB"
pass "volume expanded to 256 MiB"
[[ $(api_status POST "/workspaces/$WS_ID/volumes/$VOL_ID/expand" '{"size_mb":64}') == 400 ]] || fail "shrinking a volume was not refused"
pass "shrinking is refused"

log "Application"
APP_ID=$(api POST "/workspaces/$WS_ID/apps" "$(jq -nc '{
  name: "web", display_name: "web", source_type: "image", image: "nginx", tag: "alpine",
  ports: [{container_port: 80, protocol: "tcp", scheme: "http"}]}')" | jq -r '.data.id')
save APP_ID "$APP_ID"
api PUT "/workspaces/$WS_ID/apps/$APP_ID/volumes" "{\"volume_id\":$VOL_ID,\"path\":\"/srv\"}" >/dev/null
api POST "/workspaces/$WS_ID/apps/$APP_ID/deploy" '{}' >/dev/null
deployed() {
  local status
  status=$(api GET "/workspaces/$WS_ID/apps/$APP_ID/deployments" | jq -r '.data[0].status')
  [[ $status == failed ]] && fail "deployment failed"
  [[ $status == succeeded ]]
}
wait_until "deployment succeeded" 300 deployed

log "External access"
api PUT "/workspaces/$WS_ID/apps/$APP_ID/external-access" '{"ports":[80]}' >/dev/null
APP_HOST=$(api GET "/workspaces/$WS_ID/apps/$APP_ID/external-access" | jq -r '.data.ports[0].host // empty')
[[ $APP_HOST == *".$APPS_DOMAIN" ]] || fail "external host '$APP_HOST' is not under $APPS_DOMAIN"
save APP_HOST "$APP_HOST"
pass "app exposed as $APP_HOST"

log "Node import"
# Regression: a node with nothing of a kind to import must answer [] rather than null, or the page is blank.
api GET "/admin/nodes/$MANAGER_ID/importable" | jq -e '(.data.containers|type) == "array" and (.data.volumes|type) == "array" and (.data.networks|type) == "array"' >/dev/null ||
  fail "importable resources are not all lists"
pass "importable resources are lists"
