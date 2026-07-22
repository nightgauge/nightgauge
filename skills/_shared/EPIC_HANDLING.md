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
LABELS=$(echo "$ISSUE_JSON" | jq -r '.labels[].name' 2>/dev/null)
if echo "$LABELS" | grep -q "^type:epic$"; then
  IS_EPIC=true
fi
```

#### If Epic Detected

1. **Check if all sub-issues are complete** using Go binary `epic check-completion`:

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
   if [ -z "$BINARY" ]; then
     echo "ERROR: nightgauge binary not found in PATH or bin/nightgauge" >&2
     exit 1
   fi
   EPIC_STATUS=$("$BINARY" epic check-completion "$ISSUE_NUMBER" --json 2>/dev/null || echo '{"complete":false}')
   EPICS_CLOSED=$(echo "$EPIC_STATUS" | jq 'if .complete == true then 1 else 0 end')
   if [ "$EPICS_CLOSED" -gt 0 ]; then
     echo "Epic #$ISSUE_NUMBER had all sub-issues completed. Auto-closed and synced to Done."
     exit 1
   fi
   ```

2. **Fetch sub-issues** using the GraphQL subIssues API (the `gh` CLI lacks
   `--json subIssues`, so GraphQL is required):

   ```bash
   # GraphQL query for sub-issue detection
   # Exact template — do NOT modify field names or argument format
   gh api graphql -f query='
     query($owner: String!, $repo: String!, $number: Int!) {
       repository(owner: $owner, name: $repo) {
         issue(number: $number) {
           subIssues(first: 50) {
             nodes { number title state }
           }
         }
       }
     }
   ' -f owner="OWNER" -f repo="REPO" -F number=ISSUE_NUMBER
   ```

   Replace `OWNER`, `REPO`, and `ISSUE_NUMBER` with actual values. Parse the
   response with:

   ```bash
   echo "$RESULT" | jq -r '.data.repository.issue.subIssues.nodes[] |
     "#\(.number) - \(.title) [\(.state)]"'
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
  echo "ERROR: nightgauge binary not found in PATH or bin/nightgauge" >&2
  exit 1
fi
EPIC_RESULT=$("$BINARY" epic check-completion "$ISSUE_NUMBER" --json 2>/dev/null || echo '{"complete":false}')
# Determine if epic is ready to close
EPICS_READY=$(echo "$EPIC_RESULT" | jq 'if .complete == true then 1 else 0 end')
EPIC_ACTION=$( [ "$EPICS_READY" -gt 0 ] && echo "ready-to-close" || echo "not-ready" )
```

**Step 2: Generate summary if epic is ready to close**

If `action == "ready-to-close"`:

```bash
EPIC_NUMBER=$(echo "$EPIC_RESULT" | jq -r '.epicNumber')
EPIC_TITLE=$(echo "$EPIC_RESULT" | jq -r '.title')
HOOKS_DIR="${CLAUDE_PLUGIN_ROOT:-claude-plugins/nightgauge}/hooks/lib"

# a. Classify summary tier (deterministic, no AI tokens)
if [ ! -x "$HOOKS_DIR/classify-epic-summary-tier.sh" ]; then
  echo "WARNING: Optional hook script not found: $HOOKS_DIR/classify-epic-summary-tier.sh. Summary tier classification will be skipped." >&2
  TIER="none"
else
  TIER_RESULT=$("$HOOKS_DIR/classify-epic-summary-tier.sh" "$EPIC_NUMBER") || true
  TIER=$(echo "$TIER_RESULT" | jq -r '.tier // "none"')
fi

# b. Generate summary if tier is not "none"
SUMMARY=""
if [ "${TIER:-none}" != "none" ]; then
  if [ ! -x "$HOOKS_DIR/generate-epic-summary.sh" ]; then
    echo "WARNING: Optional hook script not found: $HOOKS_DIR/generate-epic-summary.sh. Epic summary generation will be skipped." >&2
  else
    SUMMARY=$("$HOOKS_DIR/generate-epic-summary.sh" "$EPIC_NUMBER" "$TIER") || true
  fi
fi

# c. Post summary as issue comment (if generated)
if [ -n "$SUMMARY" ]; then
  gh issue comment "$EPIC_NUMBER" --body "$SUMMARY" 2>/dev/null || true
fi

# d. For full tier: commit summary to docs/epics/{N}-{slug}.md
if [ "${TIER:-none}" = "full" ] && [ -n "$SUMMARY" ]; then
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
  echo "ERROR: nightgauge binary not found in PATH or bin/nightgauge" >&2
  exit 1
fi
# Find epic branch
EPIC_BRANCH=$(git branch -r --list "origin/epic/${EPIC_NUMBER}-*" 2>/dev/null | head -1 | sed 's|origin/||; s/^ *//')
if [ -z "$EPIC_BRANCH" ]; then
  echo "WARNING: No epic branch found for epic #$EPIC_NUMBER — skipping PR creation" >&2
else
  PR_RESULT=$("$BINARY" pr create     --title "feat(#${EPIC_NUMBER}): ${EPIC_TITLE}"     --head "$EPIC_BRANCH"     --base main     --body "Epic #${EPIC_NUMBER} completion: all sub-issues are closed."     --json 2>/dev/null) || true
  PR_ACTION=$(echo "$PR_RESULT" | jq -r '.action // "created"')
  PR_URL=$(echo "$PR_RESULT" | jq -r '.prUrl // ""')

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
