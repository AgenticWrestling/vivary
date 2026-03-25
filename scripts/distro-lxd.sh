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
#   linux.kernel.modules=overlay,nf_tables,ip_tables,ip6_tables,nf_nat
#     Ensures the host kernel has these modules loaded before the container
#     starts.  overlay is needed by nspawn; the nf_* set is needed for the
#     per-agent nftables egress rules keeperd applies at agent spawn time.
#
launch_container() {
  local image name
  image="${1:-vivary-runtime}"
  name="${2:-vivary}"

  "$LXD_CHECK" --check
  lxc launch "$image" "$name" \
    --config security.nesting=true \
    --config linux.kernel.modules=overlay,nf_tables,ip_tables,ip6_tables,nf_nat
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
