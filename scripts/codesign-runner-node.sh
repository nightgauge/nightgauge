#!/usr/bin/env bash
# codesign-runner-node.sh — stop the macOS "Do you want node to accept incoming
# network connections?" prompt on a self-hosted GitHub Actions runner.
#
# WHY THIS IS NEEDED
#   The runner executes every JavaScript action (actions/checkout,
#   actions/setup-go, actions/cache, actions/github-script, ...) with its OWN
#   bundled Node at ~/actions-runner/externals/node*/bin/node — NOT the nvm Node
#   the workflows use for project code. That bundled binary is unsigned, so the
#   macOS Application Firewall re-prompts every reboot, and again whenever the
#   runner self-updates and replaces the binary. (The workflows already use the
#   runner's nvm Node for all project `node`/`npm` invocations and never call
#   actions/setup-node, so no *downloaded* Node is involved — the prompt is
#   purely these runner-internal binaries plus the nvm ones that run build/test
#   tools like vitest/esbuild.)
#
#   Ad-hoc codesigning (`codesign -s -`) gives each binary a stable identity, so
#   the firewall's allow decision persists across reboots. We also pre-authorize
#   the binaries with socketfilterfw so the first run never prompts at all.
#
# WHEN TO RUN
#   - Once after setting up the runner.
#   - Again after the runner auto-updates (it replaces externals/node*). Wire it
#     into the runner's startup (e.g. the runsvc.sh / LaunchAgent) to make it
#     durable.
#
# USAGE
#   bash scripts/codesign-runner-node.sh [RUNNER_DIR]
#     RUNNER_DIR defaults to ~/actions-runner
#
# Requires sudo only for the firewall pre-authorization; codesigning your own
# binaries does not.
set -uo pipefail

RUNNER_DIR="${1:-$HOME/actions-runner}"
FW="/usr/libexec/ApplicationFirewall/socketfilterfw"
signed=0

sign() {
  local bin="$1"
  [ -f "$bin" ] || return 0
  if codesign --force --sign - "$bin" 2>/dev/null; then
    echo "  signed: $bin"
    signed=$((signed + 1))
  else
    echo "  WARN: could not codesign $bin"
  fi
  if [ -x "$FW" ]; then
    sudo "$FW" --add "$bin" >/dev/null 2>&1 || true
    sudo "$FW" --unblockapp "$bin" >/dev/null 2>&1 || true
  fi
}

echo "Codesigning runner-bundled Node under $RUNNER_DIR/externals ..."
if [ -d "$RUNNER_DIR/externals" ]; then
  while IFS= read -r bin; do sign "$bin"; done \
    < <(find "$RUNNER_DIR/externals" -type f -name node 2>/dev/null)
else
  echo "  (no $RUNNER_DIR/externals — is the runner installed there?)"
fi

echo "Codesigning nvm Node under $HOME/.nvm/versions/node ..."
if [ -d "$HOME/.nvm/versions/node" ]; then
  while IFS= read -r bin; do sign "$bin"; done \
    < <(find "$HOME/.nvm/versions/node" -type f -name node 2>/dev/null)
else
  echo "  (no nvm Node found under $HOME/.nvm)"
fi

echo "Done — codesigned $signed Node binary(ies)."
echo "Re-run after the runner auto-updates (it replaces externals/node*)."
