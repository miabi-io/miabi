#!/usr/bin/env bash
# Usage: component-changelog.sh <component> [since-ref]; since-ref defaults to the last version bump.
set -euo pipefail
cd "$(git rev-parse --show-toplevel)"
id="${1:?usage: $0 <component> [since-ref]}"
line=$(awk -v id="$id" '$1 == id' scripts/components/components.map)
[[ -z "$line" ]] && { echo "unknown component: $id" >&2; exit 1; }
read -r _ const paths <<<"$line"
since="${2:-$(git log -1 --format=%H -S"$const" -- internal/components/versions.go)}"
range="${since:+$since..}HEAD"
# shellcheck disable=SC2086
git log --no-merges --format='%h %s' "$range" -- $paths | grep -E '^[0-9a-f]+ (feat|fix|perf)(\(|!|:)' || echo "no feat/fix/perf changes since ${since:-the beginning}"
