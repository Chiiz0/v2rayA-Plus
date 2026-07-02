#!/usr/bin/env bash
set -Eeuo pipefail

APP_DIR="${APP_DIR:-/opt/v2raya-plus}"
IMAGE="${IMAGE:-v2raya-plus:ubuntu-transparent}"
CONTAINER_NAME="${CONTAINER_NAME:-v2raya-plus}"
WEB_PORT="${WEB_PORT:-2017}"
PROXY_NETWORK="${PROXY_NETWORK:-v2raya-plus-proxy}"
PROXY_NETWORK_SUBNET="${PROXY_NETWORK_SUBNET:-}"
BUILD_IMAGE="${BUILD_IMAGE:-1}"
COMPOSE_PROJECT_NAME="${COMPOSE_PROJECT_NAME:-v2raya-plus}"

log() {
  printf '[v2raya-plus] %s\n' "$*"
}

die() {
  printf '[v2raya-plus] ERROR: %s\n' "$*" >&2
  exit 1
}

require_root() {
  if [ "$(id -u)" -ne 0 ]; then
    die "please run this script as root: sudo bash $0"
  fi
}

check_supported_ubuntu() {
  if [ ! -r /etc/os-release ]; then
    die "cannot detect OS; Ubuntu 24.x or 26.x is required"
  fi

  # shellcheck disable=SC1091
  . /etc/os-release
  if [ "${ID:-}" != "ubuntu" ]; then
    die "Ubuntu 24.x or 26.x is required, detected: ${PRETTY_NAME:-unknown}"
  fi

  case "${VERSION_ID:-}" in
    24.*|26.*) ;;
    *) die "Ubuntu 24.x or 26.x is required, detected: ${PRETTY_NAME:-unknown}" ;;
  esac
}

install_docker_repo() {
  install -m 0755 -d /etc/apt/keyrings
  if [ ! -f /etc/apt/keyrings/docker.asc ]; then
    curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
    chmod a+r /etc/apt/keyrings/docker.asc
  fi

  # shellcheck disable=SC1091
  . /etc/os-release
  local arch
  arch="$(dpkg --print-architecture)"
  printf 'deb [arch=%s signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu %s stable\n' \
    "$arch" "${VERSION_CODENAME}" >/etc/apt/sources.list.d/docker.list

  apt-get update
  DEBIAN_FRONTEND=noninteractive apt-get install -y \
    docker-ce \
    docker-ce-cli \
    containerd.io \
    docker-buildx-plugin \
    docker-compose-plugin
}

install_packages() {
  log "installing host packages"
  apt-get update
  DEBIAN_FRONTEND=noninteractive apt-get install -y ca-certificates curl gnupg lsb-release nftables iproute2

  if ! command -v docker >/dev/null 2>&1 || ! docker compose version >/dev/null 2>&1; then
    log "installing Docker Engine and compose plugin"
    install_docker_repo
  fi

  if command -v systemctl >/dev/null 2>&1; then
    systemctl enable --now docker
  fi

  modprobe nf_tables 2>/dev/null || true
  modprobe br_netfilter 2>/dev/null || true
}

build_or_pull_image() {
  local script_dir repo_dir
  script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
  repo_dir="$(cd -- "${script_dir}/.." && pwd)"

  if [ "$BUILD_IMAGE" = "1" ]; then
    [ -f "${repo_dir}/Dockerfile" ] || die "Dockerfile not found at ${repo_dir}; set BUILD_IMAGE=0 IMAGE=<image> to use a prebuilt image"
    log "building image ${IMAGE}"
    docker build -t "$IMAGE" "$repo_dir"
  else
    if docker image inspect "$IMAGE" >/dev/null 2>&1; then
      log "using local image ${IMAGE}"
      return
    fi
    log "pulling image ${IMAGE}"
    docker pull "$IMAGE"
  fi
}

verify_image_tools() {
  log "checking image tools"
  docker run --rm --entrypoint sh "$IMAGE" -c 'command -v nft >/dev/null && command -v ip >/dev/null && command -v docker >/dev/null' \
    || die "image ${IMAGE} must contain nft, ip and docker CLI; use the patched Dockerfile or another compatible image"
}

ensure_proxy_network() {
  log "ensuring docker proxy network ${PROXY_NETWORK}"
  if docker network inspect "$PROXY_NETWORK" >/dev/null 2>&1; then
    return
  fi

  if [ -n "$PROXY_NETWORK_SUBNET" ]; then
    docker network create --driver bridge --subnet "$PROXY_NETWORK_SUBNET" "$PROXY_NETWORK" >/dev/null
  else
    docker network create --driver bridge "$PROXY_NETWORK" >/dev/null
  fi
}

write_hook() {
  log "writing transparent-proxy docker proxy-network bypass hook"
  install -d -m 0755 "${APP_DIR}/hooks"
  cat >"${APP_DIR}/hooks/docker-proxy-network-bypass.sh" <<'HOOK'
#!/bin/sh
set -u

stage=""
transparent_type=""
for arg in "$@"; do
  case "$arg" in
    --stage=*) stage="${arg#*=}" ;;
    --transparent-type=*) transparent_type="${arg#*=}" ;;
  esac
done

[ "$stage" = "post-start" ] || exit 0
case "$transparent_type" in
  redirect|tproxy) ;;
  *) exit 0 ;;
esac

comment="v2raya-plus docker proxy network bypass"

log() {
  printf '%s\n' "docker proxy network bypass: $*" >&2
}

normalize_cidrs() {
  tr ',;' '\n' | tr ' ' '\n' | awk 'NF && !seen[$0]++'
}

network="${V2RAYA_DOCKER_PROXY_NETWORK:-v2raya-plus-proxy}"
cidrs=""
if command -v docker >/dev/null 2>&1 && [ -n "$network" ]; then
  cidrs="$(docker network inspect "$network" --format '{{range .IPAM.Config}}{{println .Subnet}}{{end}}' 2>/dev/null | normalize_cidrs || true)"
fi

[ -n "$cidrs" ] || {
  log "no subnet found for docker network ${network}"
  exit 0
}

insert_nft_rule() {
  cidr="$1"
  command -v nft >/dev/null 2>&1 || return 1
  nft list chain inet v2raya tp_rule >/dev/null 2>&1 || return 1

  if nft list chain inet v2raya tp_rule | grep -Fq "ip saddr ${cidr} return"; then
    return 0
  fi

  nft insert rule inet v2raya tp_rule ip saddr "$cidr" return comment "$comment" >/dev/null 2>&1 \
    || nft insert rule inet v2raya tp_rule ip saddr "$cidr" return >/dev/null 2>&1
}

insert_iptables_rule() {
  bin="$1"
  table="$2"
  cidr="$3"

  command -v "$bin" >/dev/null 2>&1 || return 1
  "$bin" -t "$table" -S TP_RULE >/dev/null 2>&1 || return 1

  "$bin" -w 2 -t "$table" -C TP_RULE -s "$cidr" -j RETURN >/dev/null 2>&1 \
    || "$bin" -w 2 -t "$table" -I TP_RULE 1 -s "$cidr" -j RETURN >/dev/null 2>&1
}

for cidr in $cidrs; do
  case "$cidr" in
    *:*) continue ;;
    */*) ;;
    *) continue ;;
  esac

  if insert_nft_rule "$cidr"; then
    log "nft bypass ensured for ${cidr}"
    continue
  fi

  inserted=0
  for bin in iptables iptables-legacy iptables-nft; do
    if insert_iptables_rule "$bin" nat "$cidr"; then
      inserted=1
    fi
    if insert_iptables_rule "$bin" mangle "$cidr"; then
      inserted=1
    fi
  done

  if [ "$inserted" -eq 1 ]; then
    log "iptables bypass ensured for ${cidr}"
  else
    log "transparent chain not ready for ${cidr}; skipped"
  fi
done

exit 0
HOOK
  chmod 0755 "${APP_DIR}/hooks/docker-proxy-network-bypass.sh"
}

write_compose() {
  log "writing compose file"
  install -d -m 0755 "$APP_DIR"
  cat >"${APP_DIR}/docker-compose.yml" <<EOF
services:
  v2raya-plus:
    image: "${IMAGE}"
    container_name: "${CONTAINER_NAME}"
    network_mode: host
    privileged: true
    restart: unless-stopped
    environment:
      V2RAYA_CONFIG: /etc/v2raya-plus
      V2RAYA_ADDRESS: "0.0.0.0:${WEB_PORT}"
      V2RAYA_NFTABLES_SUPPORT: "on"
      V2RAYA_TRANSPARENT_HOOK: /usr/local/bin/v2raya-plus-docker-bypass
      V2RAYA_DOCKER_PROXY_NETWORK: "${PROXY_NETWORK}"
    volumes:
      - /lib/modules:/lib/modules:ro
      - /var/run/docker.sock:/var/run/docker.sock
      - v2raya-plus-data:/etc/v2raya-plus
      - ${APP_DIR}/hooks/docker-proxy-network-bypass.sh:/usr/local/bin/v2raya-plus-docker-bypass:ro

volumes:
  v2raya-plus-data:
EOF
}

start_stack() {
  log "starting v2rayA-Plus"
  docker compose -p "$COMPOSE_PROJECT_NAME" -f "${APP_DIR}/docker-compose.yml" up -d --remove-orphans
}

print_result() {
  cat <<EOF

v2rayA-Plus is installed.

Web UI:
  http://<server-ip>:${WEB_PORT}

Files:
  Compose: ${APP_DIR}/docker-compose.yml
  Hook:    ${APP_DIR}/hooks/docker-proxy-network-bypass.sh
  Docker network: ${PROXY_NETWORK}

Useful commands:
  docker compose -p ${COMPOSE_PROJECT_NAME} -f ${APP_DIR}/docker-compose.yml logs -f
  docker compose -p ${COMPOSE_PROJECT_NAME} -f ${APP_DIR}/docker-compose.yml down

After you connect a node in the Web UI, enable transparent proxy there.
The hook will keep this project's Docker proxy network direct by inserting a rule like:
  ip saddr <${PROXY_NETWORK} subnet> return
EOF
}

main() {
  require_root "$@"
  check_supported_ubuntu
  install_packages
  build_or_pull_image
  verify_image_tools
  ensure_proxy_network
  write_hook
  write_compose
  start_stack
  print_result
}

main "$@"
