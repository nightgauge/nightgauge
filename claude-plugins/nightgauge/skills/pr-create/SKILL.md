---
name: pr-create
description: Create a pull request with correct base/head, issue linkage, validation
  summary, and reviewer assignment. Use after /feature-validate to open the PR
  for an implemented issue.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.25.0"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Read Write Edit Glob Grep Bash Task
inputs:
  - pipeline/validate-{N}.json
outputs:
  - pipeline/pr-{N}.json
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


# PR Create

Create a high-quality pull request from pipeline outputs with minimal manual
interpretation.

## Outcomes

- Loads development (and optional validation) pipeline context
- Verifies branch, commit, and test status prerequisites
- Creates PR with issue linkage and concise body
- Requests configured reviewers
- Optionally enables auto-merge when configured
- Writes the `pr-{N}.json` context file for pr-merge
  (`nightgauge layout write pipeline pr-{N}.json`)
- Signals pipeline status updates for integrations

## Required Inputs

- Branch includes issue number
- `dev-{N}.json` from `/nightgauge-feature-dev`
- Optional: `validate-{N}.json` from `/nightgauge-feature-validate`

Both live in the clone's pipeline state directory: read each at
`"$(nightgauge layout path pipeline <file>)"`.

If required context is missing, fail and instruct correct stage order.

## References

- Config schema: `docs/CONFIGURATION.md`
- Context schema: `docs/CONTEXT_ARCHITECTURE.md`
- PR and stage standards: `docs/ISSUE_TO_PR_WORKFLOW.md`
- Validation and test guidance: `docs/TESTING.md`

Do not duplicate full templates or schemas here; read docs on demand.

## Spike Issues (`type:spike`)

For `type:spike` PRs, include a placeholder section in the PR body:

```markdown
## Created Follow-up Issues

_Populated by `spike-materialize` after merge — see [docs/SPIKE_CONTRACT.md](../../docs/SPIKE_CONTRACT.md)._
```

The `spike-materialize` post-merge stage replaces this placeholder with the
list of materialized issue numbers. Do not pre-populate the list — it is
authoritative output of the materializer.

## Supporting files (load on demand)

- `_includes/context-load.md` (same directory as this SKILL.md) — read in Phases 1
  and 1.5 (parallel context gathering, stage-start signal, batch detection)
- `_includes/pr-sections.md` (same directory as this SKILL.md) — read in Phases
  1.7 and 1.8 (Knowledge and What-to-Test PR-body sections)
- `_includes/security-and-scope.md` (same directory as this SKILL.md) — read in
  Phases 2.5 and 2.6 (security re-scan, scope drift gate)
- `_includes/create-and-ci.md` (same directory as this SKILL.md) — read in Phases
  3, 3.6, and 3.5 (create PR, verify PR exists, snapshot CI)

## Orchestration

This skill intentionally declares **no** `orchestration:` frontmatter block. PR
creation is a **single-agent deterministic phase** by design — it is never
fanned out (epic #3899,
[docs/WORKFLOW_ORCHESTRATION.md](https://github.com/nightgauge/nightgauge/blob/main/docs/WORKFLOW_ORCHESTRATION.md) §Safety &
guardrails). The capability-routed `WorkflowEngine` runs it as one deterministic
phase node alongside the orchestrated stages.

## Gotchas

- **Never force-push, never fall back to asking.** This stage is headless — a
  force-push can destroy history and a blocked prompt hangs the run. Resolve
  deterministically or fail with a clear reason.
- **Scope-drift gate (#3040, platform incident #840).** Re-scan the diff before
  opening; unexpected changes outside the issue's scope are blocked, not shipped.
- **False-success guard.** Exiting 0 with no open PR is a failure — verify the PR
  actually exists before reporting success.
- **Epic-umbrella PRs must `Closes` every shipped sub, not just the epic (#3979).**
  When a single PR delivers more than one of an epic's sub-issue deliverables,
  enumerate `Closes #sub` for **each** sub whose work is in the diff — do NOT
  rely on `Closes #epic` alone. Closing only the epic leaves the subs OPEN, and
  the autonomous picker re-spawns them into conflicting PRs. (The post-merge
  reconciler closes such orphans as a backstop, but the PR body is the cheap fix.)
- **`Closes #N` only when every criterion is met (#2595, #2655).** The
  deterministic runner resolves each criterion from the branch diff, the tree
  re-run and `validate-{N}.json` `acceptance_criteria_verdicts`, with any
  `unmet` winning (#2665); use its verdicts when `pr-{N}.json` has them. Unless
  every criterion is `met` (any `unmet` or `not_verifiable`, or no criteria at
  all), write `Refs #N`
  and list each open criterion once under
  `## ⚠️ Unmet acceptance criteria`; post-merge then leaves the issue open and
  returns it to Ready. Do not write "close #N" anywhere in such a body: GitHub
  reads it as a closing keyword. The Validation section reports what ran:
  never pair "not run" with a pass count.
- **Never run the full local gate (#2989).** `feature-validate`'s gate already
  satisfies the "run the complete local gate once before pushing" rule. After
  the Phase 2.3 base merge, push the merge straight away: PR CI and `pr-merge`
  check the merged tree. The stage fence refuses `bash scripts/ci-local.sh` and
  `nightgauge ci gate-run` here.
- See also the cross-cutting gotchas in
  [`_shared/GOTCHAS.md`](../_shared/GOTCHAS.md).

## Workflow

### Phase Marker Protocol

At the start of each phase, emit a structured phase marker as an HTML comment on
its own line. Format:

`<!-- phase:start name="{phase-name}" index={N} total={T} stage="pr-create" -->`

This enables the orchestrator to track phase progress. Emit the marker BEFORE
any other output for that phase.

### Phase 0: Environment Preflight

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


---

### Phase 0.5: Auto-Merge Guard

```bash
printf '<!-- phase:start name="auto-merge-guard" index=0 total=13 stage="pr-create" -->\n'
```

**PURPOSE**: Verify the target repository does NOT have auto-merge enabled and
that no PR is created with the `--auto` flag. The pipeline's `pr-merge` stage
requires exclusive control over PR merging to detect check failures, apply
self-healing logic, and keep the UI in sync.

```bash
OWNER=$(git remote get-url origin 2>/dev/null | \
  grep -oE 'github\.com[:/][^/]+' | grep -oE '[^:/]+$')
REPO=$(git remote get-url origin 2>/dev/null | \
  grep -oE '[^/]+$' | sed 's/\.git$//')

if [ -z "$OWNER" ] || [ -z "$REPO" ]; then
  echo "Auto-merge guard: could not determine repository from git remote — skipping check"
else
  # Query auto-merge status via Go binary
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
  [ -n "$BINARY" ] && export PATH="$(dirname "$BINARY"):$PATH"

  if [ -n "$BINARY" ]; then
    if "$BINARY" repo check-auto-merge --owner "$OWNER" --repo "$REPO" 2>/tmp/automerge.err; then
      echo "Auto-merge guard: allow_auto_merge=false — OK"
    else
      cat /tmp/automerge.err >&2
      exit 1
    fi
  else
    echo "Auto-merge guard: Go binary not found — skipping check (non-fatal)"
  fi
fi
```

**Note**: If the Go binary is unavailable, this guard degrades gracefully (non-fatal).
The VSCode extension provides a proactive warning at workspace load time.

---

### Phase 1: Load Context and Start Stage

```bash
printf '<!-- phase:start name="load-context" index=1 total=13 stage="pr-create" -->\n'
```

**Step 1.1: Parse issue number from branch**

Extract issue number from branch name (e.g., `feat/11-description` → 11).

```bash
BRANCH=$(git branch --show-current)
ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
: "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
```

**Step 1.2: Determine base branch early**

Resolve base branch BEFORE parallel gathering (needed for `git diff` in Groups
B and subsequent phases):

```bash
ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
: "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
BASE_BRANCH=$(jq -r '.base_branch // empty' \
  "$(nightgauge layout path pipeline issue-${ISSUE_NUMBER}.json)" 2>/dev/null)
if [ -z "$BASE_BRANCH" ]; then
  BASE_BRANCH=$(git config --get nightgauge.branch.base 2>/dev/null || echo "main")
fi
```

**Steps 1.3–1.7: Gather context in parallel, merge it, load knowledge, signal start**

> **Read `_includes/context-load.md` (same directory as this SKILL.md) now and
> follow its instructions before continuing this phase.**

### Phase 1.7: Build Knowledge Section

```bash
printf '<!-- phase:start name="build-knowledge-section" index=2 total=13 stage="pr-create" -->\n'
```

**PURPOSE**: Construct the `## Knowledge` section for the PR body from knowledge
base entries created during the pipeline run. This section is **omitted
entirely** when no knowledge entries exist — never include an empty section.

> **Read `_includes/pr-sections.md` (same directory as this SKILL.md) now and
> follow its instructions before continuing this phase.**

### Phase 1.8: Build What to Test Section

```bash
printf '<!-- phase:start name="build-what-to-test-section" index=3 total=13 stage="pr-create" -->\n'
```

**PURPOSE**: Generate the `## What to Test` section from the dependency graph
and the feature branch diff. Appended to the PR body after `## Validation` and
before `## Knowledge`. **No-op** when the dependency graph file is absent or
when the git diff produces no output — set `WHAT_TO_TEST_SECTION=""` and
continue without error.

> **Read `_includes/pr-sections.md` (same directory as this SKILL.md) now and
> follow its instructions before continuing this phase.**

### Phase 2: Preflight Checks

```bash
printf '<!-- phase:start name="preflight-checks" index=4 total=13 stage="pr-create" -->\n'
```

1. Ensure current branch is not base branch (for example, not `main`).
2. Ensure working tree is clean or intentionally commit staged changes.
3. Confirm required tests pass if configured.
4. Determine base branch using this priority:
   1. `base_branch` from issue context (`issue-{N}.json`) — set by issue-pickup
      when the issue is a sub-issue of an epic (e.g.,
      `epic/28-api-server-foundation`).
   2. Config `branch.base` if no context `base_branch` is present.
   3. Default `main` if neither is set.

**Epic branch detection**: When `base_branch` starts with `"epic/"`, this is a
sub-issue PR targeting an epic branch. Additional behavior applies in Phase 3.

If checks fail, stop with clear fixes.

### Phase 2.3: Proactive Main Branch Merge

```bash
printf '<!-- phase:start name="proactive-main-merge" index=5 total=13 stage="pr-create" -->\n'
```

**PURPOSE**: Merge the latest base branch into the feature branch before
creating the PR. This prevents PRs with stale code from reaching GitHub and
catches merge conflicts early — before a PR is created — with a clear outcome
classification that allows the orchestrator to route remediation.

This phase is especially critical in batch scenarios where a sibling sub-issue
may have merged into the base branch while this branch was being implemented.

### Proactive Main Branch Merge

**PURPOSE**: Merge the latest base branch into the feature branch **before**
creating the PR. This ensures code is always tested against the latest base
branch state, prevents creating PRs with stale code that will fail to merge,
and catches conflicts early with a clear outcome classification.

**WHEN**: Run after preflight checks (clean working tree confirmed) and before
security re-scan (which will scan the merged state). Critical during epic batch
processing where sibling sub-issues may merge while this branch is being worked
on.

**Relation to `FRESHNESS_CHECK.md`**: both fragments merge the base (never
rebase), preserving both lineages so a PR branch's origin tip stays an ancestor
(#2699). This one runs before the PR exists and fails the stage on conflict;
`FRESHNESS_CHECK.md` is the best-effort pre-build check in feature-validate and
pr-merge.

```bash
# Step 1: Determine base branch (already resolved in Phase 2)
# BASE_BRANCH is set by Phase 1 / Phase 2; fall back to main if unset
MERGE_BASE="${BASE_BRANCH:-main}"
CURRENT_BRANCH=$(git branch --show-current)

echo "Phase 2.3: Checking freshness against origin/$MERGE_BASE..."

# Step 2: Fetch latest state of the base branch
git fetch origin "$MERGE_BASE" 2>/dev/null

# An open merge with every path resolved and staged is concluded, never
# aborted: committing it loses nothing (#2755). One with unmerged paths, or
# whose commit fails, is left exactly as found for the guard below.
FRESHNESS_MERGE_CONCLUDED=false
if git rev-parse -q --verify MERGE_HEAD >/dev/null 2>&1 && [ -z "$(git ls-files -u 2>/dev/null)" ]; then
  if git commit --no-edit >/dev/null 2>&1; then
    echo "Concluded the open merge with its staged resolution."
    FRESHNESS_MERGE_CONCLUDED=true
    # Push it now: the branch then reads as up-to-date below, so Step 5 never
    # runs for it. Same handling as Step 5.
    PUSH_EXIT=0
    nightgauge git push-branch 2>&1 || PUSH_EXIT=$?
    if [ "$PUSH_EXIT" -ne 0 ]; then
      if [ "$PUSH_EXIT" -eq 3 ]; then
        echo "ERROR: origin rejected '$CURRENT_BRANCH' and the rejection was not reconciled." >&2
      else
        echo "ERROR: Failed to push the concluded merge on '$CURRENT_BRANCH' to origin." >&2
        echo "Ensure you have push access and the branch is not protected." >&2
      fi
      FRESHNESS_MERGE_PERFORMED=true
      FRESHNESS_MERGE_STATUS="push-failed"
      exit 1
    fi
  fi
fi # open merge

# Step 3: Count commits on base NOT yet in our branch
BEHIND_COUNT=$(git rev-list --count "HEAD..origin/$MERGE_BASE" 2>/dev/null || echo "0")

if git rev-parse -q --verify MERGE_HEAD >/dev/null 2>&1; then
  # A merge is still open: it has unmerged paths, or its commit failed above.
  # Merging again fails with "You have not concluded your merge", and aborting
  # would discard whatever resolution is staged (#2755). Leave it as found and stop: this is not a base conflict, so the
  # stale-branch-merge-conflict outcome is not emitted.
  echo "ERROR: A merge is already in progress (MERGE_HEAD $(git rev-parse --short MERGE_HEAD))." >&2
  echo "Resolve and commit it (or abandon it yourself), then re-run the pipeline." >&2
  FRESHNESS_MERGE_PERFORMED=false
  FRESHNESS_MERGE_STATUS="merge-in-progress"
  exit 1
elif [ "$BEHIND_COUNT" -eq 0 ] && [ "${FRESHNESS_MERGE_CONCLUDED:-false}" = "true" ]; then
  echo "Concluded merge pushed. Branch is up-to-date with origin/$MERGE_BASE."
  FRESHNESS_MERGE_PERFORMED=true
  FRESHNESS_MERGE_STATUS="merged"
elif [ "$BEHIND_COUNT" -eq 0 ]; then
  echo "Branch is up-to-date with origin/$MERGE_BASE — no merge needed."
  FRESHNESS_MERGE_PERFORMED=false
  FRESHNESS_MERGE_STATUS="up-to-date"
else
  echo "Branch is $BEHIND_COUNT commit(s) behind origin/$MERGE_BASE. Merging..."

  # Step 4: Attempt merge (preserve both lineages; safe for batch scenarios)
  MERGE_MSG="Merge origin/${MERGE_BASE} into ${CURRENT_BRANCH}"
  if git merge "origin/$MERGE_BASE" --no-edit -m "$MERGE_MSG" 2>&1; then
    FRESHNESS_MERGE_PERFORMED=true
    FRESHNESS_MERGE_STATUS="merged"
    echo "Merge successful. Branch now includes latest $MERGE_BASE."

    # Step 5: Push merged branch
    PUSH_EXIT=0
    nightgauge git push-branch 2>&1 || PUSH_EXIT=$?
    if [ "$PUSH_EXIT" -ne 0 ]; then
      if [ "$PUSH_EXIT" -eq 3 ]; then
        # Exit 3 printed `[branch-forked]`: the run stops on the fork.
        echo "ERROR: origin rejected '$CURRENT_BRANCH' and the rejection was not reconciled." >&2
      else
        # Exit 1 is a permission/infra error — not a conflict
        echo "ERROR: Failed to push merged branch '$CURRENT_BRANCH' to origin." >&2
        echo "Ensure you have push access and the branch is not protected." >&2
      fi
      FRESHNESS_MERGE_STATUS="push-failed"
      exit 1
    fi
    echo "Merged branch pushed. CI will re-run on the merged commit."

  else
    # Step 6: Merge failed — detect conflicts and classify outcome
    CONFLICT_FILES=$(git diff --name-only --diff-filter=U 2>/dev/null | tr '\n' ', ' | sed 's/,$//')
    echo "" >&2
    echo "ERROR: Merge conflicts detected." >&2
    echo "Conflicting files: ${CONFLICT_FILES:-<none detected>}" >&2
    echo "" >&2
    echo "Outcome: stale-branch-merge-conflict" >&2
    echo "" >&2
    echo "The feature branch is behind origin/$MERGE_BASE and has merge conflicts." >&2
    echo "This PR will not be created until conflicts are resolved." >&2
    echo "" >&2
    echo "To fix:" >&2
    echo "  1. git fetch origin $MERGE_BASE" >&2
    echo "  2. git merge origin/$MERGE_BASE" >&2
    echo "  3. Resolve conflicts manually" >&2
    echo "  4. git add <resolved-files> && git commit" >&2
    echo "  5. Re-run the pipeline" >&2

    # Abort the failed merge to restore a clean working tree. The open-merge
    # probe above ran first, so this only undoes this block's own unresolved
    # merge.
    git merge --abort 2>/dev/null || true

    FRESHNESS_MERGE_PERFORMED=false
    FRESHNESS_MERGE_STATUS="conflict"
    exit 1
  fi
fi
```

**Output variables** (available to downstream phases):

- `FRESHNESS_MERGE_PERFORMED` — `true` if a merge was performed or an open one
  was concluded, `false` if branch was already up-to-date
- `FRESHNESS_MERGE_STATUS` — `up-to-date` | `merged` | `conflict` |
  `push-failed` | `merge-in-progress` (a merge was already open; the stage
  exits 1 and leaves it untouched)

**Failure outcome**: When conflicts are detected, the stage exits with status 1
and emits `stale-branch-merge-conflict` to stderr. The Go binary's failure
classifier recognizes this string and maps it to `CatStaleBranchMergeConflict`.

**Safety**: Uses standard `git merge` (not `--force`). If push fails, the
merge commit stays local and the stage exits 1; nothing is undone. A merge that
was already open before this block ran is never merged over or aborted: a fully
staged one is committed and pushed, any other is left as found (#2755).


A clean merge produces a new tree; push the merge straight away and do not run
the full local gate on it (#2989). PR CI and `pr-merge` check the merged tree.

**Failure outcome**: When conflicts are detected, the stage exits with status 1.
The Go binary's failure classifier maps `stale-branch-merge-conflict` in stderr
to `CatStaleBranchMergeConflict` — a deterministic, non-retryable failure that
requires manual conflict resolution.

---

### Phase 2.5: Security Re-Scan

```bash
printf '<!-- phase:start name="security-rescan" index=6 total=13 stage="pr-create" -->\n'
```

**PURPOSE**: Defense-in-depth secret and vulnerability scan on changed files
before PR is submitted. This runs AFTER feature-validate to catch any secrets
introduced during implementation that tests wouldn't detect.

**Inputs**: `COMMIT_SHA` from `validate-{N}.json` (set in Phase 1). Scans files
changed between `BASE_BRANCH` and HEAD.

> **Read `_includes/security-and-scope.md` (same directory as this SKILL.md) now
> and follow its instructions before continuing this phase.**

### Phase 2.6: Scope Drift Gate

```bash
printf '<!-- phase:start name="scope-drift-gate" index=7 total=13 stage="pr-create" -->\n'
```

**PURPOSE**: For `type:docs` and `type:chore` issues, verify that modified files
fall within the configured allowlist. Out-of-scope changes indicate scope drift
— often caused by stale worktrees reverting recently-merged work alongside
legitimate scoped changes (see Issue #3040 and platform incident #840).

> **Read `_includes/security-and-scope.md` (same directory as this SKILL.md) now
> and follow its instructions before continuing this phase.**

The resulting `$SCOPE_DRIFT_STATUS` flows into `preflight_results.scope_drift_check`
when `pr-{N}.json` is written in Phase 4.

---

### Phase 3: Create PR via Go Binary

```bash
printf '<!-- phase:start name="create-pr" index=8 total=13 stage="pr-create" -->\n'
```

Create the PR using the Go binary. `nightgauge pr-stage title` renders the title
(#2846). The skill constructs the body (with correct closing keywords) and
handles reviewer assignment with self-reviewer guard. Append `KNOWLEDGE_SECTION` to the PR body when non-empty.

> **Read `_includes/create-and-ci.md` (same directory as this SKILL.md) now and
> follow its instructions before continuing this phase.**

### Phase 3.6: Verify PR Created

```bash
printf '<!-- phase:start name="verify-pr-created" index=9 total=13 stage="pr-create" -->\n'
```

**PURPOSE**: Post-condition verification that a PR actually exists on GitHub for
the feature branch. Protects against silent pr-create failures where the subagent
exits cleanly but never invokes `nightgauge forge pr create` (or the Go binary's `pr create`
command).

> **Read `_includes/create-and-ci.md` (same directory as this SKILL.md) now and
> follow its instructions before continuing this phase.**

**Result**: If PR was found, continue to Phase 3.5 normally. If not found, stage
exits with status 1, and the orchestrator classifies this as
`errorCategory: "pr-not-created"`.

---

### Phase 3.5: Snapshot CI Status

```bash
printf '<!-- phase:start name="monitor-ci-status" index=10 total=13 stage="pr-create" -->\n'
```

**PURPOSE**: Take **one** non-blocking reading of CI check status after PR
creation, classify anything already failing deterministically, and record what
we observed into Phase 4's `ci_monitoring` block so pr-merge has the context to
take over.

**HARD RULE — DO NOT WAIT FOR CI HERE (Issue #1531):**

> This phase makes a single snapshot call and returns. **Do not run
> `nightgauge ci wait`, a forge-CLI `pr checks --watch` loop, `sleep`, or any
> polling loop.**
> pr-create emits no commit, no new file, no phase marker and no tool call
> while it blocks, so the progress-runaway monitor's clocks all go cold exactly
> while the stage is behaving correctly and it is killed with its PR already
> open — the run is then reported as "PR creation failed" for a PR that exists
> and is running CI. `CI_FINAL_STATUS=pending` is the correct, complete answer
> when checks have not finished; pr-merge owns polling and auto-fix.

**HARD RULE — DO NOT FIX CI HERE (Issue #3666):**

> pr-create's only job is to open the PR and exit cleanly. **Do not regenerate
> golden images, do not run formatters, do not push fix commits, do not retry
> the agent to "make CI green"**, even if you can see exactly what is wrong.
> The pr-merge stage owns the auto-fix loop — it has its own budget, its own
> turn allowance, and the right retry/escalation machinery (Step 2.5 baseline
> detection, RALPH Loop, model escalation). When pr-create attempts inline
> fixes it duplicates that work against a tight budget and routinely trips
> the BudgetEnforcer on legitimate progress — see the #215 / $7 retro that
> motivated this rule.
>
> If you observe a CI failure here, the correct action is: record it in
> `CI_FAILURES_JSON`, leave a concise `CI_NOTES` describing what you saw,
> and exit. pr-merge will read your handoff and act.

**Activation**: Runs when `PR_NUMBER` is available from Phase 3. Skips
gracefully (sets `CI_MONITORED=false`) if it is absent.

**Headless safe**: No interactive prompts. All output is informational only.

> **Read `_includes/create-and-ci.md` (same directory as this SKILL.md) now and
> follow its instructions before continuing this phase.** It carries Steps
> 3.5.1–3.5.5, including the full list of `CI_*` variables passed to Phase 4.

---

### Phase 4: Write Context and Finalize Stage

```bash
printf '<!-- phase:start name="write-context" index=11 total=13 stage="pr-create" -->\n'
```

1. Write `pr-{N}.json` where **N is the ISSUE number**
   (NOT the PR number). The filename must match the issue being worked on (e.g.,
   `pr-870.json` for issue #870, even if the PR number is #876). Write it with
   `nightgauge layout write pipeline pr-{N}.json` (JSON on stdin; it creates
   the directory). Schema:

   ```json
   {
     "schema_version": "1.0",
     "issue_number": <N>,
     "pr_number": <PR_NUMBER>,
     "pr_url": "<full PR URL>",
     "title": "<PR title>",
     "base_branch": "<base branch>",
     "status": "open",
     "reviewers": ["<reviewer handles or empty array>"],
     "knowledge_path": "<path to knowledge directory or null>",
     "preflight_results": {
       "json_validation": "passed|failed|skipped",
       "yaml_validation": "passed|failed|skipped",
       "version_consistency": "passed|failed|skipped",
       "security_scan": "passed|failed|skipped",
       "coverage_check": "passed|failed|skipped",
       "scope_drift_check": "passed|failed|skipped"
     },
     "ci_monitoring": {
       "monitored": false,
       "monitor_duration_secs": 0,
       "final_status": "pending",
       "checks_summary": {
         "total": 0,
         "passed": 0,
         "failed": 0,
         "pending": 0
       },
       "failures": [],
       "timestamp": null,
       "notes": ""
     },
     "created_at": "<ISO 8601 timestamp>"
   }
   ```

   Populate `preflight_results` from Phase 2, Phase 2.5, and Phase 2.6 outcomes
   (`security_scan` from `$SECURITY_SCAN_STATUS`; `scope_drift_check` from
   `$SCOPE_DRIFT_STATUS`). Populate `ci_monitoring` from the `CI_*` variables
   set by Phase 3.5:

   ```bash
   CI_MONITORING_JSON=$(jq -n \
     --argjson monitored "${CI_MONITORED:-false}" \
     --argjson duration "${CI_MONITOR_DURATION:-0}" \
     --arg final_status "${CI_FINAL_STATUS:-pending}" \
     --argjson total "${CI_CHECKS_TOTAL:-0}" \
     --argjson passed "${CI_CHECKS_PASSED:-0}" \
     --argjson failed "${CI_CHECKS_FAILED:-0}" \
     --argjson pending "${CI_CHECKS_PENDING:-0}" \
     --argjson failures "$(printf '%s\n' "${CI_FAILURES_JSON:-[]}" | jq -c .)" \
     --arg timestamp "${CI_MONITOR_TIMESTAMP:-}" \
     --arg notes "${CI_NOTES:-}" \
     '{
       monitored: $monitored,
       monitor_duration_secs: $duration,
       final_status: $final_status,
       checks_summary: { total: $total, passed: $passed, failed: $failed, pending: $pending },
       failures: $failures,
       timestamp: (if $timestamp != "" then $timestamp else null end),
       notes: $notes
     }')
   ```

2. Signal stage completion via Go binary: `"$BINARY" project move-status "$ISSUE_NUMBER" "in-review" 2>/dev/null || true`
3. Return PR number/URL and status summary.
4. Provide next action:
   - `/nightgauge-pr-merge` if policy is auto-merge or post-approval merge
   - or manual review instructions

### Phase 5: Self-Assessment Epilogue

```bash
printf '<!-- phase:start name="self-assessment" index=12 total=13 stage="pr-create" -->\n'
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


---

## Decision Rules

- Prefer deterministic context files over free-form inference.
- Respect repository branch protection and merge policy.
- If validation context is absent, proceed with explicit warning.
- Never create PR from `main`/base branch.

## Failure Conditions

Fail with actionable remediation when:

- `dev-{N}.json` missing or invalid
- branch or base determination is ambiguous
- required tests fail under configured policy
- `nightgauge forge pr create` fails (include command + reason)

## Completion Checklist

- [ ] Required context loaded
- [ ] Preflight checks passed
- [ ] PR created with issue linkage
- [ ] Reviewers/metadata applied
- [ ] `pr-{N}.json` context file written
- [ ] Stage status signaled
- [ ] PR URL returned
