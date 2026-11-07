---
name: assess-epic
description: Analyze an epic's sub-issues and recommend batch vs sequential pipeline
  processing strategy. Use before starting work on an epic to determine optimal
  execution approach.
license: Apache-2.0
metadata:
  author: nightgauge
  version: "1.0.2"
  source: https://github.com/nightgauge/nightgauge
  chainable: true
allowed-tools: Read Bash Glob Grep
---

# Nightgauge Assess Epic

## Description

Analyzes an epic's sub-issues to determine whether they should be processed via
batch, sequential, or hybrid pipeline execution. Extracts file overlap, size
variance, and dependency signals from sub-issues, then computes a deterministic
strategy recommendation with confidence scoring and estimated savings.

The recommendation is advisory: every sub-issue still runs its own pipeline and
PR into the epic branch, and `epic-complete` opens the single epic PR. There is
no multi-issue pipeline run (#366).

**Use Cases:**

- Planning pipeline execution strategy before starting an epic
- Evaluating whether sub-issues have sufficient file overlap for batching
- Estimating token and cost savings from parallel batch execution
- Identifying dependency chains that require sequential processing

**When to Use:**

- Before queuing an epic for pipeline processing
- When an epic has 3+ sub-issues and you want to optimize execution
- After sub-issues change (new issues added, scope changes) to refresh strategy
- When deciding the order in which to queue an epic's sub-issues

## Invocation

| Tool        | Command                                 |
| ----------- | --------------------------------------- |
| Claude Code | `/nightgauge:assess-epic <epic-number>` |
| Copilot     | Invoke via Agent Skills extension       |
| Cursor      | Run via Agent Skills or direct SKILL.md |
| Headless    | `claude -p "/nightgauge:assess-epic"`   |

## Arguments

| Argument        | Description                     | Required | Default |
| --------------- | ------------------------------- | -------- | ------- |
| `<epic-number>` | GitHub issue number of the epic | Yes      | -       |

### Examples

```bash
# Assess epic #799's execution strategy
/nightgauge:assess-epic 799

# Assess a different epic
/nightgauge:assess-epic 1672
```

---

## Prerequisites

- `gh` CLI installed and authenticated (`gh auth login`)
- `jq` installed for JSON processing
- Epic issue must exist with the `type:epic` label
- Sub-issues must be linked via GitHub's native sub-issue feature (GraphQL
  `subIssues` API)
- Git repository with GitHub remote

**CRITICAL**: This skill runs headless. Do NOT use AskUserQuestion. Make
autonomous decisions or fail with clear error.

---

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


---

### Phase 1: Validate Input and Fetch Epic

#### Step 1.1: Validate Epic Number

```bash
EPIC_NUMBER="${1:?ERROR: Epic number is required. Usage: /nightgauge:assess-epic <epic-number>}"

# Validate numeric input
if ! printf '%s\n' "$EPIC_NUMBER" | grep -qE '^[0-9]+$'; then
  echo "ERROR: Epic number must be a positive integer (got: $EPIC_NUMBER)"
  exit 1
fi
```

#### Step 1.2: Fetch Epic Issue

```bash
EPIC_JSON=$(gh issue view "$EPIC_NUMBER" --json number,title,labels,state,body 2>&1)
if [ $? -ne 0 ]; then
  echo "ERROR: Could not fetch issue #$EPIC_NUMBER. Verify the issue exists."
  echo "  Try: gh issue view $EPIC_NUMBER"
  exit 1
fi

# Verify it has the type:epic label
IS_EPIC=$(printf '%s\n' "$EPIC_JSON" | jq -r '.labels[]?.name' | grep -c '^type:epic$' || echo "0")
if [ "$IS_EPIC" -eq 0 ]; then
  echo "ERROR: Issue #$EPIC_NUMBER does not have the 'type:epic' label."
  echo "  This command only works on epic issues."
  exit 1
fi

EPIC_TITLE=$(printf '%s\n' "$EPIC_JSON" | jq -r '.title')
echo "Epic #$EPIC_NUMBER: $EPIC_TITLE"
```

#### Step 1.3: Fetch Sub-Issues

The shared epic-handling reference, `skills/_shared/EPIC_HANDLING.md`, covers
sub-issue detection (read it directly — `assess-epic` is not a pipeline stage,
so the composer does not expand the directive below).

### Epic Detection and Handling

## Contents

- [Epic Branch Lifecycle](#epic-branch-lifecycle)
- [Epic Setup Requirements](#epic-setup-requirements)
- [Cross-Epic Blocking](#cross-epic-blocking-mandatory)
- [Detect Epic Type](#detect-epic-type)
- [If Epic Detected](#if-epic-detected)
- [Auto-Selection Exclusion](#auto-selection-exclusion)
- [Epic Completion Check](#epic-completion-check-post-merge)

**PURPOSE**: Detect epic issues and handle them appropriately. Epics are
tracking issues with the `type:epic` label that organize sub-issues — they are
not directly actionable work items.

#### Epic Branch Lifecycle

Every sub-issue of an epic uses an epic branch as its merge target:

1. **First sub-issue pickup**: Creates `epic/{PARENT}-{slug}` from default
   branch (lazy creation in issue-pickup Step 5.4)
2. **Subsequent sub-issues**: Branch from existing epic branch
3. **Sub-issue PRs**: Target epic branch (not main). Use `Part of #EPIC` instead
   of `Closes #ISSUE` to avoid premature issue closure
4. **Last sub-issue merges**: pr-merge detects epic branch target, runs
   completion check, creates epic→main PR (Step 2.5)
5. **Epic PR merges**: Epic branch deleted by GitHub PR cleanup

The epic branch belongs to the epic's own repository: `epic/{PARENT}-*` names
issue `#{PARENT}` of the repository it is pushed to. A sub-issue whose epic
lives in **another** repository has no epic branch. It branches from, and its
PR targets, its own repository's default branch, and `git branch-create`
reports `parent_issue` and `epic_branch` as `null` for it.

This workflow is **STATELESS** — relies on git remote state (`git ls-remote`)
and GitHub API (GraphQL parent detection, issue state). Works regardless of
queuing method (batch, individual, mixed).

#### Epic Setup Requirements

When creating epics with sub-issues, ALL of the following are required:

1. **Link sub-issues** via `addSubIssue` GraphQL mutation (not body text)
2. **Add to project board** — epic AND all sub-issues via `addProjectV2ItemById`
3. **Set Status field** — epic and **all sub-issues to "Ready"**. The pipeline
   uses `blockedBy` to enforce ordering, NOT board status. Only use "Backlog"
   for issues genuinely not ready for work.
4. **Set intra-epic blocking** (for sequential phases) via `addBlockedBy`
5. **Set cross-epic blocking** when this epic depends on another epic

Without steps 2-3, issues are invisible in the extension's board views. Without
steps 4-5, the pipeline may execute issues out of order.

#### Cross-Epic Blocking (MANDATORY)

When an epic depends on work from another epic, you MUST set blocking
relationships at **both** levels:

1. **Epic-to-epic**: The dependent epic is blocked by the prerequisite epic.
   This lets the scheduler skip the entire epic's sub-issues with a single
   parent check.
2. **Sub-issue-to-sub-issue**: The dependent epic's root sub-issues (those with
   no intra-epic blockers) are blocked by the prerequisite epic's leaf
   sub-issues (those that no other sub-issue depends on).

**Example**: Epic B (Pipeline Integration) depends on Epic A (Schema
Foundation):

```
Epic A (#1672) ──blocks──→ Epic B (#1678)

Epic A leaf sub-issues:
  #1675 (KnowledgeService)  ──blocks──→  #1679 (first root of Epic B)
  #1676 (wiki-link resolver) ──blocks──→  #1679
```

The scheduler checks both direct `blockedBy` on each issue AND whether the
issue's parent epic has open `blockedBy` entries. Without cross-epic blocking,
the pipeline will execute dependent work before its prerequisites exist.

**Detection**: When creating an epic, scan its body for references to other
epics ("builds on #NNN", "depends on", "requires", "after epic #NNN"). See
`issue-create` SKILL.md Phase 3.5 Step 1 for the full procedure.

Each sub-issue appears in exactly one board tab matching its project board
status. Blocked sub-issues display with 🔒 lock icons in the Ready tab.

#### Detect Epic Type

```bash
LABELS=$(printf '%s\n' "$ISSUE_JSON" | jq -r '.labels[].name' 2>/dev/null)
if printf '%s\n' "$LABELS" | grep -q "^type:epic$"; then
  IS_EPIC=true
fi
```

#### If Epic Detected

1. **Check if all sub-issues are complete** using Go binary `epic check-completion`:

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
   [ -n "$BINARY" ] && export PATH="$(dirname "$BINARY"):$PATH"
   if [ -z "$BINARY" ]; then
     echo "ERROR: nightgauge binary not found via NIGHTGAUGE_BIN, PATH or ~/go/bin" >&2
     exit 1
   fi
   EPIC_STATUS=$("$BINARY" epic check-completion "$ISSUE_NUMBER" --json 2>/dev/null || echo '{"complete":false}')
   EPICS_CLOSED=$(printf '%s\n' "$EPIC_STATUS" | jq 'if .complete == true then 1 else 0 end')
   if [ "$EPICS_CLOSED" -gt 0 ]; then
     echo "Epic #$ISSUE_NUMBER had all sub-issues completed. Auto-closed and synced to Done."
     exit 1
   fi
   ```

2. **Fetch sub-issues** with the binary, which reads the whole list, page by
   page, or exits non-zero; it never returns part of it as the whole (#1701).
   A fixed `subIssues(first: N)` read stops at its first page with no sign
   that more exist.

   ```bash
   ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
   : "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
   # stdout only: a warning on stderr must not land in the JSON.
   if ! RESULT=$(nightgauge issue list --epic "$ISSUE_NUMBER" --json); then
     echo "ERROR: could not read the sub-issues of epic #$ISSUE_NUMBER" >&2
     exit 1
   fi
   # Each entry carries its own repo; the binary writes subIssues as null
   # when there are none.
   printf '%s\n' "$RESULT" | jq -r '.subIssues // [] | .[] |
     "#\(.number) - \(.title) [\(.state)] (\(.repo))"'
   ```

3. **Display Epic message**: Show that the issue is an Epic and list its
   sub-issues with their labels/status.

4. **Offer sub-issue selection**: Let user pick a ready sub-issue instead.

5. **If user selects a sub-issue**: Proceed with that issue number.

6. **If user cancels**: Exit with non-zero code to halt pipeline (`exit 1`).

   **CRITICAL**: The `exit 1` ensures the orchestrator receives a non-zero exit
   code, which triggers `success: false` in the skill result.

#### Auto-Selection Exclusion

Epics are **automatically excluded** from all tiers of the auto-selection
algorithm via the jq filter:

```bash
select(.labels | map(.name) | index("type:epic") | not)
```

#### Epic Completion Check (Post-Merge)

After merging a PR, check if the completed issue's parent epic is ready to
close. This check triggers in two scenarios:

1. **Epic branch merge**: pr-merge detects `MERGED_INTO` starts with `epic/`,
   extracts the epic number from the branch name, and runs the check directly
2. **Non-epic-branch merge**: The standard check below runs using the issue
   number to find parent epics

Both paths converge at Step 2.5 (epic branch PR creation) when all sub-issues
are complete.

**Step 1: Check completion status (check-only mode)**

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
[ -n "$BINARY" ] && export PATH="$(dirname "$BINARY"):$PATH"
if [ -z "$BINARY" ]; then
  echo "ERROR: nightgauge binary not found via NIGHTGAUGE_BIN, PATH or ~/go/bin" >&2
  exit 1
fi
EPIC_RESULT=$("$BINARY" epic check-completion "$ISSUE_NUMBER" --json 2>/dev/null || echo '{"complete":false}')
# Determine if epic is ready to close
EPICS_READY=$(printf '%s\n' "$EPIC_RESULT" | jq 'if .complete == true then 1 else 0 end')
EPIC_ACTION=$( [ "$EPICS_READY" -gt 0 ] && echo "ready-to-close" || echo "not-ready" )
```

**Step 2: Generate summary if epic is ready to close**

If `action == "ready-to-close"`:

```bash
EPIC_NUMBER=$(printf '%s\n' "$EPIC_RESULT" | jq -r '.epicNumber')
EPIC_TITLE=$(printf '%s\n' "$EPIC_RESULT" | jq -r '.title')
# a+b. Classify the tier and generate the summary in one deterministic call
#      (no AI tokens). Tiers: minimal, standard, detailed.
SUMMARY_JSON=$(nightgauge epic summary "$EPIC_NUMBER" --json 2>/dev/null || echo '{}')
TIER=$(printf '%s\n' "$SUMMARY_JSON" | jq -r '.tier // "minimal"')
SUMMARY=$(printf '%s\n' "$SUMMARY_JSON" | jq -r '.summary // empty')

# c. Post summary as issue comment (if generated)
if [ -n "$SUMMARY" ]; then
  gh issue comment "$EPIC_NUMBER" --body "$SUMMARY" 2>/dev/null || true
fi

# d. For the detailed tier: commit summary to docs/epics/{N}-{slug}.md
if [ "$TIER" = "detailed" ] && [ -n "$SUMMARY" ]; then
  SLUG=$(echo "$EPIC_TITLE" | tr '[:upper:]' '[:lower:]' | \
    sed 's/[^a-z0-9 -]//g' | tr ' ' '-' | sed 's/--*/-/g' | sed 's/^-//;s/-$//' | \
    cut -c1-40 | sed 's/-$//')
  SUMMARY_FILE="docs/epics/${EPIC_NUMBER}-${SLUG}.md"
  mkdir -p docs/epics
  echo "$SUMMARY" > "$SUMMARY_FILE"
  git add "$SUMMARY_FILE"
  git commit -m "docs(#${EPIC_NUMBER}): add epic completion summary" || true
  git push origin HEAD 2>/dev/null || true
fi
```

**Step 2.5: Create Epic Branch PR — DETERMINISTIC SCRIPT**

**CRITICAL**: Use the Go binary `pr create` command to create the epic branch PR.

```bash
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
if [ -z "$BINARY" ]; then
  echo "ERROR: nightgauge binary not found via NIGHTGAUGE_BIN, PATH or ~/go/bin" >&2
  exit 1
fi
# Find epic branch
EPIC_BRANCH=$(git branch -r --list "origin/epic/${EPIC_NUMBER}-*" 2>/dev/null | head -1 | sed 's|origin/||; s/^ *//')
if [ -z "$EPIC_BRANCH" ]; then
  echo "WARNING: No epic branch found for epic #$EPIC_NUMBER — skipping PR creation" >&2
else
  PR_RESULT=$("$BINARY" pr create     --title "feat(#${EPIC_NUMBER}): ${EPIC_TITLE}"     --head "$EPIC_BRANCH"     --base main     --body "Epic #${EPIC_NUMBER} completion: all sub-issues are closed."     --json 2>/dev/null) || true
  PR_ACTION=$(printf '%s\n' "$PR_RESULT" | jq -r '.action // "created"')
  PR_URL=$(printf '%s\n' "$PR_RESULT" | jq -r '.prUrl // ""')

  if [ -n "$PR_URL" ] || [ "$PR_ACTION" = "created" ] || [ "$PR_ACTION" = "already-exists" ]; then
    echo "Epic PR ready: $PR_URL"
    EPIC_BRANCH_PR_CREATED=true
  else
    echo "WARNING: Go binary pr create returned action=$PR_ACTION"
  fi
fi
```

**Step 3: Close the epic** (only when no epic branch workflow)

Skipped if `EPIC_BRANCH_PR_CREATED=true` (Step 2.5 handled it). When the epic
branch PR merges, GitHub auto-closes the epic via `Closes #N` in the PR body.

```bash
if [ "${EPIC_BRANCH_PR_CREATED:-false}" != "true" ]; then
  CLOSE_COMMENT="All sub-issues have been completed. Closing epic."
  if [ -n "$SUMMARY" ]; then
    CLOSE_COMMENT="Epic summary has been posted above. Closing epic."
  fi

  gh issue close "$EPIC_NUMBER" --comment "$CLOSE_COMMENT" 2>/dev/null || true
  # GitHub Projects built-in workflow handles Status → Done on close.
fi
```

**Error handling**: Summary generation is wrapped in fallback logic. If any step
fails, execution falls through to closing the epic with a standard comment.
Summary failure NEVER blocks epic closure.

**No epics ready**: If `action != "ready-to-close"` or no epics found, this is a
no-op.


The binary reads every sub-issue, page by page, or exits non-zero; it never
returns part of the list as the whole of it (#1701).

```bash
# Detect owner/repo from git remote
REMOTE_URL=$(git remote get-url origin 2>/dev/null)
OWNER=$(echo "$REMOTE_URL" | sed -E 's|.*[:/]([^/]+)/([^/.]+)(\.git)?$|\1|')
REPO=$(echo "$REMOTE_URL" | sed -E 's|.*[:/]([^/]+)/([^/.]+)(\.git)?$|\2|')

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
if [ -z "$BINARY" ]; then
  echo "ERROR: nightgauge binary not found via NIGHTGAUGE_BIN, PATH or ~/go/bin"
  echo "Install via: go install github.com/nightgauge/nightgauge/cmd/nightgauge@latest"
  exit 1
fi

# stdout only: a warning on stderr must not land in the JSON.
if ! SUB_ISSUES_JSON=$("$BINARY" issue list --epic "$EPIC_NUMBER" \
    --owner "$OWNER" --repo "$REPO" --json); then
  echo "ERROR: Failed to fetch sub-issues for epic #$EPIC_NUMBER (the error is above)"
  exit 1
fi

# The binary writes subIssues as null when there are none.
SUB_COUNT=$(printf '%s\n' "$SUB_ISSUES_JSON" | jq '.subIssues // [] | length')
if [ "$SUB_COUNT" -eq 0 ]; then
  echo "ERROR: Epic #$EPIC_NUMBER has no sub-issues linked via GitHub's sub-issue feature."
  echo "  Sub-issues must be linked using the addSubIssue GraphQL mutation."
  echo "  Body text references (e.g., '- [ ] #123') do NOT count."
  exit 1
fi

echo "Found $SUB_COUNT sub-issues"
printf '%s\n' "$SUB_ISSUES_JSON" | jq -r '.subIssues[] |
  "  #\(.number) - \(.title) [\(.state)]"'
```

---

### Phase 2: Assess with Go Binary (Deterministic)

`$BINARY` is the binary Step 1.3 resolved.

```bash
ASSESS_JSON=$("$BINARY" epic assess "$EPIC_NUMBER" \
  --owner "$OWNER" --repo "$REPO" --json 2>/dev/null)

if [ $? -ne 0 ] || [ -z "$ASSESS_JSON" ]; then
  echo "ERROR: nightgauge epic assess failed for epic #$EPIC_NUMBER"
  exit 1
fi

STRATEGY=$(printf '%s\n' "$ASSESS_JSON" | jq -r '.strategy')
REASONING=$(printf '%s\n' "$ASSESS_JSON" | jq -r '.reasoning')
EST_COST=$(printf '%s\n' "$ASSESS_JSON" | jq -r '.estimatedCostUsd')
EST_MINUTES=$(printf '%s\n' "$ASSESS_JSON" | jq -r '.estimatedMinutes')
OPEN_COUNT=$(printf '%s\n' "$ASSESS_JSON" | jq '.issues | length')

echo "Strategy: $(echo "$STRATEGY" | tr '[:lower:]' '[:upper:]')"
echo "Reasoning: $REASONING"
```

---

### Phase 4: Generate Report

#### Step 4.1: Format Assessment Output

```bash
echo ""
echo "Epic #$EPIC_NUMBER Assessment"
echo "===================="
echo ""
echo "Strategy: $(echo "$STRATEGY" | tr '[:lower:]' '[:upper:]')"
echo "Reasoning: $REASONING"
echo ""
echo "Sub-Issues: $OPEN_COUNT open / $SUB_COUNT total"
echo ""
echo "Estimates:"
echo "  Cost: \$$EST_COST"
echo "  Time: $EST_MINUTES min"
echo ""
echo "Per-Issue Breakdown:"
printf '%s\n' "$ASSESS_JSON" | jq -r '.issues[] |
  "  #\(.issueNumber) complexity=\(.complexityScore) model=\(.recommendedModel)\(if .hasDependencies then " [blocked]" else "" end)"'
echo ""
echo "Recommendation:"
case "$STRATEGY" in
  parallel)
    echo "  All $OPEN_COUNT sub-issues are independent — queue them concurrently."
    ;;
  mixed)
    echo "  Queue independent sub-issues concurrently."
    echo "  Run dependency-blocked issues sequentially after their blockers."
    ;;
  sequential)
    echo "  Process sub-issues one at a time in dependency order."
    echo "  High dependency complexity makes batching counterproductive."
    ;;
esac
```

#### Step 4.2: Write JSON Report

```bash
REPORT_FILE=$(printf '%s\n' "$ASSESS_JSON" | jq \
  --arg epic_number "$EPIC_NUMBER" \
  --arg epic_title "$(echo "$EPIC_TITLE" | sed 's/"/\\"/g')" \
  --argjson sub_total "$SUB_COUNT" \
  --arg assessed_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  '. + {
    "schema_version": "2",
    "epic_number": ($epic_number | tonumber),
    "epic_title": $epic_title,
    "sub_issues": { "total": $sub_total, "open": (.issues | length) },
    "assessed_at": $assessed_at
  }' | nightgauge layout write pipeline "epic-assessment-${EPIC_NUMBER}.json")

echo ""
echo "Report written to: $REPORT_FILE"
```

---

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


---

## Error Handling

| Condition                     | Action                                       |
| ----------------------------- | -------------------------------------------- |
| Missing epic number argument  | Error with usage hint                        |
| Invalid epic number (non-int) | Error with validation message                |
| Epic issue not found          | Error with GitHub issue link suggestion      |
| Issue lacks `type:epic` label | Error explaining epic requirement            |
| No sub-issues linked          | Error explaining GraphQL linking requirement |
| All sub-issues closed         | Exit 0 with "no assessment needed" message   |
| `gh` CLI not authenticated    | Error with `gh auth login` instructions      |
| GraphQL query failure         | Error with raw response for debugging        |
| `bc` not available            | Fall back to integer arithmetic              |
| Sub-issue body fetch failure  | Continue with partial data, warn user        |

---

## Pipeline Position

```
UTILITIES (not part of main pipeline)

/nightgauge:assess-epic <epic-number>
       |
  Standalone analysis tool for epic batch planning
  Reads: GitHub API (epic + sub-issues + blocking)
  Writes: pipeline/epic-assessment-{N}.json (nightgauge layout write)
```

This is a standalone utility skill. It does not affect pipeline state and can be
run at any time without interfering with active pipeline runs. The queue command
(`/nightgauge:queue`) automatically invokes this assessment when an epic
issue number is queued.

---

## Related Skills

- **`/nightgauge:queue`** - Queue management; auto-invokes assess-epic when
  epic issues are queued
- **`skills/_shared/EPIC_HANDLING.md`** - Shared epic detection and handling
  patterns
- **`skills/_shared/DEPENDENCY_CHECKING.md`** - Shared dependency checking logic

---

**Author:** nightgauge **License:** Apache-2.0
