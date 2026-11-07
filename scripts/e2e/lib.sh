# shellcheck shell=bash
# Sourced, never run; state crosses scripts through $E2E_STATE since each CI step is its own shell.

set -euo pipefail

E2E_STATE=${E2E_STATE:-${RUNNER_TEMP:-/tmp}/miabi-e2e.env}
MIABI_URL=${MIABI_URL:-http://127.0.0.1:9000}
IMAGE=${IMAGE:-miabi/miabi:e2e}

log() { printf '\n\033[1m==> %s\033[0m\n' "$*"; }
pass() { printf '  \033[32mPASS\033[0m %s\n' "$*"; }
fail() {
  printf '  \033[31mFAIL\033[0m %s\n' "$*" >&2
  exit 1
}

save() { printf '%s=%q\n' "$1" "$2" >>"$E2E_STATE"; }
load() {
  # shellcheck disable=SC1090
  [[ -f $E2E_STATE ]] && source "$E2E_STATE"
  return 0
}

# A fixed sleep either wastes minutes or flakes.
wait_until() {
  local what=$1 timeout=$2
  shift 2
  local deadline=$((SECONDS + timeout))
  until "$@" >/dev/null 2>&1; do
    if ((SECONDS >= deadline)); then
      fail "timed out after ${timeout}s waiting for: $what"
    fi
    sleep 2
  done
  pass "$what"
}

# api METHOD PATH [JSON] prints the response body; a non-2xx status fails the script with the body.
api() {
  local method=$1 path=$2 body=${3:-}
  local out code
  out=$(mktemp)
  local args=(-sS -X "$method" -o "$out" -w '%{http_code}' -H 'Content-Type: application/json')
  [[ -n ${TOKEN:-} ]] && args+=(-H "Authorization: Bearer $TOKEN")
  [[ -n $body ]] && args+=(-d "$body")
  code=$(curl "${args[@]}" "$MIABI_URL/api/v1$path")
  if [[ $code != 2* ]]; then
    printf '%s %s -> %s\n%s\n' "$method" "$path" "$code" "$(cat "$out")" >&2
    rm -f "$out"
    return 1
  fi
  cat "$out"
  rm -f "$out"
}

# api_status METHOD PATH [JSON] prints only the HTTP status, for asserting refusals.
api_status() {
  local method=$1 path=$2 body=${3:-}
  local args=(-sS -o /dev/null -X "$method" -w '%{http_code}' -H 'Content-Type: application/json')
  [[ -n ${TOKEN:-} ]] && args+=(-H "Authorization: Bearer $TOKEN")
  [[ -n $body ]] && args+=(-d "$body")
  curl "${args[@]}" "$MIABI_URL/api/v1$path"
}

# login ADMIN_EMAIL ADMIN_PASSWORD exports TOKEN. The field is `username` and the token is data.token.
login() {
  TOKEN=$(api POST /auth/login "$(jq -nc --arg u "$1" --arg p "$2" '{username: $u, password: $p}')" | jq -r '.data.token // empty')
  [[ -n $TOKEN ]] || fail "login returned no token"
  export TOKEN
}

healthy() { [[ $(docker inspect -f '{{.State.Health.Status}}' "$1" 2>/dev/null) == healthy ]]; }
