# Reference: CI Parity, Knowledge Coverage & Pre-Push Gate (Phases 2.5, 2.6, 2.7)

Procedural detail for **Phase 2.5 (CI Parity Check)**, **Phase 2.6 (Knowledge
Coverage Check)**, and **Phase 2.7 (Pre-Push Merge Validation Gate)**. Read this
when those phases fire.

## Contents

- [Phase 2.5: CI Parity Check](#phase-25-ci-parity-check-deterministic)
- [Phase 2.6: Knowledge Coverage Check](#phase-26-knowledge-coverage-check)
- [Phase 2.7: Pre-Push Merge Validation Gate](#phase-27-pre-push-merge-validation-gate)

---

## Phase 2.5: CI Parity Check (Deterministic)

**PURPOSE**: Run the repository's actual CI workflow commands locally to catch
failures that unit/integration tests alone miss (format checks, lint, typecheck,
full build with all workspaces). This closes the test parity gap between
feature-validate and CI — PRs arrive at pr-create already green instead of
failing on first CI run and requiring pr-merge auto-fix cycles.

> **Why here and not in pr-merge**: The agent still has full code context during
> feature-validate. Fixing a typecheck error or failing test here costs one
> auto-fix attempt. Discovering it in pr-merge (after context is lost) costs a
> full CI round-trip per attempt, and pr-merge's auto-fix is limited to 3
> attempts with a narrow context window.

### Step 2.5.0a: Scoped Checks While the Review Runs (#3207)

The full gate runs once per run, on the tree the independent review last
passed. While the review is on (`pipeline.adversarial_review.enabled` is not
`false`), every review `catch` fix changes the tree, so a full gate started
before the final round is void and gets killed and restarted. So with the
review on, this phase runs only the **scoped checks**: build, vet, and the
tests of the changed packages. The run's one full gate is
[Step 2.7.4](#step-274-the-runs-one-full-gate-3207), after the final review
round. With the review off, skip this step: Steps 2.5.0 to 2.5.3 below are the
run's one full gate, as written.

```bash
REVIEW_ENABLED=$(nightgauge config show --key pipeline.adversarial_review.enabled --raw 2>/dev/null || echo true)
SCOPED_CHECKS_ONLY=false
[ "$REVIEW_ENABLED" = "false" ] || SCOPED_CHECKS_ONLY=true
if [ "$SCOPED_CHECKS_ONLY" = "true" ]; then
  BASE_REF=$(git rev-parse --abbrev-ref origin/HEAD 2>/dev/null || echo origin/main)
  # Changed files: tracked edits since the merge base, plus untracked new files.
  CHANGED_FILES=$( { git diff --name-only "$(git merge-base HEAD "$BASE_REF")"; \
    git ls-files --others --exclude-standard; } | sort -u)
  if [ -f go.mod ]; then
    # Build, vet, and the tests of the changed packages only: never ./...
    CHANGED_PKGS=$(printf '%s\n' "$CHANGED_FILES" | grep '\.go$' | xargs -n1 dirname 2>/dev/null \
      | sort -u | while read -r d; do [ -d "$d" ] && echo "./$d"; done)
    # shellcheck disable=SC2086 # one argument per package
    go build ./... && { [ -z "$CHANGED_PKGS" ] || { go vet $CHANGED_PKGS && go test $CHANGED_PKGS; }; } \
      || CI_PARITY_PASSED=false
  fi
  if [ -f package.json ]; then
    # Not Go: the repository's build and lint, and the tests of the changed
    # workspaces only. Never the full gate between rounds.
    CHANGED_WS=$(printf '%s\n' "$CHANGED_FILES" | awk -F/ '$1 == "packages" && NF > 2 {print $1 "/" $2}' \
      | sort -u | while read -r d; do [ -f "$d/package.json" ] && echo "$d"; done)
    { npm run build --if-present && npm run lint --if-present; } || CI_PARITY_PASSED=false
    for WS in $CHANGED_WS; do
      npm run -w "$WS" test --if-present || CI_PARITY_PASSED=false
    done
  fi
fi
```

For a repository that is neither Go nor npm, run its build and lint and the
tests of the changed packages or modules only, by the commands its `AGENTS.md`
names. Never run the full gate between review rounds.

A scoped failure is fixed here, like any test failure, and the scoped checks run
again. Then go to Step 2.7.3. Steps 2.5.0 to 2.5.3 do not run now: Step 2.7.4
runs them, on the final tree.

### Step 2.5.0: One Gate Per Tree (#2784)

This step starts the run's one full gate: here when the review is off,
otherwise in Step 2.7.4. Ask the ledger first: if this exact tree
already passed, say so and move on. Otherwise a repository with a complete local
gate runs that gate as this phase's only command, because the discovered commands
are a subset of it and a pass of them cannot satisfy `pre-push validate`. A
repository with no gate falls through to the discovered commands. An older binary
answers with neither, which also means the discovered commands. `gate_command`
is a display string and is never executed: `nightgauge ci gate-run` runs each
step as an argv. A declared gate that fails validation also goes to `gate-run`,
which fails with the reason instead of falling back to the discovered commands.

```bash
CI_GATE_STATUS=$(nightgauge ci gate-status --kind parity --json 2>/dev/null || \
  echo '{"reusable":false,"reason":"this binary has no ci gate-status","tree":"","gate_command":"","gate_records_itself":false}')
CI_GATE_REUSABLE=$(printf '%s\n' "$CI_GATE_STATUS" | jq -r '.reusable')
CI_GATE_REASON=$(printf '%s\n' "$CI_GATE_STATUS" | jq -r '.reason')
CI_GATE_TREE=$(printf '%s\n' "$CI_GATE_STATUS" | jq -r '.tree')
CI_GATE_COMMAND=$(printf '%s\n' "$CI_GATE_STATUS" | jq -r '.gate_command')
CI_GATE_RECORDS_ITSELF=$(printf '%s\n' "$CI_GATE_STATUS" | jq -r '.gate_records_itself')
CI_GATE_ERROR=$(printf '%s\n' "$CI_GATE_STATUS" | jq -r '.gate_error // ""')
```

### Step 2.5.1: Discover CI Commands from Workflow

Scan every workflow under `.github/workflows/` that runs on `pull_request` or
`merge_group` (format and lint checks live in `lint.yml`, not `ci.yml`). The
binary drops runner setup (`sudo`, `apt-get`, `$GITHUB_ENV`, installs, global git
config) and returns a multi-line `run:` block whole. Fall back to standard
commands if no workflow is found.

```bash
CI_DISCOVER_RESULT=$(nightgauge ci discover-commands --json 2>/dev/null || \
  echo '{"commands":[],"checks":[],"workflow_path":"","framework":"unknown","timestamp":""}')
CI_PARITY_COMMANDS=()
while IFS= read -r -d '' C; do CI_PARITY_COMMANDS+=("$C"); done \
  < <(printf '%s\n' "$CI_DISCOVER_RESULT" | jq -j '.commands[] | ., "\u0000"' 2>/dev/null)
if [ "$CI_GATE_REUSABLE" != "true" ] && { [ -n "$CI_GATE_COMMAND" ] || [ -n "$CI_GATE_ERROR" ]; }; then
  CI_PARITY_COMMANDS=("nightgauge ci gate-run")
fi
echo "CI parity commands discovered (${#CI_PARITY_COMMANDS[@]}): ${CI_PARITY_COMMANDS[*]}"
```

### Step 2.5.2: Execute CI Commands and Collect Failures

Run each CI command in order. On first failure, stop and attempt auto-fix.

When `nightgauge ci gate-run` is the command, start this step with the Bash
tool's `run_in_background: true` and a `timeout` of at least the gate's limit in
milliseconds (`hooks.pre_push.timeout_seconds` × 1000; 1,800,000 by default),
then wait for its completion notification. Why: a foreground call is killed at
twice its timeout (1200 s by default), and the gate takes longer, so a
foreground gate is discarded after twenty minutes. A `&` inside a foreground
call orphans it instead. The `gate-background` hook denies both forms (#2953).
`gate_command` is never run directly: run `nightgauge ci gate-run`. Its log is
declared at spawn so the wait reads as work
(`_shared/LONG_RUNNING_PROCESSES.md`, #2694).

`nightgauge ci gate-run` allows one gate per worktree, and records the start
and finish of every gate it runs, so this step records nothing for it (#3207).
A second `gate-run` on the same tree waits for the running one and exits with
its result: never start another, and never kill a running gate. Its own exit
codes are:

- `75`: another gate holds this worktree on a different tree and could not be
  stopped, so nothing ran. Wait for that gate to end, then run `gate-run` again.
- `76`: this gate was superseded by a newer tree in this worktree. That is not
  a failure, and the ledger already records it as `superseded`: run the gate
  for the current tree, never record a fail.

The full gate runs the unit suite, so its result is the unit result. Its log
is kept as `gate-{N}.log` in the pipeline state directory, and
`UNIT_TESTS_LOG` and, from its Go `--- PASS` / `--- FAIL` lines, the
`UNIT_TESTS_*` result and counts are set from it for Step 6.1. pr-create reads that log's `--- PASS` / `--- FAIL` lines to answer a
criterion naming a test, and the gate ledger's pass for this tree when the
block is missing. A gate started in the background and polled still leaves
both, so its result reaches the PR (#2941).

```bash
ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
: "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
CI_PARITY_PASSED=true
CI_PARITY_FAILED_CMD=""
CI_PARITY_FAILED_OUTPUT=""
CI_PARITY_MAX_ATTEMPTS=3
CI_PARITY_ATTEMPT=0

CI_PARITY_RAN=false
CI_PARITY_RESULTS_JSON="[]"

# Runs every discovered check and records one result per check, joined to
# `.checks[]` for its workflow and step. Exit 127 means the tool is not
# installed here (e.g. gitleaks): recorded as skipped with its reason, not as a
# failure. The first real failure stops the run for the auto-fix loop.
function run_ci_parity() {
  CI_PARITY_RAN=true
  CI_PARITY_RESULTS_JSON="[]"
  for CMD in "${CI_PARITY_COMMANDS[@]}"; do
    echo "Running: $CMD"
    # A command may be a multi-line block; run it through a shell, as CI does.
    CI_PARITY_LOG=$(mktemp "${TMPDIR:-/tmp}/ci-parity-XXXXXX")
    IS_GATE=false
    if [ "$CMD" = "nightgauge ci gate-run" ]; then
      # The gate's log outlives the step: pr-create reads its per-test lines (#2941).
      IS_GATE=true
      CI_PARITY_LOG="$(nightgauge layout path pipeline "gate-${ISSUE_NUMBER}.log")"
    fi
    bash -c "$CMD" >"$CI_PARITY_LOG" 2>&1 &
    CI_PARITY_PID=$!
    echo "NIGHTGAUGE_PROGRESS: {\"pid\": $CI_PARITY_PID, \"log\": \"$CI_PARITY_LOG\", \"label\": \"ci parity\"}"
    wait "$CI_PARITY_PID"
    EXIT_CODE=$?
    OUTPUT=$(tail -c 65536 "$CI_PARITY_LOG")
    if [ "$IS_GATE" = "true" ]; then
      UNIT_TESTS_LOG="$CI_PARITY_LOG"
      # Counts come from Go's own result lines; a gate with none leaves the
      # unit block to the earlier phases (a zero count reads inconclusive).
      GATE_TESTS_RUN=$(grep -cE '^[[:space:]]*--- (PASS|FAIL): ' "$CI_PARITY_LOG" || true)
      if [ "${GATE_TESTS_RUN:-0}" -gt 0 ]; then
        UNIT_TESTS_RAN=true
        UNIT_TESTS_PASSED=$([ $EXIT_CODE -eq 0 ] && echo true || echo false)
        UNIT_TESTS_RUN_COUNT=$GATE_TESTS_RUN
        UNIT_TESTS_PASSED_COUNT=$(grep -cE '^[[:space:]]*--- PASS: ' "$CI_PARITY_LOG" || true)
      fi
    else
      rm -f "$CI_PARITY_LOG"
    fi
    if [ $EXIT_CODE -eq 0 ]; then STATUS=passed; REASON=""
    elif [ $EXIT_CODE -eq 127 ]; then STATUS=skipped; REASON="command not found"
    else STATUS=failed; REASON=""
    fi
    CI_PARITY_RESULTS_JSON=$(printf '%s\n' "$CI_PARITY_RESULTS_JSON" | jq -c \
      --arg cmd "$CMD" --arg status "$STATUS" --argjson exit_code "$EXIT_CODE" \
      --arg reason "$REASON" --argjson checks "$(printf '%s\n' "$CI_DISCOVER_RESULT" | jq -c '.checks // []')" \
      '. + [($checks | map(select(.command == $cmd)) | first // {command: $cmd})
            + {status: $status, exit_code: $exit_code}
            + (if $reason != "" then {reason: $reason} else {} end)]')
    if [ "$STATUS" = "failed" ]; then
      CI_PARITY_PASSED=false
      CI_PARITY_FAILED_CMD="$CMD"
      CI_PARITY_FAILED_OUTPUT="$OUTPUT"
      echo "✗ CI parity check failed: $CMD (exit $EXIT_CODE)"
      return 1
    fi
    echo "${STATUS}: $CMD"
  done
  return 0
}

# `nightgauge ci gate-run` records every gate it runs, so it needs no recording
# here. Discovered commands are recorded as kind parity, so every repository is
# counted and reusable. A command skipped as not installed means the pass was
# not full.
function record_ci_parity() { # record_ci_parity <verdict>
  [ "${CI_PARITY_COMMANDS[0]:-}" = "nightgauge ci gate-run" ] && return 0
  local full_flag="--full"
  printf '%s\n' "$CI_PARITY_RESULTS_JSON" | jq -e 'any(.status == "skipped")' >/dev/null && full_flag=""
  nightgauge ci gate-record --event finish --run-id "$CI_GATE_RUN" --kind parity \
    --tree "$CI_GATE_TREE" --verdict "$1" --seconds "$((SECONDS - CI_GATE_STARTED))" \
    $full_flag >/dev/null 2>&1 || true
}

if [ "$CI_GATE_REUSABLE" = "true" ]; then
  # The tree already passed, so nothing runs. A skipped check keeps `ci_parity`
  # non-null for the lifecycle handoff and says why.
  echo "⏭ Gate skipped: $CI_GATE_REASON"
  nightgauge ci gate-record --event reused --kind parity --tree "$CI_GATE_TREE" >/dev/null 2>&1 || true
  CI_PARITY_RAN=true
  CI_PARITY_RESULTS_JSON=$(jq -cn --arg reason "$CI_GATE_REASON" \
    '[{command: "ci gate-status", status: "skipped", reason: $reason}]')
elif [ "${#CI_PARITY_COMMANDS[@]}" -eq 0 ]; then
  # A skipped phase is never silent: record why.
  SKIPPED_PHASES=$(printf '%s\n' "$SKIPPED_PHASES" | jq '. + [{"phase": "ci_parity_check", "reason": "no runnable CI commands discovered"}]')
else
  CI_GATE_STARTED=$SECONDS
  if [ "${CI_PARITY_COMMANDS[0]:-}" != "nightgauge ci gate-run" ]; then
    CI_GATE_RUN=$(nightgauge ci gate-record --event start --kind parity --tree "$CI_GATE_TREE" --full 2>/dev/null || true)
  fi
  if run_ci_parity; then record_ci_parity pass; else record_ci_parity fail; fi
fi
```

`CI_PARITY_RAN`, `CI_PARITY_PASSED` and `CI_PARITY_RESULTS_JSON` (the last run's
per-check results) are written to the validate context's `ci_parity` field by
the writer in `context-and-board.md`.

### Step 2.5.2b: Publication-Boundary Failure Is an Owner Decision (#2562)

A `publication boundary` failure on a path THIS change adds is not a defect an
auto-fix can settle: the path is denied (or unclassified) by
`.github/publication-boundary.yaml`, and choosing another location or changing
the manifest is the owner's publication decision. Reported as an ordinary
`validation_failed`, it halted the whole repository's queue on #1684. Ask the
binary, with the same rules CI enforces, whether the change's added paths are
rejected:

```bash
if [ "$CI_PARITY_PASSED" = "false" ] && \
   printf '%s\n' "$CI_PARITY_FAILED_CMD" | grep -q 'publication-boundary'; then
  BASE_REF="${BASE_REF:-origin/main}"
  ADDED_PATHS=$( { git diff --name-only --diff-filter=A "$BASE_REF"...HEAD; \
    git ls-files --others --exclude-standard; } 2>/dev/null | sort -u | paste -sd, -)
  if [ -n "$ADDED_PATHS" ]; then
    PUB_RESULT=$(nightgauge gate check-publication --paths "$ADDED_PATHS" --json)
    if [ $? -eq 1 ]; then
      PUB_EVIDENCE=$(printf '%s\n' "$PUB_RESULT" | jq -c '[.verdicts[]
        | select(.verdict != "allow")
        | "\(.path): \(.verdict) by publication rule \(.rule // "none") (class \(.class // "none"))"]')
      FEEDBACK_JSON=$(jq -n --argjson ev "$PUB_EVIDENCE" '[{
        signal_type: "NOT_PIPELINE_ACTIONABLE",
        emitted_by_stage: "feature-validate",
        backtrack_target_stage: null,
        severity: "blocking",
        rationale: "The change adds a path the publication boundary rejects. Choosing an allowed location or changing .github/publication-boundary.yaml is an owner publication decision.",
        evidence: $ev
      }]')
      CI_PARITY_MAX_ATTEMPTS=0 # no auto-fix: moving or reclassifying the path is not the agent's call
      VALIDATION_STATUS="failed"
    fi
  fi
fi
```

`FEEDBACK_JSON` reaches `validate-{N}.json`'s `feedback` field through the
writer in `context-and-board.md`, and the orchestrator books the run `blocked`
for that one issue (`owner-action` label, Backlog, Action Center card) instead
of halting the repository. A boundary failure on a path this change did NOT add
stays an ordinary failure.

### Step 2.5.3: Auto-Fix Loop (If Failures Detected)

When a CI parity command fails, attempt to fix it using the same pattern as the
RALPH loop. This loop re-runs the full gate after each fix because the gate
itself failed; that is not a review round. A review `catch` never re-runs the
full gate (Step 2.7.3). The agent has full code context here, making fixes far more likely
to succeed than in pr-merge.

```bash
while [ "$CI_PARITY_PASSED" = "false" ] && \
      [ $CI_PARITY_ATTEMPT -lt $CI_PARITY_MAX_ATTEMPTS ]; do
  CI_PARITY_ATTEMPT=$((CI_PARITY_ATTEMPT + 1))
  echo "CI parity auto-fix attempt $CI_PARITY_ATTEMPT/$CI_PARITY_MAX_ATTEMPTS"

  # Classify the failure type from the failed command name
  case "$CI_PARITY_FAILED_CMD" in
    *format*|*prettier*|*black*|*dprint*)  FAILURE_TYPE="format" ;;
    *lint*|*eslint*|*pylint*)              FAILURE_TYPE="lint" ;;
    *typecheck*|*tsc*|*mypy*)              FAILURE_TYPE="typecheck" ;;
    *build*|*compile*)                     FAILURE_TYPE="build" ;;
    *test*|*vitest*|*jest*|*pytest*)        FAILURE_TYPE="test" ;;
    *)                                     FAILURE_TYPE="unknown" ;;
  esac

  echo "Failure type: $FAILURE_TYPE"
  echo "Failed output (last 50 lines):"
  echo "$CI_PARITY_FAILED_OUTPUT" | tail -50

  # Generate fix based on failure type and output
  # (Probabilistic — AI reads error output and applies targeted fix)
  # Fix approach by type:
  #   format   → run formatter with --write/--fix flag
  #   lint     → run linter with --fix, or manually fix reported issues
  #   typecheck → fix type errors based on compiler output
  #   build    → fix missing imports, syntax errors, config issues
  #   test     → analyze test failure, fix assertion or implementation
  #   unknown  → read logs carefully, attempt minimal targeted fix

  # After applying fix, re-run ALL CI parity commands (not just the failed one)
  CI_PARITY_PASSED=true
  CI_GATE_STATUS=$(nightgauge ci gate-status --kind parity --json 2>/dev/null || echo '{"tree":""}')
  CI_GATE_TREE=$(printf '%s\n' "$CI_GATE_STATUS" | jq -r '.tree')
  CI_GATE_STARTED=$SECONDS
  if run_ci_parity; then record_ci_parity pass; else record_ci_parity fail; fi
done

if [ "$CI_PARITY_PASSED" = "false" ]; then
  echo "✗ CI parity check failed after $CI_PARITY_MAX_ATTEMPTS auto-fix attempts"
  echo "Failed command: $CI_PARITY_FAILED_CMD"
  VALIDATION_STATUS="failed"
fi
```

### Step 2.5.4: Skip Conditions

Skip this phase entirely when:

- `PTC_AVAILABLE=true` and PTC validation already passed (Phase 1.8 ran
  successfully) — PTC runs the same commands
- `BUILD_SKIPPED_REASON` is set AND `UNIT_TESTS_SKIPPED=true` AND no CI
  workflow was discovered — nothing new to check

```bash
if [ "$PTC_AVAILABLE" = "true" ] && [ "$PTC_EXIT" = "0" ]; then
  echo "⏭ CI parity skipped — PTC validation already passed"
  SKIPPED_PHASES=$(printf '%s\n' "$SKIPPED_PHASES" | jq '. + [{"phase": "ci_parity_check", "reason": "PTC validation passed — same commands already executed"}]')
fi
```

---

## Phase 2.6: Knowledge Coverage Check

**PURPOSE**: Cross-check the implementation against the active issue's `PRD.md`
acceptance criteria and `decisions.md` architectural constraints. For each AC,
find evidence (test names, code paths, Recall API). For each decision constraint,
scan modified files for potential violations. Output a structured coverage map
and emit telemetry.

**Non-blocking by default**: `no_evidence` ACs produce a PR-body annotation but
do not fail validation. Configurable via `knowledge.validate.strict: true`.

**Skip conditions**: `knowledge_path` is unset in dev context, or PRD.md /
decisions.md do not exist at the knowledge path.

### Step 2.6.1: Load Knowledge Path from Dev Context

```bash
ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
: "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
KNOWLEDGE_PATH=$(jq -r '.knowledge_path // empty' "$(nightgauge layout path pipeline dev-${ISSUE_NUMBER}.json)" 2>/dev/null)
COVERAGE_MAP_PATH=""

if [ -z "$KNOWLEDGE_PATH" ]; then
  echo "⏭ Phase 2.6: knowledge_path not set in dev context — skipping coverage check"
else
  PRD_FILE="${KNOWLEDGE_PATH}/PRD.md"
  DECISIONS_FILE="${KNOWLEDGE_PATH}/decisions.md"

  if [ ! -f "$PRD_FILE" ] || [ ! -f "$DECISIONS_FILE" ]; then
    echo "⏭ Phase 2.6: PRD.md or decisions.md missing at $KNOWLEDGE_PATH — skipping"
  else
    echo "Phase 2.6: Running knowledge coverage check for issue #${ISSUE_NUMBER}"
    echo "  PRD.md: $PRD_FILE"
    echo "  decisions.md: $DECISIONS_FILE"
  fi
fi
```

### Step 2.6.2: Emit knowledge.read Telemetry

When PRD.md and decisions.md are loaded:

```bash
ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
: "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
if [ -n "$KNOWLEDGE_PATH" ] && [ -f "$PRD_FILE" ]; then
  "$BINARY" telemetry emit \
    --type knowledge.read \
    --scope "issue:${ISSUE_NUMBER}" \
    --path "$PRD_FILE" \
    --stage "feature-validate" 2>/dev/null || true
  "$BINARY" telemetry emit \
    --type knowledge.read \
    --scope "issue:${ISSUE_NUMBER}" \
    --path "$DECISIONS_FILE" \
    --stage "feature-validate" 2>/dev/null || true
fi
```

### Step 2.6.3: Extract Test Names from Diff

Collect `describe()` / `it()` / `test()` names from new and modified test files
to use as evidence signals:

```bash
TEST_NAMES_JSON="[]"
if [ -n "$KNOWLEDGE_PATH" ] && [ -f "$PRD_FILE" ]; then
  # Collect changed test file names
  CHANGED_TEST_FILES=$(git diff HEAD~1 --name-only 2>/dev/null | grep -E '\.(test|spec)\.(ts|js|go)$' || true)
  TEST_NAMES=""
  while IFS= read -r tf; do
    [ -z "$tf" ] || [ ! -f "$tf" ] && continue
    # Extract test names from TypeScript/JS test files
    names=$(grep -oE '(describe|it|test)\s*\(\s*['"'"'"`]([^'"'"'"`]+)['"'"'"`]' "$tf" 2>/dev/null | \
      sed 's/.*['"'"'"`]\([^'"'"'"`]*\)['"'"'"`].*/\1/' | head -20)
    TEST_NAMES="${TEST_NAMES}${names}"$'\n'
  done <<< "$CHANGED_TEST_FILES"
  TEST_NAMES_JSON=$(printf '%s\n' "$TEST_NAMES" | grep -v '^$' | jq -R . | jq -s . 2>/dev/null || echo "[]")
fi
```

### Step 2.6.4: Compute Coverage Map via Go Binary

```bash
ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
: "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
if [ -n "$KNOWLEDGE_PATH" ] && [ -f "$PRD_FILE" ]; then
  COVERAGE_MAP_PATH="$(nightgauge layout path pipeline coverage-map-${ISSUE_NUMBER}.json)"

  # Get list of changed file paths for code evidence scanning
  CHANGED_FILES=$(git diff HEAD~1 --name-only 2>/dev/null | head -50 || true)
  CHANGED_FILES_JSON=$(printf '%s\n' "$CHANGED_FILES" | grep -v '^$' | jq -R . | jq -s . 2>/dev/null || echo "[]")

  # Invoke the Go binary coverage check subcommand
  COVERAGE_RESULT=$("$BINARY" knowledge coverage-check \
    --issue "$ISSUE_NUMBER" \
    --prd "$PRD_FILE" \
    --decisions "$DECISIONS_FILE" \
    --changed-files "$CHANGED_FILES_JSON" \
    --test-names "$TEST_NAMES_JSON" \
    --output "$COVERAGE_MAP_PATH" \
    --json 2>/dev/null || echo '{"error":"coverage-check not available"}')

  COVERAGE_ERROR=$(printf '%s\n' "$COVERAGE_RESULT" | jq -r '.error // empty' 2>/dev/null)
  if [ -n "$COVERAGE_ERROR" ]; then
    echo "WARNING: Coverage check unavailable ($COVERAGE_ERROR) — skipping"
    COVERAGE_MAP_PATH=""
  else
    echo "Coverage map written: $COVERAGE_MAP_PATH"
    COVERED=$(printf '%s\n' "$COVERAGE_RESULT" | jq -r '.covered_count // 0')
    TOTAL=$(printf '%s\n' "$COVERAGE_RESULT" | jq -r '.total_count // 0')
    NO_EVIDENCE=$(printf '%s\n' "$COVERAGE_RESULT" | jq -r '.no_evidence_count // 0')
    VIOLATIONS=$(printf '%s\n' "$COVERAGE_RESULT" | jq -r '.violation_count // 0')
    echo "Coverage: ${COVERED}/${TOTAL} ACs covered, ${NO_EVIDENCE} no-evidence, ${VIOLATIONS} violation(s)"
  fi
fi
```

### Step 2.6.5: Evaluate Strict Mode

If `knowledge.validate.strict: true` and `no_evidence` count exceeds the
configured threshold, fail validation:

```bash
if [ -n "$COVERAGE_MAP_PATH" ] && [ -f "$COVERAGE_MAP_PATH" ]; then
  STRICT_MODE=$(jq -r '.knowledge.validate.strict // false' .nightgauge/config.yaml 2>/dev/null || echo "false")
  NO_EVIDENCE_THRESHOLD=$(jq -r '.knowledge.validate.no_evidence_threshold // 0' .nightgauge/config.yaml 2>/dev/null || echo "0")

  if [ "$STRICT_MODE" = "true" ] && [ "$NO_EVIDENCE_THRESHOLD" -gt 0 ]; then
    NO_EVIDENCE_ACTUAL=$(jq '[.criteria[] | select(.status == "no_evidence")] | length' "$COVERAGE_MAP_PATH" 2>/dev/null || echo "0")
    if [ "$NO_EVIDENCE_ACTUAL" -gt "$NO_EVIDENCE_THRESHOLD" ]; then
      echo "ERROR: knowledge.validate.strict=true — ${NO_EVIDENCE_ACTUAL} ACs have no evidence (threshold: ${NO_EVIDENCE_THRESHOLD})"
      VALIDATION_STATUS="failed"
    fi
  fi
fi
```

### Step 2.6.6: Emit knowledge.recall_hit if Recall API was consulted

If the coverage check called the Recall API for any AC:

```bash
if [ -n "$COVERAGE_MAP_PATH" ] && [ -f "$COVERAGE_MAP_PATH" ]; then
  RECALL_HITS=$(jq '[.criteria[] | .evidence[] | select(startswith("recall:"))] | length' "$COVERAGE_MAP_PATH" 2>/dev/null || echo "0")
  if [ "$RECALL_HITS" -gt 0 ]; then
    "$BINARY" telemetry emit \
      --type knowledge.recall_hit \
      --stage "feature-validate" \
      --result-count "$RECALL_HITS" 2>/dev/null || true
  fi
fi
```

The `COVERAGE_MAP_PATH` variable is threaded to Phase 6 (write-validate-context)
and recorded in `validate-{N}.json` as `coverage_map_path`.

---

## Phase 2.7: Pre-Push Merge Validation Gate

**PURPOSE**: Validate changes against the latest target branch before committing
and pushing. Catches merge conflicts, build failures, test regressions, and
security issues in the merged state (feature + target combined). This shifts
validation left — failures that would otherwise be caught during `pr-create` or
`pr-merge` are caught here while the agent still has full code context.

### Step 2.7.1: Resolve Binary and Run Gate

With the independent review on, Step 2.7.3 (the review) and Step 2.7.4 (the
run's one full gate) run before this step, in that order.

Pass no timeout flag: the verb resolves `hooks.pre_push.timeout_seconds`, then
its default, which covers the repository's suite. That limit exceeds what a
foreground call survives, so start this step with the Bash tool's
`run_in_background: true` and a `timeout` of at least that limit in milliseconds
(`hooks.pre_push.timeout_seconds` × 1000; 1,800,000 by default), and wait for its
completion notification before continuing. Why: a foreground call is killed at
twice its timeout (1200 s by default), so a foreground gate is discarded after
twenty minutes; a `&` inside a foreground call orphans it. The `gate-background`
hook denies both forms (#2953). `gate_command` is never run directly.

Exit 3 means a limit expired, not that an assertion failed. If the reason names
the gate's limit, do not edit code: raise the limit it names. If it names Go's
per-package limit, a package ran for most of the budget, so treat it as a likely
hang in the change and find the package in the goroutine dump.

```bash
ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
: "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
BINARY="${NIGHTGAUGE_BIN:-}"
[ -n "$BINARY" ] && [ ! -x "$BINARY" ] && BINARY=""
[ -z "$BINARY" ] && BINARY=$(command -v nightgauge 2>/dev/null || echo "")
if [ -z "$BINARY" ] && [ "${NIGHTGAUGE_DEV_REPO_BIN:-}" = "1" ]; then
  for NG_ROOT in "$(git rev-parse --show-toplevel 2>/dev/null)" \
    "$(cd "$(git rev-parse --git-common-dir 2>/dev/null)/.." 2>/dev/null && pwd)"; do
    [ -x "$NG_ROOT/bin/nightgauge" ] || continue
    grep -qx 'module github.com/nightgauge/nightgauge' "$NG_ROOT/go.mod" 2>/dev/null || continue
    NG_ORIGIN="$(git -C "$NG_ROOT" config --get remote.origin.url 2>/dev/null)"
    case "${NG_ORIGIN%.git}" in
      git@github.com:nightgauge/nightgauge | https://github.com/nightgauge/nightgauge | \
        ssh://git@github.com/nightgauge/nightgauge) BINARY="$NG_ROOT/bin/nightgauge"; break ;;
    esac
  done
fi
[ -z "$BINARY" ] && [ -x "$HOME/go/bin/nightgauge" ] && BINARY="$HOME/go/bin/nightgauge"

PREPUSH_STATUS="skipped"

if [ -n "$BINARY" ]; then
  echo "=== Pre-Push Merge Validation Gate ==="
  TARGET_BRANCH=$(jq -r '.base_branch // "main"' "$(nightgauge layout path pipeline issue-${ISSUE_NUMBER}.json)" 2>/dev/null)
  [ -z "$TARGET_BRANCH" ] && TARGET_BRANCH="main"

  PREPUSH_EXIT=0
  "$BINARY" pre-push validate "$ISSUE_NUMBER" --target "$TARGET_BRANCH" || PREPUSH_EXIT=$?
  case "$PREPUSH_EXIT" in
    0)
      PREPUSH_STATUS="passed"
      echo "Pre-push validation passed"
      ;;
    3)
      PREPUSH_STATUS="timeout"
      ERROR_CATEGORY="gate-timeout"
      echo "GATE TIMED OUT: a limit expired before the gate finished; no check failed."
      ;;
    *)
      PREPUSH_STATUS="failed"
      echo "GATE BLOCKED: Pre-push validation failed. Fix issues before committing/pushing."
      ;;
  esac
  if [ "$PREPUSH_EXIT" -ne 0 ]; then
    echo "See $(nightgauge layout path pipeline pre-push-${ISSUE_NUMBER}.json) for details."
    VALIDATION_STATUS="failed"
    SKIPPED_PHASES=$(printf '%s\n' "$SKIPPED_PHASES" | jq '. + [{"phase": "commit-and-push", "reason": "pre-push gate blocked"}]')
  fi
else
  # Graceful degradation: binary unavailable — run shell-based checks
  echo "WARNING: nightgauge binary not available — running shell-based pre-push checks"
  PREPUSH_STATUS="degraded"

  # Shell fallback: basic JSON validation
  JSON_ERRORS=0
  for f in $(git diff --name-only --diff-filter=AM HEAD 2>/dev/null | grep '\.json$' | grep -v node_modules); do
    jq . "$f" > /dev/null 2>&1 || { echo "Invalid JSON: $f"; JSON_ERRORS=$((JSON_ERRORS + 1)); }
  done

  # Shell fallback: grep for secret patterns
  SECRET_HITS=$(git diff HEAD 2>/dev/null | grep -cE '(?i)(password|secret|api_key)\s*[:=]\s*['\''"][^'\''"]{8,}['\''"]' || true)
  if [ "$SECRET_HITS" -gt 0 ]; then
    echo "WARNING: $SECRET_HITS potential secret(s) detected in diff"
  fi

  if [ "$JSON_ERRORS" -gt 0 ]; then
    PREPUSH_STATUS="failed"
    VALIDATION_STATUS="failed"
  fi
fi
```

### Step 2.7.2: Record Pre-Push Status

The `PREPUSH_STATUS` variable is threaded to Phase 6 (write-validate-context)
and recorded in `validate-{N}.json` as `pre_push_status`.

### Step 2.7.3: Independent Review Gate (#2839)

One review per tree, in a fresh context that holds only the diff, the issue and
the working tree. You do not review this change: you start the review and act on
its result. This is one of this stage's gates, so a provider rule against adding
reviewers does not remove it. It applies the passes in
[docs/PR_REVIEW_PASSES.md](../../../docs/PR_REVIEW_PASSES.md). The verdict reaches
`FeatureValidateGate` via gate-metrics, per the "network/LLM checks are NOT
StageGates" precedent in [docs/STAGE_GATES.md](../../../docs/STAGE_GATES.md).
`pipeline.adversarial_review.enabled: false` turns it off.

Every round runs the `review prepare` block below (or the `review run` block,
which prepares itself), including the round after each `catch` fix. Its first
line re-prints this phase's marker: that is the productive signal that keeps
the progress monitor from reading the fix-and-re-review loop as churn (#3168).

```bash
printf '<!-- phase:start name="ci-parity-check" index=12 total=22 stage="feature-validate" -->\n'
ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
: "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
"$BINARY" review prepare --issue "$ISSUE_NUMBER" --json
```

Act on the JSON:

- `disabled`: the gate is off. Continue.
- `reusable`: this tree was already reviewed. Use `result`, and run no model.
- `cap_reached`: the last catch stands. Set `VALIDATION_STATUS="failed"` and
  `ERROR_CATEGORY="review-cap-reached"`, and fix nothing more: `result.fix` is
  what stands, not more work for this run. Commit and push stay skipped, so the
  work stays in this worktree. The issue parks; the repository keeps dispatching.
- Otherwise it returned a bundle. If your host has a subagent tool, start one
  non-forked subagent, with no inherited conversation, and give it only the text
  of `prompt_path`. Pass it nothing about this change. Then record its result,
  with `tree`, `findings_path` and `started_at` copied from the JSON, because each
  block runs in a new shell:

  ```bash
  ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
  : "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
  TREE="<tree>" FINDINGS_PATH="<findings_path>" STARTED_AT="<started_at>"
  "$BINARY" review record --issue "$ISSUE_NUMBER" --tree "$TREE" \
    --findings "$FINDINGS_PATH" --started-at "$STARTED_AT"
  ```

  With no subagent tool, or when the subagent left no file at `findings_path`,
  discard that bundle and let the binary spawn the reviewer instead. It runs
  prepare and record itself and prints the log path. That log is the review's
  session log:

  ```bash
  printf '<!-- phase:start name="ci-parity-check" index=12 total=22 stage="feature-validate" -->\n'
  ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
  : "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
  "$BINARY" review run --issue "$ISSUE_NUMBER"
  ```

Never edit the findings file. The verdict is the binary's, and it writes the one
gate-metric row, the duration, the severity counts and any discovered work.

- `pass`: continue. Findings it routed to `discovered` need nothing from you.
- `catch` with items in `fix`, and no `cap_reached`: fix each one, re-run the
  scoped checks (build, vet, tests of the changed packages, Step 2.5.0a): never
  the full gate, and never kill a running gate. Then go back to
  `review prepare`. The tree changed, so that is a new round. A later
  `pass` supersedes the catch, and after three catches in a run `prepare` stops.
- `catch` with an empty `fix`: the review did not complete, which is not a pass.
  Set `VALIDATION_STATUS="failed"` so commit and push are skipped, and put the
  `summary` in the validate notes.

### Step 2.7.4: The Run's One Full Gate (#3207)

Run this once, after Step 2.7.3 returns `pass`, `reusable` or `disabled` on the
final tree, and before the pre-push gate (Step 2.7.1). It is Steps 2.5.0 to 2.5.3
as written, on the tree the review passed: `nightgauge ci gate-status`, then
`nightgauge ci gate-run` in the background, then the auto-fix loop if the gate
fails. Skip it when the review is off, because Phase 2.5 already ran the full
gate. Skip it too when the review ended in `cap_reached` or a failed review,
because commit and push are skipped and no gate result would be used. A gate
fix that changes the tree after the review passed needs a new review round:
go back to Step 2.7.3 once, then return here.

The pre-push gate then reuses this tree's ledger pass instead of running again.
Put the run's gate count in the validate notes, so the stage record carries it:

```bash
GATE_COUNT=$(nightgauge ci gate-count --json 2>/dev/null || echo '{}')
echo "Full gates this run: $(printf '%s\n' "$GATE_COUNT" | jq -r '.full // "unknown"')"
```
