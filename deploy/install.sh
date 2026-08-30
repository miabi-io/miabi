#!/usr/bin/env bash
#
# Miabi one-line installer.
#   curl -fsSL https://get.miabi.io | sudo bash
#
# Installs Docker if missing, installs the `miabi` CLI, then hands off to it. The CLI builds the
# stack — network, volumes, PostgreSQL, Redis, the Goma gateway and the control plane — straight
# against the Docker API. So all this script really does is:
#
#   miabi setup --domain ... --image miabi/miabi:<tag>
#
# Why not Docker Compose (which this script used to set up)? Compose owns what Compose
# created: a container Miabi recreated out-of-band would be silently reverted by the
# next `docker compose up -d`. So a Compose-managed Miabi could never truthfully update
# itself. Miabi owns these containers (io.miabi.managed-by=miabi), which is what makes
# `miabi upgrade` — including replacing the control plane's own container, with rollback — possible.
#
# Compose is still supported for anyone who wants to drive it themselves:
# examples/compose/compose.yaml is unchanged. This script simply no longer does it for you, and
# it refuses to install alongside an existing Compose stack (they do not share volumes).
#
# It leaves behind /usr/local/bin/miabi — one tool for the panel API and for this host:
#
#   miabi setup | upgrade | stack {status,restart,uninstall}
#
# Environment overrides:
#   MIABI_DOMAIN                panel domain; required over a pipe (asked only when
#                               the script is run as a file from a terminal)
#   MIABI_ACME_EMAIL            Let's Encrypt contact
#   MIABI_ADMIN_EMAIL           first admin's login (default: MIABI_ACME_EMAIL)
#   MIABI_CONTROL_URL           URL remote nodes/agents dial back on (default: the
#                               panel's own URL)
#   MIABI_REGISTRY_ENABLED      enable the built-in registry   (skips the prompt)
#   MIABI_REGISTRY_HOST         its hostname                   (default registry.<domain>)
#   MIABI_NO_HOST_PROC          1 = do not bind the host's /proc into Miabi. Set it
#                               where the bind is refused (a rootless daemon, a
#                               hardened host, a socket proxy that forbids host binds);
#                               host metrics then fall back to the container's /proc,
#                               which already reflects host CPU/memory.
#   MIABI_SUBNET                CIDR for the shared app network (default 10.63.0.0/16)
#   MIABI_INTERNAL_SUBNET       CIDR for the platform's private network — control plane,
#                               database, cache (default 10.62.0.0/16). Set either one
#                               where the default collides with an existing route or VPN.
#   MIABI_ETC                   manifest directory             (default /etc/miabi)
#   MIABI_VERSION               Miabi release to install       (default: pinned below)
#   MIABI_CLI_VERSION           miabi CLI release to install   (default: pinned below)
#   MIABI_CLI_BASE_URL          mirror to fetch the CLI from    (default: the GitHub release)
#   GOMA_VERSION                Goma Gateway release           (default: pinned below)
#   RUNNER_VERSION              miabi/runner release           (default: pinned below)
#   MIABI_SKIP_DOCKER_INSTALL   1 = never touch the host's packages; Docker must exist
#   MIABI_FORCE_STACK           1 = install even though a Compose stack is present
#   ASSUME_YES                  1 = never prompt
#
# A piped install NEVER prompts — stdin is the script, so there is nothing to read
# an answer from. Every value must come from the environment, and MIABI_DOMAIN is
# the only one without a sensible fallback:
#   curl -fsSL https://get.miabi.io | sudo MIABI_DOMAIN=miabi.example.com \
#     MIABI_ACME_EMAIL=you@example.com bash
#
# To be asked instead, download first and run it as a file:
#   curl -fsSLO https://get.miabi.io && sudo bash ./get.miabi.io
#
# Install or pin a specific release:
#   curl -fsSL https://get.miabi.io | sudo MIABI_VERSION=v1.4.0 bash

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

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" && pwd)"
SKIP_DOCKER_INSTALL="${MIABI_SKIP_DOCKER_INSTALL:-0}"

# ── versions ─────────────────────────────────────────────────────────────────
#
# The single place every image is pinned. CI bumps these on release (see
# .github/workflows/release.yml) and they are passed straight to `miabi setup`, so
# the manifest it writes records exactly what this release was tested against.
MIABI_VERSION="${MIABI_VERSION:-v1.9.2}"
GOMA_VERSION="${GOMA_VERSION:-v0.14.0}"
RUNNER_VERSION="${RUNNER_VERSION:-v0.0.10}"

# The miabi CLI is what installs and then manages the stack, and it releases from its own repo
# (miabi-io/cli) on its own cadence — a standalone CLI can be older or newer than the stack it
# manages, so it carries its own pin rather than following MIABI_VERSION.
MIABI_CLI_VERSION="${MIABI_CLI_VERSION:-v0.10.0}"

# Docker tags carry no leading "v" (git tag v1.2.3 → image tag 1.2.3) across all
# three images. The :latest fallback only applies if a caller deliberately blanks
# a version (MIABI_VERSION= ), since the defaults above are always set.
#
# miabi/runner versions independently of miabi/miabi (runner 0.0.x while the
# panel is 1.0.x), so it carries its own version — deriving its tag from
# MIABI_VERSION would ask for an image that was never published.
MIABI_IMAGE_TAG="${MIABI_VERSION#v}";   MIABI_IMAGE_TAG="${MIABI_IMAGE_TAG:-latest}"
GOMA_IMAGE_TAG="${GOMA_VERSION#v}";     GOMA_IMAGE_TAG="${GOMA_IMAGE_TAG:-latest}"
RUNNER_IMAGE_TAG="${RUNNER_VERSION#v}"; RUNNER_IMAGE_TAG="${RUNNER_IMAGE_TAG:-latest}"


# How to re-run for an update: a local path when run from a checkout, otherwise
# the canonical one-liner (a piped `curl | bash` has no re-runnable path).
#
# get.miabi.io — not a release asset. The script is served from the repository, so
# it exists at every commit; a release asset only exists once the release is
# published, and pointing users at one that is mid-build hands them a 404.
if [ -f "${SCRIPT_DIR}/install.sh" ]; then
  UPDATE_HINT="sudo ${SCRIPT_DIR}/install.sh"
else
  UPDATE_HINT="curl -fsSL https://get.miabi.io | sudo bash"
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
  if command -v docker >/dev/null 2>&1; then
    printf "  • Inspect logs:   docker logs miabi\n" >&2
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

# ── prompt (interactive only) ────────────────────────────────────────────────
#
# interactive() is the single definition of "there is a human here to answer",
# used by every prompt AND by the --yes decision below. It tests
# STDIN, not /dev/tty.
#
# That distinction is the whole point. Under the documented install line —
#
#     curl -fsSL https://get.miabi.io | sudo bash
#
# — stdin is the pipe carrying the script itself, but /dev/tty is still the
# operator's terminal and therefore still readable. A `[ -r /dev/tty ]` guard
# passes, the prompt prints, and `read < /dev/tty` blocks forever on input the
# operator was never asked for and cannot see a reason to give: the install
# hangs on the first unset value. Testing stdin instead means a piped run never
# prompts at all, which is what the header has always told operators to expect.
interactive() {
  [ "${ASSUME_YES:-0}" != "1" ] && [ -t 0 ] && [ -r /dev/tty ]
}

# A pre-set environment variable always wins. Only when the value is unset do we
# ask, and only when someone is there. Otherwise keep the default and let the
# caller decide whether a missing value is fatal.
prompt() { # <env-var> <question> <default>
  local preset ans=""
  eval "preset=\${$1:-}"
  if [ -n "$preset" ]; then printf '%s' "$preset"; return 0; fi

  if interactive; then
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
  interactive || return 1
  printf "    ${C_CYAN}?${C_RESET} %s [y/N]: " "$2" > /dev/tty
  read -r ans < /dev/tty || ans=""
  case "$ans" in y|Y|yes|YES) return 0 ;; *) return 1 ;; esac
}

# ── an existing Compose install ──────────────────────────────────────────────
#
# This installer builds the Miabi-managed stack (`miabi setup`, straight against
# the Docker API). It no longer sets up Docker Compose.
#
# That matters for a host that ALREADY runs Miabi under Compose, because the two do
# not share volumes:
#
#   compose:  miabi_pgdata          (project-prefixed by Compose)
#   stack:    mb-platform-pgdata
#
# So installing the stack here would not adopt the existing database — it would
# create an empty one beside it, and the operator would think their data had
# vanished. Refuse, and say what to do instead.
STACK_ETC="${MIABI_ETC:-/etc/miabi}"

compose_miabi_present() {
  command -v docker >/dev/null 2>&1 || return 1
  # The label is authoritative and survives a custom project name; the volume is the
  # fallback for a stack that predates the labels.
  [ -n "$(docker ps -aq --filter label=io.miabi.managed-by=compose 2>/dev/null)" ] && return 0
  docker volume inspect miabi_pgdata >/dev/null 2>&1 && return 0
  return 1
}

if compose_miabi_present && [ "${MIABI_FORCE_STACK:-0}" != "1" ]; then
  warn "this host already runs Miabi under Docker Compose."
  cat >&2 <<'EOM'

  This installer now builds the Miabi-managed stack, which uses DIFFERENT volumes —
  your database would not come with it, and you would get an empty one instead.

  To keep your Compose install (nothing changes):

      cd /opt/miabi && docker compose pull && docker compose up -d

  To move to the Miabi-managed stack, migrate the data deliberately:

      1. Back up:  https://miabi.io/docs/storage/backups
      2. cd /opt/miabi && docker compose down
      3. Re-run this installer with MIABI_FORCE_STACK=1
      4. Restore the backup into the new stack

EOM
  die "refusing to install alongside a Compose stack (set MIABI_FORCE_STACK=1 to proceed anyway)."
fi

# install_cli downloads the `miabi` CLI for this host from the GitHub release, verifies it against
# the release's checksums.txt, and installs it to /usr/local/bin. The CLI is what installs and then
# manages the stack, so this replaces the old `docker run miabi/miabi install` entirely.
install_cli() {
  local arch archive url base tmp
  case "$(uname -m)" in
    x86_64|amd64)   arch="amd64" ;;
    aarch64|arm64)  arch="arm64" ;;
    *) die "no miabi CLI build for CPU architecture '$(uname -m)'." ;;
  esac

  # MIABI_CLI_BASE_URL points the download at a mirror — an internal artifact store for an
  # air-gapped install, or a staging build. It must serve the same archive and checksums.txt names.
  base="${MIABI_CLI_BASE_URL:-https://github.com/miabi-io/cli/releases/download/${MIABI_CLI_VERSION}}"
  archive="miabi_${MIABI_CLI_VERSION#v}_linux_${arch}.tar.gz"
  url="${base}/${archive}"

  tmp="$(mktemp -d)"
  # The temp dir holds only the archive and the extracted binary; clean it up on every exit path,
  # including the ERR trap, so a failed download leaves nothing behind.
  trap 'rm -rf "$tmp"' RETURN

  log "Downloading the miabi CLI ${MIABI_CLI_VERSION} (linux/${arch})"
  curl -fsSL -o "${tmp}/${archive}" "$url" \
    || die "could not download ${url} — check MIABI_CLI_VERSION, or install the CLI manually from https://github.com/miabi-io/cli/releases"

  # Verify against the release's own checksums.txt. A tampered or truncated download must not be
  # installed as a root-run binary that then talks to the Docker socket.
  if curl -fsSL -o "${tmp}/checksums.txt" "${base}/checksums.txt"; then
    if command -v sha256sum >/dev/null 2>&1; then
      ( cd "$tmp" && grep " ${archive}\$" checksums.txt | sha256sum -c - >/dev/null 2>&1 ) \
        || die "checksum mismatch for ${archive} — refusing to install."
      ok "Checksum verified"
    else
      warn "sha256sum not found; skipping checksum verification."
    fi
  else
    warn "could not fetch checksums.txt; skipping checksum verification."
  fi

  tar -xzf "${tmp}/${archive}" -C "$tmp" miabi || die "could not extract miabi from ${archive}."
  install -m 0755 "${tmp}/miabi" /usr/local/bin/miabi
  ok "Installed $(/usr/local/bin/miabi --version 2>/dev/null || echo miabi) to /usr/local/bin/miabi"
}

install_stack() {
  local domain acme admin image
  domain="$(prompt MIABI_DOMAIN 'Panel domain (e.g. miabi.example.com)' '')"
  [ -n "$domain" ] || die "MIABI_DOMAIN is required: pass it as MIABI_DOMAIN=miabi.example.com (answering a prompt over a pipe is unreliable)."
  # Neither email is defaulted here. `miabi setup` owns the whole rule: the two
  # addresses fall back to each other, and admin@<domain> is the last resort. Defaulting
  # either one in shell would defeat that fallback — an operator who set only
  # MIABI_ADMIN_EMAIL would silently get admin@<domain> as their Let's Encrypt contact,
  # because we'd have handed the stack a value it has no way to tell from a real choice.
  acme="$(prompt MIABI_ACME_EMAIL "Let's Encrypt contact email (blank = admin email)" '')"
  admin="$(prompt MIABI_ADMIN_EMAIL "First admin's email (blank = admin@${domain})" '')"
  image="miabi/miabi:${MIABI_IMAGE_TAG}"

  # Optional flags, built as an array so an unset one contributes nothing (an empty
  # string would arrive as a stray "" argument).
  local extra=()
  [ -n "$acme" ] && extra+=(--acme-email "$acme")
  [ -n "$admin" ] && extra+=(--admin-email "$admin")

  # Built-in registry. Enablement and the hostname are environment-only settings the
  # admin UI shows read-only (the host anchors every stored image reference and
  # decides which workspace owns an image, so it cannot move at runtime). Declining
  # leaves the keys out of the manifest, which reads the same as disabled; turning it
  # on later means editing the manifest and restarting.
  if prompt_yn MIABI_REGISTRY_ENABLED 'Enable the built-in container registry?'; then
    local registry_host
    registry_host="$(prompt MIABI_REGISTRY_HOST 'Registry host' "registry.${domain}")"
    extra+=(--registry --registry-host "$registry_host")
    # The CLI validates the hostname (it gets its own certificate, so a stray "y"
    # would have the gateway request one for a name that cannot exist) — no need to
    # re-implement that check here.
  fi

  # Where remote nodes and agents dial back. Defaults to the panel's own URL, which is
  # right for a single public hostname — but a node on a private network may reach the
  # control plane at an address the public URL never resolves to.
  if [ -n "${MIABI_CONTROL_URL:-}" ]; then
    extra+=(--control-url "$MIABI_CONTROL_URL")
  fi

  # The two Docker subnets, for a host whose existing routes or VPN already occupy the defaults
  # (10.63.0.0/16 for the shared app network, 10.62.0.0/16 for the platform's private one). Left
  # unset, `miabi setup` picks those defaults — this only exists so a collision is fixable without
  # hand-editing the manifest after the fact.
  [ -n "${MIABI_SUBNET:-}" ] && extra+=(--subnet "$MIABI_SUBNET")
  [ -n "${MIABI_INTERNAL_SUBNET:-}" ] && extra+=(--internal-subnet "$MIABI_INTERNAL_SUBNET")

  # Some hosts refuse a bind of /proc: a rootless daemon, a hardened host, a socket
  # proxy that forbids host binds. Miabi then reads its own /proc, which inside a
  # container already reflects host CPU/memory, so the Nodes page keeps working.
  case "${MIABI_NO_HOST_PROC:-0}" in
    1|true|yes) extra+=(--no-host-proc) ;;
  esac

  install_cli

  log "Installing the Miabi stack with ${image}"

  # Non-interactive implies --yes, since there is nobody there to answer. Same interactive() the
  # prompts use, so the two can never disagree about whether a human is present.
  local assume=""
  interactive || assume="--yes"

  # The manifest (mode 0600) is the desired state AND the only copy of the database
  # password. It lives on the host, not in a volume, so it survives
  # `uninstall --volumes` and can be backed up like any other config file.
  mkdir -p "$STACK_ETC"

  # --image is passed explicitly rather than left to the CLI's own build stamp: this script pins
  # every image in one place, and the CLI version and the stack version are now independent.
  # MIABI_CONFIG_FILE honours MIABI_ETC, which the old bind mount used to do implicitly.
  # shellcheck disable=SC2086
  MIABI_CONFIG_FILE="${STACK_ETC}/miabi.yaml" /usr/local/bin/miabi setup \
    --domain "$domain" \
    --image "$image" \
    --gateway-image "jkaninda/goma-gateway:${GOMA_IMAGE_TAG}" \
    --runner-image "miabi/runner:${RUNNER_IMAGE_TAG}" \
    ${extra[@]+"${extra[@]}"} \
    $assume || die "miabi setup failed"
    # ${extra[@]+"..."}, not a bare "${extra[@]}": under `set -u`, expanding an EMPTY
    # array is an "unbound variable" error on bash < 4.4 (still shipped on EL7-era
    # hosts). This form expands to nothing when the array is empty and quotes each
    # element when it is not.

  printf '\n'
  ok "Installed. Manage it with:"
  echo "    miabi stack status"
  echo "    miabi upgrade                           # moves Miabi forward, and rolls back if it fails"
  echo "    miabi stack uninstall                   # keeps your data; add --volumes to destroy it"
  printf '\n'
  echo "    Manifest (KEEP A BACKUP — it holds the database password):  ${STACK_ETC}/miabi.yaml"
  exit 0
}

install_stack
