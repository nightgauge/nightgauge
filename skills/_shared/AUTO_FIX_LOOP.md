### Auto-Fix Retry Loop

## Contents

- [Configuration and Loop Structure](#configuration-and-loop-structure)
- [Step 0: Fast-Path Merge State Check](#step-0-fast-path-merge-state-check)
- [Step 0.5: Transient-Failure Re-Run Gate](#step-05-transient-failure-re-run-gate-deterministic)
- [Step 1: Fetch Failure Logs](#step-1-fetch-failure-logs)
- [Step 2: Classify Failure Type](#step-2-classify-failure-type)
- [Step 2.5: Baseline-Failure Detection](#step-25-baseline-failure-detection-deterministic)
- [Step 3: Generate Fix](#step-3-generate-fix-probabilistic---ai)
- [Step 4: Commit and Push Fix](#step-4-commit-and-push-fix)
- [Step 5: Wait for Re-Check](#step-5-wait-for-re-check)
- [Handle Loop Exit](#handle-loop-exit)

**PURPOSE**: Automatically attempt to fix CI failures before merge, following
the Ralph Loop pattern. Skipped if CI checks pass or auto-fix is disabled.

**ENTRY CONDITIONS**: `PROCEED_TO_AUTO_FIX=true` or user selected auto-fix.

**EXIT CONDITIONS**:

- CI checks pass after fix -> Proceed to merge
- Max attempts reached -> Report failure and exit
- Non-fixable failure (security) -> Report and exit

#### Configuration and Loop Structure

```bash
AUTO_FIX_MAX_ATTEMPTS=${NIGHTGAUGE_PR_AUTO_FIX_MAX_ATTEMPTS:-2}
CI_CHECK_TIMEOUT=${NIGHTGAUGE_PR_CI_CHECK_TIMEOUT:-10}  # minutes (not seconds)
AUTO_FIX_ATTEMPT=0
AUTO_FIX_SUCCESS=false

while [ $AUTO_FIX_ATTEMPT -lt $AUTO_FIX_MAX_ATTEMPTS ]; do
  AUTO_FIX_ATTEMPT=$((AUTO_FIX_ATTEMPT + 1))
  # Steps 0-5 execute within this loop
done
```

The default cap was lowered to 2 in #3108. After two LLM-driven fix attempts the next dollar of spend rarely produces new progress; surface to the user instead of silently looping. Override with `pr.auto_fix_max_attempts` in `.nightgauge/config.yaml` or `NIGHTGAUGE_PR_AUTO_FIX_MAX_ATTEMPTS`.

#### Step 0: Fast-Path Merge State Check

Before each fix iteration, check whether the PR already merged out-of-band (e.g. a previous attempt's push triggered a successful CI run that auto-merged, or a teammate manually merged). This is the cheapest possible deterministic guard against the failure mode in #3108: the LLM kept iterating on E2E selectors after the PR had already merged, burning ~$8 in the 60s after the work shipped.

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
PR_PRECHECK_STATE=$("$BINARY" pr view "$PR_NUMBER" --json 2>/dev/null | jq -r '.state // "UNKNOWN"')
case "$PR_PRECHECK_STATE" in
  MERGED)
    echo "PR #$PR_NUMBER already merged — exiting auto-fix loop (saved one LLM iteration)."
    AUTO_FIX_SUCCESS=true
    break
    ;;
  CLOSED)
    echo "PR #$PR_NUMBER closed without merge — escalating, no further auto-fix attempts."
    break
    ;;
esac
```

This call costs roughly one GraphQL request (~50ms, $0) and runs before any model turn for the iteration. It also handles the race where attempt N's push lands while we are evaluating attempt N+1.

#### Step 0.5: Transient-Failure Re-Run Gate (Deterministic)

**PURPOSE**: Honor the transient-failure signal pr-create already wrote to
`pr-{N}.json`. Before spending any model turns generating a fix, check whether
the failures the previous stage observed were classified as transient
(`is_transient: true`) — network blips, registry timeouts, runner outages.
For those, `gh run rerun --failed` avoids an unnecessary model turn and often
resolves the failure faster than generating a code change for an infrastructure
problem.

For example, when pr-create classifies an action-download timeout as transient
and writes `notes: "pr-merge should re-trigger CI"`, pr-merge should honor the
signal instead of asking RALPH to modify code for a network failure.

```bash
# Read transient-failure flags from the prior stage's handoff.
# pr-merge reads pr-{N}.json (written by pr-create Phase 4).
PR_CONTEXT=".nightgauge/pipeline/pr-${ISSUE_NUMBER}.json"
if [ -f "$PR_CONTEXT" ]; then
  ALL_TRANSIENT=$(jq -r '
    (.ci_monitoring.failures // []) as $f
    | if ($f | length) == 0 then "false"
      else ($f | all(.is_transient == true)) | tostring
      end' "$PR_CONTEXT" 2>/dev/null || echo "false")
  TRANSIENT_NOTE=$(jq -r '.ci_monitoring.notes // ""' "$PR_CONTEXT" 2>/dev/null || echo "")
else
  ALL_TRANSIENT="false"
  TRANSIENT_NOTE=""
fi

if [ "$ALL_TRANSIENT" = "true" ]; then
  echo "[transient-rerun-gate] All failures classified as transient by prior stage."
  echo "[transient-rerun-gate] Note from prior stage: ${TRANSIENT_NOTE:-(none)}"
  echo "[transient-rerun-gate] Re-running failed checks deterministically before engaging RALPH."

  # Find the most recent failed run for this PR and rerun its failed jobs.
  FAILED_RUN=$("$BINARY" pr ci-wait "$PR_NUMBER" --timeout 1 --json 2>/dev/null \
    | jq -r '.checks[] | select(.conclusion == "FAILURE") | .detailsUrl' \
    | grep -oE '[0-9]+' | head -1)
  if [ -z "$FAILED_RUN" ]; then
    # Fallback: ask gh directly for the latest failed run on this PR's branch.
    BRANCH=$(gh pr view "$PR_NUMBER" --json headRefName --jq '.headRefName' 2>/dev/null || echo "")
    if [ -n "$BRANCH" ]; then
      FAILED_RUN=$(gh run list --branch "$BRANCH" --status failure --limit 1 --json databaseId --jq '.[0].databaseId' 2>/dev/null || echo "")
    fi
  fi

  if [ -n "$FAILED_RUN" ]; then
    gh run rerun "$FAILED_RUN" --failed 2>&1 | head -3 || true
    echo "[transient-rerun-gate] Re-run triggered for run $FAILED_RUN. Waiting for CI…"

    # Wait for the rerun in ONE bounded 90s chunk — a full
    # --timeout ${CI_CHECK_TIMEOUT} wait is SIGTERMed by the ~2-minute Bash
    # tool budget (#187). Exit 2 = chunk expired, checks still pending: if the
    # rerun deserves more waiting, re-run THIS wait command in a NEW Bash tool
    # call (up to CI_CHECK_TIMEOUT minutes cumulative — you track the budget);
    # otherwise fall through to normal RALPH so we never get stuck here.
    if "$BINARY" ci wait "$PR_NUMBER" --timeout-secs 90 --json > /tmp/rerun-result.json 2>/dev/null; then
      RERUN_STATE=$(jq -r '.state // "UNKNOWN"' /tmp/rerun-result.json)
      if [ "$RERUN_STATE" = "SUCCESS" ]; then
        echo "[transient-rerun-gate] Re-run passed — proceeding to merge without RALPH."
        AUTO_FIX_SUCCESS=true
        break
      fi
      echo "[transient-rerun-gate] Re-run did NOT pass cleanly (state=$RERUN_STATE). Falling through to RALPH."
    fi
  else
    echo "[transient-rerun-gate] Could not locate a failed run to rerun. Falling through to RALPH."
  fi
fi
```

**Cost**: one `gh run rerun` call + one CI wait — both $0 from a token
perspective. If the transient hypothesis was right, the whole loop exits here
without a single model turn. If wrong (rerun also fails), fall through to the
normal RALPH path with no time lost beyond the CI wait.

**Trust boundary**: the gate only fires when the prior stage's classifier
marked **every** failure transient. A mixed batch (one transient + one code
bug) skips this gate and goes straight to RALPH so the code bug gets attention.

This gate exists to prevent model-driven repair from running before a cheap,
deterministic retry of a purely transient failure.

#### Step 1: Fetch Failure Logs

Use `$HOOKS_DIR/fetch-ci-failure-logs.sh "$PR_NUMBER"` to retrieve logs. Parse
`total_failures`, `logs_fetched`, and extract first failure's `name`, `logs`,
and `logs_url` from the JSON result.

#### Step 2: Classify Failure Type

| Check Name Pattern                            | Failure Type | Auto-Fixable |
| --------------------------------------------- | ------------ | ------------ |
| `*lint*`, `*eslint*`, `*pylint*`, `*style*`   | `lint`       | Yes          |
| `*test*`, `*spec*`, `*vitest*`, `*jest*`      | `test`       | Maybe        |
| `*build*`, `*compile*`, `*bundle*`            | `build`      | Maybe        |
| `*type*`, `*tsc*`, `*typescript*`, `*mypy*`   | `typecheck`  | Maybe        |
| `*format*`, `*prettier*`, `*black*`           | `format`     | Yes          |
| `*security*`, `*audit*`, `*snyk*`, `*codeql*` | `security`   | No (break)   |

If `security` type detected, break out of retry loop immediately.

#### Step 2.5: Baseline-Failure Detection (Deterministic)

**PURPOSE**: Distinguish failures introduced by this PR (regressions) from
failures that already exist on the base branch. Before spending model turns
attempting an LLM fix, run the same failing test(s) at the PR's merge-base on
the base branch. This costs one local test run (~1-3 min for most repos), no
model spend, and routinely saves $10+ of useless LLM iteration when a PR
inherits a broken main.

**When to run this check**: Only for `test`, `build`, `typecheck`, or `unknown`
failure types. Lint/format failures are deterministic and don't benefit from
the comparison.

**Procedure** (skip cleanly if any step errors — we treat the _classification_
as a best-effort optimization. Once a classification exists, the _exit gate_
in step 9 below is mandatory, not advisory — see acme-api#100
retro for why making this advisory burned a sham `[skip build]` commit).

1. Extract the specific test names / build targets from the failure logs.
2. Identify the merge base: `MERGE_BASE=$(git merge-base HEAD origin/$BASE_REF)`.
3. Stash any uncommitted changes (`git stash push -u -m pre-baseline 2>/dev/null`).
4. `git checkout "$MERGE_BASE"` (detached HEAD is fine).
5. Run the same failing tests/builds on the base. Capture pass/fail per item.
6. `git checkout -` and `git stash pop 2>/dev/null` to return to the PR branch.
7. Classify each failure as one of:
   - **`regression`** — passed on base, fails on HEAD. Caused by this PR.
     The agent must fix.
   - **`inherited`** — fails on both base and HEAD. Pre-existing on main; the
     PR did not introduce it. Do **not** ask the LLM to repair main from
     within a dependent PR (see step 9).
8. Persist the classification to `.nightgauge/pipeline/auto-fix-baseline-{PR}.json`
   so subsequent iterations can read it without re-running.
9. **Inherited-only exit gate (deterministic, mandatory).** Count failures by
   classification:

   ```bash
   BASELINE=".nightgauge/pipeline/auto-fix-baseline-${PR_NUMBER}.json"
   if [ -f "$BASELINE" ]; then
     INHERITED=$(jq '[.failures[]? | select(.classification == "inherited")] | length' "$BASELINE" 2>/dev/null || echo 0)
     REGRESSIONS=$(jq '[.failures[]? | select(.classification == "regression")] | length' "$BASELINE" 2>/dev/null || echo 0)
     if [ "$REGRESSIONS" = "0" ] && [ "$INHERITED" -gt "0" ]; then
       echo "[baseline-exit-gate] $INHERITED inherited failure(s), 0 regressions."
       echo "[baseline-exit-gate] Surfacing to user — main is broken, not this PR."
       # Surface: post a PR comment with the inherited test names + a hint that
       # the fix belongs on main, then label the PR `pipeline-failed-inherited`
       # and exit the loop. Do NOT proceed to Step 3 (LLM fix generation) —
       # asking the model to repair main from a feature branch produces sham
       # commits like `chore: re-trigger CI [skip build]` (platform#956).
       AUTO_FIX_EXIT_REASON="inherited-only"
       break
     fi
     if [ "$REGRESSIONS" -gt "0" ] && [ "$INHERITED" -gt "0" ]; then
       echo "[baseline-exit-gate] Mixed: $REGRESSIONS regression(s) + $INHERITED inherited. Fixing regressions only."
       # Continue to Step 3, but the fix-generation prompt MUST be scoped to
       # the `regression` failures (Step 3 reads the baseline file).
     fi
   fi
   ```

   This is the load-bearing change vs. the original advisory text: when the
   PR introduces _zero_ regressions, the loop exits before any model spend
   instead of trusting the LLM to choose "surface or fix" correctly.

**Cost guardrail**: If more than `NIGHTGAUGE_PR_BASELINE_MAX_FAILURES`
(default 30) tests fail on base, skip baseline detection entirely and treat all
failures as inherited — at that scale the cheapest action is to surface to
the user, not to spend model turns blindly fixing main. Emit a one-line
warning: `"baseline check skipped: base branch has N failing tests (>$cap)"`.

**Agent instruction when failures are mostly `inherited`**: tell the user
plainly. Example output:

> Detected 22 failing tests on PR #218. Re-running the same tests on the
> merge-base showed 22 of 22 also fail on main — these are not regressions
> introduced by this PR. Main is broken (likely from PR #197). Recommend
> blocking on a main-branch fix before merging further work, or fixing all
> 22 in this PR. Estimated fix scope: ~12 of 22 are stale `find.text` matches
> easy to repair; remainder need Riverpod 3.x AsyncValue handling changes.

This step is the highest-leverage addition to auto-fix: a deterministic check
that prevents the LLM from chasing inherited failures it has no signal to
recognize. Reference: #3662 (Flutter PR #218 burned $12 hitting `num_turns: 40`
on a stack of 22 pre-existing failures it could not have known were not its).

#### Step 3: Generate Fix (Probabilistic - AI)

**This is the ONLY probabilistic step in the auto-fix loop.** Based on failure
type and logs, generate an appropriate fix.

**Subagent Model**: When spawning a Task subagent for fix generation, specify
`model: "sonnet"` to use a cost-optimized model.

| Failure Type | Fix Approach                                           |
| ------------ | ------------------------------------------------------ |
| `lint`       | Run linter with --fix, or manually fix reported issues |
| `format`     | Run formatter (prettier, black, etc.)                  |
| `typecheck`  | Fix type errors based on compiler output               |
| `build`      | Fix missing imports, syntax errors, config issues      |
| `test`       | Analyze test failure, fix assertion or implementation  |
| `unknown`    | Read logs carefully, attempt minimal targeted fix      |

Fix generation process:

1. Read the failure logs to understand the specific error
2. If Step 2.5 ran, read
   `.nightgauge/pipeline/auto-fix-baseline-{PR}.json`. **Only attempt to
   fix failures whose `classification` is `regression`.** Inherited failures
   were already handled by Step 2.5's exit gate (inherited-only) or are
   intentionally being ignored by Step 2.5's mixed-batch branch — touching
   them here re-introduces the same wasted-spend pattern.
3. Identify the affected file(s) and line number(s)
4. Make the minimal fix required to address the failure
5. Ensure fix doesn't break other functionality
6. If you cannot produce a real diff for any regression failure, stop.
   Step 4's sham-commit guard will reject an empty commit anyway — making
   that decision here saves a model turn.

#### Step 4: Commit and Push Fix

Check for changes (`git diff --quiet`). If no changes, **break out of the
loop** — do not continue to the next attempt and do not invent a re-trigger
commit. "No diff" means Step 3 produced nothing meaningful; making another
attempt without new diagnostics will produce the same nothing.

Otherwise stage the working tree and run the sham-commit guard **before**
calling `git commit`:

```bash
git add -A

# --- Sham-commit guard (deterministic, mandatory) ---
# Reject any "fix" that has no real diff or that resembles a CI re-trigger
# nudge. These produce noise on the PR, do not change CI behavior, and hide
# the real failure from the user. See acme-api#100 retro:
# the LLM pushed `chore: re-trigger CI [skip build]` (an empty commit, with
# a Netlify-only directive that GitHub Actions ignores) after baseline
# detection had already classified all failures as inherited.

if git diff --cached --quiet; then
  echo "[sham-commit-guard] No staged changes — refusing to push empty commit."
  echo "[sham-commit-guard] Surfacing to user; main loop will exit."
  AUTO_FIX_EXIT_REASON="empty-fix"
  break
fi

PROPOSED_MSG="fix(#$ISSUE_NUMBER): auto-fix CI failure (attempt $AUTO_FIX_ATTEMPT)"
# Defense-in-depth: even if a future change moves the commit message into a
# subagent's hands, refuse known re-trigger phrasings.
case "$PROPOSED_MSG" in
  *"re-trigger CI"*|*"retrigger CI"*|*"[skip build]"*|*"[skip ci]"*|*"empty commit"*)
    echo "[sham-commit-guard] Commit message matches a re-trigger pattern — refusing."
    AUTO_FIX_EXIT_REASON="sham-commit"
    break
    ;;
esac

git commit -m "$PROPOSED_MSG"
```

Then push the fix to the remote so CI can see the new commit:

```bash
git push origin HEAD
```

If push fails (e.g., remote rejected, network error), report the error and break
out of the retry loop — do NOT proceed to Step 5. CI cannot re-check commits
that are not on the remote.

**Never use `git commit --allow-empty` in the auto-fix loop.** An empty commit
is a strong signal that the loop has run out of useful work; the correct
response is to break and surface, not to pretend a fix happened.

#### Step 5: Wait for Re-Check

Wait briefly (`sleep 10`), then use Go binary `nightgauge ci wait` again.
**NEVER** substitute your own polling loop using `gh pr checks` — always use the
Go binary as shown in CI_GATE.md.

- Exit code 0: `AUTO_FIX_SUCCESS=true`, break loop
- Exit code 1: Compare failures to previous. If same failure repeats 2+ times,
  log as potentially unfixable. If different failure, log as progress made.
- Other: Log error/timeout

#### Handle Loop Exit

If `AUTO_FIX_SUCCESS != true` after the loop ends:

- In batch mode: write failure info to context file (`ci_auto_fix_failed`,
  `ci_failure` with name/url/type/attempts), set `ci_auto_fix_exit_reason`
  from `$AUTO_FIX_EXIT_REASON` (one of: `max-attempts`, `inherited-only`,
  `empty-fix`, `sham-commit`, `security`, `push-failed`), then exit 1.
- pr-merge uses `ci_auto_fix_exit_reason` to choose the surface message and
  PR label:
  - `inherited-only` → label `pipeline-failed-inherited`, comment names the
    failing tests and the most-recent merge to the base branch as the likely
    culprit. Does **not** ask the PR author to fix main from this branch.
  - `empty-fix` / `sham-commit` → label `pipeline-failed-no-diagnosis`,
    comment includes the last failure log and the regressions list — the
    pipeline ran out of useful fixes, not the user.
  - `max-attempts` → label `pipeline-failed`, existing behavior.
- Options the user has from the surface comment: view failure details, try
  more attempts, fix manually, force merge with admin.

#### Auto-Fix Success

On success, update context file with `ci_auto_fix_applied: true` and
`ci_auto_fix_attempts`, then proceed to the pre-merge safety net.
