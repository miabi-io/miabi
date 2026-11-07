#!/usr/bin/env bash
# Dumps container state and platform logs so a failed run can be diagnosed without a rerun.
docker ps -a --format 'table {{.Names}}\t{{.Image}}\t{{.Status}}'
for c in miabi mb-node-gateway miabi-gateway miabi-postgres miabi-redis; do
  if docker inspect "$c" >/dev/null 2>&1; then
    printf '\n===== %s =====\n' "$c"
    docker logs --tail 300 "$c" 2>&1
  fi
done
