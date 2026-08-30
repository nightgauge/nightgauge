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

```bash
TIMESTAMP=$(date -u +%Y-%m-%dT%H:%M:%SZ)
CONTEXT_FILE=".nightgauge/pipeline/issue-${ISSUE_NUMBER}.json"

# Extract labels and body from issue JSON for safe jq interpolation
LABELS_JSON=$(printf '%s\n' "$ISSUE_JSON" | jq -c '[.labels[].name]')
ISSUE_BODY=$(printf '%s\n' "$ISSUE_JSON" | jq -r '.body // ""')

# Extract requirements from issue body
REQ_SUMMARY=$(printf '%s\n' "$ISSUE_JSON" | jq -r '.body' | head -5 | tr '\n' ' ')
REQ_AC=$(printf '%s\n' "$ISSUE_JSON" | jq -r '.body' | grep -E '^\s*-\s*\[' | jq -R -s 'split("\n") | map(select(. != ""))')

jq -n \
  --argjson issue_number "$ISSUE_NUMBER" \
  --arg title "${TITLE:-}" \
  --arg body "${ISSUE_BODY:-}" \
  --arg branch "${BRANCH_NAME:-}" \
  --arg base_branch "${BASE_BRANCH:-main}" \
  --arg issue_type "${ISSUE_TYPE:-feature}" \
  --argjson labels "${LABELS_JSON}" \
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
      acceptance_criteria: ($req_ac | fromjson),
      user_story: null,
      technical_notes: null
    },
    labels: $labels,
    routing: null,
    dependency_analysis: null,
    knowledge_path: null,
    created_at: $created_at
  }' > "$CONTEXT_FILE"

jq . "$CONTEXT_FILE" > /dev/null && \
  echo "Context written: $CONTEXT_FILE" || \
  { echo "ERROR: Context file JSON invalid" >&2; exit 1; }
```

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
jq . ".nightgauge/pipeline/issue-${ISSUE_NUMBER}.json" > /dev/null && \
  echo "Context file written: .nightgauge/pipeline/issue-${ISSUE_NUMBER}.json"
```

## Step 8.6: Signal Stage Complete

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
