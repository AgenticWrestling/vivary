#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
CONTAINER="${CONTAINER:-vivary-static}"
WORKSPACE="${KEEPERD_WORKSPACE:-/var/lib/vivary/workspace}"
TEMPLATE_ROOT="${E2E_TEMPLATE_ROOT:-/var/lib/vivary/e2e-browser-template}"
FAKE_CLAUDE_BIN="$ROOT_DIR/.tmp/e2e-fakeclaude"
RUN_ID="$(date +%s)"
ALLOW_AGENT="browser-e2e-allow-$RUN_ID"
DENY_DOMAIN_AGENT="browser-e2e-deny-domain-$RUN_ID"
DENY_PATH_AGENT="browser-e2e-deny-path-$RUN_ID"

cleanup() {
  lxc exec "$CONTAINER" -- sh -lc "viv --socket $WORKSPACE/keeper.sock agent destroy --id $ALLOW_AGENT >/dev/null 2>&1 || true"
  lxc exec "$CONTAINER" -- sh -lc "viv --socket $WORKSPACE/keeper.sock agent destroy --id $DENY_DOMAIN_AGENT >/dev/null 2>&1 || true"
  lxc exec "$CONTAINER" -- sh -lc "viv --socket $WORKSPACE/keeper.sock agent destroy --id $DENY_PATH_AGENT >/dev/null 2>&1 || true"
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

check_browser_path_behavior() {
  local allow_agent deny_domain_agent deny_path_agent
  allow_agent="$1"
  deny_domain_agent="$2"
  deny_path_agent="$3"
  lxc exec "$CONTAINER" -- sh -lc "journalctl -u keeperd --no-pager | grep -F 'browser session acquired' | grep -F 'agent=$allow_agent' >/dev/null"
  if lxc exec "$CONTAINER" -- sh -lc "journalctl -u keeperd --no-pager | grep -F 'browser session acquired' | grep -F 'agent=$deny_domain_agent' >/dev/null"; then
    printf 'Error: deny-domain agent %s unexpectedly acquired a browser session\n' "$deny_domain_agent" >&2
    return 1
  fi
  if lxc exec "$CONTAINER" -- sh -lc "journalctl -u keeperd --no-pager | grep -F 'browser session acquired' | grep -F 'agent=$deny_path_agent' >/dev/null"; then
    printf 'Error: deny-path agent %s unexpectedly acquired a browser session\n' "$deny_path_agent" >&2
    return 1
  fi
}

printf 'Building e2e fake claude helper...\n'
mkdir -p "$ROOT_DIR/.tmp"
CGO_ENABLED=0 go build -o "$FAKE_CLAUDE_BIN" ./cmd/e2e-fakeclaude

printf 'Checking runtime container %s...\n' "$CONTAINER"
lxc info "$CONTAINER" >/dev/null
lxc exec "$CONTAINER" -- test -S /run/vivary/chromed-host/chromed.sock
lxc exec "$CONTAINER" -- test -S "$WORKSPACE/keeper.sock"

printf 'Preparing browser e2e template in %s...\n' "$CONTAINER"
lxc exec "$CONTAINER" -- sh -lc "rm -rf '$TEMPLATE_ROOT' && mkdir -p '$TEMPLATE_ROOT/output' '$TEMPLATE_ROOT/usr/bin' '$TEMPLATE_ROOT/usr/lib/vivary'"
lxc exec "$CONTAINER" -- sh -lc "cat > '$TEMPLATE_ROOT/agent.kdl' <<'EOF'
id \"template-browser-e2e\"
browser {
    headless true
}
capabilities \"Browser_Page_Read\" {
    Link {
        domain \"en.wikipedia.org\"
        path-prefix \"/wiki\"
    }
}
capabilities \"Filesystem_File_Write\"
EOF"
lxc file push "$FAKE_CLAUDE_BIN" "$CONTAINER$TEMPLATE_ROOT/usr/bin/claude"
systemd_bin_dir="$(lxc exec "$CONTAINER" -- sh -lc 'dirname "$(readlink -f /run/current-system/sw/bin/systemd)"')"
lxc exec "$CONTAINER" -- sh -lc "mkdir -p '$TEMPLATE_ROOT$systemd_bin_dir' '$TEMPLATE_ROOT/run/current-system/sw/bin' && cp /run/current-system/sw/bin/capwrap '$TEMPLATE_ROOT/usr/lib/vivary/capwrap' && cp '$TEMPLATE_ROOT/usr/bin/claude' '$TEMPLATE_ROOT$systemd_bin_dir/claude' && cp '$TEMPLATE_ROOT/usr/bin/claude' '$TEMPLATE_ROOT/run/current-system/sw/bin/claude' && chmod 755 '$TEMPLATE_ROOT/usr/bin/claude' '$TEMPLATE_ROOT$systemd_bin_dir/claude' '$TEMPLATE_ROOT/run/current-system/sw/bin/claude' '$TEMPLATE_ROOT/usr/lib/vivary/capwrap'"

printf 'Creating allow agent %s...\n' "$ALLOW_AGENT"
lxc exec "$CONTAINER" -- viv --socket "$WORKSPACE/keeper.sock" agent create --id "$ALLOW_AGENT" --template "$TEMPLATE_ROOT"
printf 'Creating deny-domain agent %s...\n' "$DENY_DOMAIN_AGENT"
lxc exec "$CONTAINER" -- viv --socket "$WORKSPACE/keeper.sock" agent create --id "$DENY_DOMAIN_AGENT" --template "$TEMPLATE_ROOT"
printf 'Creating deny-path agent %s...\n' "$DENY_PATH_AGENT"
lxc exec "$CONTAINER" -- viv --socket "$WORKSPACE/keeper.sock" agent create --id "$DENY_PATH_AGENT" --template "$TEMPLATE_ROOT"

printf 'Running allow-path browser prompt...\n'
lxc exec "$CONTAINER" -- viv --socket "$WORKSPACE/keeper.sock" prompt --agent "$ALLOW_AGENT" --seq 1 'allow browser e2e prompt'
printf 'Running deny-domain browser prompt...\n'
lxc exec "$CONTAINER" -- viv --socket "$WORKSPACE/keeper.sock" prompt --agent "$DENY_DOMAIN_AGENT" --seq 1 'deny domain browser e2e prompt'
printf 'Running deny-path browser prompt...\n'
lxc exec "$CONTAINER" -- viv --socket "$WORKSPACE/keeper.sock" prompt --agent "$DENY_PATH_AGENT" --seq 1 'deny path browser e2e prompt'

printf 'Waiting for completion outcomes...\n'
allow_line="$(wait_for_outcome "$ALLOW_AGENT" success)"
deny_domain_line="$(wait_for_outcome "$DENY_DOMAIN_AGENT" success)"
deny_path_line="$(wait_for_outcome "$DENY_PATH_AGENT" success)"

printf 'Checking allow/deny browser mediation behavior...\n'
check_browser_path_behavior "$ALLOW_AGENT" "$DENY_DOMAIN_AGENT" "$DENY_PATH_AGENT"

printf 'Verifying audit trail via vivlog...\n'
# Check for completion events.
lxc exec "$CONTAINER" -- sh -lc "/usr/local/bin/vivlog --db $WORKSPACE/audit.db grep --msg-type CompletionEvent | grep -F '$ALLOW_AGENT' >/dev/null"
lxc exec "$CONTAINER" -- sh -lc "/usr/local/bin/vivlog --db $WORKSPACE/audit.db grep --msg-type CompletionEvent | grep -F '$DENY_DOMAIN_AGENT' >/dev/null"
# Check for security events (capability_denied).
lxc exec "$CONTAINER" -- sh -lc "/usr/local/bin/vivlog --db $WORKSPACE/audit.db security | grep -F '$DENY_DOMAIN_AGENT' >/dev/null"
lxc exec "$CONTAINER" -- sh -lc "/usr/local/bin/vivlog --db $WORKSPACE/audit.db security | grep -F '$DENY_PATH_AGENT' >/dev/null"

printf 'Verifying agent destruction cleanup...\n'
lxc exec "$CONTAINER" -- viv --socket "$WORKSPACE/keeper.sock" agent destroy --id "$ALLOW_AGENT"
# Check subvolume gone.
if lxc exec "$CONTAINER" -- test -d "/var/lib/vivary/agents/$ALLOW_AGENT"; then
  printf 'Error: subvolume for %s still exists after destroy\n' "$ALLOW_AGENT" >&2
  exit 1
fi
# Check nftables table gone.
if lxc exec "$CONTAINER" -- nft list table ip "vivary-$ALLOW_AGENT" >/dev/null 2>&1; then
  printf 'Error: nftables table for %s still exists after destroy\n' "$ALLOW_AGENT" >&2
  exit 1
fi

printf '\nBrowser e2e passed.\n'
printf 'Allow: %s\n' "$allow_line"
printf 'Deny Domain: %s\n' "$deny_domain_line"
printf 'Deny Path:   %s\n' "$deny_path_line"
printf 'Observed browser session only for allow-path agent.\n'
