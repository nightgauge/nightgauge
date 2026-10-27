# Feedback Context and Load Context (Phases 0, 1)

This reference carries the procedural detail for two early planning phases:
the feedback/revision detection (Phase 0) and context loading and stage start
(Phase 1). Follow the section
matching the phase you are currently in.

## Contents

- [Phase 0: Feedback Context Check](#phase-0-feedback-context-check)
- [Phase 1: Load Context and Start Stage](#phase-1-load-context-and-start-stage)

## Phase 0: Feedback Context Check

**PURPOSE**: Detect whether this is a plan revision run (backtrack from a
downstream stage). When a `feedback-{N}.json` exists, the plan must explicitly
address the failures in the previous attempt rather than producing a generic
plan.

**This phase is a no-op when no `feedback-{N}.json` exists** (first run).
Silently skip to Phase 1.

```bash
BRANCH=$(git branch --show-current)
ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
: "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
FEEDBACK_FILE="$(nightgauge layout path pipeline feedback-${ISSUE_NUMBER}.json)"

REVISION_COUNT=0
REVISION_REASONS="[]"
IS_REVISION=false

if [ -f "$FEEDBACK_FILE" ]; then
  IS_REVISION=true
  SIGNALS=$(jq -r '.signals' "$FEEDBACK_FILE")
  SIGNAL_COUNT=$(printf '%s\n' "$SIGNALS" | jq 'length')
  REVISION_COUNT=$(jq -r '.revision_count // 0' "$(nightgauge layout path pipeline planning-${ISSUE_NUMBER}.json)" 2>/dev/null || echo 0)

  echo "=== PLAN REVISION (Attempt $((REVISION_COUNT + 1))) ==="
  echo ""
  echo "## Previous Plan Failures"
  echo ""
  echo "This is a revision run. The following feedback signals were emitted by"
  echo "a downstream stage. The revised plan MUST directly address each failure."
  echo ""

  # Print verbatim evidence for each signal
  printf '%s\n' "$SIGNALS" | jq -r '.[] | "### \(.signal_type) (\(.severity))\nRationale: \(.rationale)\nEvidence:\n\(.evidence | map("  - " + .) | join("\n"))\n"'

  # Collect revision_reasons from all evidence strings
  REVISION_REASONS=$(printf '%s\n' "$SIGNALS" | jq '[.[].evidence[]] ')

  # Per-signal-type handling instructions
  echo "## Revision Instructions"
  echo ""

  # PLAN_REVISION_NEEDED
  HAS_REVISION_NEEDED=$(printf '%s\n' "$SIGNALS" | jq '[.[] | select(.signal_type == "PLAN_REVISION_NEEDED")] | length')
  if [ "$HAS_REVISION_NEEDED" -gt 0 ]; then
    echo "**PLAN_REVISION_NEEDED**: Revise the specific files, APIs, and patterns"
    echo "that were wrong. Do NOT reuse the same file list or approach without"
    echo "verifying the APIs actually exist in the codebase."
    echo ""
  fi

  # SCOPE_DISCOVERED
  HAS_SCOPE=$(printf '%s\n' "$SIGNALS" | jq '[.[] | select(.signal_type == "SCOPE_DISCOVERED")] | length')
  if [ "$HAS_SCOPE" -gt 0 ]; then
    echo "**SCOPE_DISCOVERED**: Explicitly expand the plan's scope section to"
    echo "include all newly discovered files listed in the evidence. These files"
    echo "MUST appear in files_to_modify or files_to_create."
    echo "Evidence files the acceptance criteria do not need are recorded with"
    echo "'nightgauge discovered-work record', not absorbed — see"
    echo "docs/FEEDBACK_LOOPS.md#record-vs-backtrack-deferrable-discovered-work."
    echo ""
  fi

  # ACCEPTANCE_CRITERIA_AMBIGUOUS
  HAS_AMBIGUOUS=$(printf '%s\n' "$SIGNALS" | jq '[.[] | select(.signal_type == "ACCEPTANCE_CRITERIA_AMBIGUOUS")] | length')
  if [ "$HAS_AMBIGUOUS" -gt 0 ]; then
    echo "**ACCEPTANCE_CRITERIA_AMBIGUOUS**: You cannot change the acceptance"
    echo "criteria. Read the original issue body and its comments, fetched with"
    echo "\`nightgauge forge issue view <N> --repo <owner/repo> --json --comments\`"
    echo "(only comments by users with write access are returned). Make an"
    echo "explicit interpretation decision and document it in the plan with"
    echo "rationale, so feature-validate knows exactly what to check."
    echo ""
  fi

  # COMPLEXITY_UNDERESTIMATED
  HAS_COMPLEXITY=$(printf '%s\n' "$SIGNALS" | jq '[.[] | select(.signal_type == "COMPLEXITY_UNDERESTIMATED")] | length')
  if [ "$HAS_COMPLEXITY" -gt 0 ]; then
    echo "**COMPLEXITY_UNDERESTIMATED**: Escalate the documentation scope and"
    echo "re-assess the Size board field. If the issue is sized S or M, consider"
    echo "whether it should be M or L. Document the rationale for the size change."
    echo ""
  fi
fi
```

**Required plan structure when IS_REVISION=true**:

The plan file produced in Phase 4 MUST include a section titled exactly:

```markdown
## What the Previous Plan Got Wrong
```

This section must list specific corrections keyed to each piece of feedback
evidence. It forces the planning agent to engage with the failure evidence
rather than producing a re-written plan that ignores the original failures.

**Example** (when previous plan specified a non-existent class):

```markdown
## What the Previous Plan Got Wrong

1. Plan specified `UserRepository.findById()` but this class does not exist.
   Correction: Use `db.query.users.findFirst({ where: eq(users.id, id) })`
   (Drizzle ORM pattern from `packages/db/src/schema/users.ts`).

2. Plan listed 3 files to modify but implementation required 6 (scope
   underestimated). Correction: Scope section now includes all 6 files.
```

## Phase 1: Load Context and Start Stage

1. Extract issue number from branch name.
2. Load `"$(nightgauge layout path pipeline issue-{N}.json)"`.
3. Read title, requirements, acceptance criteria, and labels.
4. Signal stage start using Go binary `project move-status`:
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
