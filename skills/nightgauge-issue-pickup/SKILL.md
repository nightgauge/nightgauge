---
name: nightgauge-issue-pickup
description: Claim a GitHub issue, extract requirements, and set up the development
  environment. Use at the start of any issue-based development work.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.19.1"
  source: https://github.com/nightgauge/nightgauge
allowed-tools: Read Write Edit Glob Grep Bash Task
context: fork
agent: pipeline-researcher
model: haiku
inputs: []
outputs:
  - .nightgauge/pipeline/issue-{N}.json
---

<!-- include: ../_shared/PIPELINE_CONTEXT.md -->
<!-- include: ../_shared/AUTONOMY_CONTRACT.md -->

# Issue Pickup

> Claim a GitHub issue, extract requirements, and set up development

## Description

This skill starts the development pipeline by:

1. Fetching and analyzing a GitHub issue
2. Extracting structured requirements
3. Creating a properly-named feature branch
4. Setting up the development environment

## Invocation

| Tool           | Command                                                |
| -------------- | ------------------------------------------------------ |
| Claude Code    | `/nightgauge-issue-pickup [issue-number]` (via plugin) |
| OpenAI Codex   | `$nightgauge-issue-pickup [issue-number]`              |
| GitHub Copilot | Invoke via Agent Skills                                |
| Cursor         | Invoke via Agent Skills                                |

## Arguments

```bash
# Pick up a specific issue
/nightgauge-issue-pickup 42

# Auto-select highest priority issue (DEFAULT behavior)
/nightgauge-issue-pickup

# Force interactive mode (list and choose)
/nightgauge-issue-pickup -i
/nightgauge-issue-pickup --interactive

# Filter by label
/nightgauge-issue-pickup --label "ready-for-dev"
```

The `$ARGUMENTS` variable contains everything after the skill name.

### Flags

| Flag                  | Description                                              |
| --------------------- | -------------------------------------------------------- |
| `-i`, `--interactive` | Force interactive mode (list issues and choose manually) |
| `--label <label>`     | Filter issues by label before selection                  |

## Prerequisites

- **Nightgauge binary**: Must have `nightgauge` binary installed and configured
- **Git repository**: Must be in a git repository with GitHub remote
- **docs/GIT_WORKFLOW.md**: Should exist for branch naming conventions (or will
  use defaults)

## Philosophy

- **Issue-driven development** — Every change starts with an issue
- **Traceability** — Branch names include issue numbers
- **Structured requirements** — Extract clear requirements before coding
- **Documentation-first** — Read workflow docs before creating branches

---

<!-- include: ../_shared/CONFIGURATION.md -->

---

## Supporting files (load on demand)

- `skills/nightgauge-issue-pickup/_includes/issue-selection-and-gates.md` — read in Phases 2.5, 2.7, 2.8 (signal stage start, size gate, baseline-CI gate)
- `skills/nightgauge-issue-pickup/_includes/issue-analysis.md` — read in Phase 3 (fetch, parse, route, requirements summary)
- `skills/nightgauge-issue-pickup/_includes/branch-and-env.md` — read in Phase 5 (deterministic branch creation)
- `skills/nightgauge-issue-pickup/_includes/context-and-knowledge.md` — read in Phases 8, 9 (write context file, knowledge scaffolding)

---

## Gotchas

- **Validate arguments before any phase runs.** A missing/invalid issue number
  must fail fast — never part-execute (claim, branch) on bad input.
- **Re-run invariant.** On a rerun, re-check claim/branch state before acting —
  don't re-claim the issue or create a duplicate branch that already exists.
- **Context isolation — stop after handoff.** Issue-pickup must not continue into
  feature-planning in the same session. Write the context file and exit.
- See also the cross-cutting gotchas in
  [`_shared/GOTCHAS.md`](../_shared/GOTCHAS.md).

## Workflow

### Phase Marker Protocol

At the start of each phase, emit a structured phase marker as an HTML comment on
its own line. Format:

`<!-- phase:start name="{phase-name}" index={N} total={T} stage="issue-pickup" -->`

This enables the orchestrator to track phase progress. Emit the marker BEFORE
any other output for that phase.

### CRITICAL: Argument Check (Before Any Other Phase)

**This check MUST happen FIRST, before any other workflow logic.**

Parse `$ARGUMENTS` immediately:

```bash
# $ARGUMENTS contains everything after the skill name
# Example: "/nightgauge:issue-pickup 42" → $ARGUMENTS = "42"
# Example: "/nightgauge:issue-pickup -i" → $ARGUMENTS = "-i"
# Example: "/nightgauge:issue-pickup" → $ARGUMENTS = ""
```

**Decision tree:**

1. **If `$ARGUMENTS` contains a number** (e.g., "42", "123"):
   - This is the issue number provided by the user
   - **SKIP Phase 2 entirely** - do NOT ask for issue selection
   - **Proceed directly to Phase 3** with this issue number

2. **If `$ARGUMENTS` contains `-i` or `--interactive`**:
   - Use Interactive Mode (Phase 2, Step 2.5)

3. **If `$ARGUMENTS` is empty or contains only `--label`**:
   - Use auto-selection (Phase 2)

| Command                                    | $ARGUMENTS          | Action                          |
| ------------------------------------------ | ------------------- | ------------------------------- |
| `/nightgauge:issue-pickup 42`              | `"42"`              | Skip to Phase 3 with issue #42  |
| `/nightgauge:issue-pickup 123 --label bug` | `"123 --label bug"` | Skip to Phase 3 with issue #123 |
| `/nightgauge:issue-pickup -i`              | `"-i"`              | Use interactive mode            |
| `/nightgauge:issue-pickup`                 | `""`                | Use auto-selection              |

---

### Phase 0: Environment Preflight

<!-- include: ../_shared/PREFLIGHT.md -->

---

### Phase 1: Validate Environment

```bash
printf '<!-- phase:start name="validate-environment" index=0 total=14 stage="issue-pickup" -->\n'
```

#### Step 1.1: Check Forge Authentication

```bash
nightgauge forge auth status
```

If not authenticated: "Please run `nightgauge forge auth login` to authenticate."

#### Step 1.2: Verify Git Repository

```bash
git remote -v | grep -E "(github\.com|github\.)"
```

#### Step 1.3: Get Repository Info

```bash
nightgauge forge repo view --repo $REPO --json nameWithOwner -q .nameWithOwner
```

#### Step 1.4: Verify Repo Identity

<!-- include: ../_shared/REPO_IDENTITY_CHECK.md -->

---

### Phase 2: Issue Selection

```bash
printf '<!-- phase:start name="issue-selection" index=1 total=14 stage="issue-pickup" -->\n'
```

#### Step 2.1: Parse Arguments

Check `$ARGUMENTS` for issue number, `-i`/`--interactive` flag, or `--label`.

**DEFAULT BEHAVIOR**: When no issue number and no `-i` flag is provided, the
skill MUST use auto-selection mode. Interactive mode is ONLY used when
explicitly requested with `-i` or `--interactive`.

<!-- include: ../_shared/AUTO_SELECTION.md -->

---

### Phase 2.5: Signal Stage Start

```bash
printf '<!-- phase:start name="signal-stage-start" index=2 total=14 stage="issue-pickup" -->\n'
```

Signal the stage start in state.json (runs after issue selection so
`$ISSUE_NUMBER` is set).

> **Read `skills/nightgauge-issue-pickup/_includes/issue-selection-and-gates.md` now and follow its instructions before continuing this phase.**

---

### Phase 2.7: Size Gate Preflight

```bash
printf '<!-- phase:start name="size-gate-preflight" index=3 total=14 stage="issue-pickup" -->\n'
```

Reject or soft-route issues that exceed pipeline size thresholds before
committing resources to branch creation and analysis.

> **Read `skills/nightgauge-issue-pickup/_includes/issue-selection-and-gates.md` now and follow its instructions before continuing this phase.**

---

### Phase 2.8: Baseline-CI Dependency Gate

```bash
printf '<!-- phase:start name="baseline-ci-gate" index=4 total=14 stage="issue-pickup" -->\n'
```

Defer dispatch of issues whose acceptance criteria require promoting a CI check
on `main` when `main`'s recent runs of that check are failing.

> **Read `skills/nightgauge-issue-pickup/_includes/issue-selection-and-gates.md` now and follow its instructions before continuing this phase.**

---

### Phase 2.9: Native blockedBy Dependency Gate

```bash
printf '<!-- phase:start name="blocked-dependency-gate" index=5 total=14 stage="issue-pickup" -->\n'
```

Defer pickup of issues that have an OPEN native `blockedBy` dependency (the
blocker's PR is not merged). A controlled hold, not a failure — the item is
paused and automatically re-queued when its blockers close.

> **Read `skills/nightgauge-issue-pickup/_includes/issue-selection-and-gates.md` now and follow its instructions before continuing this phase.**

---

### Phase 3: Issue Analysis

```bash
printf '<!-- phase:start name="issue-analysis" index=6 total=14 stage="issue-pickup" -->\n'
```

Fetch the full issue, parse its content, derive the change-detection/routing
decision, and produce the requirements summary.

> **Read `skills/nightgauge-issue-pickup/_includes/issue-analysis.md` now and follow its instructions before continuing this phase.**

The following two sub-steps stay in this body (they expand shared `_shared`
includes) — apply them after Step 3.1.4 and before Step 3.2 as the reference
file directs:

#### Step 3.1.5: Check for Epic Type

<!-- include: ../_shared/EPIC_HANDLING.md -->

#### Step 3.1.6: Check Dependencies

<!-- include: ../_shared/DEPENDENCY_CHECKING.md -->

---

### Phase 4: Read Git Workflow

```bash
printf '<!-- phase:start name="read-git-workflow" index=7 total=14 stage="issue-pickup" -->\n'
```

#### Step 4.1: Check for Workflow Documentation

```bash
ls docs/GIT_WORKFLOW.md 2>/dev/null || ls docs/TFS_WORKFLOW.md 2>/dev/null
```

#### Step 4.2: If docs/GIT_WORKFLOW.md Exists

Read it to extract branch naming conventions, commit message format, and any
special requirements.

#### Step 4.3: If No Workflow Docs

Use default conventions: `feat/<issue>-<desc>`, `fix/<issue>-<desc>`,
`docs/<issue>-<desc>`, `refactor/<issue>-<desc>`.

---

### Phase 5: Branch Creation

```bash
printf '<!-- phase:start name="branch-creation" index=8 total=14 stage="issue-pickup" -->\n'
```

Verify a clean working tree, then create the feature branch deterministically
via the Go binary (prefix/slug derivation, parent-epic detection, lazy
epic-branch creation, idempotent re-runs).

> **Read `skills/nightgauge-issue-pickup/_includes/branch-and-env.md` now and follow its instructions before continuing this phase.**

---

### Phase 6: Environment Setup

```bash
printf '<!-- phase:start name="environment-setup" index=9 total=14 stage="issue-pickup" -->\n'
```

#### Step 6.1: Push Branch to Remote

```bash
git push -u origin <branch-name>
```

#### Step 6.2: Assign Issue (Optional)

Offer self-assignment. If yes: `nightgauge forge issue edit <number> --repo $REPO --add-assignee @me`

---

<!-- include: ../_shared/DATE_AUTOMATION.md -->

---

<!-- include: ../_shared/SPRINT_ASSIGNMENT.md -->

---

### Phase 7: Output Summary

```bash
printf '<!-- phase:start name="output-summary" index=10 total=14 stage="issue-pickup" -->\n'
```

Present final summary:

```
Issue:    #<number> - <title>
Type:     <bug|feature|docs|refactor>
Branch:   <branch-name>
Status:   Ready for development

## Next Steps
1. Run `/feature-planning` to design the implementation
2. Or start coding if the approach is clear
```

---

### Phase 8: Write Context File

```bash
printf '<!-- phase:start name="write-context" index=11 total=14 stage="issue-pickup" -->\n'
```

**PURPOSE**: Write structured context file for downstream pipeline skills.
All data is available in shell variables at this point — write the context
inline using `jq -n` for safe JSON construction.

> **Read `skills/nightgauge-issue-pickup/_includes/context-and-knowledge.md` now and follow its instructions before continuing this phase.** Run Steps 8.1 and 8.2 there, then emit the `knowledge-scaffolding` marker below and continue through Steps 8.3–8.7.

#### Step 8.3: Knowledge Scaffolding (MANDATORY)

```bash
printf '<!-- phase:start name="knowledge-scaffolding" index=12 total=14 stage="issue-pickup" -->\n'
```

Scaffold the per-issue (and optionally workspace-level) knowledge base, populate
the routing field, verify the context file, and signal stage completion — all
detailed in the reference file already loaded for this phase (Steps 8.3–8.7).

---

<!-- include: ../_shared/BATCH_MODE.md -->

---

## Output Contract

This skill outputs `.nightgauge/pipeline/issue-{N}.json` for use by
downstream skills.

**Schema**: See
[docs/CONTEXT_ARCHITECTURE.md](../../docs/CONTEXT_ARCHITECTURE.md) for full
schema documentation.

**Read by**: `/nightgauge-feature-planning`

---

### Phase 10: Self-Assessment Epilogue

```bash
printf '<!-- phase:start name="self-assessment" index=13 total=14 stage="issue-pickup" -->\n'
```

<!-- include: ../_shared/SELF_ASSESSMENT_EPILOGUE.md -->

---

## Error Handling

| Condition             | Action                                                                                                                                                         |
| --------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Issue not found       | Display error with issue number, suggest verifying access. Hint: `nightgauge forge issue view <number> --repo $REPO --web`                                     |
| Branch already exists | Switch to existing branch (`git checkout <branch-name>`). Do NOT prompt — this is a re-run. Continue through all phases to ensure `issue-{N}.json` is written. |
| Authentication failed | Display: "Forge auth not configured. Run: `nightgauge forge auth login`"                                                                                       |
