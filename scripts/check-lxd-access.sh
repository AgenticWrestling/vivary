#!/usr/bin/env bash

set -euo pipefail

MODE="doctor"
if [[ "${1:-}" == "--check" ]]; then
  MODE="check"
fi

FAILURES=0
WARNINGS=0

CURRENT_USER="$(id -un)"
CURRENT_GROUPS="$(id -nG)"
LXC_BIN="$(command -v lxc || true)"
LXD_SOCKET="/var/lib/lxd/unix.socket"
LXD_LOG_DIR="/var/log/lxd"
APPARMOR_ENABLED="/sys/module/apparmor/parameters/enabled"
CGROUP_CONTROLLERS="/sys/fs/cgroup/cgroup.controllers"
QEMU_BIN="qemu-system-$(uname -m)"

print_line() {
  printf '%s\n' "$1"
}

record_ok() {
  if [[ "$MODE" == "doctor" ]]; then
    print_line "OK: $1"
  fi
}

record_warn() {
  WARNINGS=$((WARNINGS + 1))
  print_line "WARN: $1" >&2
}

record_fail() {
  FAILURES=$((FAILURES + 1))
  print_line "FAIL: $1" >&2
}

if [[ "$MODE" == "doctor" ]]; then
  print_line "VIVARY LXD launch doctor"
  print_line "User: $CURRENT_USER"
  print_line "Groups: $CURRENT_GROUPS"
  print_line ""
fi

if [[ -z "$LXC_BIN" ]]; then
  record_fail "\`lxc\` is not installed or not on PATH"
else
  record_ok "found lxc at $LXC_BIN"
fi

if command -v systemctl >/dev/null 2>&1; then
  if systemctl is-active --quiet lxd; then
    record_ok "lxd service is active"
  else
    record_fail "lxd service is not active; try: sudo systemctl start lxd"
  fi
else
  record_warn "systemctl is unavailable; skipping lxd service check"
fi

if [[ -S "$LXD_SOCKET" ]]; then
  socket_group="$(stat -c '%G' "$LXD_SOCKET" 2>/dev/null || printf 'unknown')"
  socket_mode="$(stat -c '%A' "$LXD_SOCKET" 2>/dev/null || printf 'unknown')"
  record_ok "found LXD socket at $LXD_SOCKET ($socket_mode, group $socket_group)"
  if [[ "$socket_group" == "unknown" ]]; then
    record_warn "could not determine the socket-owning group for $LXD_SOCKET"
  elif printf '%s\n' "$CURRENT_GROUPS" | tr ' ' '\n' | grep -Fx "$socket_group" >/dev/null 2>&1; then
    record_ok "current user is in the socket-owning group"
  else
    record_fail "current user is not in the socket-owning group '$socket_group'; try: sudo usermod -aG $socket_group $CURRENT_USER"
  fi
else
  record_fail "LXD socket $LXD_SOCKET does not exist"
fi

lxc_info_output=""
if [[ -n "$LXC_BIN" ]]; then
  if lxc_info_output="$(lxc info 2>&1 >/dev/null)"; then
    record_ok "lxc can talk to the LXD daemon"
  else
    if [[ "$lxc_info_output" == *"unix socket"* && "$lxc_info_output" == *"permission denied"* ]]; then
      record_fail "current user cannot access the LXD socket"
    else
      record_fail "lxc could not talk to the LXD daemon: $lxc_info_output"
    fi
  fi
fi

if [[ -r "$APPARMOR_ENABLED" ]]; then
  if grep -q '^Y' "$APPARMOR_ENABLED"; then
    record_ok "AppArmor kernel support is enabled"
  else
    record_warn "AppArmor kernel support is disabled; LXD will warn and proceed without AppArmor confinement"
  fi
else
  record_warn "AppArmor kernel support could not be detected; LXD may warn and proceed without AppArmor confinement"
fi

if [[ -r "$CGROUP_CONTROLLERS" ]]; then
  if grep -qw 'hugetlb' "$CGROUP_CONTROLLERS"; then
    record_ok "cgroup hugetlb controller is available"
  else
    record_warn "cgroup hugetlb controller is missing; LXD will ignore hugepage limits"
  fi
else
  record_warn "cgroup controllers file is unavailable; skipping hugetlb check"
fi

if command -v "$QEMU_BIN" >/dev/null 2>&1; then
  record_ok "found $QEMU_BIN for LXD VM support"
else
  record_warn "$QEMU_BIN is not installed; LXD virtual-machine instances will be unavailable, but containers still work"
fi

if [[ -d "$LXD_LOG_DIR" ]]; then
  if [[ -r "$LXD_LOG_DIR" && -x "$LXD_LOG_DIR" ]]; then
    record_ok "LXD log directory is accessible at $LXD_LOG_DIR"
  else
    record_warn "LXD log directory exists at $LXD_LOG_DIR but is not readable by the current user; use sudo or journalctl -u lxd for daemon logs"
  fi
else
  record_warn "LXD log directory $LXD_LOG_DIR is missing"
fi

if (( FAILURES > 0 )); then
  if [[ -n "$lxc_info_output" ]]; then
    print_line "" >&2
    print_line "lxc info output:" >&2
    print_line "$lxc_info_output" >&2
  fi
  if [[ "$MODE" == "doctor" ]]; then
    print_line "" >&2
    print_line "Result: $FAILURES failure(s), $WARNINGS warning(s)" >&2
  fi
  exit 1
fi

if [[ "$MODE" == "doctor" ]]; then
  print_line ""
  print_line "Result: ready for LXD container launch ($WARNINGS warning(s))"
fi
