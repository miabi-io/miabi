#!/usr/bin/env bash
#
# Miabi one-line installer.
#   curl -fsSL https://github.com/miabi-io/miabi/releases/latest/download/install.sh | sudo bash
#
# Installs Docker if missing, fetches the production stack (compose.yaml +
# goma.yml) into /opt/miabi, generates secrets into .env, and brings it up.
# Re-running is safe: an existing .env is kept (only still-blank secrets are
# filled) and the stack is just updated.
#
# Run it from a checkout (deploy/install.sh) and it copies the local files
# instead of downloading them.
#
# Environment overrides:
#   MIABI_DIR                   install directory              (default /opt/miabi)
#   MIABI_VERSION               Miabi release to install       (default: stamped)
#   GOMA_VERSION                Goma Gateway release           (default: stamped)
#   RUNNER_VERSION              miabi/runner release           (default: stamped)
#   MIABI_RAW                   base URL for remote file fetch (default: the tag)
#   MIABI_NO_START              set to 1 to configure but not `up -d`
#   MIABI_SKIP_DOCKER_INSTALL   set to 1 to never touch the host's packages;
#                               Docker + Compose v2 must already be present
#   MIABI_DOMAIN                panel domain; skips the prompt
#   MIABI_ACME_EMAIL            Let's Encrypt email; skips the prompt
#   MIABI_ADMIN_EMAIL           first admin's login (default: MIABI_ACME_EMAIL)
#   MIABI_ADMIN_PASSWORD        first admin's password (default: generated)
#   ASSUME_YES                  set to 1 to skip interactive prompts (placeholders)
#
# Answering the prompts over a pipe is unreliable, so prefer passing the values:
#   curl -fsSL .../install.sh | sudo MIABI_DOMAIN=miabi.example.com \
#     MIABI_ACME_EMAIL=you@example.com bash
#
# Install or downgrade to a specific release:
#   curl -fsSL .../install.sh | sudo MIABI_VERSION=v1.1.0 bash

# This script is bash (pipefail, ERR traps, BASH_SOURCE, local). Invoked as
# `sh install.sh` the shebang is ignored: under dash that is `set: Illegal option
# -o pipefail` and an immediate exit before a single line of output. Re-exec
# under bash so the invocation does not matter. Keep this block POSIX-clean and
# above `set -o pipefail`, or dash dies before reaching it.
if [ -z "${BASH_VERSION:-}" ]; then
  if command -v bash >/dev/null 2>&1; then
    exec bash "$0" "$@"
  fi
  echo "miabi: this installer needs bash. Install it, then: sudo bash $0" >&2
  exit 1
fi

set -euo pipefail

INSTALL_DIR="${MIABI_DIR:-/opt/miabi}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" && pwd)"
START_STACK="${MIABI_NO_START:-0}"
SKIP_DOCKER_INSTALL="${MIABI_SKIP_DOCKER_INSTALL:-0}"

# ── versions ─────────────────────────────────────────────────────────────────
# CI rewrites these two assignments at release time (release.yml), so a released
# install.sh pins the exact images that release was tested with. An unstamped
# copy — a git checkout, or main — keeps the placeholders and falls back to the
# main branch and :latest.
#
# The `#__` test deliberately avoids spelling the full placeholder token, or the
# release-time sed would substitute the check along with the assignment.
MIABI_VERSION="${MIABI_VERSION:-__MIABI_VERSION__}"
GOMA_VERSION="${GOMA_VERSION:-__GOMA_VERSION__}"
RUNNER_VERSION="${RUNNER_VERSION:-__RUNNER_VERSION__}"
[ "${MIABI_VERSION#__}" = "$MIABI_VERSION" ] || MIABI_VERSION=""
[ "${GOMA_VERSION#__}" = "$GOMA_VERSION" ] || GOMA_VERSION=""
[ "${RUNNER_VERSION#__}" = "$RUNNER_VERSION" ] || RUNNER_VERSION=""

# Docker tags carry no leading "v" (git tag v1.2.3 → image tag 1.2.3) across all
# three images. An unpinned build tracks :latest.
#
# miabi/runner versions independently of miabi/miabi (runner 0.0.x while the
# panel is 1.0.x), so it carries its own stamped version — deriving its tag from
# MIABI_VERSION would ask for an image that was never published.
MIABI_IMAGE_TAG="${MIABI_VERSION#v}";   MIABI_IMAGE_TAG="${MIABI_IMAGE_TAG:-latest}"
GOMA_IMAGE_TAG="${GOMA_VERSION#v}";     GOMA_IMAGE_TAG="${GOMA_IMAGE_TAG:-latest}"
RUNNER_IMAGE_TAG="${RUNNER_VERSION#v}"; RUNNER_IMAGE_TAG="${RUNNER_IMAGE_TAG:-latest}"

# Fetch the stack files from the SAME release as the images, so compose.yaml and
# the image it references can never disagree. Unstamped falls back to main.
if [ -n "$MIABI_VERSION" ]; then
  DEFAULT_RAW="https://raw.githubusercontent.com/miabi-io/miabi/refs/tags/${MIABI_VERSION}/deploy"
else
  DEFAULT_RAW="https://raw.githubusercontent.com/miabi-io/miabi/main/deploy"
fi
REPO_RAW="${MIABI_RAW:-$DEFAULT_RAW}"

# How to re-run for an update: a local path when run from a checkout, otherwise
# the canonical one-liner (a piped `curl | bash` has no re-runnable path). The
# release asset always resolves to the newest release, so it is the update path.
if [ -f "${SCRIPT_DIR}/install.sh" ]; then
  UPDATE_HINT="sudo ${SCRIPT_DIR}/install.sh"
else
  UPDATE_HINT="curl -fsSL https://github.com/miabi-io/miabi/releases/latest/download/install.sh | sudo bash"
fi

# ── logging ──────────────────────────────────────────────────────────────────
if [ -t 1 ]; then
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

on_error() {
  local code=$?
  printf "\n${C_RED}Installation failed${C_RESET} (exit %s) at line %s.\n" "$code" "${1:-?}" >&2
  # Only suggest compose logs once there is a stack to read them from.
  if command -v docker >/dev/null 2>&1 && [ -f "${INSTALL_DIR}/compose.yaml" ]; then
    printf "  • Inspect logs:   cd %s && docker compose logs\n" "$INSTALL_DIR" >&2
  fi
  printf "  • Re-run safely:  %s\n" "$UPDATE_HINT" >&2
  exit "$code"
}
trap 'on_error $LINENO' ERR

# ── preflight checks ─────────────────────────────────────────────────────────
log "Running preflight checks"

[ "$(id -u)" -eq 0 ] || die "please run as root (or via sudo)."

case "$(uname -s)" in
  Linux) : ;;
  *) die "this installer supports Linux only (detected $(uname -s))." ;;
esac

case "$(uname -m)" in
  x86_64|amd64|aarch64|arm64) : ;;
  *) warn "unsupported CPU architecture '$(uname -m)'; images may be unavailable." ;;
esac

command -v curl >/dev/null 2>&1 || die "curl is required but not installed."
ok "Host looks good ($(uname -s)/$(uname -m), root)"

# ── docker ───────────────────────────────────────────────────────────────────
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

# `podman-docker` installs a /usr/bin/docker shim that execs podman and ships no
# Compose v2 plugin. It satisfies `command -v docker`, so check what the binary
# actually *is* rather than that it merely exists.
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
  printf "\n    Install Docker Engine + Compose v2 on %s, then re-run:\n\n" "$(distro_id)" >&2
  case "$id" in
    almalinux|rocky|ol|centos|rhel)
      cat >&2 <<-EOF
	      curl -fsSL https://download.docker.com/linux/centos/docker-ce.repo \\
	        -o /etc/yum.repos.d/docker-ce.repo
	      dnf -y --releasever=${major} install docker-ce docker-ce-cli containerd.io \\
	        docker-buildx-plugin docker-compose-plugin
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
	      dnf -y install docker docker-compose-plugin
	      systemctl enable --now docker
	EOF
      ;;
    alpine)
      cat >&2 <<-EOF
	      apk add docker docker-cli-compose
	      rc-update add docker default && service docker start
	EOF
      ;;
    opensuse*|sles)
      cat >&2 <<-EOF
	      zypper install -y docker docker-compose
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

  # Pin $releasever: on AlmaLinux 10.2 it can expand to the full "10.2".
  if ! "$pm" -y --releasever="$major" install \
        docker-ce docker-ce-cli containerd.io \
        docker-buildx-plugin docker-compose-plugin >>"$DOCKER_LOG" 2>&1; then
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
  # baffling "Compose v2 is required" error.
  if docker_is_podman_shim; then
    warn "the 'docker' on this host is the podman-docker shim, not Docker Engine."
    warn "Miabi needs Docker Engine and the Compose v2 plugin; remove podman-docker or install Docker alongside it."
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

if ! docker compose version >/dev/null 2>&1; then
  warn "the Compose v2 plugin is missing (a legacy standalone 'docker-compose' does not count)."
  docker_install_instructions
  die "Docker Compose v2 is required."
fi
ok "Docker daemon is running, Compose v2 available"

# Warn (don't block) when the web ports are already taken — Goma binds 80/443.
port_busy() { # <port>
  if command -v ss >/dev/null 2>&1; then ss -ltn 2>/dev/null | awk '{print $4}' | grep -qE "[:.]$1\$"
  elif command -v lsof >/dev/null 2>&1; then lsof -iTCP:"$1" -sTCP:LISTEN >/dev/null 2>&1
  else return 1; fi
}
for p in 80 443; do
  if port_busy "$p"; then
    warn "port $p is already in use; Goma needs 80 and 443 for HTTP + TLS."
  fi
done

# ── install dir + files ──────────────────────────────────────────────────────
log "Setting up ${INSTALL_DIR}"
mkdir -p "${INSTALL_DIR}/goma"

# Fetch a file from the local checkout when available, otherwise from the repo.
fetch() { # <relative-path> <dest>
  if [ -f "${SCRIPT_DIR}/$1" ]; then
    cp "${SCRIPT_DIR}/$1" "$2"
  else
    curl -fsSL "${REPO_RAW}/$1" -o "$2" || die "failed to download $1 from ${REPO_RAW}"
  fi
}

cd "${INSTALL_DIR}"
fetch "compose.yaml"  "compose.yaml"
fetch "goma/goma.yml" "goma/goma.yml"
fetch ".env.example"  ".env.example"
ok "Stack files in place"

# Generate a 32-byte hex secret; prefer openssl, fall back to /dev/urandom.
rand() {
  if command -v openssl >/dev/null 2>&1; then
    openssl rand -hex 32
  else
    LC_ALL=C tr -dc 'a-f0-9' </dev/urandom | head -c 64
  fi
}

# .env helpers.
#
# get_kv MUST NOT fail when the key is absent — an unset key is a normal state,
# not an error. Optional settings ship COMMENTED OUT in .env.example (e.g.
# MIABI_NETWORK_CIDR), so grep finds nothing and exits 1; under `set -euo
# pipefail` that failure propagates out of the command substitution and trips the
# ERR trap, killing the install. The trailing `|| true` makes "absent" mean
# "empty string", which is what every caller already assumes via `${x:-default}`.
get_kv() { grep -E "^$1=" .env 2>/dev/null | head -n1 | cut -d= -f2- || true; }
set_kv() { sed -i "s|^$1=.*|$1=$2|" .env; }
# Fill a key only when its current value is empty (idempotent self-heal).
fill_if_blank() { [ -z "$(get_kv "$1")" ] && set_kv "$1" "$2" || true; }

# Resolve the host's docker group GID so the non-root container can read the
# Docker socket.
docker_gid() {
  local gid
  gid="$(stat -c '%g' /var/run/docker.sock 2>/dev/null || true)"
  [ -n "$gid" ] || gid="$(getent group docker 2>/dev/null | cut -d: -f3)"
  printf '%s' "${gid:-999}"
}

# ── prompt (interactive only) ────────────────────────────────────────────────
# A pre-set environment variable always wins: over a pipe the tty is shared with
# whatever is still typing into it, so reads there are racy. Only when the value
# is unset do we ask, and only with a real tty. Otherwise keep the placeholder
# and warn at the end.
prompt() { # <env-var> <question> <default>
  local preset ans=""
  eval "preset=\${$1:-}"
  if [ -n "$preset" ]; then printf '%s' "$preset"; return 0; fi

  if [ "${ASSUME_YES:-0}" != "1" ] && [ -r /dev/tty ]; then
    printf "    ${C_CYAN}?${C_RESET} %s [%s]: " "$2" "$3" > /dev/tty
    read -r ans < /dev/tty || ans=""
  fi
  printf '%s' "${ans:-$3}"
}

# Yes/no variant. A pre-set env var wins; non-interactive answers "no".
prompt_yn() { # <env-var> <question>
  local preset ans=""
  eval "preset=\${$1:-}"
  if [ -n "$preset" ]; then
    case "$preset" in true|1|yes|y|YES|Y) return 0 ;; *) return 1 ;; esac
  fi
  [ "${ASSUME_YES:-0}" != "1" ] && [ -r /dev/tty ] || return 1
  printf "    ${C_CYAN}?${C_RESET} %s [y/N]: " "$2" > /dev/tty
  read -r ans < /dev/tty || ans=""
  case "$ans" in y|Y|yes|YES) return 0 ;; *) return 1 ;; esac
}

# Set a key, appending when .env only carries it as a comment (as .env.example
# does for every optional setting) — a sed rewrite would silently match nothing.
set_or_append() { # <key> <value>
  if grep -qE "^$1=" .env; then set_kv "$1" "$2"; else printf '%s=%s\n' "$1" "$2" >> .env; fi
}

fresh=0
registry_host=""
if [ ! -f .env ]; then
  fresh=1
  log "Generating .env with fresh secrets"
  cp .env.example .env
  chmod 600 .env

  domain="$(prompt MIABI_DOMAIN 'Panel domain' 'miabi.example.com')"
  email="$(prompt MIABI_ACME_EMAIL "Let's Encrypt email" 'admin@example.com')"
  set_kv MIABI_DOMAIN  "$domain"
  set_kv MIABI_WEB_URL "https://$domain"
  set_kv MIABI_ACME_EMAIL "$email"

  # The admin is seeded on first boot and Miabi refuses to start outside dev on
  # an empty or default password, so it must always be set. Default the login to
  # the ACME email rather than leaving the example address in place.
  set_kv MIABI_ADMIN_EMAIL "${MIABI_ADMIN_EMAIL:-$email}"

  # Built-in OCI registry. Only written when enabled: any non-empty registry var
  # is a one-way override that pins the setting out of the UI's reach. Declining
  # leaves the keys absent, so the registry stays UI-managed.
  if prompt_yn MIABI_REGISTRY_ENABLED 'Enable the built-in container registry?'; then
    # Validate: this host gets a public DNS record and its own TLS certificate, so
    # a bad value (notably a stray "y" carried over from the previous y/N prompt)
    # would have the gateway request a certificate for a nonsense name. Re-ask on
    # a tty; fall back to the default when there is nobody to ask.
    registry_host=""
    for _ in 1 2 3; do
      registry_host="$(prompt MIABI_REGISTRY_HOST 'Registry host' "registry.${domain}")"
      case "$registry_host" in
        *.*.*|*.*) case "$registry_host" in
                     *[!a-zA-Z0-9.-]*|-*|.*|*.) ;;   # illegal chars / bad edges
                     *) break ;;                     # looks like a hostname
                   esac ;;
      esac
      warn "'${registry_host}' is not a valid hostname (expected something like registry.${domain})"
      unset MIABI_REGISTRY_HOST                      # so prompt() asks again
      [ -r /dev/tty ] && [ "${ASSUME_YES:-0}" != "1" ] || { registry_host="registry.${domain}"; break; }
      registry_host=""
    done
    [ -n "$registry_host" ] || registry_host="registry.${domain}"

    set_or_append MIABI_REGISTRY_ENABLED true
    set_or_append MIABI_REGISTRY_HOST "$registry_host"
    ok "Registry enabled (host: ${registry_host})"
  fi

  set_kv DOCKER_GID "$(docker_gid)"
  ok "Created .env (domain: $(get_kv MIABI_DOMAIN))"
else
  log "Existing .env found — keeping it (filling only blank secrets)"
fi

# Always ensure the required secrets and docker GID are populated, even on an
# upgrade from an older .env that pre-dates a setting.
for k in MIABI_DB_PASSWORD MIABI_REDIS_PASSWORD MIABI_JWT_SECRET MIABI_ENCRYPTION_KEY; do
  if [ -z "$(get_kv "$k")" ]; then
    fill_if_blank "$k" "$(rand)"
    [ "$fresh" -eq 1 ] || warn "filled previously-empty ${k} with a new secret"
  fi
done

# An .env written before the admin keys existed has neither line to rewrite;
# append rather than sed, or compose fails interpolation on MIABI_ADMIN_PASSWORD.
grep -qE '^MIABI_ADMIN_EMAIL=' .env || printf 'MIABI_ADMIN_EMAIL=%s\n' "${MIABI_ADMIN_EMAIL:-$(get_kv MIABI_ACME_EMAIL)}" >> .env
grep -qE '^MIABI_ADMIN_PASSWORD=' .env || printf 'MIABI_ADMIN_PASSWORD=\n' >> .env

admin_generated=0
if [ -z "$(get_kv MIABI_ADMIN_PASSWORD)" ]; then
  if [ -n "${MIABI_ADMIN_PASSWORD:-}" ]; then
    set_kv MIABI_ADMIN_PASSWORD "$MIABI_ADMIN_PASSWORD"
  else
    # 32 hex chars: comfortably past the default-password check, still pasteable.
    set_kv MIABI_ADMIN_PASSWORD "$(rand | cut -c1-32)"
    admin_generated=1
  fi
  [ "$fresh" -eq 1 ] || warn "filled previously-empty MIABI_ADMIN_PASSWORD"
fi

if [ -z "$(get_kv DOCKER_GID)" ]; then set_kv DOCKER_GID "$(docker_gid)"; fi
ok "Secrets ready"

# ── image pins ───────────────────────────────────────────────────────────────
# Re-running the installer is how you upgrade, so the pins must move with it.
# A value pointing at some other repository is a deliberate override (a private
# mirror, a locally built image) and is never rewritten.
pin_image() { # <key> <repo> <tag>
  local cur; cur="$(get_kv "$1")"
  case "$cur" in
    ""|"$2":*) set_or_append "$1" "$2:$3" ;;
    *) warn "$1 points at a custom image ($cur) — leaving it untouched" ;;
  esac
}
pin_image MIABI_IMAGE  miabi/miabi            "$MIABI_IMAGE_TAG"
pin_image GOMA_IMAGE   jkaninda/goma-gateway  "$GOMA_IMAGE_TAG"
pin_image RUNNER_IMAGE miabi/runner           "$RUNNER_IMAGE_TAG"

if [ "$MIABI_IMAGE_TAG" = latest ]; then
  warn "unstamped installer — pinning images to :latest (a release build pins exact versions)."
else
  ok "Pinned miabi/miabi:${MIABI_IMAGE_TAG}, jkaninda/goma-gateway:${GOMA_IMAGE_TAG}, miabi/runner:${RUNNER_IMAGE_TAG}"
fi

# ── shared app network ───────────────────────────────────────────────────────
# Goma and every routed app/database container share the `miabi` bridge. Create
# it up front as an EXTERNAL network with a roomy, controllable CIDR (compose
# references it as external), so it survives `compose down` and never falls back
# to Docker's small default address pool — which caps a plain network's IP space
# and how many networks a host can have. Per-workspace/db/stack networks use a
# separate managed pool (MIABI_NETWORK_POOL_CIDR, default 10.64.0.0/12).
ensure_app_network() {
  local cidr; cidr="$(get_kv MIABI_NETWORK_CIDR)"; cidr="${cidr:-10.63.0.0/16}"
  if docker network inspect miabi >/dev/null 2>&1; then
    ok "Docker network 'miabi' already exists (leaving its CIDR unchanged)"
  elif docker network create --driver bridge --subnet "$cidr" miabi >/dev/null 2>&1; then
    ok "Created Docker network 'miabi' (${cidr})"
  else
    warn "could not create 'miabi' with subnet ${cidr} (in use? overlaps a route?); creating with Docker defaults"
    docker network create miabi >/dev/null 2>&1 || die "failed to create the 'miabi' network"
  fi
}
ensure_app_network

# ── start ────────────────────────────────────────────────────────────────────
if [ "${START_STACK}" = "1" ]; then
  log "MIABI_NO_START set — skipping startup."
  ok "Configured ${INSTALL_DIR}. Start with: cd ${INSTALL_DIR} && docker compose up -d"
  exit 0
fi

log "Pulling images (this can take a minute)…"
docker compose pull >/dev/null 2>&1 && ok "Images pulled" \
  || warn "image pull reported a problem; continuing (compose will pull on up)"

log "Starting Miabi…"
docker compose up -d

# Wait for the main container to come up so we can report a real status.
log "Waiting for the panel to come up…"
healthy=0
for _ in $(seq 1 60); do
  cid="$(docker compose ps -q miabi 2>/dev/null || true)"
  if [ -n "$cid" ]; then
    state="$(docker inspect -f '{{.State.Status}}' "$cid" 2>/dev/null || echo '')"
    health="$(docker inspect -f '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$cid" 2>/dev/null || echo 'none')"
    case "$state" in
      running) if [ "$health" = healthy ] || [ "$health" = none ]; then healthy=1; break; fi ;;
      exited|dead) break ;;
    esac
  fi
  sleep 2
done

echo
if [ "$healthy" -eq 1 ]; then
  ok "Miabi is up and running."
else
  warn "Miabi did not report healthy yet — it may still be starting."
  warn "Check status with: cd ${INSTALL_DIR} && docker compose ps && docker compose logs miabi"
fi

docker compose ps 2>/dev/null || true
echo

# ── domain reminder ──────────────────────────────────────────────────────────
cur_domain="$(get_kv MIABI_DOMAIN)"
cur_email="$(get_kv MIABI_ACME_EMAIL)"
if [ "$cur_domain" = "miabi.example.com" ] || [ "$cur_email" = "admin@example.com" ]; then
  warn "MIABI_DOMAIN / MIABI_ACME_EMAIL are still placeholders."
  printf "      Edit %s/.env, set real values, then: docker compose up -d\n" "${INSTALL_DIR}"
fi

# ── summary ──────────────────────────────────────────────────────────────────
printf "${C_GREEN}%s${C_RESET}\n" "──────────────────────────────────────────────────────────"
if [ "$fresh" -eq 1 ]; then
  printf "  ${C_GREEN}Miabi installed${C_RESET}\n\n"
  printf "  Open ${C_CYAN}https://%s${C_RESET} once DNS points here and sign in as\n" "$cur_domain"
  printf "  the platform admin seeded on first boot:\n\n"
  printf "      email:    %s\n" "$(get_kv MIABI_ADMIN_EMAIL)"
  if [ "$admin_generated" -eq 1 ]; then
    printf "      password: ${C_CYAN}%s${C_RESET}\n\n" "$(get_kv MIABI_ADMIN_PASSWORD)"
    printf "  ${C_YELLOW}This password is shown once.${C_RESET} It is stored in %s/.env —\n" "${INSTALL_DIR}"
    printf "  change it from the UI after your first sign-in.\n\n"
  else
    printf "      password: (the MIABI_ADMIN_PASSWORD you supplied)\n\n"
  fi
else
  printf "  ${C_GREEN}Miabi updated and running${C_RESET}\n\n"
fi
if [ -n "$registry_host" ]; then
  printf "  Registry: ${C_CYAN}%s${C_RESET} — point an A record at this host so\n" "$registry_host"
  printf "            Goma can issue its certificate, then docker login there.\n\n"
fi
printf "  Config:   %s/.env\n" "${INSTALL_DIR}"
printf "  Logs:     cd %s && docker compose logs -f miabi\n" "${INSTALL_DIR}"
printf "  Restart:  cd %s && docker compose restart\n" "${INSTALL_DIR}"
printf "  Update:   %s\n" "$UPDATE_HINT"
printf "${C_GREEN}%s${C_RESET}\n" "──────────────────────────────────────────────────────────"
