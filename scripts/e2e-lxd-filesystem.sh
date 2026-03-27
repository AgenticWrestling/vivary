#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
CONTAINER="${CONTAINER:-vivary-static}"
WORKSPACE="${KEEPERD_WORKSPACE:-/var/lib/vivary/workspace}"
TEMPLATE_ROOT="${E2E_TEMPLATE_ROOT:-/var/lib/vivary/e2e-fs-template}"
FAKE_CLAUDE_BIN="$ROOT_DIR/.tmp/e2e-fakeclaude"
RUN_ID="$(date +%s)"
AGENT="fs-e2e-$RUN_ID"

cleanup() {
  lxc exec "$CONTAINER" -- sh -lc "viv --socket $WORKSPACE/keeper.sock agent destroy --id $AGENT >/dev/null 2>&1 || true"
}
trap cleanup EXIT

wait_for_outcome() {
  local agent expected deadline line
  agent="$1"
  expected="$2"
  deadline=$((SECONDS + 60))
  while (( SECONDS < deadline )); do
    line="$(lxc exec "$CONTAINER" -- sh -lc "viv --socket $WORKSPACE/keeper.sock status | grep '^$agent[[:space:]]' || true")"
    if [[ "$line" == *" $expected "* ]]; then
      printf '%s\n' "$line"
      return 0
    fi
    sleep 1
  done
  printf 'Error: timed out waiting for %s outcome %s\n' "$agent" "$expected" >&2
  lxc exec "$CONTAINER" -- sh -lc "viv --socket $WORKSPACE/keeper.sock status" >&2 || true
  return 1
}

printf 'Building e2e fake claude helper...\n'
mkdir -p "$ROOT_DIR/.tmp"
CGO_ENABLED=0 go build -o "$FAKE_CLAUDE_BIN" ./cmd/e2e-fakeclaude

printf 'Preparing fs e2e template in %s...\n' "$CONTAINER"
lxc exec "$CONTAINER" -- sh -lc "rm -rf '$TEMPLATE_ROOT' && mkdir -p '$TEMPLATE_ROOT/output' '$TEMPLATE_ROOT/usr/bin' '$TEMPLATE_ROOT/usr/lib/vivary'"
lxc exec "$CONTAINER" -- sh -lc "cat > '$TEMPLATE_ROOT/agent.kdl' <<'EOF'
id \"template-fs-e2e\"
capabilities \"Filesystem_File_Write\" {
    File {
        path-prefix \"/var/lib/vivary/agents/$AGENT/output\"
    }
}
EOF"
lxc file push "$FAKE_CLAUDE_BIN" "$CONTAINER$TEMPLATE_ROOT/usr/bin/claude"
systemd_bin_dir="$(lxc exec "$CONTAINER" -- sh -lc 'dirname "$(readlink -f /run/current-system/sw/bin/systemd)"')"
lxc exec "$CONTAINER" -- sh -lc "mkdir -p '$TEMPLATE_ROOT$systemd_bin_dir' '$TEMPLATE_ROOT/run/current-system/sw/bin' && cp /run/current-system/sw/bin/capwrap '$TEMPLATE_ROOT/usr/lib/vivary/capwrap' && cp '$TEMPLATE_ROOT/usr/bin/claude' '$TEMPLATE_ROOT$systemd_bin_dir/claude' && cp '$TEMPLATE_ROOT/usr/bin/claude' '$TEMPLATE_ROOT/run/current-system/sw/bin/claude' && chmod 755 '$TEMPLATE_ROOT/usr/bin/claude' '$TEMPLATE_ROOT$systemd_bin_dir/claude' '$TEMPLATE_ROOT/run/current-system/sw/bin/claude' '$TEMPLATE_ROOT/usr/lib/vivary/capwrap'"

printf 'Creating agent %s...\n' "$AGENT"
lxc exec "$CONTAINER" -- viv --socket "$WORKSPACE/keeper.sock" agent create --id "$AGENT" --template "$TEMPLATE_ROOT"

printf 'Running allow-write prompt...\n'
lxc exec "$CONTAINER" -- viv --socket "$WORKSPACE/keeper.sock" prompt --agent "$AGENT" --seq 1 'write allow'
wait_for_outcome "$AGENT" success >/dev/null

printf 'Verifying file exists in subvolume...\n'
lxc exec "$CONTAINER" -- test -f "/var/lib/vivary/agents/$AGENT/output/test.txt"
lxc exec "$CONTAINER" -- grep "hello" "/var/lib/vivary/agents/$AGENT/output/test.txt"

printf 'Running deny-write prompt (out of scope)...\n'
lxc exec "$CONTAINER" -- viv --socket "$WORKSPACE/keeper.sock" prompt --agent "$AGENT" --seq 2 'write deny'
wait_for_outcome "$AGENT" success >/dev/null

printf '\nFilesystem e2e passed.\n'
