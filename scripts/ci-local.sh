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

LIST_STEPS=0
if [ "${1:-}" = "--list-steps" ]; then
  LIST_STEPS=1
fi

# --- Preflight: every path this gate consumes must EXIST (#983) --------------
#
# Every step below used to be wrapped in `if [ -f <the script it runs> ]; then`
# with no `else` and no warning. If the file was deleted, renamed or moved, the
# step vanished from the gate and `ci-local.sh` still exited 0 — a gate that
# silently stops gating, in the one place the reduction is invisible: its own
# green exit. An existence check is not a consumption check (#975); every path
# here is a TRACKED file in this repository, and an absent tracked file is an
# error, not a configuration to degrade gracefully around.
#
# So the guards are gone and the requirement is asserted ONCE, up front, by
# name. A missing path fails in under a second instead of after the full gate,
# and says which path is missing.
#
# Keep these lists in step with the `run_step` calls below. `--list-steps` plus
# scripts/ci-local-steps.txt (asserted by scripts/test-ci-local-inventory.sh)
# is what makes a drift between the two impossible to land silently.
REQUIRED_FILES=(
  go.mod
  scripts/test-ci-local-inventory.sh
  scripts/ci-local-steps.txt
  scripts/test-branch-merged-check.sh
  scripts/test-ci-change-class.sh
  scripts/npm-audit-check.js
  scripts/validate-skill-metadata.sh
  scripts/test-validate-skill-metadata.sh
  scripts/publication-boundary-check.py
  scripts/test-publication-boundary.sh
  scripts/test-publication-boundary-rename.py
  scripts/test-publication-boundary-ceiling.py
  scripts/test-publication-boundary-erosion.py
  scripts/test-publication-boundary-attribution.sh
  scripts/test-publication-boundary-hermeticity.sh
  scripts/check-band-vocabulary.py
  scripts/test-band-vocabulary-check.sh
  scripts/check-visibility-prose.py
  scripts/test-workflow-refs-check.sh
  scripts/check-workflow-refs.py
  .github/scripts/cla-check.test.mjs
  scripts/test-measure-cache-boundary-loss.sh
  scripts/validate-phase-markers.ts
  packages/nightgauge-vscode/scripts/generate-package-contributions.ts
  packages/nightgauge-vscode/scripts/check-engine-types.mjs
  scripts/check-md-links.sh
  scripts/test-check-md-links.sh
  scripts/test-mirror-drift-gate.sh
  scripts/test-issue-body-contract.sh
  scripts/check-issue-body-contract.py
  scripts/install-agent-skills.sh
  scripts/test-mirror-link-check.sh
  scripts/check-mirror-links.py
)

# Makefile targets the gate invokes. `[ -f Makefile ] && grep -q '^t:' Makefile`
# was the compound form of the same defect: it skipped silently on a missing
# Makefile OR a renamed target.
REQUIRED_MAKE_TARGETS=(
  generate-ipc-client
  check-terminal-kind-table
  check-platform-operations
)

# package.json scripts the gate invokes. Same shape again: `grep -q '"lint"'
# package.json` skipped the step when the script was renamed.
REQUIRED_NPM_SCRIPTS=(
  lint
  format:check
  build
  test
)

MISSING=()
for path in "${REQUIRED_FILES[@]}"; do
  [ -e "$path" ] || MISSING+=("missing required file: $path")
done
if [ ! -f Makefile ]; then
  MISSING+=("missing required file: Makefile")
else
  for target in "${REQUIRED_MAKE_TARGETS[@]}"; do
    grep -q "^${target}:" Makefile || MISSING+=("missing required Makefile target: ${target}")
  done
fi
if [ ! -f package.json ]; then
  MISSING+=("missing required file: package.json")
else
  for script in "${REQUIRED_NPM_SCRIPTS[@]}"; do
    grep -q "\"${script}\":" package.json || MISSING+=("missing required package.json script: ${script}")
  done
fi
if [ "${#MISSING[@]}" -gt 0 ]; then
  {
    echo "✗ ci-local.sh preflight failed — this gate cannot run as written."
    printf '  - %s\n' "${MISSING[@]}"
    echo ""
    echo "Every path above is a tracked file, Makefile target or package.json"
    echo "script that a step below invokes. A missing one used to make its step"
    echo "disappear from the gate silently (#983); it is now a hard failure."
    echo "Restore the path, or delete its step AND its entry in"
    echo "scripts/ci-local-steps.txt together."
  } >&2
  exit 1
fi

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
if [ "$LIST_STEPS" -eq 0 ]; then
  mkdir -p "$LOG_DIR"
  rm -f "$LOG_DIR"/*.log 2>/dev/null || true
fi

run_step() {
  local label="$1"
  shift
  # `--list-steps` prints the inventory the gate WILL run, in execution order,
  # and runs nothing. scripts/test-ci-local-inventory.sh diffs it against the
  # checked-in scripts/ci-local-steps.txt, so a deleted or reworded step goes
  # red — an assertion against the list the script actually runs, which a
  # substring grep for `run_step` structurally cannot make.
  if [ "$LIST_STEPS" -eq 1 ]; then
    printf '%s\n' "$label"
    return 0
  fi
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

if [ "$LIST_STEPS" -eq 0 ]; then
  echo "CI-parity local validation — order mirrors .github/workflows/ci.yml"
fi

# 0. Step-inventory guard (#983) — FIRST, because it is the only check that can
#    tell whether the rest of this file is still the gate it claims to be. The
#    steps below used to be guarded by `if [ -f <script> ]` with no `else`, so a
#    deleted script removed its step and this file still exited 0. It cannot
#    detect that itself: a skipped step and a passing step are identical in its
#    output and in its exit code. This diffs `--list-steps` against the
#    checked-in scripts/ci-local-steps.txt, which is a second observer.
run_step "ci-local.sh step inventory" bash scripts/test-ci-local-inventory.sh

# 1. Go build + tests (internal/ + cmd/)
run_step "go build ./..." go build ./...
run_step "go test ./... -count=1" go test ./... -count=1
# Mirrors ci.yml's "Test (race, orchestrator)" step (#428). Scoped to one
# package: the race detector is the only thing that fails when a
# drainBackground() join is deleted from a test body.
run_step "go test -race -count=1 ./internal/orchestrator/" \
  go test -race -count=1 ./internal/orchestrator/
run_step "gofmt -l ./internal ./cmd" \
  bash -c '! gofmt -l ./internal ./cmd | grep .'

# 1b. branch-merged-check.sh regression suite — the shell-side decision
#     procedure `git branch -D` reclaim decisions defer to (AGENTS.md § Clean
#     up on merge). Kept aligned with the Go sweep's own ancestry-acceptance
#     door (#593).
run_step "branch-merged-check.sh regression suite" \
  bash scripts/test-branch-merged-check.sh

# 1c. CI change-class gate (#647) — drives scripts/ci-change-class.sh against
#     real git fixtures AND asserts .github/workflows/ci.yml still consumes its
#     outputs. Mirrors ci.yml's own ungated step in the Go job; the wiring half
#     is what makes the gate impossible to document without shipping.
run_step "CI change-class gate regression suite" \
  bash scripts/test-ci-change-class.sh

# 2. Generated files must be in sync
run_step "make generate-ipc-client" make generate-ipc-client
run_step "generated IPC client in sync" \
  git diff --exit-code packages/nightgauge-vscode/src/services/IpcClient.generated.ts

# 2b. Canonical terminal-kind rule table (#306) — the generated SDK module and
#     the behaviour golden must be exactly what table.json renders.
run_step "terminal-kind table consumers in sync" make check-terminal-kind-table

# 2c. Platform operation registry (#750) — api/generated/go/platform/operations.gen.go
#     must be exactly what api/platform-operations.yaml renders. That file carries
#     each operation's credential requirement, which is what the conformance test
#     in internal/platform checks call sites against; a hand-edited copy is a
#     contract nobody reviewed.
run_step "platform operation registry in sync" make check-platform-operations

# No step here for the hand-rolled-platform-request gate (#750): it is enforced
# by TestPlatformRawHTTP_RealPackageIsClean, which step 1's `go test ./...`
# already runs — and which ci.yml runs UNGATED via `go test ./internal/preflight/`.
# `nightgauge preflight platform-raw-http` is the human/skill-facing entry point
# for the same check, not a second gate.

# 3. npm audit allow-list
run_step "npm audit allow-list" node scripts/npm-audit-check.js

# 4. SKILL.md metadata validation. Self-test first, same reasoning as 11 and 5b:
#    the validator reported `missing required field: metadata.source` once, on a
#    file that provably had it and validated clean five times over the identical
#    tree, because a frontmatter block that never closed handed the field checks
#    a truncated buffer (#856). A torn read now fails under its own UNREADABLE
#    message and exit code rather than wearing a missing field's costume — a
#    nondeterministic gate teaches operators to re-run until green, which is the
#    same as not having the gate.
run_step "SKILL.md metadata gate regression suite" \
  bash scripts/test-validate-skill-metadata.sh
run_step "SKILL.md metadata" bash scripts/validate-skill-metadata.sh

# 5. Publication boundary — allowlist, fail-closed. Catches private-class content
#    before it is pushed rather than after CI rejects it.
#
#    Scope is the tracked tree PLUS untracked, non-ignored files (#716). Those
#    are the newest content in a change and the guard used to skip them
#    silently, so this step handed back a pass it had not earned and CI failed
#    on the very files it never opened. A run that scanned untracked files says
#    so on its success line; one that exits non-zero names them inline.
run_step "publication boundary" python3 scripts/publication-boundary-check.py

# 5b. Publication boundary self-test — proves the guard still fails closed
#     (mirrors .github/workflows/publication-boundary.yml's second step).
run_step "Publication boundary regression suite" bash scripts/test-publication-boundary.sh

# 5b-iii. Rename carry-over (#837) — hermetic, ~0.3s. Deliberately NOT a case
#     in the suite above: exercising a rename means `git mv`-ing a real tracked
#     file, and that suite is re-run inside sandboxes and SIGKILLed mid-run by
#     the hermeticity tests below. A kill between the mv and the restore breaks
#     the checkout. This builds its own throwaway repo instead.
run_step "Publication boundary rename carry-over" \
  python3 scripts/test-publication-boundary-rename.py

# 5b-i-b. Derived reference ceiling (#1078) — the ceiling is inferred from merge
#     history rather than recorded, so the inference is load-bearing. The case
#     that matters is that a pull-request TITLE cannot raise it: the mark comes
#     from text an author partly controls, and a raised ceiling weakens the rule
#     silently. Same throwaway-repo shape as the rename suite, for the same
#     crash-safety reason.
run_step "Publication boundary derived ceiling" \
  python3 scripts/test-publication-boundary-ceiling.py

# 5b-i-c. Branch-local ceiling lag and baseline erosion (#1129) — the derived
#     ceiling is per-branch and `git merge origin/main` does NOT advance it
#     (the merge is the second parent), while `tree_baseline` is one global
#     integer compared against a count that GROWS as the ceiling falls. The two
#     together blocked a branch with references it never wrote. Same
#     throwaway-repo shape: these cases need controlled merge topology.
run_step "Publication boundary ceiling lag and erosion" \
  python3 scripts/test-publication-boundary-erosion.py

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
run_step "Publication boundary attribution" \
  bash scripts/test-publication-boundary-attribution.sh

run_step "Publication boundary suite hermeticity" \
  bash scripts/test-publication-boundary-hermeticity.sh

# 5c. Band-vocabulary reintroduction gate (#582) — fails on hand-inlined band
#     closed sets / regex alternations in production source, outside the
#     allowed surfaces (spike #568 §5).
run_step "band-vocabulary reintroduction gate" python3 scripts/check-band-vocabulary.py

# 5d. Band-vocabulary gate self-test — proves the gate still fails closed.
run_step "Band-vocabulary gate regression suite" bash scripts/test-band-vocabulary-check.sh

# 5e. Stale-visibility-prose reintroduction gate (#697) — fails on a tracked
#     artifact unconditionally asserting this repository is private, the
#     exact shape all four #697 instances took.
run_step "visibility-prose reintroduction gate" python3 scripts/check-visibility-prose.py

# 5f. Nonexistent-workflow-reference gate (#545) — fails when a tracked file
# names a `.github/workflows/*.yml` path that does not exist. Self-test first:
# a gate nothing exercises degrades into an unconditional pass.
run_step "Workflow-reference gate regression suite" bash scripts/test-workflow-refs-check.sh
run_step "nonexistent-workflow-reference gate" python3 scripts/check-workflow-refs.py

# 5g. CLA gate regression suite (#976) — spawns the real .github/scripts/cla-check.mjs
#     against a local HTTP stub and pins its bounded retry: a transient 5xx/429
#     or socket failure is retried, every other 4xx is the gate's own verdict and
#     is raised on the first shot. Mirrors .github/workflows/lint.yml's own step
#     (not ci.yml's — this block is the lint.yml mirror region). Costs ~12s: the
#     backoff is deliberately real, because a test-only zero-delay knob would
#     re-open the mutation the timing assertion exists to kill.
run_step "CLA gate regression suite" node --test .github/scripts/cla-check.test.mjs

# 4b. Cache-boundary measurement smoke test
run_step "Cache-boundary measurement smoke" bash scripts/test-measure-cache-boundary-loss.sh

# 4b. Test-tree typecheck (#499).
# tsconfig.json covers src/** only, vitest transforms through esbuild
# (transpile-only, so `import type` is erased unresolved), and eslint registers
# the TS parser for tests without a `project` — so nothing typechecked tests/**
# or the Playwright fixtures. A type-only import could name an export that no
# longer exists and the suite stayed green.
run_step "VSCode test-tree typecheck" npm run typecheck:tests -w nightgauge-vscode

# 5. ESLint
run_step "ESLint" npm run lint

# 6. Prettier formatting — the #1 cause of avoidable CI failures.
run_step "Prettier format:check" npm run format:check

# 7. Build all workspaces
run_step "npm run build (all workspaces)" npm run build

# 7b. Phase markers ↔ PHASE_REGISTRY drift check
# Runs after the SDK build so the script can import PHASE_REGISTRY from
# the workspace package. Catches the class of registry↔skill marker drift
# before it reaches the orchestrator.
run_step "Phase markers ↔ PHASE_REGISTRY" npx tsx scripts/validate-phase-markers.ts

# 8. Tests (single run — NEVER bare vitest which hangs in watch mode)
run_step "npm run test (all workspaces)" npm run test -- --run

# 9. Generated package contributions in sync
run_step "Generated VSCode contributions in sync" \
  npx -w nightgauge-vscode tsx scripts/generate-package-contributions.ts --check

# 9b. @types/vscode must not exceed engines.vscode. `vsce package` enforces this
# at packaging time (dev-install.sh / release) but no build/test step does — a
# Dependabot bump (#165) raised the types past the engine floor and only broke
# at install. Guard it here so the mismatch fails locally, not at install.
run_step "@types/vscode <= engines.vscode" \
  node packages/nightgauge-vscode/scripts/check-engine-types.mjs

# 10. Markdown link check — cross-document reference integrity (root *.md +
#     docs/**). Self-test first: `markdown-link-check` reports `Status: 0` for a
#     request that never completed, and the gate used to fail on that exactly as
#     on a 404 (#1004). Errored requests are now re-probed and sorted into
#     dead / unreachable-from-runner / alive-after-reprobe; the suite pins both
#     directions, so "stop failing on an errored fetch" cannot be satisfied by
#     no longer failing on a dead internal link.
run_step "Link-check gate regression suite" bash scripts/test-check-md-links.sh
run_step "Markdown link check" bash scripts/check-md-links.sh

# 11. Drift-gate self-test — proves the mirror gate below still fails closed
#     rather than passing vacuously, the defect it was created to fix (#539),
#     and that it no longer goes red on a dirty tree it has no quarrel with
#     (#546). Paired with 11b as 5b is paired with 5.
run_step "Mirror drift gate regression suite" \
  bash scripts/test-mirror-drift-gate.sh

# 11a2. Issue-body heading contract (#711) — the required-heading table exists
#       in three files (issue-audit SKILL.md, docs/ISSUE_AUDIT.md, and
#       issue-create's authoring rules) and issue-create runs issue-audit as its
#       own terminal gate. When the copies drift, every issue the pipeline
#       authors fails its own audit; that shipped as a WARNING nobody read until
#       #711. Self-test first, same reasoning as 11 and 5b.
run_step "Issue-body contract gate regression suite" \
  bash scripts/test-issue-body-contract.sh
run_step "Issue-body heading contract" \
  python3 scripts/check-issue-body-contract.py

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
run_step "Plugin skills mirror in sync" \
  bash scripts/install-agent-skills.sh --check-mirror

# 11c. Mirror link integrity — the question 11b structurally cannot ask. The
#      drift gate compares the mirror to the generator's own output, so when the
#      generator copied `../../docs/X.md` verbatim into a directory two levels
#      deeper, both sides carried the same ~90 dead links and 11b was green by
#      construction (#831). This gate resolves each link against the file that
#      contains it, which is a fact about the tree rather than about the copy.
#      Self-test first, same reasoning as 11 and 5b.
run_step "Mirror link gate regression suite" \
  bash scripts/test-mirror-link-check.sh
run_step "Mirror link integrity" python3 scripts/check-mirror-links.py

if [ "$LIST_STEPS" -eq 1 ]; then
  exit 0
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
