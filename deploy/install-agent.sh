#!/usr/bin/env bash
#
# Miabi node-agent installer.
#   curl -fsSL https://get.miabi.io/agent | \
#     MIABI_CONTROL_URL=https://miabi.example.com MIABI_NODE_TOKEN=mbn_xxxxxxxx bash
#
# The agent is a thin Docker-socket proxy: it dials your Miabi control plane over
# an outbound WebSocket tunnel and relays Docker API calls to the local engine.
# It installs Docker Engine when it is missing (as root), then starts (or restarts) the agent
# container and verifies it is up.
#
# Configuration (environment or flags):
#   MIABI_CONTROL_URL   control plane base URL, e.g. https://miabi.example.com   (--control-url)
#   MIABI_NODE_TOKEN    the node's join token, mbn_…  (shown once at creation)   (--token)
#   AGENT_VERSION       agent release to install       (default: newest stable, from versions.miabi.io)
#   MIABI_AGENT_IMAGE   full agent image ref           (overrides AGENT_VERSION)     (--image)
#   MIABI_AGENT_NAME    container name                 (default miabi-agent)         (--name)
#   MIABI_AGENT_INSECURE_SKIP_VERIFY  skip control-plane TLS verification (default false)  (--insecure)
#   MIABI_SKIP_DOCKER_INSTALL  1 = never touch the host's packages; Docker must exist
#   DOCKER_HOST         local Docker endpoint          (default unix:///var/run/docker.sock)
#
set -euo pipefail

# === pretty output (matches install.sh) =============================
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

# === config (env with flag overrides) ===========================
CONTROL_URL="${MIABI_CONTROL_URL:-${MIABI_API_URL:-}}"
NODE_TOKEN="${MIABI_NODE_TOKEN:-}"
AGENT_IMAGE="${MIABI_AGENT_IMAGE:-}"
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
      if [ -r "$0" ]; then sed -n '2,21p' "$0" | sed 's/^# \{0,1\}//'
      else echo "See https://miabi.io/docs — run this script from a file for the full help."; fi
      exit 0 ;;
    *) die "unknown argument: $1 (see --help)" ;;
  esac
done

CONTROL_URL="${CONTROL_URL%/}" # trim trailing slash (the agent does this too)

# ====== prompt for anything still missing (interactive only) =======================
if [ -z "$CONTROL_URL" ] && [ -t 0 ]; then
  printf 'Control plane URL (e.g. https://miabi.example.com): '; read -r CONTROL_URL
  CONTROL_URL="${CONTROL_URL%/}"
fi
if [ -z "$NODE_TOKEN" ] && [ -t 0 ]; then
  printf 'Node join token (mbn_…): '; read -r NODE_TOKEN
fi

[ -n "$CONTROL_URL" ] || die "MIABI_CONTROL_URL is required (pass it as an env var or --control-url)."
[ -n "$NODE_TOKEN" ]  || die "MIABI_NODE_TOKEN is required (pass it as an env var or --token). Get it from the node page in the console."

# ========== agent version ==========================================
# The agent releases on its own cadence (0.x while the panel is 1.x), so it takes its own newest
# stable release from versions.miabi.io rather than the panel's version. Resolved before Docker is
# installed, so a failed lookup changes nothing on the host. Docker tags carry no leading "v".
if [ -z "$AGENT_IMAGE" ]; then
  [ "${AGENT_VERSION:-}" = "latest" ] && AGENT_VERSION=""
  if [ -z "${AGENT_VERSION:-}" ]; then
    _index="$(curl -fsSL --retry 3 --retry-delay 2 --max-time 20 https://versions.miabi.io/v1/index.json | tr -d ' \n\r\t')" || _index=""
    case "$_index" in
      *'"agent":{"stable":{'*)
        _entry="${_index#*\"agent\":\{\"stable\":\{}"
        AGENT_VERSION="$(printf '%s' "$_entry" | sed -n 's/^"version":"\([^"]*\)".*/\1/p')"
        ;;
    esac
    unset _index _entry
    [ -n "${AGENT_VERSION:-}" ] || die "could not determine the agent version from versions.miabi.io. Retry, or pin one: AGENT_VERSION=vX.Y.Z (current at https://versions.miabi.io/v1/index.json)."
  fi
  case "$AGENT_VERSION" in
    v[0-9]*.[0-9]*.[0-9]*|[0-9]*.[0-9]*.[0-9]*) ;;
    *) die "AGENT_VERSION='${AGENT_VERSION}' is not a version; use vX.Y.Z." ;;
  esac
  AGENT_IMAGE="miabi/agent:${AGENT_VERSION#v}"
fi
log "agent image ${AGENT_IMAGE}"

# ==== Docker: installed when missing, and running =================================
# Installing Docker needs root. Without it Docker must already be usable by this user.
SKIP_DOCKER_INSTALL="${MIABI_SKIP_DOCKER_INSTALL:-0}"
if [ "$(id -u)" -ne 0 ]; then
  command -v docker >/dev/null 2>&1 ||
    die "Docker is not installed, and installing it needs root. Install Docker, or re-run with sudo."
  docker info >/dev/null 2>&1 ||
    die "this user cannot use Docker. Add it to the \`docker\` group (then log in again), or re-run with sudo."
fi

# >>> shared: docker-install — identical in install.sh and install-agent.sh (scripts/check-installers.sh)
# Read one field out of /etc/os-release without leaking its vars into our shell.
os_release() { # <field>
  # shellcheck disable=SC1091
  ( . /etc/os-release 2>/dev/null && eval "printf '%s' \"\${$1:-}\"" ) || true
}
distro_id()    { printf '%s %s' "$(os_release ID)" "$(os_release VERSION_ID)"; }
# EL major version: VERSION_ID is "10.2" on AlmaLinux, "9" on Rocky. dnf's
# $releasever must be the major only, or the Docker repo URL 404s.
el_major()     { local v; v="$(os_release VERSION_ID)"; printf '%s' "${v%%.*}"; }

# EL rebuilds that Docker's centos/ repo serves but get.docker.com rejects,
# because it matches ID exactly. Deliberately an allowlist and NOT an ID_LIKE
# sniff: Amazon Linux and Fedora both set ID_LIKE=fedora, yet their VERSION_IDs
# (2023, 42) have no matching centos/<major>/ tree.
is_el_rebuild() {
  case "$(os_release ID)" in
    almalinux|rocky|ol|centos|rhel) return 0 ;;
    *) return 1 ;;
  esac
}

DOCKER_LOG="/var/log/miabi-docker-install.log"

# `podman-docker` installs a /usr/bin/docker shim that execs podman. It satisfies
# `command -v docker` but is not the Docker Engine API Miabi builds its stack
# against, so check what the binary actually *is* rather than that it exists.
docker_is_podman_shim() {
  command -v docker >/dev/null 2>&1 && docker --version 2>&1 | grep -qi podman
}
have_real_docker() {
  command -v docker >/dev/null 2>&1 && ! docker_is_podman_shim
}

# Exact, copy-pasteable commands for the detected distro. Printed on every path
# that gives up, so the user is never left with a bare docs link.
docker_install_instructions() {
  local id major
  id="$(os_release ID)"; major="$(el_major)"
  printf "\n    Install Docker Engine on %s, then re-run:\n\n" "$(distro_id)" >&2
  case "$id" in
    almalinux|rocky|ol|centos|rhel)
      cat >&2 <<-EOF
	      curl -fsSL https://download.docker.com/linux/centos/docker-ce.repo \\
	        -o /etc/yum.repos.d/docker-ce.repo
	      dnf -y --releasever=${major} install docker-ce docker-ce-cli containerd.io \\
	        docker-buildx-plugin
	      systemctl enable --now docker
	EOF
      ;;
    ubuntu|debian|raspbian)
      cat >&2 <<-EOF
	      curl -fsSL https://get.docker.com | sh
	      systemctl enable --now docker
	EOF
      ;;
    amzn)
      cat >&2 <<-EOF
	      dnf -y install docker
	      systemctl enable --now docker
	EOF
      ;;
    alpine)
      cat >&2 <<-EOF
	      apk add docker docker-cli
	      rc-update add docker default && service docker start
	EOF
      ;;
    opensuse*|sles)
      cat >&2 <<-EOF
	      zypper install -y docker
	      systemctl enable --now docker
	EOF
      ;;
    *)
      printf "      See https://docs.docker.com/engine/install/\n" >&2
      ;;
  esac
  printf "\n" >&2
}

# Docker CE from the official EL repo. Written as a plain .repo file rather than
# via `dnf config-manager --add-repo`: EL10 ships dnf5, which dropped that flag.
install_docker_el() {
  local major arch pm
  major="$(el_major)"
  arch="$(uname -m)"
  [ -n "$major" ] || die "could not determine the EL major version from /etc/os-release."

  if command -v dnf >/dev/null 2>&1; then pm=dnf
  elif command -v yum >/dev/null 2>&1; then pm=yum
  else die "neither dnf nor yum was found; cannot install Docker on this host."
  fi

  # Never point dnf at a repo that isn't there — fail with a clear reason instead.
  if ! curl -fsI --max-time 15 \
        "https://download.docker.com/linux/centos/${major}/${arch}/stable/" >/dev/null 2>&1; then
    warn "Docker publishes no EL${major}/${arch} packages."
    docker_install_instructions
    die "Docker Engine must be installed manually on this host."
  fi

  log "Installing Docker CE from Docker's EL${major} repository"
  curl -fsSL https://download.docker.com/linux/centos/docker-ce.repo \
    -o /etc/yum.repos.d/docker-ce.repo \
    || die "could not download Docker's EL repository definition."

  # No compose plugin: Miabi drives the Docker API directly and never shells out
  # to `docker compose`. Anyone who wants to run examples/compose/compose.yaml
  # themselves can add docker-compose-plugin after the fact.
  #
  # Pin $releasever: on AlmaLinux 10.2 it can expand to the full "10.2".
  if ! "$pm" -y --releasever="$major" install \
        docker-ce docker-ce-cli containerd.io \
        docker-buildx-plugin >>"$DOCKER_LOG" 2>&1; then
    printf "\n" >&2
    warn "dnf install of Docker CE failed. Last 20 lines of ${DOCKER_LOG}:"
    tail -n 20 "$DOCKER_LOG" >&2 || true
    printf "\n" >&2
    die "Docker could not be installed from the EL${major} repository."
  fi
}

install_docker() {
  local script
  script="$(mktemp)"
  : >"$DOCKER_LOG" 2>/dev/null || DOCKER_LOG="$(mktemp)"

  curl -fsSL https://get.docker.com -o "$script" \
    || die "could not download https://get.docker.com (network or DNS problem?)."

  # get.docker.com prints its fatal "Unsupported distribution" error on stdout,
  # not stderr — capture *both* streams or the failure is invisible.
  if sh "$script" </dev/null >>"$DOCKER_LOG" 2>&1; then
    rm -f "$script"
    return 0
  fi
  rm -f "$script"

  # It only knows ubuntu/debian/raspbian/centos/fedora/rhel/rocky/sles. Cover
  # the rest of the RHEL family (AlmaLinux, Oracle Linux, …) ourselves.
  if is_el_rebuild; then
    warn "get.docker.com does not support '$(os_release ID)' — using Docker's EL repo instead"
    install_docker_el
    return 0
  fi

  printf "\n" >&2
  warn "get.docker.com failed on '$(distro_id)'. Last 20 lines of ${DOCKER_LOG}:"
  tail -n 20 "$DOCKER_LOG" >&2 || true
  docker_install_instructions
  die "Docker could not be installed automatically."
}

if have_real_docker; then
  ok "Docker present ($(docker --version 2>/dev/null | awk '{print $3}' | tr -d ','))"
else
  # Name the podman shim explicitly — otherwise this surfaces later as a
  # baffling failure deep inside the install, against an API that is not Docker's.
  if docker_is_podman_shim; then
    warn "the 'docker' on this host is the podman-docker shim, not Docker Engine."
    warn "Miabi needs Docker Engine; remove podman-docker or install Docker alongside it."
  fi

  if [ "$SKIP_DOCKER_INSTALL" = "1" ]; then
    warn "MIABI_SKIP_DOCKER_INSTALL=1 — not touching this host's packages."
    docker_install_instructions
    die "Docker Engine is required but was not found."
  fi

  log "Docker not found — installing Docker Engine"
  install_docker
  ok "Docker installed"
fi

# Make sure the daemon is actually running (a fresh install often leaves it
# stopped/disabled).
if ! docker info >/dev/null 2>&1; then
  if command -v systemctl >/dev/null 2>&1; then
    log "Starting the Docker daemon"
    systemctl enable --now docker >/dev/null 2>&1 || true
  fi
  docker info >/dev/null 2>&1 || die "the Docker daemon is not running; start it and re-run."
fi

ok "Docker daemon is running"
# <<< shared: docker-install

# ======= resolve the Docker endpoint the agent must reach =========================
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

# === (re)create the agent container ====================================
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

# ===== verify it stayed up (a bad token/URL exits almost immediately) ===================
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
