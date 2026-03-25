#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
NIX_FLAGS=(--extra-experimental-features "nix-command flakes")
LXD_CHECK="$ROOT_DIR/scripts/check-lxd-access.sh"

usage() {
  cat <<'EOF'
Usage:
  scripts/distro-lxd.sh build  [base|runtime]
  scripts/distro-lxd.sh import [base|runtime] [alias]
  scripts/distro-lxd.sh launch [image-alias]  [container-name]
  scripts/distro-lxd.sh stop   [container-name]
  scripts/distro-lxd.sh delete [container-name]
  scripts/distro-lxd.sh export <alias>        [output-dir]

Defaults:
  build target:      base
  import alias:      vivary-base | vivary-runtime
  launch image:      vivary-runtime
  launch name:       vivary
  export dir:        ./dist/lxd-export

The launch subcommand creates the container with the settings required for
nested systemd-nspawn (security.nesting=true, cgroup v2 delegation, and the
kernel modules needed for nftables and overlay filesystems).
EOF
}

chromed_service_name() {
  printf 'chromed@%s.service' "$1"
}

chromed_state_root() {
  printf '%s/.vivary/chromed' "$ROOT_DIR"
}

chromed_state_dir() {
  printf '%s/%s' "$(chromed_state_root)" "$1"
}

chromed_host_dir() {
  local name
  name="$1"
  if chromed_use_systemd; then
    printf '/run/chromed-%s' "$name"
  else
    printf '%s/runtime' "$(chromed_state_dir "$name")"
  fi
}

chromed_use_systemd() {
  systemctl cat chromed@.service >/dev/null 2>&1 && sudo -n true >/dev/null 2>&1
}

chromed_binary_path() {
  if [[ -x /usr/local/bin/chromed ]]; then
    printf '/usr/local/bin/chromed'
  elif [[ -x "$ROOT_DIR/bin/chromed" ]]; then
    printf '%s/bin/chromed' "$ROOT_DIR"
  else
    printf 'Error: no chromed binary found at /usr/local/bin/chromed or %s/bin/chromed. Run `task build:chromed` or `task distro:chromed:install` first.\n' "$ROOT_DIR" >&2
    exit 1
  fi
}

wait_for_socket() {
  local socket_path deadline
  socket_path="$1"
  deadline=$((SECONDS + 10))
  while (( SECONDS < deadline )); do
    if [[ -S "$socket_path" ]]; then
      return 0
    fi
    sleep 0.1
  done
  printf 'Error: timed out waiting for socket %s\n' "$socket_path" >&2
  return 1
}

start_chromed_user() {
  local name state_dir host_dir log_file pid_file chromed_bin chrome_bin
  name="$1"
  state_dir="$(chromed_state_dir "$name")"
  host_dir="$(chromed_host_dir "$name")"
  log_file="$state_dir/chromed.log"
  pid_file="$state_dir/chromed.pid"
  chromed_bin="$(chromed_binary_path)"
  chrome_bin="$(command -v google-chrome-beta || true)"

  if [[ -z "$chrome_bin" ]]; then
    printf 'Error: /usr/bin/google-chrome-beta is required for chromed.\n' >&2
    exit 1
  fi

  mkdir -p "$host_dir" "$state_dir/profiles"
  if [[ -f "$pid_file" ]] && kill -0 "$(cat "$pid_file")" >/dev/null 2>&1; then
    wait_for_socket "$host_dir/chromed.sock"
    return 0
  fi

  nohup "$chromed_bin" \
    --socket "$host_dir/chromed.sock" \
    --profile-root "$state_dir/profiles" \
    --chrome-binary "$chrome_bin" \
    --container-name "$name" \
    >"$log_file" 2>&1 &
  printf '%s\n' "$!" > "$pid_file"
  wait_for_socket "$host_dir/chromed.sock"
}

stop_chromed_user() {
  local name state_dir pid_file pid
  name="$1"
  state_dir="$(chromed_state_dir "$name")"
  pid_file="$state_dir/chromed.pid"

  if [[ -f "$pid_file" ]]; then
    pid="$(cat "$pid_file")"
    if kill -0 "$pid" >/dev/null 2>&1; then
      kill "$pid" >/dev/null 2>&1 || true
      wait "$pid" 2>/dev/null || true
    fi
    rm -f "$pid_file"
  fi
}

start_chromed_for_container() {
  local name service host_dir
  name="$1"
  service="$(chromed_service_name "$name")"
  host_dir="$(chromed_host_dir "$name")"

  if chromed_use_systemd; then
    sudo systemctl start "$service"
  else
    start_chromed_user "$name"
  fi
  lxc config device remove "$name" chromed-sock >/dev/null 2>&1 || true
  lxc config device add "$name" chromed-sock disk source="$host_dir" path=/run/vivary/chromed-host
}

stop_chromed_for_container() {
  local name service
  name="$1"
  service="$(chromed_service_name "$name")"

  lxc config device remove "$name" chromed-sock >/dev/null 2>&1 || true
  if chromed_use_systemd; then
    sudo systemctl stop "$service" >/dev/null 2>&1 || true
  else
    stop_chromed_user "$name"
  fi
}

launch_lxc_container() {
  local image name tmp tmp_log status
  image="$1"
  name="$2"
  tmp="$(mktemp)"
  tmp_log="$(mktemp)"

  if lxc launch "$image" "$name" \
    --config security.nesting=true \
    --config security.idmap.size=65536 \
    --config linux.kernel.modules=overlay,nf_tables,ip_tables,ip6_tables,nf_nat \
    >"$tmp" 2>&1; then
    cat "$tmp"
    rm -f "$tmp"
    return 0
  fi

  status=$?
  if grep -q "\"linux.kernel.modules\" isn't supported for \"container\"" "$tmp"; then
    printf 'WARN: LXD does not support linux.kernel.modules for containers on this host; retrying without it.\n' >&2
    if lxc launch "$image" "$name" --config security.nesting=true --config security.idmap.size=65536 >"$tmp" 2>&1; then
      cat "$tmp"
      rm -f "$tmp" "$tmp_log"
      return 0
    fi
    status=$?
  fi

  lxc info "$name" --show-log >"$tmp_log" 2>&1 || true
  if grep -q 'newuidmap failed to write mapping' "$tmp" || grep -q 'newuidmap failed to write mapping' "$tmp_log"; then
    printf 'WARN: LXD user namespace mapping is unavailable on this host; retrying with a privileged outer container. The agent nspawn boundary remains the primary isolation layer.\n' >&2
    lxc delete -f "$name" >/dev/null 2>&1 || true
    if lxc launch "$image" "$name" --config security.nesting=true --config security.privileged=true >"$tmp" 2>&1; then
      cat "$tmp"
      rm -f "$tmp" "$tmp_log"
      return 0
    fi
    status=$?
  fi

  cat "$tmp" >&2
  rm -f "$tmp" "$tmp_log"
  return "$status"
}

target_to_attr() {
  case "${1:-base}" in
    base)    printf '%s' 'distrobuild'         ;;
    runtime) printf '%s' 'distrobuild-runtime' ;;
    *) printf 'unknown target: %s\n' "$1" >&2; exit 1 ;;
  esac
}

target_to_alias() {
  case "${1:-base}" in
    base)    printf '%s' 'vivary-base'    ;;
    runtime) printf '%s' 'vivary-runtime' ;;
    *) printf 'unknown target: %s\n' "$1" >&2; exit 1 ;;
  esac
}

target_to_files() {
  case "${1:-base}" in
    base)
      printf '%s\n%s\n' \
        'vivary-lxc-base-metadata.tar.xz' \
        'vivary-lxc-base-rootfs.tar.xz'
      ;;
    runtime)
      printf '%s\n%s\n' \
        'vivary-lxc-runtime-metadata.tar.xz' \
        'vivary-lxc-runtime-rootfs.tar.xz'
      ;;
    *) printf 'unknown target: %s\n' "$1" >&2; exit 1 ;;
  esac
}

build_image() {
  local target attr
  target="${1:-base}"
  attr="$(target_to_attr "$target")"
  nix "${NIX_FLAGS[@]}" build "$ROOT_DIR#${attr}"
}

import_image() {
  local target alias
  target="${1:-base}"
  alias="${2:-$(target_to_alias "$target")}"

  build_image "$target"
  "$LXD_CHECK" --check
  mapfile -t files < <(target_to_files "$target")
  local metadata rootfs
  metadata="$ROOT_DIR/result/${files[0]}"
  rootfs="$ROOT_DIR/result/${files[1]}"

  lxc image import "$metadata" "$rootfs" --alias "$alias"
}

# launch creates an LXD container from the given image alias with the exact
# configuration required for VIVARY:
#
#   security.nesting=true
#     Allows systemd-nspawn to run inside the LXD guest.  LXD automatically
#     grants the guest CAP_SYS_ADMIN and delegates a cgroup v2 subtree when
#     this flag is set.
#
#   security.idmap.size=65536
#     Pins the container UID/GID map to the narrow range VIVARY expects for the
#     MVP runtime instead of relying on broader host-specific defaults.
#
#   security.privileged=true (fallback only)
#     Used only when the host cannot satisfy LXD's user namespace mapping
#     requirements. This keeps the launch path working on under-provisioned
#     developer hosts while preserving the inner nspawn boundary as the primary
#     runtime isolation layer.
#
#   linux.kernel.modules=overlay,nf_tables,ip_tables,ip6_tables,nf_nat
#     Used when the local LXD supports container-level kernel module hints.
#     Some newer LXD builds reject this key for containers; in that case we
#     retry without it and rely on the host kernel's current module state.
#
launch_container() {
  local image name
  image="${1:-vivary-runtime}"
  name="${2:-vivary}"

  "$LXD_CHECK" --check
  launch_lxc_container "$image" "$name"
  start_chromed_for_container "$name"
}

stop_container() {
  local name
  name="${1:-vivary}"

  "$LXD_CHECK" --check
  lxc stop "$name"
  stop_chromed_for_container "$name"
}

delete_container() {
  local name
  name="${1:-vivary}"

  "$LXD_CHECK" --check
  stop_chromed_for_container "$name"
  lxc delete -f "$name"
}

export_image() {
  local alias outdir prefix
  alias="$1"
  outdir="${2:-$ROOT_DIR/dist/lxd-export}"
  prefix="$outdir/$alias"

  "$LXD_CHECK" --check
  mkdir -p "$outdir"
  rm -f "$prefix".tar.xz "$prefix".rootfs.tar.xz
  lxc image export "$alias" "$prefix"
}

command="${1:-}"
case "$command" in
  build)
    build_image "${2:-base}"
    ;;
  import)
    import_image "${2:-base}" "${3:-}"
    ;;
  launch)
    launch_container "${2:-vivary-runtime}" "${3:-vivary}"
    ;;
  stop)
    stop_container "${2:-vivary}"
    ;;
  delete)
    delete_container "${2:-vivary}"
    ;;
  export)
    if [ $# -lt 2 ]; then usage; exit 1; fi
    export_image "$2" "${3:-}"
    ;;
  -h|--help|help|"")
    usage
    ;;
  *)
    usage >&2
    exit 1
    ;;
esac
