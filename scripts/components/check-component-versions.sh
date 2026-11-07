#!/usr/bin/env bash
# Warn-only: exits 0 so a missing version bump never blocks a merge.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
base="${BASE:-origin/dev}"
map="scripts/components/components.map"
versions="internal/components/versions.go"

changed=$(git diff --name-only "$base"...HEAD)
version_diff=$(git diff "$base"...HEAD -- "$versions")
warned=0
while read -r id const paths; do
  [[ -z "$id" || "$id" == \#* ]] && continue
  touched=""
  for p in $paths; do
    if grep -q "^$p" <<<"$changed"; then touched=1; fi
  done
  [[ -z "$touched" ]] && continue
  if ! grep -q "^+.*$const" <<<"$version_diff"; then
    msg="$id changed without a version bump: update $const in $versions (major: users must act, minor: feature, patch: fix)"
    if [[ -n "${GITHUB_ACTIONS:-}" ]]; then echo "::warning file=$versions::$msg"; else echo "warning: $msg"; fi
    warned=1
  fi
done <"$map"
[[ $warned -eq 0 ]] && echo "component versions: ok"
exit 0
