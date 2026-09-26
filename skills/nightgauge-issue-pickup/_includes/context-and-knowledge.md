# Issue Pickup — Phase 8 (Write Context) + Phase 9 (Knowledge Scaffolding)

Procedural detail for two adjacent phases:

- **Phase 8** (`write-context`, index 10) — Steps 8.1, 8.2, 8.4, 8.5, 8.6, 8.7
- **Phase 9** (`knowledge-scaffolding`, index 11) — Steps 8.3, 8.3.5

> The `printf` lines that emit the `index=10` and `index=11` phase markers stay
> in the SKILL.md body. Emit each marker there before running the matching steps
> below.

## Contents

- [Step 8.1: Create Context Directory](#step-81-create-context-directory)
- [Step 8.2: Write issue-{N}.json inline](#step-82-write-issue-njson-inline)
- [Step 8.3: Knowledge scaffolding — done by the binary](#step-83-knowledge-scaffolding--done-by-the-binary-not-by-this-skill)
- [Step 8.4: AI populates routing field](#step-84-ai-populates-routing-field)
- [Step 8.5: Verify final context file](#step-85-verify-final-context-file)
- [Step 8.6: Signal Stage Complete](#step-86-signal-stage-complete)
- [Step 8.7: Display Completion Message](#step-87-display-completion-message)

---

## Step 8.1: Create Context Directory

```bash
mkdir -p .nightgauge/pipeline
```

## Step 8.2: Write issue-{N}.json inline

**Every value below is re-derived in THIS block, from a source that survives.**
Nothing is inherited from an earlier phase. Each `Bash` call is a fresh shell,
so a variable assigned in Phase 3 or Phase 5 is gone by the time this block
runs, and the old `${VAR:-}` defaults turned that into a schema-valid context
file with an empty `branch`, an empty title and no labels — a stage that exits
0 and produces nothing the gate will accept (#1919).

Durable sources, in order of authority:

| Field         | Source                                                                    |
| ------------- | ------------------------------------------------------------------------- |
| issue number  | `NIGHTGAUGE_ISSUE_NUMBER` (set by the orchestrator), else the branch name |
| repo          | `NIGHTGAUGE_REPO`, else `nightgauge git repo-slug` (the origin remote)    |
| `branch`      | the worktree's own `HEAD`; branch creation checks it out                  |
| `base_branch` | `origin/HEAD`                                                             |
| issue content | one `nightgauge forge issue view` fetch                                   |

```bash
set -u
ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
: "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
REPO="${NIGHTGAUGE_REPO:-$(nightgauge git repo-slug)}"
: "${REPO:?set NIGHTGAUGE_REPO or run inside a clone with an origin remote}"

# The branch. HEAD is authoritative: `nightgauge git branch-create` creates AND
# checks out, so by this phase the worktree is already on the feature branch.
BRANCH_NAME=$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "")
case "$BRANCH_NAME" in
  */"$ISSUE_NUMBER"-*) ;;
  *)
    echo "ERROR: HEAD is on '$BRANCH_NAME', which is not issue #$ISSUE_NUMBER's branch." >&2
    echo "       Re-run Phase 5 (nightgauge git branch-create --issue $ISSUE_NUMBER --json)," >&2
    echo "       which is idempotent, then repeat this step." >&2
    exit 1
    ;;
esac

BASE_BRANCH="${BASE_BRANCH:-}"
if [ -z "$BASE_BRANCH" ]; then
  BASE_BRANCH=$(git symbolic-ref --quiet --short refs/remotes/origin/HEAD 2>/dev/null | sed 's#^origin/##')
fi
BASE_BRANCH="${BASE_BRANCH:-main}"

# The issue itself. One fetch, only when this shell does not already hold it.
ISSUE_JSON="${ISSUE_JSON:-}"
if [ -z "$ISSUE_JSON" ]; then
  ISSUE_JSON=$(nightgauge forge issue view "$ISSUE_NUMBER" --repo "$REPO" --json)
fi
if [ -z "$ISSUE_JSON" ] || ! printf '%s\n' "$ISSUE_JSON" | jq -e . >/dev/null 2>&1; then
  echo "ERROR: could not fetch issue #$ISSUE_NUMBER as JSON" >&2
  exit 1
fi

TIMESTAMP=$(date -u +%Y-%m-%dT%H:%M:%SZ)
CONTEXT_FILE=".nightgauge/pipeline/issue-${ISSUE_NUMBER}.json"

TITLE="${TITLE:-$(printf '%s\n' "$ISSUE_JSON" | jq -r '.title // ""')}"
LABELS_JSON=$(printf '%s\n' "$ISSUE_JSON" | jq -c '[.labels[].name]')
ISSUE_BODY=$(printf '%s\n' "$ISSUE_JSON" | jq -r '.body // ""')

# Type from the branch prefix, which the binary already derived from the
# labels — one derivation, not two that can disagree.
ISSUE_TYPE="${ISSUE_TYPE:-}"
if [ -z "$ISSUE_TYPE" ]; then
  case "${BRANCH_NAME%%/*}" in
    fix) ISSUE_TYPE="bug" ;;
    docs) ISSUE_TYPE="docs" ;;
    chore) ISSUE_TYPE="chore" ;;
    refactor) ISSUE_TYPE="refactor" ;;
    test) ISSUE_TYPE="test" ;;
    *) ISSUE_TYPE="feature" ;;
  esac
fi

# Extract requirements from issue body
REQ_SUMMARY=$(printf '%s\n' "$ISSUE_BODY" | head -5 | tr '\n' ' ')
REQ_AC=$(printf '%s\n' "$ISSUE_BODY" | grep -E '^\s*-\s*\[' | jq -R -s 'split("\n") | map(select(. != ""))')

jq -n \
  --argjson issue_number "$ISSUE_NUMBER" \
  --arg title "$TITLE" \
  --arg body "$ISSUE_BODY" \
  --arg branch "$BRANCH_NAME" \
  --arg base_branch "$BASE_BRANCH" \
  --arg issue_type "$ISSUE_TYPE" \
  --argjson labels "${LABELS_JSON:-[]}" \
  --arg req_summary "${REQ_SUMMARY:-}" \
  --argjson req_ac "${REQ_AC:-[]}" \
  --arg created_at "$TIMESTAMP" \
  '{
    schema_version: "1.5",
    issue_number: $issue_number,
    title: $title,
    body: $body,
    branch: $branch,
    base_branch: $base_branch,
    type: $issue_type,
    requirements: {
      summary: $req_summary,
      acceptance_criteria: $req_ac,
      user_story: null,
      technical_notes: null
    },
    labels: $labels,
    routing: null,
    dependency_analysis: null,
    knowledge_path: null,
    created_at: $created_at
  }' > "$CONTEXT_FILE.tmp.$$"
# Atomic write: a plain redirect truncates the file before jq writes it, so a
# concurrent reader (the gate, loadFeatureBranch) could see it empty (#1904).
mv "$CONTEXT_FILE.tmp.$$" "$CONTEXT_FILE"

jq -e '.branch != "" and .title != ""' "$CONTEXT_FILE" > /dev/null && \
  echo "Context written: $CONTEXT_FILE (branch=$BRANCH_NAME)" || \
  { echo "ERROR: context file is missing branch or title" >&2; exit 1; }
```

**If this block exits non-zero, do not continue.** Fix what it reported and run
it again — it is idempotent.

**Backstop:** the orchestrator repairs an empty `branch` from the worktree's
`HEAD` after the stage exits and before the post-condition gate reads the file,
so a run that loses it anyway is no longer a wasted, escalated stage. That is a
safety net, not a licence — a context whose other fields are empty still
describes nothing to the stages downstream.

## Step 8.3: Knowledge scaffolding — done by the binary, not by this skill

Nothing to run here. The knowledge base for this issue (and the workspace-level
tree) is created by the Go pickup path before this skill starts, and
`knowledge_path` is already stamped into the issue context file.

This used to be ~100 lines of bash that called `nightgauge knowledge scaffold`
from the stage's cwd. On the scheduler path that cwd is the run's **worktree**,
so the PRD and decisions landed in `<worktree>/.nightgauge/knowledge/` —
gitignored, and deleted with the worktree at reclamation. Every later reader
(feature-dev's "read `knowledge_path/PRD.md`" rule, `/nightgauge:retro`'s
outcome append, the sidebar) then found nothing, which is why this workspace's
root knowledge base still ended at `390-` after hundreds of runs (#1205).

Root resolution is not something a skill can get right by convention — it needs
`git rev-parse --git-common-dir`, and it needs to be the same answer every time.
So it moved to the one place that already knows the canonical root. The
`knowledge.enabled` / `knowledge.auto_scaffold` gate is read there too, from the
parsed config rather than from `grep -A5 "^knowledge:"`.

To scaffold by hand (outside a pipeline run):

```bash
nightgauge knowledge scaffold --issue-number <N> --title "<title>" \
  --knowledge-enabled true --json
```

The CLI verb canonicalizes its root the same way, so it is safe to run from
inside a worktree.

## Step 8.4: AI populates routing field

The script wrote `"routing": null`. You must now update the routing field with a
deterministic JSON patch using values derived from the issue labels and content:

```bash
ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
: "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
ROUTING_JSON=$(jq -n \
  --arg change_type "code" \
  --argjson complexity_score 3 \
  --arg suggested_route "standard" \
  --arg rationale "..." \
  --argjson foundation_task "${FOUNDATION_TASK:-false}" \
  '{
    change_type: $change_type,
    task_type: "feature",
    complexity_score: $complexity_score,
    suggested_route: $suggested_route,
    foundation_task: $foundation_task,
    skip_stages: [],
    rationale: $rationale,
    estimated_time_minutes: 30
  }')

CONTEXT_FILE=".nightgauge/pipeline/issue-${ISSUE_NUMBER}.json"
tmp=$(mktemp)
jq --argjson routing "$ROUTING_JSON" '.routing = $routing' "$CONTEXT_FILE" > "$tmp"
mv "$tmp" "$CONTEXT_FILE"
```

Set `change_type`, `complexity_score`, `suggested_route`, `rationale`, and
`estimated_time_minutes` based on the issue labels and content:

- `change_type`: **MUST be exactly** `"docs"` | `"config"` | `"code"` — infer
  from issue type/labels. Do NOT use `"code_change"`, `"documentation"`,
  `"configuration"`, or any other variant.
- `complexity_score`: Fibonacci 1/2/3/5/8 — infer from size label (XS→1, S→2,
  M→3, L→5, XL→8). Do NOT exceed 8.
- `suggested_route`: **MUST be exactly** `"trivial"` | `"standard"` |
  `"extensive"`. Do NOT use `"quick"`, `"complex"`, `"deep"`, or any other
  variant.
- `skip_stages`: Array of **exactly** these values: `"feature-planning"` |
  `"feature-validate"` | `"pr-create"` | `"pr-merge"`. Use `[]` for standard
  pipeline execution.
- `rationale`: Brief explanation of routing decision

**Concrete example of a valid routing object** (copy this template and fill in
the values; the Zod schema enforces the exact field names and enum values):

```json
"routing": {
  "change_type": "code",
  "complexity_score": 3,
  "suggested_route": "standard",
  "skip_stages": [],
  "rationale": "M-size code change requiring standard pipeline execution",
  "estimated_time_minutes": 30
}
```

Other valid examples:

- Trivial docs change: `"change_type": "docs"`, `"complexity_score": 1`, `"suggested_route": "trivial"`, `"skip_stages": ["feature-planning", "feature-validate"]`
- Large feature: `"change_type": "code"`, `"complexity_score": 5`, `"suggested_route": "extensive"`, `"skip_stages": []`

Also include `pickup_recommendation` with explicit stage skip recommendations
(Issue #1593):

```json
{
  "pickup_recommendation": {
    "complexity": 2,
    "recommended_stages": ["issue-pickup", "feature-dev", "pr-create", "pr-merge"],
    "skipped_stages": ["feature-planning", "feature-validate"],
    "skip_rationale": "Trivial complexity — single file, no new logic",
    "dev_model": "sonnet",
    "validate_model": null
  }
}
```

## Step 8.5: Verify final context file

```bash
ISSUE_NUMBER="${NIGHTGAUGE_ISSUE_NUMBER:-$(git branch --show-current | sed -n 's#^[^/]*/\([0-9]*\)-.*#\1#p')}"
: "${ISSUE_NUMBER:?set NIGHTGAUGE_ISSUE_NUMBER or check out the issue branch}"
jq . ".nightgauge/pipeline/issue-${ISSUE_NUMBER}.json" > /dev/null && \
  echo "Context file written: .nightgauge/pipeline/issue-${ISSUE_NUMBER}.json"
```

## Step 8.6: Signal Stage Complete

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

## Step 8.7: Display Completion Message

```
Repository: <owner/repo>
Issue:      #<number> - <title>
Branch:     <branch-name>
Context:    .nightgauge/pipeline/issue-<number>.json

START A NEW CONVERSATION and run: /nightgauge:feature-planning

This issue pickup session is complete. Do NOT continue in this conversation.
```

**CRITICAL - CONTEXT ISOLATION RULES**:

1. This skill terminates here. No further actions are taken.
2. **DO NOT ask** "Continue to Feature Planning?" or similar questions.
3. **DO NOT use AskUserQuestion** to offer stage transitions.
4. The AI agent MUST NOT continue to feature planning in this conversation.
