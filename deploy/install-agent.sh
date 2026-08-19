#!/usr/bin/env bash
#
# Miabi node-agent installer.
#   curl -fsSL https://get.miabi.io/agent | \
#     MIABI_CONTROL_URL=https://miabi.example.com MIABI_NODE_TOKEN=mbn_xxxxxxxx bash
#
# The agent is a thin Docker-socket proxy: it dials your Miabi control plane over
# an outbound WebSocket tunnel and relays Docker API calls to the local engine.
# This script does NOT install Docker — it only checks that Docker is present and
# running, then starts (or restarts) the agent container and verifies it is up.
#
# Configuration (environment or flags):
#   MIABI_CONTROL_URL   control plane base URL, e.g. https://miabi.example.com   (--control-url)
#   MIABI_NODE_TOKEN    the node's join token, mbn_…  (shown once at creation)   (--token)
#   AGENT_VERSION       agent image tag to pull        (stamped at release; else latest)
#   MIABI_AGENT_IMAGE   full agent image ref           (overrides AGENT_VERSION)     (--image)
#   MIABI_AGENT_NAME    container name                 (default miabi-agent)         (--name)
#   MIABI_AGENT_INSECURE_SKIP_VERIFY  skip control-plane TLS verification (default false)  (--insecure)
#   DOCKER_HOST         local Docker endpoint          (default unix:///var/run/docker.sock)
#
set -euo pipefail

# --- pretty output (matches install.sh) ---------------------------------------
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
  C_RESET='\033[0m'; C_CYAN='\033[1;36m'; C_GREEN='\033[1;32m'
  C_YELLOW='\033[1;33m'; C_RED='\033[1;31m'; C_DIM='\033[2m'
else
  C_RESET=''; C_CYAN=''; C_GREEN=''; C_YELLOW=''; C_RED=''; C_DIM=''
fi
_ts()  { date '+%H:%M:%S'; }
log()  { printf "${C_DIM}%s${C_RESET} ${C_CYAN}==>${C_RESET} %s\n" "$(_ts)" "$1"; }
ok()   { printf "${C_DIM}%s${C_RESET} ${C_GREEN}✓${C_RESET}   %s\n" "$(_ts)" "$1"; }
warn() { printf "${C_DIM}%s${C_RESET} ${C_YELLOW}!${C_RESET}   %s\n" "$(_ts)" "$1" >&2; }
die()  { printf "${C_DIM}%s${C_RESET} ${C_RED}✗${C_RESET}   %s\n" "$(_ts)" "$1" >&2; exit 1; }

# --- image pin ----------------------------------------------------------------
# The agent versions independently of the panel (agent 0.x while the panel is
# 1.x), so it carries its own pin rather than inheriting one.
AGENT_VERSION="${AGENT_VERSION:-v0.4.0}"
# Docker tags carry no leading "v" (git tag v0.2.0 → image tag 0.2.0). The :latest
# fallback only applies if a caller deliberately blanks it (AGENT_VERSION= ).
AGENT_IMAGE_TAG="${AGENT_VERSION#v}"; AGENT_IMAGE_TAG="${AGENT_IMAGE_TAG:-latest}"

# --- config (env with flag overrides) -----------------------------------------
CONTROL_URL="${MIABI_CONTROL_URL:-${MIABI_API_URL:-}}"
NODE_TOKEN="${MIABI_NODE_TOKEN:-}"
AGENT_IMAGE="${MIABI_AGENT_IMAGE:-miabi/agent:${AGENT_IMAGE_TAG}}"
AGENT_NAME="${MIABI_AGENT_NAME:-miabi-agent}"
INSECURE="${MIABI_AGENT_INSECURE_SKIP_VERIFY:-false}"

# A flag whose value is missing must say so. Without this, `shift 2` with nothing
# left to shift returns non-zero, `set -e` kills the script, and the operator gets
# exit 1 and a completely empty terminal.
need_value() { # <flag> <value>
  [ -n "${2:-}" ] || die "$1 needs a value (e.g. $1 <value>)."
}

while [ $# -gt 0 ]; do
  case "$1" in
    --control-url) need_value "$1" "${2:-}"; CONTROL_URL="$2"; shift 2 ;;
    --control-url=*) CONTROL_URL="${1#*=}"; shift ;;
    --token) need_value "$1" "${2:-}"; NODE_TOKEN="$2"; shift 2 ;;
    --token=*) NODE_TOKEN="${1#*=}"; shift ;;
    --image) need_value "$1" "${2:-}"; AGENT_IMAGE="$2"; shift 2 ;;
    --image=*) AGENT_IMAGE="${1#*=}"; shift ;;
    --name) need_value "$1" "${2:-}"; AGENT_NAME="$2"; shift 2 ;;
    --name=*) AGENT_NAME="${1#*=}"; shift ;;
    --insecure) INSECURE="true"; shift ;;
    -h|--help)
      # $0 is "bash" when piped from curl, so there is no file to read the header
      # out of; only print it when we are running from one.
      if [ -r "$0" ]; then sed -n '2,20p' "$0" | sed 's/^# \{0,1\}//'
      else echo "See https://miabi.io/docs — run this script from a file for the full help."; fi
      exit 0 ;;
    *) die "unknown argument: $1 (see --help)" ;;
  esac
done

CONTROL_URL="${CONTROL_URL%/}" # trim trailing slash (the agent does this too)

# --- prompt for anything still missing (interactive only) ---------------------
if [ -z "$CONTROL_URL" ] && [ -t 0 ]; then
  printf 'Control plane URL (e.g. https://miabi.example.com): '; read -r CONTROL_URL
  CONTROL_URL="${CONTROL_URL%/}"
fi
if [ -z "$NODE_TOKEN" ] && [ -t 0 ]; then
  printf 'Node join token (mbn_…): '; read -r NODE_TOKEN
fi

[ -n "$CONTROL_URL" ] || die "MIABI_CONTROL_URL is required (pass it as an env var or --control-url)."
[ -n "$NODE_TOKEN" ]  || die "MIABI_NODE_TOKEN is required (pass it as an env var or --token). Get it from the node page in the console."

# --- preflight: Docker must be present and running (never installed here) ------
if ! command -v docker >/dev/null 2>&1; then
  die "Docker is not installed. Install Docker Engine first (https://docs.docker.com/engine/install/), then re-run."
fi
if ! docker info >/dev/null 2>&1; then
  die "the Docker daemon is not reachable. Start Docker (and ensure this user can access the socket), then re-run."
fi
ok "Docker is installed and running"

# --- resolve the Docker endpoint the agent must reach -------------------------
# The agent reads DOCKER_HOST itself (agent/main.go), so honour it here rather
# than hardcoding the default socket. Binding /var/run/docker.sock on a host whose
# daemon lives elsewhere — rootless Docker at $XDG_RUNTIME_DIR, a custom path —
# makes Docker CREATE A DIRECTORY at that path and the agent then fails to dial
# it, which this script would have reported as a bad token or a network problem.
DOCKER_ENDPOINT="${DOCKER_HOST:-unix:///var/run/docker.sock}"
DOCKER_MOUNT=()
case "$DOCKER_ENDPOINT" in
  unix://*)
    sock="${DOCKER_ENDPOINT#unix://}"
    [ -S "$sock" ] || die "no Docker socket at ${sock} (from DOCKER_HOST=${DOCKER_ENDPOINT}). Point DOCKER_HOST at the real socket, or start Docker."
    # Bind it at the same path inside the container so DOCKER_HOST is valid there too.
    DOCKER_MOUNT=(-v "${sock}:${sock}")
    ;;
  tcp://*|http://*|https://*)
    # No socket to bind: the agent dials the daemon directly, but it does so from
    # inside a container, where localhost is not the host.
    case "$DOCKER_ENDPOINT" in
      *//localhost:*|*//127.0.0.1:*|*//\[::1\]:*)
        die "DOCKER_HOST=${DOCKER_ENDPOINT} points at localhost, which inside the agent container means the container itself. Use the host's address, or a unix:// socket."
        ;;
    esac
    warn "using DOCKER_HOST=${DOCKER_ENDPOINT} — the agent container must be able to reach it"
    ;;
  *) die "unsupported DOCKER_HOST=${DOCKER_ENDPOINT} (expected unix:// or tcp://)." ;;
esac

# --- (re)create the agent container -------------------------------------------
if docker ps -a --format '{{.Names}}' | grep -qx "$AGENT_NAME"; then
  warn "an existing '$AGENT_NAME' container was found — replacing it"
  docker rm -f "$AGENT_NAME" >/dev/null 2>&1 || true
fi

log "pulling agent image $AGENT_IMAGE"
docker pull "$AGENT_IMAGE" >/dev/null || die "failed to pull $AGENT_IMAGE"

log "starting the agent"
# Platform labels give the agent an identity Miabi recognizes on this node. Without
# them it looks like any other container: it would be offered in the node's "Import
# from Docker" list, and it could be stopped from the containers page — which is the
# one container whose removal makes the node unreachable to the control plane.
# managed-by=external: installed by hand here, so Miabi must not assume it may
# recreate it. See internal/docker/labels.go.
# The join token goes in via a 0600 env-file, not `-e`: a command line is world
# readable in `ps` for as long as `docker run` is executing, and this token is
# enough to enrol a node against the control plane.
ENV_FILE="$(mktemp)"
chmod 600 "$ENV_FILE"
trap 'rm -f "$ENV_FILE"' EXIT
cat > "$ENV_FILE" <<EOF
MIABI_CONTROL_URL=${CONTROL_URL}
MIABI_NODE_TOKEN=${NODE_TOKEN}
MIABI_AGENT_INSECURE_SKIP_VERIFY=${INSECURE}
DOCKER_HOST=${DOCKER_ENDPOINT}
EOF

docker run -d --name "$AGENT_NAME" --restart unless-stopped \
  "${DOCKER_MOUNT[@]}" \
  --label io.miabi.part-of=miabi \
  --label io.miabi.role=agent \
  --label io.miabi.managed-by=external \
  --label io.miabi.protected=true \
  --env-file "$ENV_FILE" \
  "$AGENT_IMAGE" >/dev/null

# --- verify it stayed up (a bad token/URL exits almost immediately) -----------
sleep 3
if [ "$(docker inspect -f '{{.State.Running}}' "$AGENT_NAME" 2>/dev/null || echo false)" != "true" ]; then
  warn "the agent container is not running. Recent logs:"
  docker logs --tail 20 "$AGENT_NAME" 2>&1 || true
  die "agent failed to start — check MIABI_CONTROL_URL / MIABI_NODE_TOKEN and the host's outbound network access."
fi

ok "agent '$AGENT_NAME' is running and dialing $CONTROL_URL"
printf '\n'
log "recent logs:"
docker logs --tail 8 "$AGENT_NAME" 2>&1 || true
printf '\n'
ok "Done. The node flips to 'connected' in the console once the tunnel is up."
echo "    Follow logs:  docker logs -f $AGENT_NAME"
echo "    Status:       docker ps --filter name=$AGENT_NAME"
