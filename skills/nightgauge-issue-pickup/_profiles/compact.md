<!-- include: ../_shared/PIPELINE_CONTEXT.md -->
<!-- include: ../_shared/AUTONOMY_CONTRACT.md -->

# Issue Pickup (compact)

> Compact render profile (ADR 023 §Q5, #1660). Keeps the phase skeleton, the
> Phase 2.5–2.9 gates verbatim, the `issue-{N}.json` Output Contract and the
> never-push-to-main rule; the long procedure, rationale and worked examples
> are on-demand `Read` directives instead of inlined text. Content here MUST
> stay a strict subset of the base SKILL.md's and its includes' own wording —
> this file trims, it never invents new instructions.

Claim a GitHub issue, extract requirements, create a properly-named feature
branch and set up the development environment. Invoked as
`/nightgauge-issue-pickup [issue-number]`; `-i`/`--interactive` forces
interactive mode, `--label <label>` filters before selection. Full arguments,
prerequisites and configuration keys: Read `skills/nightgauge-issue-pickup/SKILL.md`
and `skills/_shared/CONFIGURATION.md` when needed.

## Gotchas

- **Validate arguments before any phase runs.** A missing/invalid issue number
  must fail fast — never part-execute (claim, branch) on bad input.
- **Re-run invariant.** On a rerun, re-check claim/branch state before acting —
  don't re-claim the issue or create a duplicate branch that already exists.
- **Context isolation — stop after handoff.** Issue-pickup must not continue into
  feature-planning in the same session. Write the context file and exit.
- See also the cross-cutting gotchas: Read `skills/_shared/GOTCHAS.md`.

## Workflow

### Phase Marker Protocol

At the start of each phase, emit a structured phase marker as an HTML comment on
its own line. Format:

`<!-- phase:start name="{phase-name}" index={N} total={T} stage="issue-pickup" -->`

This enables the orchestrator to track phase progress. Emit the marker BEFORE
any other output for that phase.

### CRITICAL: Argument Check (Before Any Other Phase)

**This check MUST happen FIRST, before any other workflow logic.** Parse
`$ARGUMENTS` immediately:

1. **If `$ARGUMENTS` contains a number** (e.g., "42", "123"): this is the issue
   number — **SKIP Phase 2 entirely** and **proceed directly to Phase 3**.
2. **If `$ARGUMENTS` contains `-i` or `--interactive`**: use Interactive Mode
   (Phase 2).
3. **If `$ARGUMENTS` is empty or contains only `--label`**: use auto-selection
   (Phase 2).

---

### Phase 0: Environment Preflight

**Read `skills/_shared/PREFLIGHT.md` now and follow it before continuing.**

---

### Phase 1: Validate Environment

```bash
printf '<!-- phase:start name="validate-environment" index=0 total=14 stage="issue-pickup" -->\n'
```

```bash
nightgauge forge auth status
```

If not authenticated: "Please run `nightgauge forge auth login` to authenticate."

```bash
git remote -v | grep -E "(github\.com|github\.)"
```

```bash
REPO="${NIGHTGAUGE_REPO:-$(nightgauge git repo-slug)}"
: "${REPO:?set NIGHTGAUGE_REPO or run inside a clone with an origin remote}"
nightgauge forge repo view --repo "$REPO" --json | jq -r .nameWithOwner
```

#### Step 1.4: Verify Repo Identity

<!-- include: ../_shared/REPO_IDENTITY_CHECK.md -->

---

### Phase 2: Issue Selection

```bash
printf '<!-- phase:start name="issue-selection" index=1 total=14 stage="issue-pickup" -->\n'
```

**DEFAULT BEHAVIOR**: When no issue number and no `-i` flag is provided, the
skill MUST use auto-selection mode. Interactive mode is ONLY used when
explicitly requested with `-i` or `--interactive`.

**Read `skills/_shared/AUTO_SELECTION.md` now and follow it before continuing
this phase.**

---

### Phase 2.5: Signal Stage Start

```bash
printf '<!-- phase:start name="signal-stage-start" index=2 total=14 stage="issue-pickup" -->\n'
```

**PURPOSE**: Move the issue's board status to `in-progress` to signal that
this stage has started.

**IMPORTANT**: This phase runs AFTER issue selection because we need the
`$ISSUE_NUMBER` variable.

```bash
ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
: "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
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
  "$BINARY" project move-status "$ISSUE_NUMBER" "in-progress" 2>/dev/null || true
fi
```

---

### Phase 2.7: Size Gate Preflight

```bash
printf '<!-- phase:start name="size-gate-preflight" index=3 total=14 stage="issue-pickup" -->\n'
```

Reject or soft-route issues that exceed pipeline size thresholds before
committing resources to branch creation and analysis.

#### Step 2.7.1: Read Configuration

```bash
CONFIG_PATH=".nightgauge/config.yaml"
GATE_ENABLED="true"

if [ -f "$CONFIG_PATH" ]; then
  # Check if size gate is explicitly disabled
  GATE_ENABLED_RAW=$(grep -A2 'size_gate:' "$CONFIG_PATH" | grep 'enabled:' | awk '{print $2}' | head -1)
  if [ "$GATE_ENABLED_RAW" = "false" ]; then
    GATE_ENABLED="false"
  fi
fi

if [ "$GATE_ENABLED" != "true" ]; then
  echo "Size gate: disabled in config — skipping"
  # Continue to Phase 3
fi
```

#### Step 2.7.2: Evaluate Issue Size via Go Binary

When `GATE_ENABLED=true` and `ISSUE_NUMBER` is set:

```bash
ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
: "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
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

if [ -z "$BINARY" ]; then
  echo "Size gate: nightgauge binary not found — skipping gate check"
  # Continue to Phase 3 (graceful degradation)
else
  # Capacity (#1655): judge the issue's size against the repository's target
  # model — `size-gate capacity` names it. A binary without that verb, or no
  # resolvable target, runs the check without capacity, as before.
  CAPACITY_TARGET=$("$BINARY" size-gate capacity --json 2>/dev/null || true)
  CAP_ADAPTER=$(jq -r '.adapter // empty' 2>/dev/null <<< "$CAPACITY_TARGET" || true)
  CAP_MODEL=$(jq -r '.model // empty' 2>/dev/null <<< "$CAPACITY_TARGET" || true)
  if [ -n "$CAP_ADAPTER" ] && [ -n "$CAP_MODEL" ]; then
    GATE_OUTPUT=$("$BINARY" size-gate check \
      --issue "$ISSUE_NUMBER" \
      --config "${CONFIG_PATH:-.nightgauge/config.yaml}" \
      --adapter "$CAP_ADAPTER" --model "$CAP_MODEL" 2>&1)
  else
    GATE_OUTPUT=$("$BINARY" size-gate check \
      --issue "$ISSUE_NUMBER" \
      --config "${CONFIG_PATH:-.nightgauge/config.yaml}" 2>&1)
  fi
  GATE_EXIT=$?

  if [ $GATE_EXIT -ne 0 ]; then
    # Gate rejected the issue
    echo "Size gate: REJECTED"
    echo "$GATE_OUTPUT"

    # Record outcome for learning loop visibility
    "$BINARY" outcome record \
      --issue "$ISSUE_NUMBER" \
      --stage "issue-pickup" \
      --outcome "failed" \
      --reason "issue-too-large: $GATE_OUTPUT" 2>/dev/null || true

    echo ""
    echo "To unblock: decompose this issue into smaller sub-issues and link them"
    echo "using the GitHub sub-issue API, then retry /nightgauge-issue-pickup"

    exit 1
  fi

  echo "Size gate: PASSED"
fi
```

#### Step 2.7.3: Soft-Route Option

When `pipeline.size_gate.routes.reject_action = "soft-route"` in config,
`size-gate check` exits 0 for an issue over the target model's **capacity**
when an entry of `pipeline.size_gate.routes.capacity_fallback_models` admits
its size, and prints `Soft-routed to: <model>`; the scheduler makes the same
move at dispatch. Soft-route applies to the capacity check only: an issue
rejected for LOC in the title or for size:L/XL without sub-issues still exits 1.

**Default behavior (`reject_action: fail`)**: Exit 1 stops the pipeline.

---

### Phase 2.8: Baseline-CI Dependency Gate

```bash
printf '<!-- phase:start name="baseline-ci-gate" index=4 total=14 stage="issue-pickup" -->\n'
```

Defer dispatch of issues whose acceptance criteria require promoting a CI check
on `main` when `main`'s recent runs of that check are failing.

#### Step 2.8.1: Read Configuration

```bash
GATE_ENABLED="true"
if [ -f "$CONFIG_PATH" ]; then
  GATE_ENABLED_RAW=$(grep -A2 'baseline_ci_gate:' "$CONFIG_PATH" | grep 'enabled:' | awk '{print $2}' | head -1)
  if [ "$GATE_ENABLED_RAW" = "false" ]; then
    GATE_ENABLED="false"
  fi
fi

if [ "$GATE_ENABLED" != "true" ]; then
  echo "Baseline-CI gate: disabled in config — skipping"
fi
```

#### Step 2.8.2: Evaluate Issue via Go Binary

When `GATE_ENABLED=true` and `ISSUE_NUMBER` is set:

```bash
ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
: "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
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

if [ -z "$BINARY" ]; then
  echo "Baseline-CI gate: nightgauge binary not found — skipping"
else
  GATE_OUTPUT=$("$BINARY" baseline-gate check \
    --issue "$ISSUE_NUMBER" \
    --config "${CONFIG_PATH:-.nightgauge/config.yaml}" \
    --json 2>&1)
  GATE_EXIT=$?

  if [ $GATE_EXIT -eq 1 ]; then
    # Defer: parse JSON for evidence and post deferral comment.
    WORKFLOW=$(printf '%s\n' "$GATE_OUTPUT" | jq -r '.workflow // "unknown"' 2>/dev/null)
    JOB=$(printf '%s\n' "$GATE_OUTPUT" | jq -r '.job // empty' 2>/dev/null)
    FAILED=$(printf '%s\n' "$GATE_OUTPUT" | jq -r '.failed_runs // 0' 2>/dev/null)
    SAMPLED=$(printf '%s\n' "$GATE_OUTPUT" | jq -r '.sampled_runs // 0' 2>/dev/null)
    REASON=$(printf '%s\n' "$GATE_OUTPUT" | jq -r '.reason // ""' 2>/dev/null)

    JOB_LINE=""
    [ -n "$JOB" ] && JOB_LINE=" job=\`$JOB\`"

    COMMENT_BODY="## Baseline-CI Dependency Gate — Deferred

This issue's acceptance criteria require promoting a CI check on \`main\`,
but \`main\`'s recent runs are currently red:

- **Workflow**: \`$WORKFLOW\`$JOB_LINE
- **Failed**: $FAILED of last $SAMPLED runs
- **Reason**: $REASON

The pipeline has paused this item ([\`baseline-ci-deferred\`](../../docs/FAILURE_TAXONOMY.md#infrastructure)).
The autonomous daemon resumes it once \`main\`'s recent runs of this workflow are
green again; \`nightgauge baseline-gate promote\` in the workspace releases it
immediately."

    nightgauge forge issue comment --subject-id "$ISSUE_NUMBER" -b "$COMMENT_BODY" 2>/dev/null || \
      echo "warning: failed to post deferral comment to #$ISSUE_NUMBER"

    "$BINARY" outcome record \
      --issue "$ISSUE_NUMBER" \
      --stage "issue-pickup" \
      --outcome "deferred" \
      --reason "[baseline-ci-deferred] $WORKFLOW $JOB failed $FAILED/$SAMPLED" 2>/dev/null || true

    echo "Baseline gate: DEFERRED"
    echo "signal=deferred"
    exit 0
  fi

  if [ $GATE_EXIT -eq 0 ]; then
    echo "Baseline gate: PASSED"
  else
    # Exit 2 = config/IO error — log and continue (best-effort, never over-defer).
    echo "warning: baseline-gate check failed with exit $GATE_EXIT, continuing"
  fi
fi
```

When the gate decides to defer, `signal=deferred` is printed to stdout so the
orchestrator short-circuits remaining stages. When the gate cannot extract a
workflow path from the AC text (decision `unparseable`), exit code is 0 and
dispatch proceeds. Queue and resume detail: Read
`_includes/issue-selection-and-gates.md` (Step 2.8.3) when needed.

---

### Phase 2.9: Native blockedBy Dependency Gate

```bash
printf '<!-- phase:start name="blocked-dependency-gate" index=5 total=14 stage="issue-pickup" -->\n'
```

Defer pickup of issues that have an OPEN native `blockedBy` dependency (the
blocker's PR is not merged). A controlled hold, not a failure — the item is
paused and automatically re-queued when its blockers close.

#### Step 2.9.1: Read Configuration

```bash
DEP_ENABLED="true"
DEP_MODE="warn"
if [ -f "$CONFIG_PATH" ]; then
  DEP_ENABLED=$(yq -r '.enforcement.dependencies.enabled // "true"' "$CONFIG_PATH" 2>/dev/null || echo "true")
  DEP_MODE=$(yq -r '.enforcement.dependencies.mode // "warn"' "$CONFIG_PATH" 2>/dev/null || echo "warn")
fi

# The deterministic deferral gate only runs in `block` mode. `warn`/`ignore`
# stay interactive and are handled by the DEPENDENCY_CHECKING include in Phase 3.
if [ "$DEP_ENABLED" != "true" ] || [ "$DEP_MODE" != "block" ]; then
  echo "Dependency gate: skipped (mode=$DEP_MODE)"
fi
```

#### Step 2.9.2: Evaluate Issue via Go Binary

When `DEP_ENABLED=true`, `DEP_MODE=block`, and `ISSUE_NUMBER` is set:

```bash
ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
: "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
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

if [ -z "$BINARY" ]; then
  echo "Dependency gate: nightgauge binary not found — skipping"
else
  GATE_OUTPUT=$("$BINARY" deps-gate check --issue "$ISSUE_NUMBER" --json 2>&1)
  GATE_EXIT=$?

  if [ $GATE_EXIT -eq 1 ]; then
    # Defer: parse the open blockers and post a deferral comment.
    BLOCKERS=$(printf '%s\n' "$GATE_OUTPUT" | jq -r '.open_dependencies[]? | "- #\(.number) \(.title)"' 2>/dev/null)
    REASON=$(printf '%s\n' "$GATE_OUTPUT" | jq -r '.reason // "blocked by open dependency"' 2>/dev/null)

    COMMENT_BODY="## Dependency Gate — Deferred

This issue has an open \`blockedBy\` dependency (the blocker's PR is not merged),
so pickup is deferred ([\`blocked-dependency\`](../../docs/FAILURE_TAXONOMY.md#infrastructure)):

$BLOCKERS

The pipeline has paused this item ([\`blocked_dependency\`](../../docs/FAILURE_TAXONOMY.md#infrastructure))
and will automatically resume dispatch when the blockers close
(\`deps-gate promote\` sweep, or the autonomous cascade). No operator action required."

    nightgauge forge issue comment --subject-id "$ISSUE_NUMBER" -b "$COMMENT_BODY" 2>/dev/null || \
      echo "warning: failed to post deferral comment to #$ISSUE_NUMBER"

    "$BINARY" outcome record \
      --issue "$ISSUE_NUMBER" \
      --stage "issue-pickup" \
      --outcome "deferred" \
      --reason "[blocked-dependency] $REASON" 2>/dev/null || true

    echo "Dependency gate: DEFERRED"
    echo "signal=deferred"
    exit 0
  fi

  if [ $GATE_EXIT -eq 0 ]; then
    echo "Dependency gate: PASSED"
  else
    # Exit 2 = config/IO error — log and continue (best-effort, never over-defer).
    echo "warning: deps-gate check failed with exit $GATE_EXIT, continuing"
  fi
fi
```

When the gate decides to defer, `signal=deferred` is printed to stdout so the
orchestrator short-circuits remaining stages, exactly mirroring Phase 2.8.
`warn` and `ignore` dependency modes are unchanged — they stay interactive and
are handled by the `DEPENDENCY_CHECKING` include in Phase 3. Queue and resume
detail: Read `_includes/issue-selection-and-gates.md` (Step 2.9.3) when needed.

---

### Phase 3: Issue Analysis

```bash
printf '<!-- phase:start name="issue-analysis" index=6 total=14 stage="issue-pickup" -->\n'
```

Fetch the full issue, parse its content, derive the change-detection/routing
decision, and produce the requirements summary.

> **Read `_includes/issue-analysis.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

Apply these two sub-steps after Step 3.1.4 and before Step 3.2 as the reference
file directs:

#### Step 3.1.5: Check for Epic Type

**Read `skills/_shared/EPIC_HANDLING.md` now and follow it.**

#### Step 3.1.6: Check Dependencies

**Read `skills/_shared/DEPENDENCY_CHECKING.md` now and follow it.**

---

### Phase 4: Read Git Workflow

```bash
printf '<!-- phase:start name="read-git-workflow" index=7 total=14 stage="issue-pickup" -->\n'
```

```bash
ls docs/GIT_WORKFLOW.md 2>/dev/null || ls docs/TFS_WORKFLOW.md 2>/dev/null
```

If docs/GIT_WORKFLOW.md exists, read it to extract branch naming conventions,
commit message format, and any special requirements. If no workflow docs, use
default conventions: `feat/<issue>-<desc>`, `fix/<issue>-<desc>`,
`docs/<issue>-<desc>`, `refactor/<issue>-<desc>`.

---

### Phase 5: Branch Creation

```bash
printf '<!-- phase:start name="branch-creation" index=8 total=14 stage="issue-pickup" -->\n'
```

Verify a clean working tree, then create the feature branch deterministically
via the Go binary (prefix/slug derivation, parent-epic detection, lazy
epic-branch creation, idempotent re-runs).

> **Read `_includes/branch-and-env.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.**

---

### Phase 6: Environment Setup

```bash
printf '<!-- phase:start name="environment-setup" index=9 total=14 stage="issue-pickup" -->\n'
```

```bash
git push -u origin <branch-name>
```

Offer self-assignment only when the configured forge exposes an assignment
command. The provider-neutral `forge issue edit` command edits by node ID and
does not support `--add-assignee`; do not pass GitHub CLI-only flags to it.
Optional date and sprint fields: Read `skills/_shared/DATE_AUTOMATION.md` and
`skills/_shared/SPRINT_ASSIGNMENT.md` when the project uses them.

---

### Phase 7: Output Summary

```bash
printf '<!-- phase:start name="output-summary" index=10 total=14 stage="issue-pickup" -->\n'
```

Present the final summary: issue number and title, type, branch, status
"Ready for development", and the next step (`/feature-planning`).

---

### Phase 8: Write Context File

```bash
printf '<!-- phase:start name="write-context" index=11 total=14 stage="issue-pickup" -->\n'
```

**PURPOSE**: Write structured context file for downstream pipeline skills.

> **Read `_includes/context-and-knowledge.md` (same directory as this SKILL.md) now and follow its instructions before continuing this phase.** Run Steps 8.1 and 8.2 there, then emit the `knowledge-scaffolding` marker below and continue through Steps 8.3–8.7.

#### Step 8.3: Knowledge Scaffolding (MANDATORY)

```bash
printf '<!-- phase:start name="knowledge-scaffolding" index=12 total=14 stage="issue-pickup" -->\n'
```

Scaffold the per-issue (and optionally workspace-level) knowledge base, populate
the routing field, verify the context file, and signal stage completion — all
detailed in the reference file already loaded for this phase (Steps 8.3–8.7).
Step 8.5 verifies the final context file:

```bash
ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
: "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
jq . ".nightgauge/pipeline/issue-${ISSUE_NUMBER}.json" > /dev/null && \
  echo "Context file written: .nightgauge/pipeline/issue-${ISSUE_NUMBER}.json"
```

**CRITICAL - CONTEXT ISOLATION RULES**: this skill terminates after Step 8.7.
**DO NOT ask** "Continue to Feature Planning?" or similar questions. The AI
agent MUST NOT continue to feature planning in this conversation.

---

<!-- include: ../_shared/BATCH_MODE.md -->

---

## Output Contract

This skill outputs `.nightgauge/pipeline/issue-{N}.json` for use by
downstream skills.

**Schema**: See
[docs/CONTEXT_ARCHITECTURE.md](../../../docs/CONTEXT_ARCHITECTURE.md) for full
schema documentation.

**Read by**: `/nightgauge-feature-planning`

---

### Phase 10: Self-Assessment Epilogue

```bash
printf '<!-- phase:start name="self-assessment" index=13 total=14 stage="issue-pickup" -->\n'
```

**Read `skills/_shared/SELF_ASSESSMENT_EPILOGUE.md` now and follow it before
continuing this phase.**

---

## Error Handling

| Condition             | Action                                                                                                                                                         |
| --------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Issue not found       | Display error with issue number, suggest verifying access. Hint: `nightgauge forge issue view <number> --repo $REPO --web`                                     |
| Branch already exists | Switch to existing branch (`git checkout <branch-name>`). Do NOT prompt — this is a re-run. Continue through all phases to ensure `issue-{N}.json` is written. |
| Authentication failed | Display: "Forge auth not configured. Run: `nightgauge forge auth login`"                                                                                       |
