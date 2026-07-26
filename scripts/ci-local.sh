#!/usr/bin/env bash
# CI-parity validation runner — mirrors the steps in `.github/workflows/ci.yml`
# so developers and pipeline skills can verify a change locally before pushing.
#
# Prints a summary of each check and exits non-zero on the first failure so the
# caller (shell, skill, CI hook) can fail loudly. The motivating incident: a
# format-drift PR slipped past feature-dev because its validation swallowed
# non-zero exits.
set -u

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$REPO_ROOT"

FAIL_COUNT=0
FAILED_STEPS=()
FAILED_LOGS=()

# Every step's output is captured as well as streamed. Without this a failure
# that does not reproduce is unidentifiable after the fact: the run scrolls
# past, `npm` only logs its own exit code (never the test runner's stdout), and
# a caller that pipes to `tail` discards the very lines naming the failing
# test. Recovering "which test failed" must never depend on having guessed the
# right pipeline beforehand.
LOG_DIR="${CI_LOCAL_LOG_DIR:-$REPO_ROOT/.ci-local-logs}"
mkdir -p "$LOG_DIR"
rm -f "$LOG_DIR"/*.log 2>/dev/null || true

run_step() {
  local label="$1"
  shift
  local slug log code
  slug="$(printf '%s' "$label" | tr -c '[:alnum:]' '-' | tr -s '-' | sed 's/^-//; s/-$//')"
  log="$LOG_DIR/${slug}.log"
  echo ""
  echo "▶ $label"
  echo "  \$ $*"
  # `tee` keeps the terminal output live; PIPESTATUS[0] preserves the command's
  # own exit code, which a bare pipeline would otherwise replace with tee's.
  "$@" 2>&1 | tee "$log"
  code=${PIPESTATUS[0]}
  if [ "$code" -eq 0 ]; then
    echo "  ✓ $label"
  else
    echo "  ✗ $label (exit $code)"
    FAIL_COUNT=$((FAIL_COUNT + 1))
    FAILED_STEPS+=("$label")
    FAILED_LOGS+=("$log")
  fi
}

echo "CI-parity local validation — order mirrors .github/workflows/ci.yml"

# 1. Go build + tests (internal/ + cmd/)
if [ -f go.mod ]; then
  run_step "go build ./..." go build ./...
  run_step "go test ./... -count=1" go test ./... -count=1
fi

# 2. Generated files must be in sync
if [ -f Makefile ] && grep -q '^generate-ipc-client:' Makefile; then
  run_step "make generate-ipc-client" make generate-ipc-client
  run_step "generated IPC client in sync" \
    git diff --exit-code packages/nightgauge-vscode/src/services/IpcClient.generated.ts
fi

# 3. npm audit allow-list
if [ -f scripts/npm-audit-check.js ]; then
  run_step "npm audit allow-list" node scripts/npm-audit-check.js
fi

# 4. SKILL.md metadata validation
if [ -f scripts/validate-skill-metadata.sh ]; then
  run_step "SKILL.md metadata" bash scripts/validate-skill-metadata.sh
fi

# 5. Publication boundary — allowlist, fail-closed. Catches private-class content
#    before it is pushed rather than after CI rejects it.
if [ -f scripts/publication-boundary-check.py ]; then
  run_step "publication boundary" python3 scripts/publication-boundary-check.py
fi

# 4b. Cache-boundary measurement smoke test
if [ -f scripts/test-measure-cache-boundary-loss.sh ]; then
  run_step "Cache-boundary measurement smoke" bash scripts/test-measure-cache-boundary-loss.sh
fi

# 5. ESLint
if grep -q '"lint"' package.json 2>/dev/null; then
  run_step "ESLint" npm run lint
fi

# 6. Prettier formatting — the #1 cause of avoidable CI failures.
if grep -q '"format:check"' package.json 2>/dev/null; then
  run_step "Prettier format:check" npm run format:check
fi

# 7. Build all workspaces
if grep -q '"build"' package.json 2>/dev/null; then
  run_step "npm run build (all workspaces)" npm run build
fi

# 7b. Phase markers ↔ PHASE_REGISTRY drift check
# Runs after the SDK build so the script can import PHASE_REGISTRY from
# the workspace package. Catches the class of registry↔skill marker drift
# before it reaches the orchestrator.
if [ -f scripts/validate-phase-markers.ts ]; then
  run_step "Phase markers ↔ PHASE_REGISTRY" npx tsx scripts/validate-phase-markers.ts
fi

# 8. Tests (single run — NEVER bare vitest which hangs in watch mode)
if grep -q '"test"' package.json 2>/dev/null; then
  run_step "npm run test (all workspaces)" npm run test -- --run
fi

# 9. Generated package contributions in sync
if [ -f packages/nightgauge-vscode/scripts/generate-package-contributions.ts ]; then
  run_step "Generated VSCode contributions in sync" \
    npx -w nightgauge-vscode tsx scripts/generate-package-contributions.ts --check
fi

# 9b. @types/vscode must not exceed engines.vscode. `vsce package` enforces this
# at packaging time (dev-install.sh / release) but no build/test step does — a
# Dependabot bump (#165) raised the types past the engine floor and only broke
# at install. Guard it here so the mismatch fails locally, not at install.
if [ -f packages/nightgauge-vscode/scripts/check-engine-types.mjs ]; then
  run_step "@types/vscode <= engines.vscode" \
    node packages/nightgauge-vscode/scripts/check-engine-types.mjs
fi

# 10. Markdown link check — cross-document reference integrity (root *.md + docs/**)
if [ -f scripts/check-md-links.sh ]; then
  run_step "Markdown link check" bash scripts/check-md-links.sh
fi

echo ""
echo "-------------------------------------------------------------------------"
if [ "$FAIL_COUNT" -eq 0 ]; then
  echo "✓ All CI-parity checks passed."
  exit 0
else
  echo "✗ $FAIL_COUNT check(s) failed:"
  for i in "${!FAILED_STEPS[@]}"; do
    echo "  - ${FAILED_STEPS[$i]}"
    echo "      full output: ${FAILED_LOGS[$i]}"
    # Pull the failing assertions up to the summary. A vitest failure can sit
    # thousands of lines above the exit line, so "scroll up" is not a usable
    # instruction — and is exactly how a failure escapes identification.
    matches="$(grep -aE '^[[:space:]]*(×|✗|FAIL |--- FAIL|AssertionError|Error:)' "${FAILED_LOGS[$i]}" 2>/dev/null | head -15 || true)"
    if [ -n "$matches" ]; then
      printf '%s\n' "$matches" | sed 's/^/      /'
    else
      echo "      (no recognised failure marker — see the log above for detail)"
    fi
  done
  echo ""
  echo "Fix the failures before pushing. Most format/lint failures are auto-fixable:"
  echo "  npm run format"
  echo "  npm run lint -- --fix"
  exit 1
fi
