

## System Context

**Product**: Nightgauge — AI-powered Issue-to-PR pipeline.

**Architecture**: Three-layer stack: portable Skills (SKILL.md), TypeScript SDK
(`packages/nightgauge-sdk/`), and VSCode Extension
(`packages/nightgauge-vscode/`). Six pipeline stages execute as isolated
subagents with JSON context handoff files.

**Execution model**: Each pipeline stage runs in a fresh conversation. Context
is passed exclusively through `.nightgauge/pipeline/*.json` files — never
through conversation history. Every stage reads its predecessor's context file
and writes its own.

**Configuration**: 6-tier config system — built-in defaults → global config →
project config → local config → env vars → CLI flags. Schema defined in
`packages/nightgauge-vscode/src/config/schema.ts`. Documentation in
`docs/CONFIGURATION.md`.

**Standards**: Code standards in `docs/CODE_STANDARDS.md`. Security rules in
`standards/security.md`. Testing patterns in `docs/TESTING.md`. Git workflow in
`docs/GIT_WORKFLOW.md`.

**Critical rules**: Never push to main. Never hardcode secrets. Never downgrade
versions. Pre-submission validation is mandatory.

## Autonomy Contract

This run is autonomous. No human is watching, and nobody can answer a
question mid-stage. `AskUserQuestion` is disabled — and _behaving_ as if it
were available is the same failure.

- **Proceed without asking** for any reversible action that follows from the
  stage's task. If a decision is genuinely undecidable from the issue, the
  context files, and the repo, fail fast with a clear error — never pause to
  ask, and never silently pick between materially different product
  directions.
- **Never end a turn on a promise.** Before ending your turn, check your last
  paragraph: if it is a plan, a question, a list of next steps, or a promise
  about work not yet done ("I'll now…", "Next, I will…"), do that work now
  with tool calls instead of describing it. A turn that ends on stated intent
  with no corresponding tool call is recorded as a `premature_turn_end` stage
  failure, not a success.
- **Do not stop because the session feels long.** End the turn only when the
  stage's output contract is satisfied (its context file and phase markers
  are written) or you are genuinely blocked — and a genuine block is reported
  as an explicit failure, never as an open question.
- **Never end a turn with background work outstanding.** There is no "await
  and resume" — your turn ending IS stage completion, and the pipeline
  advances immediately. If you started something in the background, block on
  it and read its result before you finish. "I'll wait for that to complete
  rather than poll, and pick the pipeline back up once it lands" is not a
  handoff; it is the stage reporting success on work it never saw (#202).
- **Work only inside the worktree you were given.** Never delegate the
  stage's implementation to a subagent running under worktree isolation, and
  never `cd` outside your workspace to edit files. Only your own worktree is
  read by later stages: on #202 a subagent wrote the entire fix into
  `.claude/worktrees/agent-<id>`, so the stage passed, the branch stayed
  empty, and the work was invisible to every stage after it. A gate now fails
  the stage when this happens — but the run is dead either way, so do the work
  where you stand.


# PR Merge

> Wait for reviews, address feedback, and merge pull requests

## Description

This skill completes the Issue-to-PR pipeline by:

1. Waiting for CI checks and reviews to complete
2. Fetching and parsing review feedback
3. Auto-addressing minor issues when possible
4. Presenting major/critical issues for user decision
5. Merging the PR when approved
6. Cleaning up branches and updating issue status

## Invocation

| Tool           | Command                             |
| -------------- | ----------------------------------- |
| Claude Code    | `/nightgauge-pr-merge` (via plugin) |
| OpenAI Codex   | `$nightgauge-pr-merge`              |
| GitHub Copilot | Invoke via Agent Skills             |
| Cursor         | Invoke via Agent Skills             |

## Arguments

```bash
# Merge current branch's PR (default behavior)
/nightgauge-pr-merge

# Specify PR number explicitly
/nightgauge-pr-merge --pr 57

# Set custom timeout for CI checks (default: 10 minutes)
/nightgauge-pr-merge --timeout 10

# Auto-fix minor issues without confirmation
/nightgauge-pr-merge --auto-fix

# Skip branch cleanup after merge
/nightgauge-pr-merge --no-cleanup

# Use different merge strategy
/nightgauge-pr-merge --merge    # Regular merge (preserve history)
/nightgauge-pr-merge --rebase   # Rebase and merge

# Skip CI check gate (NOT recommended - use only for emergencies)
/nightgauge-pr-merge --skip-ci-gate

# Disable auto-fix for CI failures (just report and exit on failure)
/nightgauge-pr-merge --no-auto-fix-ci

# Emergency only: bypass the blockedBy pre-merge guard
/nightgauge-pr-merge --force
```

## Prerequisites

- **GitHub CLI**: Must have `gh` installed and authenticated
- **Open PR**: Must have an open PR for the current branch
- **Feature branch**: Must be on a feature branch (not main)

## Philosophy

- **Complete automation** — Handle the entire review-to-merge workflow
- **Smart categorization** — Distinguish blocking vs non-blocking feedback
- **User control** — Present critical decisions, automate routine tasks
- **Clean state** — Leave repository in a clean, updated state after merge

## Spike Issues (`type:spike`)

For `type:spike` issues, the orchestrator appends a `spike-materialize` stage
after this skill completes. That stage parses the merged artifact's YAML
recommendations block and creates follow-up issues — it also updates the PR
description with a `## Created Follow-up Issues` section. This skill MUST NOT
attempt to populate that section itself; leave the placeholder from
`pr-create` intact for the materializer to replace. See
[docs/SPIKE_CONTRACT.md](../../docs/SPIKE_CONTRACT.md).

---

## Supporting files (load on demand)

- `<REPO_ROOT>/skills/nightgauge-pr-merge/_includes/context-bootstrap.md` — read in Phase 0 (stage start + context reconstruction)
- `<REPO_ROOT>/skills/nightgauge-pr-merge/_includes/validate-environment.md` — read in Phase 1 (verify branch, PR state, pre-CI Go build check)
- `<REPO_ROOT>/skills/nightgauge-pr-merge/_includes/reviews.md` — read in Phase 3 (fetch & parse review feedback, CI status)
- `<REPO_ROOT>/skills/nightgauge-pr-merge/_includes/merge.md` — read in Phase 6 (ruleset pre-check, conflict resolution, merge gate, execute merge)
- `<REPO_ROOT>/skills/nightgauge-pr-merge/_includes/post-merge.md` — read in Phase 7 (post-merge build, issue close, epic completion, branch cleanup, outcome recording)
- `<REPO_ROOT>/skills/nightgauge-pr-merge/_includes/feedback.md` — read in Phase 7.8 (retrospective feedback)
- `<REPO_ROOT>/skills/nightgauge-pr-merge/_includes/failure-cleanup.md` — read in Failure Cleanup (cleanup_failed_pr function + exit-point usage)

---

### Configuration Reference

Pipeline skills read configuration from `.nightgauge/config.yaml`.

**Full reference**: See `docs/CONFIGURATION.md` for the complete configuration
guide including the 6-tier precedence chain, all config options, and examples.

**Schema definition**: `packages/nightgauge-vscode/src/config/schema.ts`

**Key config sections by stage**:

| Stage            | Key Options                                           |
| ---------------- | ----------------------------------------------------- |
| issue-pickup     | `branch.base`, `branch.prefixes.*`, `project.*`       |
| feature-planning | `project.number`, `project.auto_dates`                |
| feature-dev      | `pipeline.auto_fix`, `commands.*`                     |
| feature-validate | `commands.test`, `pipeline.skip.tests`                |
| pr-create        | `pr.draft_by_default`, `pr.reviewers`, `validation.*` |
| pr-merge         | `pr.merge_strategy`, `pr.delete_branch`               |

#### Issue-Pickup Configuration

| Config Key                   | Default     | Description                       |
| ---------------------------- | ----------- | --------------------------------- |
| `branch.base`                | `main`      | Default base branch for PRs       |
| `branch.protected`           | `[main]`    | Branches that cannot be pushed to |
| `branch.prefixes.*`          | (see below) | Custom branch prefix mappings     |
| `project.number`             | -           | GitHub Project number             |
| `project.auto_dates`         | `false`     | Auto-populate Start/Target dates  |
| `project.sprint.auto_assign` | `false`     | Assign current sprint on pickup   |

**Branch Prefixes (defaults):**

| Type       | Default     |
| ---------- | ----------- |
| `feature`  | `feat/`     |
| `bugfix`   | `fix/`      |
| `docs`     | `docs/`     |
| `refactor` | `refactor/` |
| `chore`    | `chore/`    |
| `test`     | `test/`     |
| `hotfix`   | `hotfix/`   |

**Environment overrides:**

```bash
export NIGHTGAUGE_BRANCH_BASE=develop
export NIGHTGAUGE_BRANCH_PROTECTED=main,develop
export NIGHTGAUGE_PROJECT_AUTO_DATES=true
```

#### PR-Merge Configuration

| Config Key                 | Default  | Description                                             |
| -------------------------- | -------- | ------------------------------------------------------- |
| `pr.merge_strategy`        | `squash` | Merge strategy for sub-issue PRs: squash, merge, rebase |
| `pr.epic_merge_strategy`   | `merge`  | Merge strategy for epic→main PRs: merge, squash, rebase |
| `pr.delete_branch`         | `true`   | Delete feature branch after merge                       |
| `pr.auto_fix_ci`           | `true`   | Auto-fix CI failures before merge                       |
| `pr.auto_fix_max_attempts` | `3`      | Maximum auto-fix retry attempts                         |
| `pr.ci_check_timeout`      | `10`     | Timeout for CI checks in minutes                        |
| `project.number`           | -        | GitHub Project number for status sync                   |

**Environment overrides:**

```bash
export NIGHTGAUGE_PR_ADMIN_MERGE=true
export NIGHTGAUGE_PR_MERGE_STRATEGY=rebase
export NIGHTGAUGE_PR_DELETE_BRANCH=false
export NIGHTGAUGE_PR_AUTO_FIX_CI=true
export NIGHTGAUGE_PR_AUTO_FIX_MAX_ATTEMPTS=3
export NIGHTGAUGE_PR_CI_CHECK_TIMEOUT=10  # minutes (not seconds)
```

**Important notes:**

- There is no admin merge bypass — nothing skips branch protection, and CI
  checks are always waited for unless `--skip-ci-gate` is explicitly used
  (#186).
- If no CI checks are configured on the repository, the skill proceeds normally
  without waiting.

### Config Helper Functions

When skills need to read configuration values, use these inline patterns
rather than undefined helper functions:

#### Single value with default

```bash
VALUE=$(yq -r '.path.to.key // "default"' .nightgauge/config.yaml 2>/dev/null || echo "default")
```

#### Boolean with default

```bash
ENABLED=$(yq -r '.path.to.key // "true"' .nightgauge/config.yaml 2>/dev/null || echo "true")
```

#### With env var override (6-tier precedence)

```bash
# Env var takes priority, then config file, then default
VALUE="${ENV_VAR_OVERRIDE:-$(yq -r '.path.to.key // "default"' .nightgauge/config.yaml 2>/dev/null || echo "default")}"
```

**NOTE**: Do not use `get_config_bool` or `get_config_value` — these functions
are not defined. Use the inline `yq` patterns above.


---

## Input Contract

This skill requires `.nightgauge/pipeline/pr-{N}.json` from
`/nightgauge-pr-create`.

It also reads prior pipeline context for history and validation:

- `.nightgauge/pipeline/issue-{N}.json` (from issue-pickup)
- `.nightgauge/pipeline/planning-{N}.json` (from feature-planning)
- `.nightgauge/pipeline/dev-{N}.json` (from feature-dev)

**Full schema**: See
[docs/CONTEXT_ARCHITECTURE.md](../../docs/CONTEXT_ARCHITECTURE.md) for complete
schema documentation including all field types and requirements.

---

## Orchestration

This skill intentionally declares **no** `orchestration:` frontmatter block. PR
merge is a **single-agent deterministic phase** by design — it is never fanned
out (epic #3899,
[docs/WORKFLOW_ORCHESTRATION.md](../../docs/WORKFLOW_ORCHESTRATION.md) §Safety &
guardrails). The capability-routed `WorkflowEngine` runs it as one deterministic
phase node alongside the orchestrated stages.

## Gotchas

- **Never blindly accept one side of a merge conflict.** Understand both sides
  before resolving — a reflexive "accept theirs/ours" silently drops work.
- **Don't `--watch` CI from a forge loop.** Use `nightgauge ci wait` — an
  interactive `--watch` mode can hang the headless run.
- **Clean up on failure (prevents stale PRs).** A failed merge attempt must leave
  no half-open/abandoned PR state behind.
- **No follow-up issues in the merge description.** Don't bake created follow-up
  issues into the PR/merge body.
- See also the cross-cutting gotchas in
  [`_shared/GOTCHAS.md`](../_shared/GOTCHAS.md).

## Workflow

### Phase Marker Protocol

At the start of each phase, emit a structured phase marker as an HTML comment on
its own line. Format:

`<!-- phase:start name="{phase-name}" index={N} total={T} stage="pr-merge" -->`

This enables the orchestrator to track phase progress. Emit the marker BEFORE
any other output for that phase.

**IMPORTANT**: ALL phase markers MUST be emitted even in fast-track paths (e.g.,
no review feedback, CI already passed). The orchestrator counts emitted markers
to display progress (e.g., "11/11 phases"). Skipping markers causes incorrect
counts. If a phase has no work to do, still emit its marker and immediately
proceed to the next phase.

### Phase 0: Read PR Context

### Phase 0: Environment Preflight

**PURPOSE**: Verify the nightgauge binary is available and the environment
is healthy before executing any pipeline stage. Skills halt immediately with
actionable instructions when this check fails.

Two-layer check:

1. **Binary discovery** — shell-level, runs before invoking the binary
2. **Doctor check** — binary-level, full environment health via `doctor --json`

The binary discovery cascade below is **provider-neutral** (#4029): it works
identically under every adapter (Claude, Codex, Gemini, …) and references no
VSCode-extension-specific path. The host that spawns the skill (the VSCode
extension's `skillRunner`, or the Go `nightgauge` binary in auto/CLI mode)
resolves the binary authoritatively and exports `NIGHTGAUGE_BIN`, which
this cascade honors first. The remaining steps (PATH, repo `bin/`, the
worktree's canonical-repo `bin/`, `~/go/bin`) are fallbacks for direct or
terminal invocation.

This intentionally **diverges** from
`claude-plugins/nightgauge/hooks/lib/guard.sh` (#3262 → #4029): guard.sh is
Claude-Code-only and retains a trailing `~/.vscode/extensions/...` glob to serve
the standalone-terminal-Claude case (where no host exports `NIGHTGAUGE_BIN`).
Skills must stay portable Markdown and cannot reference that VSCode-only path —
so the shared resolution order (`NIGHTGAUGE_BIN` → PATH → repo → canonical
→ `~/go/bin`) stays in sync, and only guard.sh carries the extra Claude-only
fallback. The `nightgauge preflight skill-portability` gate enforces that
no skill reintroduces a `.vscode/extensions` path.

After the cascade resolves `$BINARY`, the preflight prepends
`dirname($BINARY)` to `PATH` so subsequent bare `nightgauge ...`
invocations later in the same skill body resolve through PATH rather than
failing with "command not found" (#3262).

```bash
# Layer 1: provider-neutral binary discovery (NIGHTGAUGE_BIN → PATH → repo
# bin → canonical-repo bin → ~/go/bin). No VSCode-extension path — see #4029.
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
if [ -z "$BINARY" ]; then
  echo "ERROR: nightgauge binary not found via NIGHTGAUGE_BIN, PATH, bin/nightgauge, canonical-repo bin, or ~/go/bin" >&2
  echo "" >&2
  echo "Install via: go install github.com/nightgauge/nightgauge/cmd/nightgauge@latest" >&2
  echo "Or download from: https://github.com/nightgauge/nightgauge/releases" >&2
  echo "Run \`nightgauge doctor\` after installing to verify your environment." >&2
  exit 1
fi
# Make the resolved binary callable via bare `nightgauge ...` later in this skill body (#3262).
export PATH="$(dirname "$BINARY"):$PATH"

# Per-repo GitHub token: export GH_TOKEN so every `gh` call in this skill
# authenticates as the *current repo's* configured user instead of the machine-
# global `gh auth` active account. `forge auth token` resolves per-repo (config
# github_user/token → GITHUB_TOKEN → `gh auth token --user <github_user>`), so
# concurrent sessions/workspaces owned by different GitHub users each use their
# own token. GH_TOKEN outranks GITHUB_TOKEN and the gh keyring. Only resolve when
# unset (an upstream env value — e.g. the VSCode extension's terminal env —
# wins); never fail preflight over it. Mirrors hooks/lib/guard.sh (sync, #3262).
if [ -z "${GH_TOKEN:-}" ]; then
  IB_REPO_TOKEN="$("$BINARY" forge auth token 2>/dev/null || true)"
  if [ -n "$IB_REPO_TOKEN" ]; then
    export GH_TOKEN="$IB_REPO_TOKEN"
    export GITHUB_TOKEN="${GITHUB_TOKEN:-$IB_REPO_TOKEN}"
  fi
  unset IB_REPO_TOKEN
fi

# Layer 2: full environment health check via doctor --json
DOCTOR_RESULT=$("$BINARY" doctor --json 2>/dev/null)
DOCTOR_EXIT=$?
if [ "$DOCTOR_EXIT" -eq 2 ]; then
  echo "ERROR: Environment check failed — nightgauge doctor reports broken environment." >&2
  FAILED_CHECKS=$(echo "$DOCTOR_RESULT" | jq -r '.failed_checks[]? // empty' 2>/dev/null)
  if [ -n "$FAILED_CHECKS" ]; then
    echo "Failing check(s): $(echo "$FAILED_CHECKS" | tr '\n' ',' | sed 's/,$//')" >&2
  fi
  echo "$DOCTOR_RESULT" | jq -r '.errors[]' >&2 2>/dev/null || true
  INSTALL_MSG=$(echo "$DOCTOR_RESULT" | jq -r '.install_instructions // empty' 2>/dev/null)
  [ -n "$INSTALL_MSG" ] && echo "$INSTALL_MSG" >&2
  exit 1
fi
if [ "$DOCTOR_EXIT" -eq 1 ]; then
  echo "WARNING: Environment has non-critical issues:" >&2
  echo "$DOCTOR_RESULT" | jq -r '.warnings[]' >&2 2>/dev/null || true
  # Continue — warnings do not block skill execution
fi
```

**Exit codes from `nightgauge doctor`**:

| Code | Meaning                         | Skill behavior                          |
| ---- | ------------------------------- | --------------------------------------- |
| 0    | Healthy                         | Continue                                |
| 1    | Degraded (warnings only)        | Continue with warning printed to stderr |
| 2    | Broken (required checks failed) | Halt immediately with error details     |

Do not theorize about the cause beyond what is printed here — the failing
check name (`failed_checks`) and its `.errors[]` message are the complete
diagnosis (#277).


### Repo Identity Assertion (HARD GATE — non-recoverable)

If the `NIGHTGAUGE_TARGET_REPO` environment variable is set, the orchestrator
has pinned the exact repo this stage must run in. Verify the current repository
matches **before doing any work**:

```bash
if [ -n "$NIGHTGAUGE_TARGET_REPO" ]; then
  ACTUAL_REPO=$(gh repo view --json nameWithOwner -q .nameWithOwner 2>/dev/null)
  if [ -n "$ACTUAL_REPO" ] && [ "$ACTUAL_REPO" != "$NIGHTGAUGE_TARGET_REPO" ]; then
    echo "[repo-mismatch] FATAL: repository identity assertion failed"
    echo "  Expected: $NIGHTGAUGE_TARGET_REPO"
    echo "  Actual:   $ACTUAL_REPO"
    echo "  CWD:      $(pwd)"
    echo ""
    echo "The orchestrator expected this stage to run in $NIGHTGAUGE_TARGET_REPO"
    echo "but the working directory belongs to $ACTUAL_REPO."
    exit 1
  fi
fi
```

**This is a terminal, non-recoverable stop condition. If the check prints
`[repo-mismatch]` and exits 1, you MUST immediately end the stage with a failure.**

Specifically, on a mismatch you must NOT:

- `cd` into a different directory, switch worktrees, or `gh repo set-default` to
  "make it match" — the orchestrator owns CWD; second-guessing it corrupts state.
- Continue to later phases, create or push a branch, or open a PR — every
  downstream artifact would land in the wrong repo.
- Ask the user a question (`AskUserQuestion`) — autonomous runs have no human at
  the prompt; the question is auto-dismissed and you would proceed on a guess.
- Write a context/assessment file that claims success.

The correct and ONLY action is to stop now and let the stage exit non-zero. This
is a configuration fault in repo routing (the orchestrator set the wrong
`NIGHTGAUGE_TARGET_REPO` for this worktree); it is fixed upstream in the
orchestrator, never worked around inside the stage. The `[repo-mismatch]` marker
is how the pipeline classifies and surfaces it.

This check is a no-op — and the stage proceeds normally — when
`NIGHTGAUGE_TARGET_REPO` is unset (e.g. manual single-stage CLI
invocations) or already matches the current repo.


---

```bash
printf '<!-- phase:start name="read-pr-context" index=0 total=14 stage="pr-merge" -->\n'
```

Read PR context: resolve the issue number from the branch, load
`.nightgauge/pipeline/pr-{N}.json`, signal stage start, and reconstruct the
context file from GitHub if it is missing.

> **Read `<REPO_ROOT>/skills/nightgauge-pr-merge/_includes/context-bootstrap.md` now and follow its instructions before continuing this phase.**

---

### Phase 0.5: Batch PR Detection

```bash
printf '<!-- phase:start name="batch-detection" index=1 total=14 stage="pr-merge" -->\n'
```

<!-- include: ../_shared/BATCH_MODE.md -->

---

### Phase 1: Validate Environment

```bash
printf '<!-- phase:start name="validate-environment" index=2 total=14 stage="pr-merge" -->\n'
```

Verify the feature branch (handling detached HEAD), resolve the PR number and
state, extract the issue number, and run the pre-CI Go build integrity check.

> **Read `<REPO_ROOT>/skills/nightgauge-pr-merge/_includes/validate-environment.md` now and follow its instructions before continuing this phase.**

---

### Phase 2: Wait for CI Checks (CI Gate)

```bash
printf '<!-- phase:start name="ci-gate" index=3 total=14 stage="pr-merge" -->\n'
```

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


---

### Phase 2.5: Auto-Fix Retry Loop

```bash
printf '<!-- phase:start name="auto-fix-retry" index=4 total=14 stage="pr-merge" -->\n'
```

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
3. Stash any uncommitted changes. The message shape is a contract (#330) —
   only a stash carrying the `nightgauge:` marker can be reclaimed by
   `nightgauge stash sweep` or reported by `nightgauge doctor`, and an
   unmarked one is indistinguishable from the operator's own, so no tool will
   ever touch it:
   `git stash push -u -m "nightgauge:baseline:${PR}:auto-fix" 2>/dev/null`.
4. `git checkout "$MERGE_BASE"` (detached HEAD is fine).
5. Run the same failing tests/builds on the base. Capture pass/fail per item.
6. `git checkout -` and `git stash pop 2>/dev/null` to return to the PR
   branch. If the stage is killed before this line the stash survives; the
   marker in step 3 is what lets a later `nightgauge stash sweep` reclaim it,
   since a SIGKILL runs no cleanup here at all.
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

Re-invoke the Go binary `nightgauge ci wait` directly — it blocks internally
until CI reports a result, so no separate wait step is needed. **NEVER** add a
foreground `sleep` before it and **NEVER** substitute your own polling loop
using `gh pr checks`; the harness denies foreground `sleep` outright and the
Go binary already handles polling correctly (#289, see CI_GATE.md).

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


---

### Phase 3: Fetch & Parse Review Feedback

```bash
printf '<!-- phase:start name="fetch-reviews" index=5 total=14 stage="pr-merge" -->\n'
```

Fetch PR details and reviews, wait for CI status, fetch inline review comments
and review summaries, and parse both automated and human reviews.

> **Read `<REPO_ROOT>/skills/nightgauge-pr-merge/_includes/reviews.md` now and follow its instructions before continuing this phase.**

---

### Phase 4: Categorize Issues

```bash
printf '<!-- phase:start name="categorize-issues" index=6 total=14 stage="pr-merge" -->\n'
```

#### Step 4.1: Define Categories

| Category     | Keywords                                                        | Action                   |
| ------------ | --------------------------------------------------------------- | ------------------------ |
| **Critical** | blocking, must fix, security, REQUIRED                          | Must fix before merge    |
| **Major**    | should fix, important, recommended, please fix                  | Should fix, user decides |
| **Minor**    | suggestion, nit, consider, non-blocking, optional, low priority | Can merge as-is          |

#### Step 4.2: Determine Merge Readiness

```bash
REVIEW_DECISION=$("$BINARY" pr view "$PR_NUMBER" --json 2>/dev/null | jq -r '.reviewStatus // "REVIEW_REQUIRED"')
```

Ready if `APPROVED` and no critical issues. Not ready if `REVIEW_REQUIRED` or
`CHANGES_REQUESTED`. Otherwise ready (no review required).

---

### Phase 5: Address Feedback

```bash
printf '<!-- phase:start name="address-feedback" index=7 total=14 stage="pr-merge" -->\n'
```

#### Step 5.0: Check for Fast-Track Merge

Fast-track conditions (all must be true): `READY_TO_MERGE`, no critical/major
issues, CI passed, PR mergeable.

If `FAST_TRACK=true`, skip Steps 5.1-5.4 and proceed to Phase 6.

#### Step 5.1: Handle Critical Issues

If critical issues exist, they MUST be addressed.

#### Step 5.2: Handle Major Issues

Major issues should typically be fixed but user can override.

#### Step 5.3: Handle Minor Issues

Minor issues are non-blocking. If `--auto-fix` flag is set, skip prompts.

#### Step 5.4: Apply Fixes

Make changes, stage, commit with `fix(#$ISSUE_NUMBER): address review feedback`,
push, and wait for CI to re-run.

---

### Phase 5.5: Proactive Freshness Check

```bash
printf '<!-- phase:start name="freshness-check" index=8 total=14 stage="pr-merge" -->\n'
```

**PURPOSE**: Proactively rebase the feature branch onto the latest base branch
BEFORE attempting merge. This prevents the reactive conflict resolution in Phase
6 from ever being needed in most cases. Critical for epic batch processing where
concurrent sub-issues merge and shift main/epic branch forward.

### Base Branch Freshness Check

**PURPOSE**: Ensure the feature branch is up-to-date with the latest base branch
before proceeding. This prevents merge conflicts and build failures caused by
concurrent epic sub-issues or other PRs merging into the base branch while this
branch was being worked on.

**WHEN**: Run this check before any build/test validation and before merge
attempts. It is especially critical during epic batch processing where multiple
sub-issues merge concurrently.

```bash
# Determine base branch (epic branch or main)
FRESHNESS_BASE="${BASE_BRANCH:-main}"
echo "Checking freshness against $FRESHNESS_BASE..."

# Fetch latest state of the base branch
git fetch origin "$FRESHNESS_BASE" 2>/dev/null

# Count commits on base that are NOT in our branch
BEHIND_COUNT=$(git rev-list --count "HEAD..origin/$FRESHNESS_BASE" 2>/dev/null || echo "0")

if [ "$BEHIND_COUNT" -gt 0 ]; then
  echo "Branch is $BEHIND_COUNT commit(s) behind origin/$FRESHNESS_BASE. Rebasing..."

  # Store current branch name
  CURRENT_BRANCH=$(git branch --show-current)

  # Attempt rebase
  if git rebase "origin/$FRESHNESS_BASE" 2>/dev/null; then
    echo "Rebase successful. Branch is now up-to-date with $FRESHNESS_BASE."

    # Force-push the rebased branch (with lease for safety)
    if ! git push --force-with-lease origin "$CURRENT_BRANCH" 2>/dev/null; then
      echo "WARNING: Failed to push rebased branch. Continuing with local rebase."
    fi
  else
    # Rebase failed — check if conflicts are resolvable
    CONFLICT_FILES=$(git diff --name-only --diff-filter=U 2>/dev/null)

    if [ -n "$CONFLICT_FILES" ]; then
      echo "Rebase conflicts detected in: $CONFLICT_FILES"
      echo "Attempting AI-assisted conflict resolution..."

      # For each conflicted file:
      # 1. Read the file with conflict markers
      # 2. Understand BOTH sides (ours = feature work, theirs = base updates)
      # 3. Produce a logically correct merge preserving BOTH changes
      # 4. Stage the resolved file
      #
      # CRITICAL RULES:
      # - NEVER blindly accept one side
      # - If resolution is ambiguous, abort and fail with clear error
      # - After resolution, code MUST compile

      for FILE in $CONFLICT_FILES; do
        echo "Resolving: $FILE"
        # ... AI resolves the conflict ...
        git add "$FILE"
      done

      if git rebase --continue 2>/dev/null; then
        echo "Conflict resolution successful."
        git push --force-with-lease origin "$CURRENT_BRANCH" 2>/dev/null || true
      else
        echo "ERROR: Rebase --continue failed after conflict resolution."
        git rebase --abort 2>/dev/null || true
        echo "FRESHNESS_CHECK_FAILED=true"
        echo "Manual conflict resolution required. Base branch has diverged significantly."
        # Do NOT exit — let the calling phase decide how to handle
        FRESHNESS_CHECK_FAILED=true
      fi
    else
      git rebase --abort 2>/dev/null || true
      echo "ERROR: Rebase failed with no conflict markers. Unexpected state."
      FRESHNESS_CHECK_FAILED=true
    fi
  fi
else
  echo "Branch is up-to-date with origin/$FRESHNESS_BASE."
fi
```

**Output variables**:

- `BEHIND_COUNT` — how many commits the branch was behind (0 = already fresh)
- `FRESHNESS_CHECK_FAILED` — set to `true` if rebase + conflict resolution
  failed. The calling phase should decide whether to abort or continue.

**Safety**: Uses `--force-with-lease` (not `--force`) to prevent overwriting
concurrent pushes. If the push fails, the local rebase is still valid for
build/test validation.


If `FRESHNESS_CHECK_FAILED=true`, proceed to Phase 6 anyway — the reactive
conflict resolution (Step 6.1.5) may still succeed with a different strategy.
If the rebase succeeded and pushed, CI will re-run. Wait for CI before merge:

```bash
if [ "$BEHIND_COUNT" -gt 0 ] && [ "$FRESHNESS_CHECK_FAILED" != "true" ]; then
  echo "Branch was rebased. Waiting for CI to pass on rebased commits..."

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

  if [ -n "$BINARY" ] && [ "$CI_EPIC_SKIP" != "true" ]; then
    # ONE bounded 90s chunk per Bash call (#187) — a 10-minute wait is
    # SIGTERMed by the tool budget. Exit 2 = still pending: re-run this
    # block in a NEW Bash call while the freshness budget
    # (NIGHTGAUGE_PR_CI_CHECK_TIMEOUT minutes, default 10) remains.
    CI_RESULT=$("$BINARY" ci wait "$PR_NUMBER" --timeout-secs 90 --json 2>/dev/null) || true
    # Re-check state: PR may have been merged out-of-band during CI wait.
    REBASE_POST_STATE=$("$BINARY" pr view "$PR_NUMBER" --json 2>/dev/null | jq -r '.state // "UNKNOWN"')
    if [ "$REBASE_POST_STATE" = "MERGED" ]; then
      echo "PR #$PR_NUMBER was merged (detected after rebase CI wait). Exiting cleanly."
      exit 0
    fi
    CI_ALL_PASSED=$(echo "$CI_RESULT" | jq -r 'if .state == "SUCCESS" then "true" else "false" end')
    if [ "$CI_ALL_PASSED" != "true" ]; then
      echo "WARNING: CI checks failed after rebase. Proceeding to merge phase for auto-fix."
    fi
  fi
fi
```

---

### Phase 6: Merge

```bash
printf '<!-- phase:start name="merge" index=9 total=14 stage="pr-merge" -->\n'
```

Run the ruleset pre-check, final mergeable verification, conflict resolution,
merge-strategy selection, the deterministic Go-binary merge (with its
`blockedBy` gate), and merge verification.

> **NEVER pass `--admin` (or `--auto`) to any merge command — no admin bypass
> exists in this pipeline.** A merge blocked by branch protection or required
> checks is TERMINAL for this stage: report the blocker and escalate; do not
> improvise an admin-bypass merge via raw `gh` (incident: #186). A PreToolUse
> hook blocks these flags during pipeline sessions.

> **Read `<REPO_ROOT>/skills/nightgauge-pr-merge/_includes/merge.md` now and follow its instructions before continuing this phase.**

---

### Phase 7: Post-Merge Verification & Cleanup

```bash
printf '<!-- phase:start name="post-merge-cleanup" index=10 total=14 stage="pr-merge" -->\n'
```

Verify the post-merge build, close the issue and sync the board deterministically,
fire the post-merge hook, check epic completion, delete the feature branch, and
record the outcome to the complexity model.

> **Read `<REPO_ROOT>/skills/nightgauge-pr-merge/_includes/post-merge.md` now and follow its instructions before continuing this phase.**

---

### Phase 7.8: Retrospective Feedback

```bash
printf '<!-- phase:start name="retrospective-feedback" index=11 total=14 stage="pr-merge" -->\n'
```

Capture non-blocking post-merge workflow feedback (interactive only; skipped in
headless mode) and persist it to the context file.

> **Read `<REPO_ROOT>/skills/nightgauge-pr-merge/_includes/feedback.md` now and follow its instructions before continuing this phase.**

---

### Phase 8: Output Summary

```bash
printf '<!-- phase:start name="output-summary" index=12 total=14 stage="pr-merge" -->\n'
```

```
PR:       #57
Title:    feat(#26): add parallel PR context gathering
Merged:   via squash
Branch:   feat/26-parallel-pr-context (deleted)

Status Updates:
- Issue #26: Closed
- Project board: Done (via GitHub built-in workflow)

Summary:
- CI checks: All passed
- Post-merge build: Verified OK
- Reviews: Approved (9/10 quality score)
- Merge: Squash merged to main

Next Steps:
You're now on the main branch with all changes merged.
Ready for the next issue: /nightgauge-issue-pickup
```

#### Step 8.1: Signal Stage Complete

```bash
# Go binary: project move-status
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
if [ -n "$BINARY" ]; then
  "$BINARY" project move-status "$ISSUE_NUMBER" "done" 2>/dev/null || true
fi
```

---

### Phase 9: Self-Assessment Epilogue

```bash
printf '<!-- phase:start name="self-assessment" index=13 total=14 stage="pr-merge" -->\n'
```

### Self-Assessment Epilogue

**PURPOSE**: Evaluate whether this skill's instructions matched reality during
this execution. This phase is **non-blocking** — skip entirely if any main phase
failed. A perfectly working skill produces **no output** from this phase.

> See [docs/SKILL_SELF_ASSESSMENT.md](../../docs/SKILL_SELF_ASSESSMENT.md) for
> the full strategy, synthesis algorithm, and integration architecture.

#### When to Skip

- Any prior phase exited with an error before completing
- The skill was cancelled or timed out
- Running in `--dry-run` mode

#### Step 1: Evaluate Execution Friction

Review the execution that just completed. Answer these questions honestly:

1. **Command failures**: Did any command, script, or binary call in the skill
   instructions fail? (e.g., script not found, wrong flags, missing binary)
2. **Workarounds**: Did you have to deviate from the skill instructions to
   accomplish the goal? (e.g., used `gh` directly because a referenced script
   didn't exist, skipped a step that referenced a nonexistent file)
3. **Stale references**: Did any file path, function name, API endpoint, or tool
   referenced in the instructions not exist in the current codebase?
4. **Unclear instructions**: Were any instructions ambiguous enough that you had
   to guess at the intended behavior?
5. **Missing instructions**: Was there a significant step you had to figure out
   on your own that should have been documented in this skill?

**If ALL answers are "no" — write nothing and complete the skill normally.** The
goal is silence when everything works.

#### Step 2: Write Assessment Record

Only if friction was detected in Step 1. Write a single JSON file:

```bash
ASSESSMENT_DIR=".nightgauge/pipeline/assessments"
mkdir -p "$ASSESSMENT_DIR"
```

**File**: `$ASSESSMENT_DIR/{STAGE_NAME}-${ISSUE_NUMBER}.json`

The assessment record MUST follow this schema:

```json
{
  "schema_version": "1",
  "skill": "{STAGE_NAME}",
  "skill_file": "skills/nightgauge-{STAGE_NAME}/SKILL.md",
  "issue_number": 42,
  "timestamp": "2026-03-10T14:30:00Z",
  "friction": [
    {
      "type": "command_failure",
      "severity": "high",
      "description": "hooks/lib/add-to-project.sh not found — script was deleted",
      "skill_line_hint": "claude-plugins/nightgauge/hooks/lib/add-to-project.sh <issue-number>",
      "actual_resolution": "Used gh api graphql to add issue to project board directly",
      "suggested_fix": "Replace with: nightgauge project add <issue-number>"
    }
  ]
}
```

**Friction types**: `command_failure`, `workaround`, `stale_reference`,
`unclear_instruction`, `missing_instruction`

**Severity levels**:

- `high` — instruction is **broken**. Required manual workaround to complete.
- `medium` — instruction is **misleading**. Agent adapted without user help.
- `low` — instruction is **suboptimal**. No functional impact.

**Rules**:

- **One record per execution** — multiple friction items go in the `friction`
  array, not separate files.
- **Be specific** — quote the exact instruction text that was wrong. Not "some
  commands didn't work" but "Step 5.2 calls `hooks/lib/add-to-project.sh` but
  this script was deleted in commit 65915701."
- **Suggest the fix** — every finding MUST include `suggested_fix` with the
  concrete SKILL.md change needed. Not "update the docs" but "replace
  `hooks/lib/add-to-project.sh <N>` with `nightgauge project add <N>`."
- **Don't invent friction** — only report issues you actually encountered during
  this execution. Do not speculate about potential problems.

#### Step 3: Validate and Complete

```bash
# Validate JSON if written
if [ -f "$ASSESSMENT_FILE" ]; then
  python3 -m json.tool "$ASSESSMENT_FILE" > /dev/null 2>&1 || \
    echo "WARNING: Assessment record is not valid JSON" >&2
fi
```


---

## Failure Cleanup (CRITICAL — Prevents Stale PRs)

**EVERY `exit 1` in this skill MUST go through this cleanup function first.**
Without this, failed pipeline runs leave orphaned PRs that nobody notices until
they pile up.

When the skill is about to exit with a non-zero code AND a PR number is known,
define and invoke the `cleanup_failed_pr` function. Replace ALL bare `exit 1`
calls in the phases above with the `cleanup_failed_pr` + `exit 1` pattern.

> **Read `<REPO_ROOT>/skills/nightgauge-pr-merge/_includes/failure-cleanup.md` now and follow its instructions before relying on this section.**

---

## Error Handling

| Condition         | Action                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                         |
| ----------------- | ---------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| No open PR        | Exit with error: "No open PR found for branch." Suggest `/nightgauge-pr-create`.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                               |
| Already merged    | Exit 0: "PR has already been merged."                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                          |
| CI failures       | Auto-fix up to 3 attempts. On exhaustion: label PR `pipeline-failed`, comment with details, move issue to Ready, exit 1.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                       |
| Changes requested | Report reviewer feedback. On critical unresolved: label PR `pipeline-failed`, comment, exit 1.                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                                 |
| Merge conflicts   | Attempt automatic rebase + AI conflict resolution (Step 6.1.5). If unresolvable: capture `conflict-context-{N}.json` (files + both sides) **before** `git rebase --abort`, emit a `CONFLICT_RESOLUTION_NEEDED` feedback signal targeting feature-dev, **keep the branch**, exit 1. The recovery loop re-dispatches feature-dev on the same branch to resolve — bounded by `pipeline.recovery.conflict_recovery.max_dev_redispatch`, then escalates with the specific files. (No fresh-branch restart / `conflict-restart-{N}.json`.)                                                                                                           |
| Branch protection | **Non-retryable.** Include the raw merge error (e.g. `Required status check "X" is expected`) in the failure output so the Go classifier records `ruleset-blocked` and the orchestrator skips the retry (#185). Write the structured `blocker` record into `pr-{N}.json` (`{classification, remediation, non_retryable: true}` — see context-bootstrap.md) so the orchestrator surfaces the blocked terminal state (#190). Label PR `pipeline-failed`, comment with the blocker + remediation from Step 6.0's precheck (`config_mismatches[].remediation` when present), exit 1. Never re-attempt the merge or re-run failing required checks. |
