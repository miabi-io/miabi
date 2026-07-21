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
AGENT_VERSION="${AGENT_VERSION:-v0.3.0}"
# Docker tags carry no leading "v" (git tag v0.2.0 → image tag 0.2.0). The :latest
# fallback only applies if a caller deliberately blanks it (AGENT_VERSION= ).
AGENT_IMAGE_TAG="${AGENT_VERSION#v}"; AGENT_IMAGE_TAG="${AGENT_IMAGE_TAG:-latest}"

# --- config (env with flag overrides) -----------------------------------------
CONTROL_URL="${MIABI_CONTROL_URL:-${MIABI_API_URL:-}}"
NODE_TOKEN="${MIABI_NODE_TOKEN:-}"
AGENT_IMAGE="${MIABI_AGENT_IMAGE:-miabi/agent:${AGENT_IMAGE_TAG}}"
AGENT_NAME="${MIABI_AGENT_NAME:-miabi-agent}"
INSECURE="${MIABI_AGENT_INSECURE_SKIP_VERIFY:-false}"

while [ $# -gt 0 ]; do
  case "$1" in
    --control-url) CONTROL_URL="${2:-}"; shift 2 ;;
    --control-url=*) CONTROL_URL="${1#*=}"; shift ;;
    --token) NODE_TOKEN="${2:-}"; shift 2 ;;
    --token=*) NODE_TOKEN="${1#*=}"; shift ;;
    --image) AGENT_IMAGE="${2:-}"; shift 2 ;;
    --image=*) AGENT_IMAGE="${1#*=}"; shift ;;
    --name) AGENT_NAME="${2:-}"; shift 2 ;;
    --name=*) AGENT_NAME="${1#*=}"; shift ;;
    --insecure) INSECURE="true"; shift ;;
    -h|--help)
      sed -n '2,20p' "$0" | sed 's/^# \{0,1\}//'
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
docker run -d --name "$AGENT_NAME" --restart unless-stopped \
  -v /var/run/docker.sock:/var/run/docker.sock \
  --label io.miabi.part-of=miabi \
  --label io.miabi.role=agent \
  --label io.miabi.managed-by=external \
  --label io.miabi.protected=true \
  -e MIABI_CONTROL_URL="$CONTROL_URL" \
  -e MIABI_NODE_TOKEN="$NODE_TOKEN" \
  -e MIABI_AGENT_INSECURE_SKIP_VERIFY="$INSECURE" \
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
