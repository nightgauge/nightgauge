### CI Check Gate

## Contents

- [Check Skip CI Gate Flag](#check-skip-ci-gate-flag)
- [Detect Epic Branch PRs](#detect-epic-branch-prs-no-ci-workflow)
- [Wait for CI Checks](#wait-for-ci-checks-deterministic)
- [Handle No CI Checks Configured](#handle-no-ci-checks-configured)
- [Handle Timeout](#handle-timeout)
- [Check for Failures](#check-for-failures)
- [Pre-Merge Local Test Safety Net](#pre-merge-local-test-safety-net)

**PURPOSE**: Wait for CI checks to complete before merge. This gate can only
be bypassed with explicit `--skip-ci-gate`.

**REQUIRED CHECK**: The `CI` workflow (`.github/workflows/ci.yml`) runs
`npm run build` and `npm run test` on every PR. The Go binary `nightgauge ci wait` command
automatically detects and waits for it.

#### Check Skip CI Gate Flag

```bash
if [ "$ARG_SKIP_CI_GATE" = "true" ]; then
  echo "WARNING: Skipping CI check gate (--skip-ci-gate)"
  CI_GATE_SKIP=true
fi
```

If `CI_GATE_SKIP=true`, skip CI waiting and the pre-merge safety net.

#### Detect Epic Branch PRs (No CI Workflow)

The CI workflow (`.github/workflows/ci.yml`) only triggers on PRs targeting
`main`. PRs targeting epic branches (e.g., `epic/1941-*`) will never receive
`build-and-test` or `codex-smoke` checks, so `ci wait` would time out. Skip
CI waiting for these PRs — the local pre-merge safety net still runs.

```bash
if [ -n "$BASE_BRANCH" ] && echo "$BASE_BRANCH" | grep -q "^epic/"; then
  echo "PR targets epic branch ($BASE_BRANCH) — CI workflows do not trigger for non-main targets"
  echo "Skipping CI wait; local pre-merge safety net will run instead"
  CI_CHECKS_PASSED=true
  CI_EPIC_SKIP=true
fi
```

#### Wait for CI Checks (Deterministic)

**CRITICAL**: You MUST use the Go binary `nightgauge ci wait` to poll for
CI checks. **NEVER** write your own polling loop using `gh pr checks`, `gh api`,
`sleep`, or any other ad-hoc approach. The `gh pr checks --jq` pattern is broken
(`--jq` requires `--json` which changes the output format) and causes the
pr-merge stage to hang for 15+ minutes. The Go binary handles polling correctly
with proper timeout, interval, and terminal-state detection.

**CHUNKED WAIT (#187)**: a single Bash tool call is budgeted ~2 minutes, so
one long `ci wait --timeout 10` is SIGTERMed (exit 143) before CI finishes.
Each Bash tool call therefore runs ONE bounded 90-second chunk
(`--timeout-secs 90`); a deadline file carries the cumulative `$TIMEOUT`
budget across calls. Chunk outcomes: exit 0 = green, exit 1 = failed,
**exit 2 = chunk expired with checks still pending**. On exit 2 with
cumulative budget remaining, END the current Bash call and re-run this same
wait block in a **new** Bash tool call — never `sleep` inline, never switch
to ad-hoc polling.

```bash
BINARY="${NIGHTGAUGE_BIN:-}"
[ -n "$BINARY" ] && [ ! -x "$BINARY" ] && BINARY=""
[ -z "$BINARY" ] && BINARY=$(command -v nightgauge 2>/dev/null || echo "")
if [ -z "$BINARY" ]; then
  REPO_ROOT="$(git rev-parse --show-toplevel 2>/dev/null || pwd)"
  [ -x "$REPO_ROOT/bin/nightgauge" ] && BINARY="$REPO_ROOT/bin/nightgauge"
fi
if [ -z "$BINARY" ]; then
  GIT_COMMON_DIR="$(git rev-parse --git-common-dir 2>/dev/null || true)"
  if [ -n "$GIT_COMMON_DIR" ]; then
    CANONICAL_REPO="$(cd "$GIT_COMMON_DIR/.." 2>/dev/null && pwd)"
    [ -n "$CANONICAL_REPO" ] && [ -x "$CANONICAL_REPO/bin/nightgauge" ] && BINARY="$CANONICAL_REPO/bin/nightgauge"
  fi
fi
[ -z "$BINARY" ] && [ -x "$HOME/go/bin/nightgauge" ] && BINARY="$HOME/go/bin/nightgauge"
[ -n "$BINARY" ] && export PATH="$(dirname "$BINARY"):$PATH"
TIMEOUT=${ARG_TIMEOUT:-10}  # minutes (Go binary --timeout is in minutes)

if [ -z "$BINARY" ]; then
  echo "ERROR: nightgauge binary not found in PATH or bin/nightgauge" >&2
  echo "This binary is required for the CI check gate." >&2
  exit 1
fi

CI_CHUNK_PENDING=false
CI_DEADLINE_FILE=".nightgauge/pipeline/ci-wait-deadline-${PR_NUMBER}"

if [ "$CI_EPIC_SKIP" = "true" ]; then
  echo "Skipping ci wait (epic branch PR)"
  CI_RESULT='{"state":"SUCCESS","total":0,"completed":0,"successful":0,"failed":0,"pending":0,"checks":[],"isTerminal":true,"elapsedSecs":0}'
  CI_EXIT_CODE=0
  CI_STDERR=""
else
  # Cumulative budget bookkeeping (#187): first chunk writes the deadline;
  # later chunks (fresh Bash calls) read it back.
  _CI_NOW=$(date +%s)
  if [ ! -f "$CI_DEADLINE_FILE" ]; then
    mkdir -p "$(dirname "$CI_DEADLINE_FILE")"
    echo $(( _CI_NOW + TIMEOUT * 60 )) > "$CI_DEADLINE_FILE"
  fi
  _CI_DEADLINE=$(cat "$CI_DEADLINE_FILE" 2>/dev/null || echo $(( _CI_NOW + TIMEOUT * 60 )))
  _CI_REMAINING=$(( _CI_DEADLINE - _CI_NOW ))
  _CI_CHUNK=90
  [ "$_CI_REMAINING" -lt "$_CI_CHUNK" ] && _CI_CHUNK=$_CI_REMAINING

  if [ "$_CI_REMAINING" -le 0 ]; then
    # Cumulative budget exhausted across chunks — treat as a real CI timeout.
    CI_RESULT='{"state":"TIMEOUT","total":0,"completed":0,"successful":0,"failed":0,"pending":0,"checks":[],"isTerminal":true,"elapsedSecs":0}'
    CI_EXIT_CODE=2
    CI_STDERR=""
  else
    # CRITICAL: capture stderr — silently swallowing it caused #2868, where
    # rate-limited `ci wait` runs were indistinguishable from "no checks
    # configured" and merges proceeded against unverified PRs.
    CI_STDERR_FILE=$(mktemp)
    CI_RESULT=$("$BINARY" ci wait "$PR_NUMBER" --timeout-secs "$_CI_CHUNK" --required-only --json 2>"$CI_STDERR_FILE")
    CI_EXIT_CODE=$?
    CI_STDERR=$(cat "$CI_STDERR_FILE" 2>/dev/null || echo "")
    rm -f "$CI_STDERR_FILE"

    # Chunk expired but cumulative budget remains → not a CI timeout yet.
    if [ "$CI_EXIT_CODE" = "2" ] && [ $(( _CI_DEADLINE - $(date +%s) )) -gt 0 ]; then
      CI_CHUNK_PENDING=true
      echo "[ci-gate] Chunk expired, checks still pending — $(( _CI_DEADLINE - $(date +%s) ))s of CI budget remain. Re-run this wait block in a NEW Bash call."
    fi
  fi
fi

# Terminal outcome (green/failed/cumulative-timeout) — drop the deadline file
# so the next PR (or a re-run after fixes) starts a fresh budget.
if [ "$CI_CHUNK_PENDING" != "true" ]; then
  rm -f "$CI_DEADLINE_FILE"
fi

CI_ALL_PASSED=$(echo "$CI_RESULT" | jq -r 'if .state == "SUCCESS" then "true" else "false" end')
CI_HAS_CHECKS=$(echo "$CI_RESULT" | jq -r 'if .total > 0 then "true" else "false" end')

REQUIRED_CHECKS=$(echo "$CI_RESULT" | jq -r '.requiredCheckNames // [] | join(", ")' 2>/dev/null)
if [ -n "$REQUIRED_CHECKS" ]; then
  echo "[ci-gate] Waited on required checks: $REQUIRED_CHECKS"
fi
CI_FAILED_COUNT=$(echo "$CI_RESULT" | jq -r '.failed // 0')
CI_PENDING_COUNT=$(echo "$CI_RESULT" | jq -r '.pending // 0')

case $CI_EXIT_CODE in
  0) CI_CHECKS_PASSED=true ;;
  2)
    # Exit 2 = wait window expired with checks still pending (#187). A chunk
    # expiry with cumulative budget remaining re-runs the block (see
    # CI_CHUNK_PENDING above); only cumulative exhaustion is a real timeout.
    CI_CHECKS_PASSED=false
    [ "$CI_CHUNK_PENDING" != "true" ] && CI_TIMEOUT=true
    ;;
  *) CI_CHECKS_PASSED=false; CI_FETCH_FAILED=true ;;
esac

# Exit 0 means the WAIT finished cleanly — not that checks passed: a state
# of FAILURE also exits 0. Derive pass/fail from the reported state (#187;
# the old exit-1 branch that set CI_HAS_FAILURES never fired).
if [ "$CI_EXIT_CODE" -eq 0 ] && [ "$CI_ALL_PASSED" != "true" ]; then
  CI_CHECKS_PASSED=false
  [ "${CI_FAILED_COUNT:-0}" -gt 0 ] && CI_HAS_FAILURES=true
fi

# #2868: distinguish "ci wait errored" (network/rate-limit/auth) from
# "ci wait succeeded with zero checks". Only the latter is safe to
# treat as "no checks configured". The former MUST fail closed —
# otherwise a rate-limited fetch becomes an unguarded merge.
# Exit 2 (timeout / chunk expiry) is a DETERMINATE outcome, not a fetch
# failure — it is excluded here (#187).
if [ "$CI_EXIT_CODE" -ne 0 ] && [ "$CI_EXIT_CODE" -ne 2 ] && [ "$CI_EPIC_SKIP" != "true" ]; then
  CI_FETCH_FAILED=true
  if echo "$CI_STDERR" | grep -qiE "rate.?limit|api rate"; then
    CI_RATE_LIMITED=true
    echo "ERROR: ci wait failed due to GitHub API rate limit. Cannot verify CI status." >&2
  fi
  if [ -n "$CI_STDERR" ]; then
    echo "[ci-gate] ci wait stderr: $CI_STDERR" >&2
  fi
fi

# Chunk pending (#187): checks are still running and cumulative budget
# remains. STOP at the end of this Bash call and re-run this entire
# "Wait for CI Checks" block in a NEW Bash tool call — do not proceed to
# failure handling, do not sleep inline, do not poll ad hoc.
if [ "$CI_CHUNK_PENDING" = "true" ]; then
  echo "[ci-gate] CI still pending — continue the wait in a fresh Bash call."
fi
```

#### Handle No CI Checks Configured

```bash
# Only treat "no checks" as "ok to merge" when ci wait succeeded. A failed
# fetch (rate-limit, network, auth) leaves CI_HAS_CHECKS=false too — but
# proceeding then would be an unverified merge (#2868).
if [ "$CI_HAS_CHECKS" = "false" ] && [ "$CI_EXIT_CODE" -eq 0 ]; then
  echo "No CI checks configured on this repository — proceeding with merge"
  CI_CHECKS_PASSED=true
fi

# Hard fail when we couldn't determine CI status. This is the fail-closed
# gate from #2868. Bypass requires explicit --skip-ci-gate.
if [ "$CI_FETCH_FAILED" = "true" ] && [ "$CI_GATE_SKIP" != "true" ]; then
  echo "ERROR: Cannot verify CI status (ci wait exit=$CI_EXIT_CODE)." >&2
  if [ "$CI_RATE_LIMITED" = "true" ]; then
    echo "       GitHub API rate limit exhausted. Wait for reset or use --skip-ci-gate." >&2
  fi
  echo "       Refusing to merge against unverified CI status." >&2
  exit 1
fi
```

#### Handle Timeout

`CI_TIMEOUT=true` only fires after the CUMULATIVE budget is exhausted across
chunks (a single expired chunk with budget remaining sets `CI_CHUNK_PENDING`
and re-runs the wait block instead — #187). If timeout reached in batch mode,
treat as failure and proceed to auto-fix or exit. Options: keep waiting
(re-run the wait block; a fresh deadline file grants a new budget), check
status, cancel.

#### Check for Failures

```bash
if [ "$CI_HAS_FAILURES" = "true" ]; then
  FAILED_CHECKS=$(echo "$CI_RESULT" | jq -r '[.checks[] | select(.conclusion == "FAILURE") | .name] | .[]' 2>/dev/null)

  AUTO_FIX_CI=$([ "${ARG_NO_AUTO_FIX_CI:-false}" = "true" ] && echo "false" || echo "true")

  if [ "$AUTO_FIX_CI" = "true" ]; then
    PROCEED_TO_AUTO_FIX=true
  fi
fi
```

If failures exist and auto-fix is disabled, options: view failures, attempt
auto-fix, cancel.

### Pre-Merge Local Test Safety Net

**PURPOSE**: Run build and tests locally as a final safety net before merging.
This catches issues that CI may have missed due to environment differences,
transient failures, or caching. This gate is only skipped when
`--skip-ci-gate` is explicitly set.

**SKIP CONDITION**: If `CI_GATE_SKIP=true` (from `--skip-ci-gate`), skip this
entirely.

```bash
if [ "$CI_GATE_SKIP" != "true" ]; then
  echo "Running pre-merge local test safety net..."

  # Build all workspaces
  if ! npm run build; then
    echo "ERROR: Pre-merge build failed. Fix build errors before merging."
    exit 1
  fi

  # Run all workspace tests
  # NOTE: Workspace test scripts MUST use `vitest run` (not bare `vitest`)
  # to avoid hanging in watch mode. Verify package.json "test" scripts.
  if ! npm run test; then
    echo "ERROR: Pre-merge tests failed. Fix failing tests before merging."
    echo "This is a hard gate — tests must pass before merge."
    exit 1
  fi

  echo "Pre-merge safety net: build and tests passed"
fi
```

**Important**: There is no admin merge bypass in this pipeline — never pass
`--admin` or `--auto` to a merge command (#186). Only `--skip-ci-gate`
bypasses the CI wait and the local test run, and nothing bypasses branch
protection.
