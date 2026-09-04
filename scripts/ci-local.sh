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
  scripts/test-post-merge-check.sh
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
  scripts/validate-proposal-artifact.mjs
  scripts/apply-proposal-artifact.sh
  scripts/test-validate-proposal-artifact.sh
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
  scripts/test-skill-echo-json.sh
  scripts/check-skill-echo-json.py
  scripts/install-agent-skills.sh
  scripts/test-mirror-link-check.sh
  scripts/check-mirror-links.py
  scripts/check-go-test-skips.py
  scripts/go-test-skip-allowlist.txt
  scripts/go-test-json-echo.py
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
# Per-step wall clock, parallel arrays (#1217).
STEP_SECONDS=()
STEP_LABELS=()
GATE_STARTED=$SECONDS
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
  local slug log code started elapsed
  slug="$(printf '%s' "$label" | tr -c '[:alnum:]' '-' | tr -s '-' | sed 's/^-//; s/-$//')"
  log="$LOG_DIR/${slug}.log"
  echo ""
  echo "▶ $label"
  echo "  \$ $*"
  started=$SECONDS
  # `tee` keeps the terminal output live; PIPESTATUS[0] preserves the command's
  # own exit code, which a bare pipeline would otherwise replace with tee's.
  "$@" 2>&1 | tee "$log"
  code=${PIPESTATUS[0]}
  elapsed=$((SECONDS - started))
  # Per-step wall clock (#1217). Without it "the gate is slow" is unactionable:
  # 47 sequential steps and no way to tell which three of them are the cost.
  STEP_SECONDS+=("$elapsed")
  STEP_LABELS+=("$label")
  if [ "$code" -eq 0 ]; then
    echo "  ✓ $label (${elapsed}s)"
  else
    echo "  ✗ $label (exit $code, ${elapsed}s)"
    FAIL_COUNT=$((FAIL_COUNT + 1))
    FAILED_STEPS+=("$label")
    FAILED_LOGS+=("$log")
  fi
}

# ── Parallel group runner (#1217) ───────────────────────────────────────────
#
# The gate was 836s of steps in 836s of wall clock — purely serial, at 189% CPU
# on a machine with far more cores. The fix is not "run everything with &": most
# steps here are ordered on purpose. `make generate-ipc-client` REWRITES a
# tracked file that a later step checks for drift; `npm run build` writes the
# dist/ that the test steps import; the mirror regeneration rewrites files the
# mirror gate then diffs; the publication-boundary scan reads the whole tracked
# tree and must not race a writer. Parallelising any of those produces exactly
# the order-dependent flake this gate exists to prevent — and some of those
# failures would be FALSE GREEN, which is worse than slow.
#
# So only steps that are provably read-only w.r.t. the working tree join a
# group. Each one either shells into a `mktemp -d` sandbox seeded from
# `git archive HEAD` (every regression suite here does) or only reads. The
# membership rule is written at the group's declaration site, not here — a
# reader must be able to see WHY a given step is safe to run concurrently.
#
# Failure handling is the load-bearing part. `run_step` accumulates failures in
# shell arrays, and a backgrounded step runs in a SUBSHELL whose array writes
# are discarded on exit — so a naive port loses failures silently and the gate
# reports green over a red step. Exit codes therefore travel through FILES, and
# the parent re-reads them after `wait`. `run_group_wait` is what appends to
# FAILED_STEPS, in the parent shell, in declared order.
GROUP_LABELS=()
GROUP_PIDS=()
GROUP_CODEFILES=()
GROUP_LOGS=()
GROUP_STARTS=()

# Bounded concurrency. Unbounded would put four `go test` compilations and a
# 12k-test vitest run on the box at once and thrash; the measured critical path
# is one 144s step, so there is nothing to gain past a handful of slots.
CI_LOCAL_JOBS="${CI_LOCAL_JOBS:-4}"

# Escape hatch: CI_LOCAL_SERIAL=1 runs every grouped step inline, in declared
# order, exactly as before. For bisecting a failure whose interleaving matters,
# and as the answer to "is this new runner lying to me?".
CI_LOCAL_SERIAL="${CI_LOCAL_SERIAL:-0}"

run_group() {
  local label="$1"
  shift
  if [ "$LIST_STEPS" -eq 1 ]; then
    printf '%s\n' "$label"
    return 0
  fi
  if [ "$CI_LOCAL_SERIAL" = "1" ]; then
    run_step "$label" "$@"
    return 0
  fi

  # Throttle to CI_LOCAL_JOBS in flight.
  while [ "$(jobs -rp | wc -l)" -ge "$CI_LOCAL_JOBS" ]; do
    wait -n 2>/dev/null || break
  done

  local slug log codefile
  slug="$(printf '%s' "$label" | tr -c '[:alnum:]' '-' | tr -s '-' | sed 's/^-//; s/-$//')"
  log="$LOG_DIR/${slug}.log"
  codefile="$LOG_DIR/${slug}.exitcode"
  rm -f "$codefile"
  echo "▶ $label (started, concurrent)"
  # The exit code goes to a FILE, not a variable: this subshell's variables die
  # with it. `$!` is captured immediately so the parent can wait on this exact
  # child rather than on `jobs`, whose table does not survive into the parent's
  # later commands.
  # The child records its OWN duration next to its own exit code. Measuring it
  # in the parent after `wait` timed queue-to-group-end, so every grouped step
  # reported the group's total and the summary was useless for finding the
  # expensive one.
  ( local_start=$SECONDS
    "$@" > "$log" 2>&1
    printf '%s\n' "$?" > "$codefile"
    printf '%s\n' "$((SECONDS - local_start))" > "$codefile.secs" ) &
  GROUP_PIDS+=("$!")
  GROUP_LABELS+=("$label")
  GROUP_CODEFILES+=("$codefile")
  GROUP_LOGS+=("$log")
  GROUP_STARTS+=("$SECONDS")
}

# Wait for every started group step, then fold the results into the same
# FAIL_COUNT / FAILED_STEPS the serial path uses, in DECLARED order so output is
# reproducible regardless of which step happened to finish first.
run_group_wait() {
  [ "$LIST_STEPS" -eq 1 ] && return 0
  [ "${#GROUP_PIDS[@]}" -eq 0 ] && return 0

  local i pid code elapsed
  for pid in "${GROUP_PIDS[@]}"; do
    wait "$pid" 2>/dev/null || true
  done

  for i in "${!GROUP_LABELS[@]}"; do
    if [ -r "${GROUP_CODEFILES[$i]}.secs" ]; then
      elapsed="$(cat "${GROUP_CODEFILES[$i]}.secs" 2>/dev/null || echo 0)"
    else
      elapsed=$((SECONDS - GROUP_STARTS[i]))
    fi
    # A missing exit-code file means the child died without writing one (killed,
    # disk full, a `set -e` in an unexpected place). Treat it as FAILURE, never
    # as success: an unobservable step is the false-green case this whole
    # mechanism exists to avoid.
    if [ -r "${GROUP_CODEFILES[$i]}" ]; then
      code="$(cat "${GROUP_CODEFILES[$i]}" 2>/dev/null || echo 1)"
    else
      code=1
      echo "  ! ${GROUP_LABELS[$i]}: no exit code was recorded — treating as FAILED" >&2
    fi
    [ -n "$code" ] || code=1

    cat "${GROUP_LOGS[$i]}" 2>/dev/null || true
    STEP_SECONDS+=("$elapsed")
    STEP_LABELS+=("${GROUP_LABELS[$i]}")
    if [ "$code" -eq 0 ] 2>/dev/null; then
      echo "  ✓ ${GROUP_LABELS[$i]} (${elapsed}s, concurrent)"
    else
      echo "  ✗ ${GROUP_LABELS[$i]} (exit $code, ${elapsed}s, concurrent)"
      FAIL_COUNT=$((FAIL_COUNT + 1))
      FAILED_STEPS+=("${GROUP_LABELS[$i]}")
      FAILED_LOGS+=("${GROUP_LOGS[$i]}")
    fi
  done

  GROUP_LABELS=(); GROUP_PIDS=(); GROUP_CODEFILES=(); GROUP_LOGS=(); GROUP_STARTS=()
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
# -json so the skip accounting has events to read (#474): a package whose tests
# all SKIPPED still prints `ok`, so without it a guard that stopped guarding is
# indistinguishable from one that passed. Piped back through
# go-test-json-echo.py so the log stays readable.
run_step "go build ./..." go build ./...
run_group "go test ./... -count=1 (with skip accounting)" \
  bash -c 'set -o pipefail; go test -json ./... -count=1 | tee go-test.json | python3 scripts/go-test-json-echo.py && python3 scripts/check-go-test-skips.py go-test.json'
# Mirrors ci.yml's "Test (race, whole tree)" step (#493), which replaced the
# internal/orchestrator-scoped step from #428 — one race step, not two. The
# scoped step existed because the race detector is the only thing that fails
# when a drainBackground() join is deleted from a test body, and that argument
# was never specific to one package. Measured whole-tree cost: +6% over the
# plain run (2m48s -> 2m58s on an Apple M-series), not the ~3x once feared.
run_group "go test -race -count=1 ./..." \
  go test -race -count=1 ./...
run_step "gofmt -l ./internal ./cmd" \
  bash -c '! gofmt -l ./internal ./cmd | grep .'

# 1b. branch-merged-check.sh regression suite — the shell-side decision
#     procedure `git branch -D` reclaim decisions defer to (AGENTS.md § Clean
#     up on merge). Kept aligned with the Go sweep's own ancestry-acceptance
#     door (#593).
run_group "branch-merged-check.sh regression suite" \
  bash scripts/test-branch-merged-check.sh

# 1b2. post-merge-check.sh regression suite — the OTHER verification idiom
#      AGENTS.md mandates and that hand-writing gets wrong (#1038). Its two
#      motivating states — an empty check-run list and a still-running check —
#      cannot be produced on demand against live CI, so they are only ever
#      exercised here.
run_group "post-merge-check.sh regression suite" \
  bash scripts/test-post-merge-check.sh

# 1c. CI change-class gate (#647) — drives scripts/ci-change-class.sh against
#     real git fixtures AND asserts .github/workflows/ci.yml still consumes its
#     outputs. Mirrors ci.yml's own ungated step in the Go job; the wiring half
#     is what makes the gate impossible to document without shipping.
run_group "CI change-class gate regression suite" \
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
run_group "Band-vocabulary gate regression suite" bash scripts/test-band-vocabulary-check.sh

# 5e. Stale-visibility-prose reintroduction gate (#697) — fails on a tracked
#     artifact unconditionally asserting this repository is private, the
#     exact shape all four #697 instances took.
run_step "visibility-prose reintroduction gate" python3 scripts/check-visibility-prose.py

# 5f. Nonexistent-workflow-reference gate (#545) — fails when a tracked file
# names a `.github/workflows/*.yml` path that does not exist. Self-test first:
# a gate nothing exercises degrades into an unconditional pass.
run_group "Workflow-reference gate regression suite" bash scripts/test-workflow-refs-check.sh
run_step "nonexistent-workflow-reference gate" python3 scripts/check-workflow-refs.py

# 5f2. Proposal-artifact validator regression suite (#1304) — the schema gate
# between the read-only model job and the write job in release-watchdog.yml
# and continuous-improvement.yml. Every case is a shape the gate must reject.
run_group "Proposal-artifact validator regression suite" bash scripts/test-validate-proposal-artifact.sh

# 5g. CLA gate regression suite (#976) — spawns the real .github/scripts/cla-check.mjs
#     against a local HTTP stub and pins its bounded retry: a transient 5xx/429
#     or socket failure is retried, every other 4xx is the gate's own verdict and
#     is raised on the first shot. Mirrors .github/workflows/lint.yml's own step
#     (not ci.yml's — this block is the lint.yml mirror region). Costs ~12s: the
#     backoff is deliberately real, because a test-only zero-delay knob would
#     re-open the mutation the timing assertion exists to kill.
run_group "CLA gate regression suite" node --test .github/scripts/cla-check.test.mjs

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
run_group "Link-check gate regression suite" bash scripts/test-check-md-links.sh
run_group "Markdown link check" bash scripts/check-md-links.sh

# 11. Drift-gate self-test — proves the mirror gate below still fails closed
#     rather than passing vacuously, the defect it was created to fix (#539),
#     and that it no longer goes red on a dirty tree it has no quarrel with
#     (#546). Paired with 11b as 5b is paired with 5.
run_group "Mirror drift gate regression suite" \
  bash scripts/test-mirror-drift-gate.sh

# 11a2. Issue-body heading contract (#711) — the required-heading table exists
#       in three files (issue-audit SKILL.md, docs/ISSUE_AUDIT.md, and
#       issue-create's authoring rules) and issue-create runs issue-audit as its
#       own terminal gate. When the copies drift, every issue the pipeline
#       authors fails its own audit; that shipped as a WARNING nobody read until
#       #711. Self-test first, same reasoning as 11 and 5b.
run_group "Issue-body contract gate regression suite" \
  bash scripts/test-issue-body-contract.sh
run_step "Issue-body heading contract" \
  python3 scripts/check-issue-body-contract.py

# 11a2. Skill `echo "$VAR" | jq` gate (#1215) — zsh's builtin echo expands
#       backslash escapes, so a JSON `\n` reaches jq as a real newline, the
#       parse aborts, and the caller reads an empty string. On 2026-08-30 that
#       made the issue-audit terminal gate report all five required headings
#       MISSING on an issue that had every one of them. Placed before the mirror
#       drift gate: this is a canonical-skills edit, and 11b will fail anyway if
#       the fix did not reach the mirror. Self-test first, same reasoning as 11.
run_group "Skill echo-into-jq gate regression suite" \
  bash scripts/test-skill-echo-json.sh
run_step "Skill echo-into-jq gate" \
  python3 scripts/check-skill-echo-json.py

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

# Slowest steps + total (#1217). Printed on pass AND fail: the run you most
# want to profile is often the one that failed at minute twelve.
print_timing_summary() {
  local total=$((SECONDS - GATE_STARTED))
  echo ""
  echo "Wall clock: $((total / 60))m$((total % 60))s total. Slowest steps:"
  local i
  for i in "${!STEP_LABELS[@]}"; do
    printf '%6s  %s\n' "${STEP_SECONDS[$i]}s" "${STEP_LABELS[$i]}"
  done | sort -rn | head -8 | sed 's/^/  /'
}

# Collect every concurrent step before summarising. Placed HERE, at the very
# end, so a grouped step overlaps the entire serial remainder rather than just
# its immediate neighbours — the Go suites (284s combined) run underneath the
# npm, lint and doc steps instead of in front of them.
#
# Nothing may read a grouped step's result before this point, and nothing does:
# the group is read-only by construction, so no serial step downstream depends
# on one having finished.
run_group_wait

echo ""
echo "-------------------------------------------------------------------------"
print_timing_summary
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
