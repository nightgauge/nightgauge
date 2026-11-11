#!/usr/bin/env bash
#
# playwright-os-deps.sh — install Playwright's Chromium OS dependencies (apt)
# for the Playwright webview CI job, tolerating an unreachable apt mirror
# when the cached headless Chromium launches anyway (#3199).
#
# The install runs `apt-get`; while Ubuntu's mirrors do not resolve it exits
# non-zero even though the runner image already carries every library Chromium
# links against. On install failure this launches Chromium once: a launch that
# succeeds continues with a ::warning::, a launch that fails exits non-zero.
#
# Run from the repository root: bash scripts/playwright-os-deps.sh
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"

npx -w packages/nightgauge playwright install-deps chromium
rc=$?
[ "$rc" -eq 0 ] && exit 0

echo "Playwright OS-dependency install failed (exit $rc); probing headless Chromium launch"
probe_err="$(cd "$REPO_ROOT/packages/nightgauge" && node -e '
const { chromium } = require("@playwright/test");
chromium.launch({ timeout: 60000 })
  .then((b) => b.close())
  .catch((e) => { console.error(String(e && e.stack || e)); process.exit(1); });
' 2>&1)"
probe_rc=$?
if [ "$probe_rc" -eq 0 ]; then
  echo "::warning::Playwright OS-dependency install failed (exit $rc); headless Chromium launched anyway, continuing"
  exit 0
fi
echo "$probe_err"
echo "::error::Playwright OS-dependency install failed (exit $rc) and headless Chromium did not launch (exit $probe_rc)"
exit "$probe_rc"
