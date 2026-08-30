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
  # -json so the skip accounting has events to read (#474): a package whose
  # tests all SKIPPED still prints `ok`, so without it a guard that stopped
  # guarding is indistinguishable from one that passed. Piped back through
  # go-test-json-echo.py so the log stays readable.
  run_step "go test ./... -count=1 (with skip accounting)" \
    bash -c 'set -o pipefail; go test -json ./... -count=1 | tee go-test.json | python3 scripts/go-test-json-echo.py && python3 scripts/check-go-test-skips.py go-test.json'
  # Mirrors ci.yml's "Test (race, whole tree)" step (#493), which replaced the
  # internal/orchestrator-scoped step from #428 — one race step, not two. The
  # scoped step existed because the race detector is the only thing that fails
  # when a drainBackground() join is deleted from a test body, and that argument
  # was never specific to one package. Measured whole-tree cost: +3% over the
  # plain run (2m55s -> 3m00s on an Apple M-series), not the ~3x once feared.
  run_step "go test -race -count=1 ./..." \
    go test -race -count=1 ./...
  run_step "gofmt -l ./internal ./cmd" \
    bash -c '! gofmt -l ./internal ./cmd | grep .'
fi

# 1b. branch-merged-check.sh regression suite — the shell-side decision
#     procedure `git branch -D` reclaim decisions defer to (AGENTS.md § Clean
#     up on merge). Kept aligned with the Go sweep's own ancestry-acceptance
#     door (#593).
if [ -f scripts/test-branch-merged-check.sh ]; then
  run_step "branch-merged-check.sh regression suite" \
    bash scripts/test-branch-merged-check.sh
fi

# 1c. CI change-class gate (#647) — drives scripts/ci-change-class.sh against
#     real git fixtures AND asserts .github/workflows/ci.yml still consumes its
#     outputs. Mirrors ci.yml's own ungated step in the Go job; the wiring half
#     is what makes the gate impossible to document without shipping.
if [ -f scripts/test-ci-change-class.sh ]; then
  run_step "CI change-class gate regression suite" \
    bash scripts/test-ci-change-class.sh
fi

# 2. Generated files must be in sync
if [ -f Makefile ] && grep -q '^generate-ipc-client:' Makefile; then
  run_step "make generate-ipc-client" make generate-ipc-client
  run_step "generated IPC client in sync" \
    git diff --exit-code packages/nightgauge-vscode/src/services/IpcClient.generated.ts
fi

# 2b. Canonical terminal-kind rule table (#306) — the generated SDK module and
#     the behaviour golden must be exactly what table.json renders.
if [ -f Makefile ] && grep -q '^check-terminal-kind-table:' Makefile; then
  run_step "terminal-kind table consumers in sync" make check-terminal-kind-table
fi

# 2c. Platform operation registry (#750) — api/generated/go/platform/operations.gen.go
#     must be exactly what api/platform-operations.yaml renders. That file carries
#     each operation's credential requirement, which is what the conformance test
#     in internal/platform checks call sites against; a hand-edited copy is a
#     contract nobody reviewed.
if [ -f Makefile ] && grep -q '^check-platform-operations:' Makefile; then
  run_step "platform operation registry in sync" make check-platform-operations
fi

# No step here for the hand-rolled-platform-request gate (#750): it is enforced
# by TestPlatformRawHTTP_RealPackageIsClean, which step 1's `go test ./...`
# already runs — and which ci.yml runs UNGATED via `go test ./internal/preflight/`.
# `nightgauge preflight platform-raw-http` is the human/skill-facing entry point
# for the same check, not a second gate.

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
#
#    Scope is the tracked tree PLUS untracked, non-ignored files (#716). Those
#    are the newest content in a change and the guard used to skip them
#    silently, so this step handed back a pass it had not earned and CI failed
#    on the very files it never opened. A run that scanned untracked files says
#    so on its success line; one that exits non-zero names them inline.
if [ -f scripts/publication-boundary-check.py ]; then
  run_step "publication boundary" python3 scripts/publication-boundary-check.py
fi

# 5b. Publication boundary self-test — proves the guard still fails closed
#     (mirrors .github/workflows/publication-boundary.yml's second step).
if [ -f scripts/test-publication-boundary.sh ]; then
  run_step "Publication boundary regression suite" bash scripts/test-publication-boundary.sh
fi

# 5b-iii. Rename carry-over (#837) — hermetic, ~0.3s. Deliberately NOT a case
#     in the suite above: exercising a rename means `git mv`-ing a real tracked
#     file, and that suite is re-run inside sandboxes and SIGKILLed mid-run by
#     the hermeticity tests below. A kill between the mv and the restore breaks
#     the checkout. This builds its own throwaway repo instead.
if [ -f scripts/test-publication-boundary-rename.py ]; then
  run_step "Publication boundary rename carry-over" \
    python3 scripts/test-publication-boundary-rename.py
fi

# 5b-i-b. Derived reference ceiling (#1078) — the ceiling is inferred from merge
#     history rather than recorded, so the inference is load-bearing. The case
#     that matters is that a pull-request TITLE cannot raise it: the mark comes
#     from text an author partly controls, and a raised ceiling weakens the rule
#     silently. Same throwaway-repo shape as the rename suite, for the same
#     crash-safety reason.
if [ -f scripts/test-publication-boundary-ceiling.py ]; then
  run_step "Publication boundary derived ceiling" \
    python3 scripts/test-publication-boundary-ceiling.py
fi

# 5b-i-c. Branch-local ceiling lag and baseline erosion (#1129) — the derived
#     ceiling is per-branch and `git merge origin/main` does NOT advance it
#     (the merge is the second parent), while `tree_baseline` is one global
#     integer compared against a count that GROWS as the ceiling falls. The two
#     together blocked a branch with references it never wrote. Same
#     throwaway-repo shape: these cases need controlled merge topology.
if [ -f scripts/test-publication-boundary-erosion.py ]; then
  run_step "Publication boundary ceiling lag and erosion" \
    python3 scripts/test-publication-boundary-erosion.py
fi

# 5b-ii. Publication boundary suite hermeticity (#713, #722) — the suite plants
#     deliberately-forbidden fixtures, so two properties have to hold and
#     neither is self-evident from reading it: a SIGKILLed run leaves the
#     operator's checkout byte-identical, and the worktree registration such a
#     run leaks (trap cannot catch SIGKILL, and prune cannot remove an entry
#     whose directory survives) is reclaimed by the NEXT run. Both are asserted
#     by doing it for real, which costs one extra full suite run.
# 5b-i. Attribution helpers (#832) — the hermeticity assertions below scope
# themselves to what the SUITE writes rather than to global repo state. A
# scoped assertion that scoped away the real failure would be silently useless,
# so the scoping has its own tests. Costs ~1s: no tree scan.
if [ -f scripts/test-publication-boundary-attribution.sh ]; then
  run_step "Publication boundary attribution" \
    bash scripts/test-publication-boundary-attribution.sh
fi

if [ -f scripts/test-publication-boundary-hermeticity.sh ]; then
  run_step "Publication boundary suite hermeticity" \
    bash scripts/test-publication-boundary-hermeticity.sh
fi

# 5c. Band-vocabulary reintroduction gate (#582) — fails on hand-inlined band
#     closed sets / regex alternations in production source, outside the
#     allowed surfaces (spike #568 §5).
if [ -f scripts/check-band-vocabulary.py ]; then
  run_step "band-vocabulary reintroduction gate" python3 scripts/check-band-vocabulary.py
fi

# 5d. Band-vocabulary gate self-test — proves the gate still fails closed.
if [ -f scripts/test-band-vocabulary-check.sh ]; then
  run_step "Band-vocabulary gate regression suite" bash scripts/test-band-vocabulary-check.sh
fi

# 5e. Stale-visibility-prose reintroduction gate (#697) — fails on a tracked
#     artifact unconditionally asserting this repository is private, the
#     exact shape all four #697 instances took.
if [ -f scripts/check-visibility-prose.py ]; then
  run_step "visibility-prose reintroduction gate" python3 scripts/check-visibility-prose.py
fi

# 5f. Nonexistent-workflow-reference gate (#545) — fails when a tracked file
# names a `.github/workflows/*.yml` path that does not exist. Self-test first:
# a gate nothing exercises degrades into an unconditional pass.
if [ -f scripts/test-workflow-refs-check.sh ]; then
  run_step "Workflow-reference gate regression suite" bash scripts/test-workflow-refs-check.sh
fi
if [ -f scripts/check-workflow-refs.py ]; then
  run_step "nonexistent-workflow-reference gate" python3 scripts/check-workflow-refs.py
fi

# 5g. CLA gate regression suite (#976) — spawns the real .github/scripts/cla-check.mjs
#     against a local HTTP stub and pins its bounded retry: a transient 5xx/429
#     or socket failure is retried, every other 4xx is the gate's own verdict and
#     is raised on the first shot. Mirrors .github/workflows/lint.yml's own step
#     (not ci.yml's — this block is the lint.yml mirror region). Costs ~12s: the
#     backoff is deliberately real, because a test-only zero-delay knob would
#     re-open the mutation the timing assertion exists to kill.
if [ -f .github/scripts/cla-check.test.mjs ]; then
  run_step "CLA gate regression suite" node --test .github/scripts/cla-check.test.mjs
fi

# 4b. Cache-boundary measurement smoke test
if [ -f scripts/test-measure-cache-boundary-loss.sh ]; then
  run_step "Cache-boundary measurement smoke" bash scripts/test-measure-cache-boundary-loss.sh
fi

# 4b. Test-tree typecheck (#499).
# tsconfig.json covers src/** only, vitest transforms through esbuild
# (transpile-only, so `import type` is erased unresolved), and eslint registers
# the TS parser for tests without a `project` — so nothing typechecked tests/**
# or the Playwright fixtures. A type-only import could name an export that no
# longer exists and the suite stayed green.
run_step "VSCode test-tree typecheck" npm run typecheck:tests -w nightgauge-vscode

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

# 11. Drift-gate self-test — proves the mirror gate below still fails closed
#     rather than passing vacuously, the defect it was created to fix (#539),
#     and that it no longer goes red on a dirty tree it has no quarrel with
#     (#546). Paired with 11b as 5b is paired with 5.
if [ -f scripts/test-mirror-drift-gate.sh ]; then
  run_step "Mirror drift gate regression suite" \
    bash scripts/test-mirror-drift-gate.sh
fi

# 11a2. Issue-body heading contract (#711) — the required-heading table exists
#       in three files (issue-audit SKILL.md, docs/ISSUE_AUDIT.md, and
#       issue-create's authoring rules) and issue-create runs issue-audit as its
#       own terminal gate. When the copies drift, every issue the pipeline
#       authors fails its own audit; that shipped as a WARNING nobody read until
#       #711. Self-test first, same reasoning as 11 and 5b.
if [ -f scripts/test-issue-body-contract.sh ]; then
  run_step "Issue-body contract gate regression suite" \
    bash scripts/test-issue-body-contract.sh
fi
if [ -f scripts/check-issue-body-contract.py ]; then
  run_step "Issue-body heading contract" \
    python3 scripts/check-issue-body-contract.py
fi

# 11b. Plugin skills mirror drift — claude-plugins/nightgauge/skills/ is
#      generated output committed on purpose (the marketplace manifest ships it
#      as the plugin source), so a canonical skills/ edit that never reached it
#      publishes a stale plugin. Mirrors lint.yml's step of the same name.
#
#      Read-only, like every other check in this file: `--check-mirror`
#      regenerates into a temp destination and compares the two trees — paths,
#      contents, symlink targets and the executable bit git tracks — so it
#      neither writes the checkout nor cares what is staged. That matters here
#      specifically — this script is run BECAUSE you have uncommitted work, and
#      the previous form (regenerate in place, fail on a dirty index) therefore
#      misfired in its primary use case (#546). When it does report drift, the
#      fix is `bash scripts/install-agent-skills.sh --generate-only`.
if [ -f scripts/install-agent-skills.sh ]; then
  run_step "Plugin skills mirror in sync" \
    bash scripts/install-agent-skills.sh --check-mirror
fi

# 11c. Mirror link integrity — the question 11b structurally cannot ask. The
#      drift gate compares the mirror to the generator's own output, so when the
#      generator copied `../../docs/X.md` verbatim into a directory two levels
#      deeper, both sides carried the same ~90 dead links and 11b was green by
#      construction (#831). This gate resolves each link against the file that
#      contains it, which is a fact about the tree rather than about the copy.
#      Self-test first, same reasoning as 11 and 5b.
if [ -f scripts/test-mirror-link-check.sh ]; then
  run_step "Mirror link gate regression suite" \
    bash scripts/test-mirror-link-check.sh
fi
if [ -f scripts/check-mirror-links.py ]; then
  run_step "Mirror link integrity" python3 scripts/check-mirror-links.py
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
