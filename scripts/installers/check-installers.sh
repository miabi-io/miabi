#!/usr/bin/env bash
# Checks both installers parse, and that the Docker-install block they share is identical:
# each is piped through `curl | bash`, so neither can source a common file at run time.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

block() { sed -n '/^# >>> shared: docker-install/,/^# <<< shared: docker-install/p' "$1"; }

for f in deploy/install.sh deploy/install-agent.sh; do
  bash -n "$f"
  [ -n "$(block "$f")" ] || { echo "$f: no shared docker-install block" >&2; exit 1; }
done

if ! diff <(block deploy/install.sh) <(block deploy/install-agent.sh); then
  echo "the shared docker-install block differs between deploy/install.sh and deploy/install-agent.sh" >&2
  exit 1
fi
echo "installers ok"
