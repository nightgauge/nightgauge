---
name: adversarial-review
description: Re-review the current change with fresh-eyes critics that have no
  authoring context, each attacking it from a distinct lens (correctness,
  security, reuse/simplification, tests), then drive a fix loop until findings
  degrade to nitpicks. Use to harden a diff before opening or merging a PR, when
  asked for an adversarial/critical review, or from feature-dev/pr-create as an
  optional hardening gate.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.0.1"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Read Edit Write Glob Grep Bash Task
orchestration:
  mode: fanout
  phase: adversarial-critics
  ceiling: fanout
  units:
    - id: correctness
      role: critic
      promptRef: SKILL.md#critic-lens-prompt
    - id: security
      role: critic
      promptRef: SKILL.md#critic-lens-prompt
    - id: reuse-simplification
      role: critic
      promptRef: SKILL.md#critic-lens-prompt
    - id: tests
      role: critic
      promptRef: SKILL.md#critic-lens-prompt
  judge:
    mode: merge
    quorum: 1
    promptRef: SKILL.md#judge-and-fix-loop
disable-model-invocation: true
---

# Adversarial Review

> Fresh-eyes critique → fix loop. Runs on the capability-routed workflow spine
> (#3899): the `orchestration:` block fans out the critics and the judge merges;
> the prose **Workflow** below is the single-agent portability floor.

<!-- phase-registry: standalone-skill -->

## Description

Authors are blind to their own assumptions. This skill spawns **fresh-context**
critics over the current diff — each told to **refute**, not bless — from
distinct lenses, merges what survives, fixes it, and re-runs until only nitpicks
remain. It complements `feature-dev`'s quality fanout by adding a no-prior-context
adversarial pass.

## Invocation

| Tool        | Command                                        |
| ----------- | ---------------------------------------------- |
| Claude Code | `/nightgauge-adversarial-review [--base main]` |
| Codex       | `$nightgauge-adversarial-review`               |

## When to use

- Before opening/merging a non-trivial PR — harden the diff first.
- When the user asks for an adversarial, critical, or "try to break this" review.
- From `feature-dev` Phase 5 or `pr-create` as an optional hardening gate.

## Workflow

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


### Phase 1: Resolve the change under review

```bash
BASE="${BASE:-main}"
git diff --stat "$BASE"...HEAD
git diff "$BASE"...HEAD > /tmp/adversarial-diff.patch
```

If the diff is empty, report "nothing to review" and exit 0.

### Phase 2: Fan out fresh-eyes critics

Spawn one critic per lens (correctness, security, reuse/simplification, tests).
Each critic gets the diff and the **critic lens prompt** below with **no**
authoring context. When the orchestration spine is active these run as parallel
units; otherwise spawn them with `Task`. Each returns structured findings.

#### Critic lens prompt

> You are a fresh-eyes reviewer with **no prior context** on this change. Your
> job is to **refute** it from the **{lens}** lens. Read the diff and the files
> it touches. Report concrete, actionable findings only — each with `file:line`,
> a one-line claim, severity (`blocker` | `major` | `nitpick`), and the fix.
> Default to skepticism: if something is unclear, flag it. Do **not** restate
> what the code does or praise it. Return JSON:
> `{ "findings": [ { "file", "line", "claim", "severity", "fix" } ] }`.
>
> Lens focus:
>
> - **correctness** — logic errors, edge cases, error handling, race conditions,
>   contract/handoff violations.
> - **security** — input validation, authz, secret handling, injection, unsafe
>   defaults (see `standards/security.md`).
> - **reuse-simplification** — duplicated logic, reinvented helpers, dead code,
>   needless complexity, altitude.
> - **tests** — missing/weak assertions, untested branches, tests that can't
>   fail, brittle fixtures.

The severity scale is shared with the review passes on pull requests the
pipeline did not author; its definition is in
[docs/PR_REVIEW_PASSES.md](https://github.com/nightgauge/nightgauge/blob/main/docs/PR_REVIEW_PASSES.md#severity).

### Phase 3: Judge and fix loop

#### Judge and fix loop

1. **Merge + dedup** findings across critics by `file:line` + claim.
2. **Triage**: keep `blocker`/`major`; collect `nitpick`s separately.
3. **Fix** the kept findings (`Edit`), running the repo's build/tests after each
   batch — never report success on a failing check (#2779).
4. **Re-review**: re-run the critics on the new diff. Repeat until a round
   produces no new `blocker`/`major` (findings have "degraded to nitpicks") or a
   max of 3 rounds.
5. **Stop conditions are explicit** — log the round count and why you stopped
   (dry round vs. round cap). Never loop silently.

### Phase 4: Report

Summarize: rounds run, findings fixed (by lens/severity), remaining nitpicks
(listed, not silently dropped), and the final build/test status.

### Self-Assessment Epilogue

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


## Gotchas

- **Critics must have no authoring context.** The value is fresh eyes — passing
  them the plan/rationale defeats the purpose.
- **Prompt to refute, not to bless.** A reviewer asked "is this fine?" rubber
  stamps; one asked to "find what's wrong" finds it.
- **Bound the loop and say why you stopped.** Without an explicit dry-round /
  round-cap condition the fix loop can oscillate or run forever.
- **Don't silently drop nitpicks.** List them so the author decides — a silent
  truncation reads as "all clear" when it isn't.

### Cross-cutting gotchas (every Nightgauge skill)

> These apply to **all** skills. Skill-specific gotchas live above this line in
> each skill's `## Gotchas` section. Keep both terse: symptom → why → do-instead.
> When a new failure mode is root-caused (retro / failure-taxonomy), add it here
> or to the owning skill — Gotchas are the highest-signal part of a skill.

- **Bare `nightgauge` is not always on `PATH`.** Run the Phase 0 PREFLIGHT
  binary-discovery cascade first and prepend `dirname($BINARY)` to `PATH`.
  Skipping it surfaces as `command not found` only on some machines.
- **Never emit a foreground `sleep` wait loop.** The harness denies foreground
  `sleep` — an unbounded shell idiom that polls a completion marker in a
  `while`/`until` loop with no iteration cap — the tool call is rejected
  outright. To wait on a long-running background job, start it with the
  backgrounded-command form the harness provides (e.g. a Bash call with
  `run_in_background: true`) and then block on its completion notification /
  the Monitor tool instead of polling on a timer. Never invent a foreground
  poll loop to bridge the wait (#289).
- **Never call the forge CLI directly.** Use `nightgauge forge …`
  (`issue`/`pr`/`project`/`label`/`repo`/`auth`/`graphql`), never a bare
  `gh`/`glab`. The `no-direct-gh` lint fails CI on regressions; the abstraction
  is what lets `IB_FORGE=gitlab` work unchanged.
- **Headless skills must not block on input.** If `AskUserQuestion` is not in
  `allowed-tools`, the skill runs headless — make autonomous decisions or fail
  with a clear error. A blocked prompt hangs the whole pipeline run.
- **Write your handoff/context file before exiting — even on failure.** Pipeline
  stages that exit without their `{stage}-{N}.json` in the clone's pipeline
  state directory (`nightgauge layout path pipeline`) force the orchestrator
  onto a repo-blind deterministic fallback that may misreport state (issue 3114). Do not rely on the fallback.
- **An assertion that cannot go red is decoration.** A test that passes both
  before and after a fix constrains nothing — a regression test written for an
  SSH-push bug asserted "a push succeeds with no auth configured" and passed
  identically on the broken code, because a `file://` remote needs no
  credentials either way. Prove it: `cp <file> /tmp/<file>.bak`, restore the
  pre-fix behavior **from the copy**, confirm the new test FAILS, restore, and
  confirm it passes. Never `git checkout -- <file>` to back a fix out — on an
  uncommitted branch that destroys the fix. See
  [Vacuous Assertion](https://github.com/nightgauge/nightgauge/blob/main/docs/FAILURE_TAXONOMY.md#vacuous-assertion-the-test-that-cannot-go-red).
- **A green suite is a statement about the cases that RAN.** After adding
  assertions, check the reported PASS count rose by exactly the number you
  added; a suite can skip a case silently and still print "all tests passed".
- **Verify the issue's premise before implementing it.** An issue's stated cause
  can be flatly wrong — a request to delete a sweep that was never built, a
  claim that a failure category already classified an error when it fell through
  to `CatUnknown`, a "build it or delete it" fork where building was
  architecturally impossible. Check the claim against the tree first; if the
  premise is false, do the smaller correct thing and say so in the handoff.
- **Never report success when a step failed.** Swallowing a failed build/test
  lets a broken change flow downstream where it is caught later at higher cost
  (#2779). Surface the failure with its output.
- **Never push to `main`.** Use `feat/`/`fix/`/`docs/` branches; merges are
  manual squash only (no `--auto`, no `--admin`).
- **Production safety.** Never run `docker compose down -v` against a production
  stack — `-v` destroys `postgres_data`. Fix credential mismatches with
  `ALTER USER`, not a volume wipe.


## Source

Part of the [Nightgauge](https://github.com/nightgauge/nightgauge) Issue-to-PR pipeline.
See [docs/WORKFLOW_ORCHESTRATION.md](https://github.com/nightgauge/nightgauge/blob/main/docs/WORKFLOW_ORCHESTRATION.md) for the orchestration spine.
