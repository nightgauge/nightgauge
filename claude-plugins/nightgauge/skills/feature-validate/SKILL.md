---
name: feature-validate
description: Validate feature implementation with integration/E2E tests and manual
  checklists. Use after /feature-dev to ensure the feature works end-to-end
  before creating PR.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.25.0"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Read Write Edit Glob Grep Bash Task
orchestration:
  mode: pipeline
  phase: validate
  ceiling: fanout
  units:
    - id: build
      role: stage
      promptRef: _includes/build-verification.md
    - id: tests
      role: stage
      promptRef: _includes/run-tests.md
    - id: ci-parity
      role: stage
      promptRef: _includes/ci-and-knowledge.md
  judge:
    mode: gate
    gate: true
    quorum: 1
    promptRef: _includes/ci-and-knowledge.md
programmatic-tools: run_build run_lint run_tests run_typecheck
context: fork
agent: test-runner
model: sonnet
# Completion verification is a Go StageGate (FeatureDevGate /
# FeatureValidateGate — internal/orchestrator/gates), NOT a Claude-only
# `hooks: Stop:` block: hooks silently never fired on non-Claude adapters
# (spike #33 D2, #55). Do not reintroduce hooks here — the portability
# linter rejects them.
inputs:
  - pipeline/dev-{N}.json
outputs:
  - pipeline/validate-{N}.json
disable-model-invocation: true
---

## System Context

**Product**: Nightgauge — AI-powered Issue-to-PR pipeline.

**Architecture**: Three-layer stack: portable Skills (SKILL.md), a TypeScript
SDK, and a VSCode Extension, over a Go deterministic layer. Six pipeline
stages execute as isolated subagents with JSON context handoff files.

**Execution model**: Each pipeline stage runs in a fresh conversation. Context
is passed exclusively through JSON files in the clone's pipeline state
directory (`nightgauge layout path pipeline`) — never through conversation
history. Every stage reads its predecessor's context file
and writes its own.

**Configuration**: 6-tier config system — built-in defaults → global config →
project config → local config → env vars → CLI flags. Reference:
[docs/CONFIGURATION.md](https://github.com/nightgauge/nightgauge/blob/main/docs/CONFIGURATION.md).

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


# Feature Validation

Trusts the dev context handoff (no build/unit-test/security re-runs that already passed), runs integration/E2E tests with Ralph Loop self-healing (up to 3 auto-fix attempts, [docs/RALPH_LOOP.md](https://github.com/nightgauge/nightgauge/blob/main/docs/RALPH_LOOP.md)), excludes pre-existing failures via baseline comparison, and writes validation context for `/nightgauge-pr-create`.

**Invoke**: `/nightgauge:feature-validate` (Claude Code plugin), `$nightgauge-feature-validate` (Codex), or via Agent Skills (Copilot/Cursor). **Requires**: `dev-{N}.json` (in the clone's pipeline state directory, `nightgauge layout path pipeline`) from `/nightgauge-feature-dev`, on the feature branch from `/nightgauge-issue-pickup` (schema: [docs/CONTEXT_ARCHITECTURE.md](https://github.com/nightgauge/nightgauge/blob/main/docs/CONTEXT_ARCHITECTURE.md)). **Config**: `.nightgauge/config.yaml` ([docs/CONFIGURATION.md](https://github.com/nightgauge/nightgauge/blob/main/docs/CONFIGURATION.md)); per-key defaults and env overrides in `_includes/configuration.md` (read when needed).

## Arguments

| Flag               | Description                                    |
| ------------------ | ---------------------------------------------- |
| (none)             | Run all checks (default)                       |
| `--skip-manual`    | Skip manual testing prompts (low-risk changes) |
| `--e2e-only`       | Only run E2E tests, skip manual checklist      |
| `--checklist-only` | Generate checklist without running tests       |
| `--auto-pass`      | Auto-pass all checklist items (CI/automated)   |

## Exit Contract — Read This First

**This stage is NOT complete until `validate-{N}.json` exists in the clone's pipeline state directory (written with `nightgauge layout write pipeline validate-{N}.json`).** On any error, budget exhaustion, or bail-out, STILL execute Phase 6 and write it (`validation_status: "failed"` plus the matching `errorCategory` — enum: `build-failed`, `tests-failed`, `gate-timeout`, `integration-failed`, `dead-code-blocked`, `mobile-apk-build-failed`, `mobile-mcp-tests-failed`, `verify-ui-gate-failed`, `review-cap-reached`). Exiting without it triggers a repo-blind orchestrator fallback that may misreport tests. The very last act before signaling completion MUST be:

```bash
ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
: "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
test -s "$(nightgauge layout path pipeline validate-${ISSUE_NUMBER}.json)" || \
  { echo "ERROR: validate-${ISSUE_NUMBER}.json missing — Phase 6 was skipped" >&2; exit 1; }
```

## Spike Issues (`type:spike`)

For `type:spike` issues, run `nightgauge spike materialize "$ISSUE_NUMBER" --dry-run`. Non-zero exit = the `docs/spikes/<N>-*.md` artifact is missing or its recommendations block fails schema validation — a **blocking** validation failure ([docs/SPIKE_CONTRACT.md](https://github.com/nightgauge/nightgauge/blob/main/docs/SPIKE_CONTRACT.md)).

## Supporting files (load on demand)

- `_includes/configuration.md` (same directory as this SKILL.md) — config key defaults and environment overrides (read when a config default is needed)
- `_includes/context-load.md` (same directory as this SKILL.md) — read in Phase 0 (read dev context), Phase 0.5 (batch detection), Phase 0.6 (AC ac-check steps)
- `_includes/test-setup.md` (same directory as this SKILL.md) — read in Phase 1 (detect testing environment) and Phase 1.8 (PTC detection)
- `_includes/build-verification.md` (same directory as this SKILL.md) — read in Phase 1.5 (build verification)
- `_includes/dead-code.md` (same directory as this SKILL.md) — read in Phase 1.6 (dead code)
- `_includes/baseline-comparison.md` (same directory as this SKILL.md) — read in Phase 1.7 (baseline comparison)
- `_includes/run-tests.md` (same directory as this SKILL.md) — read in Phase 2 (run tests) and Step 2.5 (evidence-of-execution gate)
- `_includes/mobile-mcp-tests.md` (same directory as this SKILL.md) — read in Phase 2.4 (mobile MCP E2E)
- `_includes/verify-ui-gate.md` (same directory as this SKILL.md) — read in Phase 2.45 (web UI verification gate)
- `_includes/ci-and-knowledge.md` (same directory as this SKILL.md) — read in Phase 2.5 (CI parity), Phase 2.6 (knowledge coverage), Phase 2.7 (pre-push gate)
- `_includes/feedback-and-commit.md` (same directory as this SKILL.md) — read in Phase 3 (checklist), Phase 4 (feedback signals), Phase 4.9 (compute status), Phase 5 (commit and push)
- `_includes/context-and-board.md` (same directory as this SKILL.md) — read in Phase 6 (write validate context) and Phase 7 (sync board)

## Orchestration

The `orchestration:` frontmatter models validation as an ordered pipeline — build (1.5) → tests (2) → CI-parity (2.5) — closed by the independent review (Step 2.7.3), whose verdict is the evidence the Go `FeatureValidateGate.Verify()` loop consumes (epic #3899): a failed verdict fails validation. A `catch` fails the run; a later `pass` on the fixed tree supersedes it, and the gate judges the latest verdict per `gate_name` in file order, except `judges` rows, where any failure in the latest attempt fails. Gate-metric records are part of that evidence — the verify-ui gate (2.45) and the independent review write their rows per their includes, and `gate_metrics[]` flows into the validate context (Phase 6); FeatureValidateGate treats zero records as a no-op failure. Each unit's `promptRef` is the SAME `_includes/*.md` the phases below read; the prose Workflow remains the single-agent portability floor ([docs/WORKFLOW_ORCHESTRATION.md](https://github.com/nightgauge/nightgauge/blob/main/docs/WORKFLOW_ORCHESTRATION.md)).

## Gotchas

- **Exit Contract**: write `validate-{N}.json` before exiting — even on failure. The orchestrator fallback is repo-blind.
- **Build hard gate**: when the build runs it MUST pass — no flag (`--auto-pass` included) bypasses it; unit tests alone miss build breaks.
- **Failed validation** → do NOT commit or push; leave the tree for triage.
- **Never skip the Phase 5 commit because `git log` shows a similar commit** — that commit is a _previous issue's_ (feature-dev never commits, #1608); skipping on a branch with zero commits ahead of base loses the entire implementation when the worktree is pruned. Only valid skip evidence: `git rev-list --count origin/<base>..HEAD` > 0.
- **Honesty rule — record every gate result as observed.** Never turn a catch into a pass by weakening the check: no lint-disable comments, skipped or deleted tests, loosened assertions or edited gate config. Fixing the code so it is genuinely correct is allowed (Ralph Loop); laundering the finding is not, and `validate-{N}.json` reports what actually ran.
- **Never dismiss a failing test as flaky without root-causing it.** Re-running until green is not a fix; a failure you cannot explain is recorded as a failure.
- **Env vars do NOT persist across Bash invocations** — re-derive `VALIDATION_STATUS` (and other gate inputs) inside the same cell that uses them; a stale/empty value spuriously skips the commit phase.
- See also [`_shared/GOTCHAS.md`](../_shared/GOTCHAS.md).

## Workflow

**Phase markers**: at the start of each phase, emit `<!-- phase:start name="{phase-name}" index={N} total={T} stage="feature-validate" -->` as an HTML comment on its own line, BEFORE any other output.

**SKIP_TO_PHASE**: if set, skip all phases up to and including the named phase (case-insensitive, kebab-case) and resume from the next one.

### Phase -1: Validate Environment

### Phase 0: Environment Preflight

**PURPOSE**: Verify the nightgauge binary is available and the environment
is healthy before executing any pipeline stage. Skills halt immediately with
actionable instructions when this check fails.

Three-layer check:

1. **Binary discovery** — shell-level, runs before invoking the binary
2. **Doctor check** — binary-level, full environment health via `doctor --json`
3. **Project environment** — the checks the project declares under
   `preflight.environment` (#101); opt-in, silent when none are declared

The binary discovery cascade below is **provider-neutral** (issue 4029): it works
identically under every adapter (Claude, Codex, Gemini, …) and references no
VSCode-extension-specific path. The host that spawns the skill (the VSCode
extension's `skillRunner`, or the Go `nightgauge` binary in auto/CLI mode)
resolves the binary authoritatively and exports `NIGHTGAUGE_BIN`, which
this cascade honors first. The remaining steps (PATH, the development-only repo
`bin/` and worktree canonical-repo `bin/`, `~/go/bin`) are fallbacks for direct
or terminal invocation.

This intentionally **diverges** from
`claude-plugins/nightgauge/hooks/lib/guard.sh` (issue 3262 → issue 4029): guard.sh is
Claude-Code-only and retains a trailing `~/.vscode/extensions/...` glob to serve
the standalone-terminal-Claude case (where no host exports `NIGHTGAUGE_BIN`).
Skills must stay portable Markdown and cannot reference that VSCode-only path —
so the shared resolution order (`NIGHTGAUGE_BIN` → PATH → gated repo → gated
canonical → `~/go/bin`) stays in sync, and only guard.sh carries the extra Claude-only
fallback. The `nightgauge preflight skill-portability` gate enforces that
no skill reintroduces a `.vscode/extensions` path, and that every cascade keeps
the `NIGHTGAUGE_DEV_REPO_BIN` gate (#2758): a foreign repository's
`bin/nightgauge` must never run.

After the cascade resolves `$BINARY`, the preflight prepends
`dirname($BINARY)` to `PATH` so subsequent bare `nightgauge ...`
invocations later in the same skill body resolve through PATH rather than
failing with "command not found" (issue 3262).

```bash
# Layer 1: provider-neutral binary discovery (NIGHTGAUGE_BIN → PATH → gated repo
# bin → gated canonical-repo bin → ~/go/bin). No VSCode-extension path — see issue 4029.
# Repo bins need NIGHTGAUGE_DEV_REPO_BIN=1 and a verified core checkout (#2758).
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
if [ -z "$BINARY" ]; then
  echo "ERROR: nightgauge binary not found via NIGHTGAUGE_BIN, PATH or ~/go/bin" >&2
  echo "" >&2
  echo "Install via: go install github.com/nightgauge/nightgauge/cmd/nightgauge@latest" >&2
  echo "Or download from: https://github.com/nightgauge/nightgauge/releases" >&2
  echo "Run \`nightgauge doctor\` after installing to verify your environment." >&2
  exit 1
fi
# Make the resolved binary callable via bare `nightgauge ...` later in this skill body (issue 3262).
export PATH="$(dirname "$BINARY"):$PATH"

# Per-repo GitHub token: export GH_TOKEN so every `gh` call in this skill
# authenticates as the *current repo's* configured user instead of the machine-
# global `gh auth` active account. `forge auth token` resolves per-repo (config
# github_user/token → GITHUB_TOKEN → `gh auth token --user <github_user>`), so
# concurrent sessions/workspaces owned by different GitHub users each use their
# own token. GH_TOKEN outranks GITHUB_TOKEN and the gh keyring. Only resolve when
# unset (an upstream env value — e.g. the VSCode extension's terminal env —
# wins); never fail preflight over it. Mirrors hooks/lib/guard.sh (sync, issue 3262).
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
  FAILED_CHECKS=$(printf '%s\n' "$DOCTOR_RESULT" | jq -r '.failed_checks[]? // empty' 2>/dev/null)
  if [ -n "$FAILED_CHECKS" ]; then
    echo "Failing check(s): $(echo "$FAILED_CHECKS" | tr '\n' ',' | sed 's/,$//')" >&2
  fi
  printf '%s\n' "$DOCTOR_RESULT" | jq -r '.errors[]' >&2 2>/dev/null || true
  INSTALL_MSG=$(printf '%s\n' "$DOCTOR_RESULT" | jq -r '.install_instructions // empty' 2>/dev/null)
  [ -n "$INSTALL_MSG" ] && echo "$INSTALL_MSG" >&2
  exit 1
fi
if [ "$DOCTOR_EXIT" -eq 1 ]; then
  echo "WARNING: Environment has non-critical issues:" >&2
  printf '%s\n' "$DOCTOR_RESULT" | jq -r '.warnings[]' >&2 2>/dev/null || true
  # Continue — warnings do not block skill execution
fi

# Layer 3: project-declared environment checks (preflight.environment, #101).
# Opt-in: with none declared this prints nothing and continues. The verb
# writes one `[environment-halt]` line per failed check, with its remedy.
if ! "$BINARY" preflight environment >/dev/null; then
  echo "[environment-halt] Project environment check failed — fix the environment, not the code." >&2
  exit 1
fi
```

**Exit codes from `nightgauge doctor`**:

| Code | Meaning                         | Skill behavior                          |
| ---- | ------------------------------- | --------------------------------------- |
| 0    | Healthy                         | Continue                                |
| 1    | Degraded (warnings only)        | Continue with warning printed to stderr |
| 2    | Broken (required checks failed) | Halt immediately with error details     |

**Layer 3** halts the stage when any check declared under
`preflight.environment` in the workspace's `.nightgauge/config.yaml` fails
(exit 1) or the declaration is invalid (exit 2). The failure is in the
operator's environment, not the code: do not edit code to work around it, do
not run the remedy yourself, and end the stage. The pipeline records it as
`environment_halt` and holds the issue for an operator.

Do not theorize about the cause beyond what is printed here — the failing
check name (`failed_checks`) and its `.errors[]` message are the complete
diagnosis (#277).


```bash
printf '<!-- phase:start name="validate-environment" index=0 total=22 stage="feature-validate" -->\n'
```

### Repo Identity Assertion (HARD GATE — non-recoverable)

If the `NIGHTGAUGE_TARGET_REPO` environment variable is set, the orchestrator
has pinned the exact repo this stage must run in. Verify the current repository
matches **before doing any work**:

```bash
if [ -n "$NIGHTGAUGE_TARGET_REPO" ]; then
  # Derive this checkout's identity from its own git remote. The question
  # is "which repository is this working directory?", and the remote answers
  # it exactly — locally, with no API call. This used to shell out to the
  # GitHub CLI for the same string, spending a request per stage on every
  # repo in the workspace, invisible to `nightgauge api-usage`.
  ACTUAL_REPO=$(git remote get-url origin 2>/dev/null \
    | sed -E 's#^git@[^:]+:#-#; s#^https?://[^/]+/#-#; s#^-##; s#\.git$##')
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


### Phase 0: Read Dev Context

```bash
printf '<!-- phase:start name="read-dev-context" index=1 total=22 stage="feature-validate" -->\n'
```

> **Read `_includes/context-load.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.** Extract the issue number from the branch; load `"$(nightgauge layout path pipeline dev-{N}.json)"`. Missing file → ask git ground truth via `gate verify feature-dev` (#134); proceed against the working tree if git finds changes, otherwise exit 1.

### Phase 0.6: AC Completion Check (type:docs)

```bash
printf '<!-- phase:start name="ac-completion-check" index=2 total=22 stage="feature-validate" -->\n'
```

> **Read `_includes/context-load.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.** `type:docs` issues only (no-op otherwise): set `AC_CHECK_REQUIRED=true` when the label is present, else `AC_CHECK_REQUIRED=false` and `AC_CHECK_SKIP=true`. Steps 0.6.2 (run ac-check), 0.6.2b (substantiate unchecked criteria against the diff and mark what the change demonstrably satisfies, #1233) and 0.6.3 (gate on result) live in the include — run all three; criteria that cannot be substantiated remain unchecked → exit 1 naming them.

### Phase 1: Detect Testing Environment (Deterministic)

```bash
printf '<!-- phase:start name="detect-testing-environment" index=3 total=22 stage="feature-validate" -->\n'
```

> **Read `_includes/test-setup.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.** Identify available frameworks and project type; no tests configured → proceed with manual checklist only.

### Phase 1.8: PTC Detection and Execution (CONDITIONAL)

```bash
printf '<!-- phase:start name="ptc-detection" index=4 total=22 stage="feature-validate" -->\n'
```

> **Read `_includes/test-setup.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.** If Programmatic Tool Calling is available, run validation through a single PTC session instead of individual Bash calls.

### Phase 1.4: Base Branch Freshness Check

```bash
printf '<!-- phase:start name="freshness-check" index=5 total=22 stage="feature-validate" -->\n'
```

### Base Branch Freshness Check

**PURPOSE**: Ensure the feature branch is up-to-date with the latest base branch
before proceeding. This prevents merge conflicts and build failures caused by
concurrent epic sub-issues or other PRs merging into the base branch while this
branch was being worked on.

**WHEN**: Run this check before any build/test validation and before merge
attempts. It is especially critical during epic batch processing where multiple
sub-issues merge concurrently.

**Merge, never rebase (#2699)**: the branch may already back an open PR. A
rebased tip stops descending from origin's tip, so the next push is rejected as
non-fast-forward and the run ends `branch_forked`. Merging the base keeps
origin's tip an ancestor, so a plain push always fast-forwards. Never rebase
the branch and never force-push it.

```bash
# Determine base branch (epic branch or main)
FRESHNESS_BASE="${BASE_BRANCH:-main}"
echo "Checking freshness against $FRESHNESS_BASE..."

# Fetch latest state of the base branch
git fetch origin "$FRESHNESS_BASE" 2>/dev/null

# Count commits on base that are NOT in our branch
BEHIND_COUNT=$(git rev-list --count "HEAD..origin/$FRESHNESS_BASE" 2>/dev/null || echo "0")

if [ "$BEHIND_COUNT" -gt 0 ]; then
  echo "Branch is $BEHIND_COUNT commit(s) behind origin/$FRESHNESS_BASE. Merging the base..."

  CURRENT_BRANCH=$(git branch --show-current)

  if git merge --no-edit -m "Merge origin/$FRESHNESS_BASE into $CURRENT_BRANCH" "origin/$FRESHNESS_BASE" 2>/dev/null; then
    echo "Merge successful. Branch is now up-to-date with $FRESHNESS_BASE."
    # Plain push; push-branch never forces and recovers a self-inflicted fork.
    # Best-effort: the cause is printed with the fork tokens filtered out, so a
    # rejection here cannot stamp the `[branch-forked]` terminal marker on a
    # stage that carries on (#2749). The filter copies the `branch_forked`
    # clauses of internal/terminalkind/table.json.
    PUSH_ERR=$(mktemp)
    # Exit 4 is a credential refusal (#2742): its `[push-auth-failed]` line
    # passes the filter, so a later push surfaces the same cause.
    if ! nightgauge git push-branch 2>"$PUSH_ERR"; then
      echo "WARNING: Failed to push merged branch. Continuing with the local merge."
      grep -viE 'branch[-_]forked|non-fast-forward|push rejected|fetch first' "$PUSH_ERR" || true
    fi
    rm -f "$PUSH_ERR"
  else
    # Merge stopped on conflicts — check if they are resolvable.
    #
    # Enumerated with `ls-files -u -z`, NOT `git diff --name-only
    # --diff-filter=U`: the latter C-quotes any path with a non-ASCII or
    # control byte, so a conflict in `café.txt` arrives as the literal
    # 12-character string `"caf\303\251.txt"` and the `git add` below fails on
    # a path that does not exist — leaving that conflict silently unresolved
    # (#301). `-z` emits raw path bytes with a NUL terminator, so neither
    # quoting nor a newline in a path can confuse the loop. git sorts the
    # output by path, so skipping a repeat of the previous path collapses that
    # path's index stages to one entry.
    #
    # Append with `+=`, never `CONFLICT_FILES[$N]=` from N=0: zsh arrays are
    # 1-indexed and abort the whole loop with "assignment to invalid subscript
    # range" on index 0, enumerating nothing — and the agent shell is not
    # guaranteed to be bash (zsh has been the macOS login shell since Catalina).
    CONFLICT_FILES=()
    CONFLICT_PREV=""
    while IFS= read -r -d '' CONFLICT_REC; do
      CONFLICT_PATH="${CONFLICT_REC#*$'\t'}"
      if [ "$CONFLICT_PATH" != "$CONFLICT_PREV" ]; then
        CONFLICT_FILES+=("$CONFLICT_PATH")
        CONFLICT_PREV="$CONFLICT_PATH"
      fi
    done < <(git ls-files -u -z 2>/dev/null)
    CONFLICT_COUNT=${#CONFLICT_FILES[@]}

    if [ "$CONFLICT_COUNT" -gt 0 ]; then
      printf 'Merge conflicts detected in:'
      printf ' %s' "${CONFLICT_FILES[@]}"
      printf '\n'
      echo "Attempting AI-assisted conflict resolution..."

      # For each conflicted file:
      # 1. Read the file with conflict markers
      # 2. Understand BOTH sides. Under `git merge`, the `<<<<<<< HEAD` side
      #    is this branch's own feature work and the `>>>>>>> origin/<base>`
      #    side is the base being merged in.
      # 3. Produce a logically correct merge preserving BOTH changes
      # 4. Stage the resolved file
      #
      # CRITICAL RULES:
      # - NEVER blindly accept one side
      # - If resolution is ambiguous, abort and fail with clear error
      # - After resolution, code MUST compile

      for FILE in "${CONFLICT_FILES[@]}"; do
        echo "Resolving: $FILE"
        # ... AI resolves the conflict ...
        git add "$FILE"
      done

      if git commit --no-edit 2>/dev/null; then
        echo "Conflict resolution successful."
        PUSH_ERR=$(mktemp)
        if ! nightgauge git push-branch 2>"$PUSH_ERR"; then
          echo "WARNING: Failed to push merged branch. Continuing with the local merge."
          grep -viE 'branch[-_]forked|non-fast-forward|push rejected|fetch first' "$PUSH_ERR" || true
        fi
        rm -f "$PUSH_ERR"
      else
        echo "ERROR: Committing the merge failed after conflict resolution."
        git merge --abort 2>/dev/null || true
        echo "FRESHNESS_CHECK_FAILED=true"
        echo "Manual conflict resolution required. Base branch has diverged significantly."
        # Do NOT exit — let the calling phase decide how to handle
        FRESHNESS_CHECK_FAILED=true
      fi
    else
      git merge --abort 2>/dev/null || true
      echo "ERROR: Merge failed with no conflict markers. Unexpected state."
      FRESHNESS_CHECK_FAILED=true
    fi
  fi
else
  echo "Branch is up-to-date with origin/$FRESHNESS_BASE."
fi
```

**Output variables**:

- `BEHIND_COUNT` — how many commits the branch was behind (0 = already fresh)
- `FRESHNESS_CHECK_FAILED` — set to `true` if the merge + conflict resolution
  failed. The calling phase should decide whether to abort or continue.

**Safety**: Merges the base and pushes with `nightgauge git push-branch`, which
never forces. If the push fails, the cause is printed without the terminal
marker, and the local merge is still valid for build/test validation.


Best-effort: if `FRESHNESS_CHECK_FAILED=true`, log a warning and continue — pr-merge resolves conflicts later.

### Phase 1.5: Run Build Verification (CONDITIONAL)

```bash
printf '<!-- phase:start name="build-verification" index=6 total=22 stage="feature-validate" -->\n'
```

> **Read `_includes/build-verification.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.** **Hard gate** (see Gotchas): build failure fails validation with `errorCategory: "build-failed"` and a captured stderr tail.

### Phase 1.6: Dead Code Detection (CONFIGURABLE GATE)

```bash
printf '<!-- phase:start name="dead-code-detection" index=7 total=22 stage="feature-validate" -->\n'
```

> **Read `_includes/dead-code.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.** When `validation.dead_code=gate` (default), current-issue error-severity dead-code findings block validation.

### Phase 1.7: Baseline Comparison for Test Failures

```bash
printf '<!-- phase:start name="baseline-comparison" index=8 total=22 stage="feature-validate" -->\n'
```

> **Read `_includes/baseline-comparison.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.** Identify pre-existing failures (already failing on main) so the Ralph Loop skips them; runs ONLY when tests fail, skipped when dev context shows all passed.

### Declare every long external process you wait on (`NIGHTGAUGE_PROGRESS:`)

The progress-runaway monitor watches the agent's own message stream: commits,
new files, phase markers, `CI_PROGRESS:` lines, novel tool calls. A stage that
starts a Playwright/Docker/Flutter/emulator run and then waits for it produces
**none of those**, so every clock the monitor has goes cold precisely while the
stage is behaving correctly, and it is killed mid-suite (#1488 — this happened
twice on the same issue, then again on a stage whose own `git commit` was still
running).

The monitor cannot infer the child. **Declare it**, by echoing one line the
moment the process is spawned:

```bash
echo "NIGHTGAUGE_PROGRESS: {\"pid\": $SUITE_PID, \"log\": \"$SUITE_LOG\", \"label\": \"playwright e2e\"}"
```

| Field   | Meaning                                                                           |
| ------- | --------------------------------------------------------------------------------- |
| `pid`   | The child's pid, captured with `PID=$!` **at spawn**. Optional if `log` is given. |
| `log`   | Absolute path of a log the process appends to. Byte growth counts as activity.    |
| `label` | Short human name for the operator-facing "waiting, not stalled" line.             |

Both fields are optional individually but at least one must be present, and
giving both is strictly better — the pid covers a quiet process, the log covers
a process whose pid the shell cannot see (a `docker compose` child, a wrapper
script's grandchild).

While a declared pid is alive **or** a declared log is still growing, the
monitor defers the no-progress kill and suppresses the churn detector, so
polling the log is no longer a stall vector. Three rules make this safe rather
than a way to become immortal, and you should know all three:

1. **The declaration is not progress.** It defers a kill; it can never satisfy
   one. The stage-cost cap, the stage hard-cap and the catastrophic-cost
   backstop are untouched.
2. **A dead pid stops deferring immediately**, and a log that stops growing
   stops deferring at the next poll. Do not declare a pid you have already
   reaped, and never `touch` a log to keep a stage alive.
3. **Twenty minutes is the ceiling.** Past that the monitor stops looking, and a
   wedged child is reclaimed exactly as before
   (`pipeline.progress_runaway.external_progress_ceiling_ms`).

**Capture the pid at spawn and poll with a bounded loop.** `PID=$!` on the line
after the `&`, then `kill -0 "$PID"` with a fixed iteration count. Never
`pgrep -f <script name>` — it matches the polling shell itself and the loop
never exits.

A stage that loops on `ls`/`cat` with nothing declared is still killed at the
window, which is the whole point: the declaration is what separates "waiting on
real work" from "spinning".

### A wait longer than the foreground limit runs in the background

A foreground Bash call is killed at twice its timeout: 1200 s with the default
600 s timeout. Any command that can run longer — the full local gate above all —
is started with the Bash tool's `run_in_background: true` and a `timeout` (in
milliseconds) at least as long as the command's own limit, then waited on
through its completion notification. Never put `&` inside a foreground call: the
process is orphaned, nothing waits for it, and its result is never read. For the
gate the limit is `hooks.pre_push.timeout_seconds` × 1000 (1,800,000 by default),
and the `gate-background` hook denies any other way of starting it (#2953).

### The other answer: do not wait at all

Declaring is for a wait that has to happen in this stage. When the wait belongs
to a **later** stage, delete it instead — a declaration is a deferral with a
twenty-minute ceiling, not a licence to block.

**pr-create's CI wait is the worked example (#1531).** Phase 3.5 used to run
`nightgauge ci wait <PR> --timeout 15 --poll 30` right after opening the PR and
sit on it. Nothing was declared, so the monitor killed the stage at 930s with
the PR already open and the run was reported as "PR creation failed". Declaring
the child would have papered over it; the real fix was that pr-create never
needed to wait — pr-merge owns CI polling and auto-fix, and the deterministic Go
pr-create runner never waited either. Phase 3.5 is now a single non-blocking
snapshot of the check rollup that records `final_status: pending` and exits.

So there is **no CI wait to declare in pr-create**. Before adding a
`NIGHTGAUGE_PROGRESS:` line to a stage, ask whether the stage owns that wait at
all.


### Phase 2: Run Tests (Redundancy-Aware)

```bash
printf '<!-- phase:start name="run-tests" index=9 total=22 stage="feature-validate" -->\n'
```

> **Read `_includes/run-tests.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.** Run integration and E2E tests (dev does NOT); do not re-run unit tests the dev context confirms passed. Failures → record in context; Ralph Loop auto-fix if enabled. On a fix issue that touched a test file, Step 2.2.5 also proves the new assertion can go red — revert the fix from a COPY and confirm the test FAILS; green there means the test is decoration. Step 2.5 then closes the phase with `nightgauge gate check-test-execution`: a suite the configured test command structurally cannot reach has never been executed by anything, so a passing run says nothing about it (#1261). Silent in any repo that excludes nothing.

### Phase 2.4: Mobile MCP E2E Tests (Agent-Driven)

```bash
printf '<!-- phase:start name="mobile-mcp-tests" index=10 total=22 stage="feature-validate" -->\n'
```

> **Read `_includes/mobile-mcp-tests.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.** Build the debug APK, boot the `Pixel_9_Pro` emulator, run every `test/mobile_mcp/specs/*.md` spec via mobile-mcp tools with screenshot/result-JSON evidence, stop the emulator. Config: `validation.mobile_mcp_tests` (default `"strict"` — spec failures block PR creation); zero-overhead skip when no runnable specs or no `flutter`/`adb`/`emulator` toolchain.

### Phase 2.45: Web UI Verification Gate (verify-ui)

```bash
printf '<!-- phase:start name="verify-ui-gate" index=11 total=22 stage="feature-validate" -->\n'
```

> **Read `_includes/verify-ui-gate.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.** When the diff touches frontend code in a UI-bearing repo, start the dev server, chain into `nightgauge-verify-ui` to drive the flow in a real browser, and gate on the result; emits the verify-ui `gate record-metric` per the include. Config: `validation.verify_ui_tests` (default `"strict"`). Trigger detection is deterministic (`nightgauge ci classify-ui-surface`), never LLM-judged. UI-relevant diff with no registered flow → record an explicit skip reason, never a silent pass.

### Phase 2.5: CI Parity Check (Deterministic)

```bash
printf '<!-- phase:start name="ci-parity-check" index=12 total=22 stage="feature-validate" -->\n'
```

> **Read `_includes/ci-and-knowledge.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.** Run this tree's one full gate: skip it when `nightgauge ci gate-status` says the tree passed, otherwise run the repository's local gate, or its CI commands when it has none. **HARD GATE**: they must pass (after up to 3 auto-fix attempts) or `VALIDATION_STATUS=failed` — PRs arrive green. Step 2.7.3 runs the independent review in a fresh context; `nightgauge review record` writes its verdict.

### Phase 2.6: Knowledge Coverage Check

```bash
printf '<!-- phase:start name="knowledge-coverage-check" index=13 total=22 stage="feature-validate" -->\n'
```

> **Read `_includes/ci-and-knowledge.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.** Cross-check the implementation against the issue's `PRD.md` ACs and `decisions.md`; emit a coverage map and telemetry. Non-blocking by default (`knowledge.validate.strict: true` to gate); skip when `knowledge_path` is unset or the files are missing.

### Phase 2.7: Pre-Push Merge Validation Gate

```bash
printf '<!-- phase:start name="pre-push-merge-validation" index=14 total=22 stage="feature-validate" -->\n'
```

> **Read `_includes/ci-and-knowledge.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.** Validate against the latest target branch before commit/push (conflicts, merged-state regressions, security). **Gate**: on failure set `VALIDATION_STATUS=failed` and skip commit/push.

### Phase 3: Generate Manual Checklist (Conditional)

```bash
printf '<!-- phase:start name="generate-checklist" index=15 total=22 stage="feature-validate" -->\n'
```

> **Read `_includes/feedback-and-commit.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.** Component-specific checklist from project type and changed files; auto-passed at >1000 passing tests and 0 failures; security items omitted (feature-dev ran them).

### Phase 4: Feedback Signal Evaluation

```bash
printf '<!-- phase:start name="feedback-signal-evaluation" index=16 total=22 stage="feature-validate" -->\n'
```

> **Read `_includes/feedback-and-commit.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.** Emit backward signals ONLY for upstream structural problems (planning errors, ambiguous requirements, model limits) — normal test failures dev simply fixes do NOT warrant a signal.

### Phase 4.9: Compute Validation Status (Pre-Commit)

> **Read `_includes/feedback-and-commit.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.** Compute `VALIDATION_STATUS` before the commit decision so Phase 5 can gate on it.

### Phase 5: Commit and Push Validated Code

```bash
printf '<!-- phase:start name="commit-and-push" index=17 total=22 stage="feature-validate" -->\n'
```

> **Read `_includes/feedback-and-commit.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.** Commit all changes (feature-dev implementation + Ralph fixes) and push — the commit lives here, not in feature-dev (#1608). **Gate**: if `VALIDATION_STATUS` is `"failed"`, do NOT commit or push. Commit-skip evidence rule: see Gotchas.

### Phase 6: Write Validate Context

```bash
printf '<!-- phase:start name="write-validate-context" index=18 total=22 stage="feature-validate" -->\n'
```

> **Read `_includes/context-and-board.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.** Write `validate-{N}.json` with `nightgauge layout write pipeline validate-{N}.json` for `/nightgauge-pr-create` (schema: [docs/CONTEXT_ARCHITECTURE.md](https://github.com/nightgauge/nightgauge/blob/main/docs/CONTEXT_ARCHITECTURE.md)) — **every run, even on failure** (Exit Contract above). Record a verdict for each acceptance criterion (Step 6.0); an `unmet` one makes pr-create write `Refs #N`, not `Closes #N` (#2595).

### Phase 7: Sync Project Board Status

```bash
printf '<!-- phase:start name="sync-project-status" index=19 total=22 stage="feature-validate" -->\n'
```

> **Read `_includes/context-and-board.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.** Sync board Status to "In progress" via Go binary `project move-status` (idempotent, best-effort — never fail validation over it).

### Phase 8: Output Summary and Signal Complete

```bash
printf '<!-- phase:start name="output-summary" index=20 total=22 stage="feature-validate" -->\n'
```

Display summary (branch, issue, status, commit SHA, build/test results, checklist). Next step: `passed`/`partial`/`skipped` → `/nightgauge-pr-create`; `failed` → fix issues first.

```bash
ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
: "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
# Resolve the nightgauge binary (standard cascade, as prior phases), then
# best-effort: "$BINARY" project move-status "$ISSUE_NUMBER" "in-progress".

# Enforce the Exit Contract — fail loudly if Phase 6 was skipped (the
# orchestrator's repo-blind fallback may misreport test status; always write
# the file ourselves).
CONTEXT_FILE="$(nightgauge layout path pipeline validate-${ISSUE_NUMBER}.json)"
if [ ! -s "$CONTEXT_FILE" ]; then
  echo "ERROR: ${CONTEXT_FILE} missing — Phase 6 (Write Validate Context) did not run." >&2
  echo "Re-run Phase 6 before exiting; do NOT rely on the orchestrator fallback." >&2
  exit 1
fi
```

### Phase 9: Self-Assessment Epilogue

```bash
printf '<!-- phase:start name="self-assessment" index=21 total=22 stage="feature-validate" -->\n'
```

### Self-Assessment Epilogue

**PURPOSE**: Evaluate whether this skill's instructions matched reality during
this execution. This phase is **non-blocking** — skip entirely if any main phase
failed. A perfectly working skill produces **no output** from this phase.

> See [docs/SKILL_SELF_ASSESSMENT.md](https://github.com/nightgauge/nightgauge/blob/main/docs/SKILL_SELF_ASSESSMENT.md) for
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

Only if friction was detected in Step 1. Write a single JSON file,
`assessments/{STAGE_NAME}-${ISSUE_NUMBER}.json` in the clone's pipeline state
directory, through the binary (JSON on stdin; never a direct file write):

```bash
ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
: "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
nightgauge layout write pipeline "assessments/{STAGE_NAME}-${ISSUE_NUMBER}.json" <<'JSON'
{ ... the record below ... }
JSON
```

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
ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
: "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
ASSESSMENT_FILE="$(nightgauge layout path pipeline "assessments/{STAGE_NAME}-${ISSUE_NUMBER}.json")"
if [ -f "$ASSESSMENT_FILE" ]; then
  python3 -m json.tool "$ASSESSMENT_FILE" > /dev/null 2>&1 || \
    echo "WARNING: Assessment record is not valid JSON" >&2
fi
```


<!--
  LEAN REWRITE — adopted 2026-07-13 on a measured composite-score win
  (+1.8 Sonnet 5 / +1.6 Opus 4.8, no deterministic-check regressions),
  replacing a 581-line predecessor at 55% of its size.
  Measurement and decision rule: evals/skill-variants/feature-validate/README.md
  Inventory preserved across the rewrite: 23/23 phase markers, 6/6 shared
  includes, exit contract x2, spike gate, gate-metric emissions.
-->
