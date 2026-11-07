#!/usr/bin/env bash
# Installs Docker and the miabi CLI, then runs `miabi setup`; not Compose, so Miabi can upgrade its own containers.

# Re-exec under bash (dash dies on pipefail); keep this block POSIX-clean and above `set -o pipefail`.
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

NON_ROOT="${MIABI_NON_ROOT:-0}"
for _arg in "$@"; do
  case "$_arg" in
    --non-root) NON_ROOT=1 ;;
    *) echo "miabi: unknown option '${_arg}' (the only option is --non-root; everything else is an environment variable)." >&2; exit 1 ;;
  esac
done
unset _arg
case "$NON_ROOT" in 1|true|yes) NON_ROOT=1 ;; *) NON_ROOT=0 ;; esac

# A failed version lookup stops the install: falling back to :latest would lose rollback and drift detection.
[ "${MIABI_VERSION:-}" = "latest" ] && MIABI_VERSION=""
VERSIONS_LOOKED_UP=""
if [ -z "${MIABI_VERSION:-}" ] || [ -z "${GOMA_VERSION:-}" ] || [ -z "${RUNNER_VERSION:-}" ] || [ -z "${MIABI_CLI_VERSION:-}" ]; then
  VERSIONS_LOOKED_UP=1
  # A component's stable entry runs from "<name>":{"stable":{ up to its prerelease entry, if any.
  _index="$(curl -fsSL --retry 3 --retry-delay 2 --max-time 20 https://versions.miabi.io/v1/index.json | tr -d ' \n\r\t')" || _index=""
  _stable_of() {
    case "$_index" in *"\"$1\":{\"stable\":{"*) ;; *) return 0 ;; esac
    local e="${_index#*\"$1\":\{\"stable\":\{}"
    printf '%s' "${e%%\"prerelease\":*}"
  }
  _version_of() { printf '%s' "$1" | sed -n 's/^"version":"\([^"]*\)".*/\1/p'; }
  _pick() { printf '%s' "$1" | sed -n "s/.*\"components\":{[^}]*\"$2\":\"\\([^\"]*\\)\".*/\\1/p"; }
  _miabi="$(_stable_of miabi)"
  _version="$(_version_of "$_miabi")"
  _gateway="$(_pick "$_miabi" gateway)"; _runner="$(_pick "$_miabi" runner)"
  # The CLI's own newest release; the one Miabi was tested with only if the index lacks it.
  _cli="$(_version_of "$(_stable_of cli)")"; [ -n "$_cli" ] || _cli="$(_pick "$_miabi" cli)"
  [ -n "${MIABI_VERSION:-}" ] || { [ -z "$_version" ] || MIABI_VERSION="v${_version}"; }
  [ -n "${GOMA_VERSION:-}" ] || { [ -z "$_gateway" ] || GOMA_VERSION="v${_gateway}"; }
  [ -n "${RUNNER_VERSION:-}" ] || { [ -z "$_runner" ] || RUNNER_VERSION="v${_runner}"; }
  [ -n "${MIABI_CLI_VERSION:-}" ] || { [ -z "$_cli" ] || MIABI_CLI_VERSION="v${_cli}"; }
  unset -f _stable_of _version_of _pick
  unset _index _miabi _version _gateway _runner _cli
fi
for _v in MIABI_VERSION GOMA_VERSION RUNNER_VERSION MIABI_CLI_VERSION; do
  case "${!_v:-}" in [0-9]*) printf -v "$_v" 'v%s' "${!_v}" ;; esac
  case "${!_v:-}" in
    v[0-9]*.[0-9]*.[0-9]*) ;;
    "")
      echo "miabi: could not determine ${_v} from versions.miabi.io." >&2
      echo "       Retry, or pin every version: MIABI_VERSION, GOMA_VERSION, RUNNER_VERSION and" >&2
      echo "       MIABI_CLI_VERSION (vX.Y.Z; current ones at https://versions.miabi.io/v1/index.json)." >&2
      exit 1
      ;;
    *)
      echo "miabi: ${_v}='${!_v}' is not a version; use vX.Y.Z." >&2
      exit 1
      ;;
  esac
done
unset _v

MIABI_IMAGE_TAG="${MIABI_VERSION#v}"
GOMA_IMAGE_TAG="${GOMA_VERSION#v}"
RUNNER_IMAGE_TAG="${RUNNER_VERSION#v}"


# Point at get.miabi.io, not a release asset, which 404s while the release is mid-build.
if [ -f "${SCRIPT_DIR}/install.sh" ]; then
  UPDATE_HINT="sudo ${SCRIPT_DIR}/install.sh"
else
  UPDATE_HINT="curl -fsSL https://get.miabi.io | sudo bash"
fi

# ============================ logging ====================
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

log "Miabi ${MIABI_VERSION}, gateway ${GOMA_VERSION}, runner ${RUNNER_VERSION}, CLI ${MIABI_CLI_VERSION}${VERSIONS_LOOKED_UP:+ (from versions.miabi.io)}"

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

# ====================== preflight checks ==========================
log "Running preflight checks"

# Root by default, so the install lands where `miabi upgrade` and the docs expect it.
if [ "$(id -u)" -eq 0 ]; then
  ROOTLESS=0
  RUN_AS="root"
  BIN_DIR="${MIABI_BIN_DIR:-/usr/local/bin}"
  STACK_ETC_DEFAULT="/etc/miabi"
elif [ "$NON_ROOT" != "1" ]; then
  die "the installer needs root. Re-run it with sudo, or pass --non-root (MIABI_NON_ROOT=1) to install into your home with Docker you can already use."
else
  RUN_AS="non-root"
  ROOTLESS=1
  BIN_DIR="${MIABI_BIN_DIR:-${HOME}/.local/bin}"
  STACK_ETC_DEFAULT="${HOME}/.miabi"
  [ -n "${HOME:-}" ] || die "running without root needs \$HOME set, to know where to install."
  command -v docker >/dev/null 2>&1 ||
    die "Docker is not installed, and installing it needs root. Install Docker, or re-run with sudo."
  docker info >/dev/null 2>&1 ||
    die "this user cannot use Docker. Add it to the \`docker\` group (then log in again), or re-run with sudo."
  warn "Installing without root: binary in ${BIN_DIR}, stack config in ${STACK_ETC_DEFAULT}."
  # Re-running a home-directory install under sudo would converge a different install.
  UPDATE_HINT="${UPDATE_HINT#sudo }"
  UPDATE_HINT="${UPDATE_HINT/| sudo bash/| bash -s --} --non-root"
fi

case "$(uname -s)" in
  Linux) : ;;
  *) die "this installer supports Linux only (detected $(uname -s))." ;;
esac

case "$(uname -m)" in
  x86_64|amd64|aarch64|arm64) : ;;
  *) warn "unsupported CPU architecture '$(uname -m)'; images may be unavailable." ;;
esac

command -v curl >/dev/null 2>&1 || die "curl is required but not installed."
ok "Host looks good ($(uname -s)/$(uname -m), ${RUN_AS})"

# Checked before Docker is installed so a bad password fails fast; this counts bytes, `miabi setup` characters.
if [ -n "${MIABI_ADMIN_PASSWORD_FILE:-}" ]; then
  [ -r "$MIABI_ADMIN_PASSWORD_FILE" ] || die "MIABI_ADMIN_PASSWORD_FILE: cannot read ${MIABI_ADMIN_PASSWORD_FILE}."
  MIABI_ADMIN_PASSWORD="$(cat "$MIABI_ADMIN_PASSWORD_FILE")"
fi
if [ -n "${MIABI_ADMIN_PASSWORD:-}" ]; then
  _pw_bytes="$(printf '%s' "$MIABI_ADMIN_PASSWORD" | LC_ALL=C wc -c | tr -d ' ')"
  [ "$_pw_bytes" -ge 12 ] || die "MIABI_ADMIN_PASSWORD must be at least 12 characters."
  [ "$_pw_bytes" -le 72 ] || die "MIABI_ADMIN_PASSWORD must be at most 72 bytes (the limit of the password hash)."
  unset _pw_bytes
  export MIABI_ADMIN_PASSWORD
  ok "Using the admin password from the environment"
fi

# >>> shared: docker-install — identical in install.sh and install-agent.sh (scripts/check-installers.sh)
os_release() { # <field>
  # shellcheck disable=SC1091
  ( . /etc/os-release 2>/dev/null && eval "printf '%s' \"\${$1:-}\"" ) || true
}
distro_id()    { printf '%s %s' "$(os_release ID)" "$(os_release VERSION_ID)"; }
# dnf's $releasever must be the major only, or the Docker repo URL 404s.
el_major()     { local v; v="$(os_release VERSION_ID)"; printf '%s' "${v%%.*}"; }

# An allowlist, not an ID_LIKE sniff: Amazon Linux and Fedora set ID_LIKE=fedora but have no centos/<major>/ tree.
is_el_rebuild() {
  case "$(os_release ID)" in
    almalinux|rocky|ol|centos|rhel) return 0 ;;
    *) return 1 ;;
  esac
}

DOCKER_LOG="/var/log/miabi-docker-install.log"

# The podman-docker shim satisfies `command -v docker` but is not the Docker Engine API.
docker_is_podman_shim() {
  command -v docker >/dev/null 2>&1 && docker --version 2>&1 | grep -qi podman
}
have_real_docker() {
  command -v docker >/dev/null 2>&1 && ! docker_is_podman_shim
}

# Printed on every path that gives up, so the user is never left with a bare docs link.
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

# A plain .repo file, not `dnf config-manager --add-repo`: EL10's dnf5 dropped that flag.
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

  # No compose plugin: Miabi never shells out to it. Pin $releasever: AlmaLinux 10.2 can expand it to "10.2".
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

  # get.docker.com prints its fatal error on stdout, so capture both streams.
  if sh "$script" </dev/null >>"$DOCKER_LOG" 2>&1; then
    rm -f "$script"
    return 0
  fi
  rm -f "$script"

  # get.docker.com rejects the rest of the RHEL family (AlmaLinux, Oracle Linux), so cover it ourselves.
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
  # Otherwise this surfaces later as a baffling failure against an API that is not Docker's.
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

# A fresh install often leaves the daemon stopped or disabled.
if ! docker info >/dev/null 2>&1; then
  if command -v systemctl >/dev/null 2>&1; then
    log "Starting the Docker daemon"
    systemctl enable --now docker >/dev/null 2>&1 || true
  fi
  docker info >/dev/null 2>&1 || die "the Docker daemon is not running; start it and re-run."
fi

ok "Docker daemon is running"
# <<< shared: docker-install

# `miabi setup` refuses a rootless daemon too; checking here fails before anything is downloaded.
if docker info --format '{{json .SecurityOptions}}' 2>/dev/null | grep -q 'name=rootless'; then
  die "this Docker daemon runs rootless, which Miabi does not support. Use a regular Docker daemon; to install without sudo, add your user to the docker group and pass --non-root."
fi

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

# Tests stdin, not /dev/tty: under `curl | sudo bash` /dev/tty is readable and a prompt would hang forever.
interactive() {
  [ "${ASSUME_YES:-0}" != "1" ] && [ -t 0 ] && [ -r /dev/tty ]
}

# A preset env var always wins; prompt only when unset and someone is there.
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

# Compose and stack volumes differ (miabi_pgdata vs mb-platform-pgdata), so refuse rather than create an empty database.
STACK_ETC="${MIABI_ETC:-$STACK_ETC_DEFAULT}"

compose_miabi_present() {
  command -v docker >/dev/null 2>&1 || return 1
  # The label survives a custom project name; the volume catches stacks that predate the labels.
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

install_cli() {
  local arch archive url base tmp
  case "$(uname -m)" in
    x86_64|amd64)   arch="amd64" ;;
    aarch64|arm64)  arch="arm64" ;;
    *) die "no miabi CLI build for CPU architecture '$(uname -m)'." ;;
  esac

  # A mirror (air-gapped or staging) must serve the same archive and checksums.txt names.
  base="${MIABI_CLI_BASE_URL:-https://github.com/miabi-io/cli/releases/download/${MIABI_CLI_VERSION}}"
  archive="miabi_${MIABI_CLI_VERSION#v}_linux_${arch}.tar.gz"
  url="${base}/${archive}"

  tmp="$(mktemp -d)"
  # Clean up on every exit path, including the ERR trap.
  trap 'rm -rf "$tmp"' RETURN

  log "Downloading the miabi CLI ${MIABI_CLI_VERSION} (linux/${arch})"
  curl -fsSL -o "${tmp}/${archive}" "$url" \
    || die "could not download ${url} — check MIABI_CLI_VERSION, or install the CLI manually from https://github.com/miabi-io/cli/releases"

  # A tampered or truncated download must not become a root-run binary with Docker socket access.
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
  mkdir -p "$BIN_DIR"
  install -m 0755 "${tmp}/miabi" "${BIN_DIR}/miabi"
  ok "Installed $("${BIN_DIR}/miabi" --version 2>/dev/null || echo miabi) to ${BIN_DIR}/miabi"

  case ":${PATH}:" in
    *":${BIN_DIR}:"*) : ;;
    *) warn "${BIN_DIR} is not on your PATH — add it, or run ${BIN_DIR}/miabi directly." ;;
  esac
}

install_stack() {
  local domain acme admin image
  domain="$(prompt MIABI_DOMAIN 'Panel domain (e.g. miabi.example.com)' '')"
  [ -n "$domain" ] || die "MIABI_DOMAIN is required: pass it as MIABI_DOMAIN=miabi.example.com (answering a prompt over a pipe is unreliable)."

  acme="$(prompt MIABI_ACME_EMAIL "Let's Encrypt contact email (blank = admin email)" '')"
  admin="$(prompt MIABI_ADMIN_EMAIL "First admin's email (blank = admin@${domain})" '')"
  image="miabi/miabi:${MIABI_IMAGE_TAG}"

  # An array, so an unset flag contributes nothing rather than a stray "" argument.
  local extra=()
  [ -n "$acme" ] && extra+=(--acme-email "$acme")
  [ -n "$admin" ] && extra+=(--admin-email "$admin")

  # The registry host anchors every stored image reference, so it is env-only and cannot move at runtime.
  if prompt_yn MIABI_REGISTRY_ENABLED 'Enable the built-in container registry?'; then
    local registry_host
    registry_host="$(prompt MIABI_REGISTRY_HOST 'Registry host' "registry.${domain}")"
    extra+=(--registry --registry-host "$registry_host")
    # The CLI validates the hostname, so it is not re-checked here.
  fi

  # A node on a private network may reach the control plane at an address the public URL never resolves to.
  if [ -n "${MIABI_CONTROL_URL:-}" ]; then
    extra+=(--control-url "$MIABI_CONTROL_URL")
  fi

  # Only so a subnet collision with an existing route or VPN is fixable without hand-editing the manifest.
  [ -n "${MIABI_SUBNET:-}" ] && extra+=(--subnet "$MIABI_SUBNET")
  [ -n "${MIABI_INTERNAL_SUBNET:-}" ] && extra+=(--internal-subnet "$MIABI_INTERNAL_SUBNET")

  # Some hosts refuse a /proc bind; a container's own /proc already reflects host CPU/memory.
  case "${MIABI_NO_HOST_PROC:-0}" in
    1|true|yes) extra+=(--no-host-proc) ;;
  esac

  case "${MIABI_NETWORK_IPV6:-false}" in
    1|true|yes) extra+=(--ipv6) ;;
  esac

  install_cli

  log "Installing the Miabi stack with ${image}"

  # Same interactive() as the prompts, so the two never disagree about whether a human is present.
  local assume=""
  interactive || assume="--yes"

  # The manifest is the only copy of the database password; on the host it survives `uninstall --volumes`.
  mkdir -p "$STACK_ETC"

  # --image is explicit because the CLI and stack versions are independent; the versions block decides every image.
  # shellcheck disable=SC2086
  MIABI_CONFIG_FILE="${STACK_ETC}/miabi.yaml" "${BIN_DIR}/miabi" setup \
    --domain "$domain" \
    --image "$image" \
    --gateway-image "jkaninda/goma-gateway:${GOMA_IMAGE_TAG}" \
    --runner-image "miabi/runner:${RUNNER_IMAGE_TAG}" \
    ${extra[@]+"${extra[@]}"} \
    $assume || die "miabi setup failed"
    # ${extra[@]+"..."}: under `set -u` an empty array is "unbound" on bash < 4.4.

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
