#!/usr/bin/env bash
# No public DNS on a runner, so ACME cannot issue and the gateway serves its default cert; hence curl -k.
source "$(dirname "$0")/lib.sh"

DOMAIN=miabi.127.0.0.1.nip.io

log "Install the stack ($IMAGE)"
docker run --rm -v /var/run/docker.sock:/var/run/docker.sock -v /etc/miabi:/etc/miabi "$IMAGE" install \
  --domain "$DOMAIN" --acme-email admin@e2e.test --admin-email admin@e2e.test --image "$IMAGE" --yes

log "Assertions"
for c in miabi miabi-gateway miabi-postgres miabi-redis; do
  wait_until "$c healthy" 180 healthy "$c"
done
panel() { [[ $(curl -sk -o /dev/null -w '%{http_code}' --resolve "$DOMAIN:443:127.0.0.1" "https://$DOMAIN/") == 200 ]]; }
wait_until "the panel answers 200 through the gateway" 120 panel
version=$(curl -sk --resolve "$DOMAIN:443:127.0.0.1" "https://$DOMAIN/api/v1/info" | jq -r '.data.version')
[[ $version == "$EXPECTED_VERSION" ]] || fail "/api/v1/info reports '$version', want '$EXPECTED_VERSION'"
pass "/api/v1/info reports $version"
