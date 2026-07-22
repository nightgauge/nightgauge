# Knowledge Base Read and Recall Prior Decisions (Phases 3.5, 3.7)

This reference carries the procedural detail for reading the scaffolded
knowledge base (Phase 3.5) and recalling semantically-related prior decisions
(Phase 3.7). Follow the section matching the phase you are currently in.

## Contents

- [Phase 3.5: Knowledge Base Read](#phase-35-knowledge-base-read)
- [Phase 3.5.1: Cross-Repo and Workspace Knowledge Detection](#phase-351-cross-repo-and-workspace-knowledge-detection)
- [Phase 3.7: Recall Prior Decisions](#phase-37-recall-prior-decisions)

## Phase 3.5: Knowledge Base Read

**PURPOSE**: When `knowledge_path` is set in the issue context, read the
scaffolded `PRD.md` to pre-load issue-derived requirements before producing the
plan. This avoids re-deriving content that issue-pickup already extracted and
ensures the plan builds on it rather than replacing it.

**No-op when `knowledge_path` is null or unset** — silently skip to Phase 4.

```bash
KNOWLEDGE_PATH=$(jq -r '.knowledge_path // empty' ".nightgauge/pipeline/issue-${ISSUE_NUMBER}.json" 2>/dev/null)

if [ -n "$KNOWLEDGE_PATH" ] && [ -d "$KNOWLEDGE_PATH" ]; then
  PRD_FILE="${KNOWLEDGE_PATH}/PRD.md"

  if [ -f "$PRD_FILE" ]; then
    echo "=== Reading scaffolded PRD.md from knowledge directory ==="
    cat "$PRD_FILE"
    echo ""
    echo "Use the above PRD content as initial context for planning."
    echo "The plan should build on these requirements, not duplicate them."
  fi
else
  echo "No knowledge_path found — skipping knowledge base read."
fi
```

### Phase 3.5.1: Cross-Repo and Workspace Knowledge Detection

Calls the Go binary to enumerate both cross-repo and workspace-level knowledge
directories in a single deterministic pass. No Python required.

```bash
KNOWLEDGE_JSON=$(nightgauge knowledge index --cross-repo --workspace --limit 20 --json 2>/dev/null || echo '{}')
CROSS_REPO_ENTRIES_JSON=$(echo "$KNOWLEDGE_JSON" | jq -c '.cross_repo_knowledge // []')
WORKSPACE_KB_ENTRIES_JSON=$(echo "$KNOWLEDGE_JSON" | jq -c '.workspace_kb // []')

REPO_COUNT=$(echo "$CROSS_REPO_ENTRIES_JSON" | jq 'length' 2>/dev/null || echo "0")
if [ "$REPO_COUNT" -gt 0 ]; then
  echo "=== Cross-Repo Knowledge Found: $REPO_COUNT sibling repo(s) ==="
  echo "$CROSS_REPO_ENTRIES_JSON" | jq -r '.[] | "  - \(.repo): \(.entries | length) entries at \(.path)"'
  echo ""
  echo "Read decisions.md and PRD.md from related sibling issues for planning context."
  echo "Focus on entries related to the current issue (same epic parent or referenced issue numbers)."
  echo "Limit reading to avoid token waste — prefer decisions.md over full PRD body."
fi

WS_KB_COUNT=$(echo "$WORKSPACE_KB_ENTRIES_JSON" | jq 'length' 2>/dev/null || echo "0")
if [ "$WS_KB_COUNT" -gt 0 ]; then
  echo "=== Workspace KB entries: $WS_KB_COUNT namespace(s) ==="
  echo "$WORKSPACE_KB_ENTRIES_JSON" | jq -r '.[] | "  - [[\(.namespace):...]]: \(.entries | length) entries at \(.path)"'
  echo ""
  echo "When planning touches cross-repo contracts, product principles, or"
  echo "ecosystem architecture, read matching entries above via the wiki-link"
  echo "namespaces: [[product:x]], [[cross-repo:x]], [[architecture:x]]."
fi
```

## Phase 3.7: Recall Prior Decisions

**PURPOSE**: Query the knowledge base for decisions from prior issues that are
semantically related to the current issue. Inject the top-N results as a
"Prior Decisions" section into the planning context before the plan is generated,
so the planning agent respects accumulated architectural decisions instead of
re-deriving them.

**No-op conditions** (skip silently, set `RECALL_HITS="[]"`):

- `knowledge.enabled` is false in `.nightgauge/config.yaml`
- No knowledge index exists (`.nightgauge/knowledge/` is empty or missing)
- `nightgauge knowledge recall` exits with an error
- Recall returns 0 results above threshold

**Query construction**: Concatenate issue title + space + first 1500 chars of
issue body + space + acceptance criteria bullets (one per line), then truncate
to 4096 characters total.

```bash
# Step 3.7.1: Check knowledge.enabled guard
KB_ENABLED=$(jq -r '.knowledge.enabled // false' .nightgauge/config.yaml 2>/dev/null || echo "false")
RECALL_HITS="[]"
RECALL_HIT_COUNT=0
RECALL_QUERY_ID=""

if [ "$KB_ENABLED" != "true" ]; then
  echo "Phase 3.7: knowledge.enabled=false — skipping recall"
else
  # Step 3.7.2: Build query string from issue title + body excerpt + ACs
  ISSUE_TITLE=$(jq -r '.title // ""' ".nightgauge/pipeline/issue-${ISSUE_NUMBER}.json" 2>/dev/null || echo "")
  ISSUE_BODY=$(jq -r '.body // ""' ".nightgauge/pipeline/issue-${ISSUE_NUMBER}.json" 2>/dev/null | head -c 1500 || echo "")
  ISSUE_ACS=$(jq -r '.requirements.acceptance_criteria[]? // empty' ".nightgauge/pipeline/issue-${ISSUE_NUMBER}.json" 2>/dev/null | head -20 | tr '\n' ' ' || echo "")

  RECALL_QUERY="${ISSUE_TITLE} ${ISSUE_BODY} ${ISSUE_ACS}"
  # Truncate to 4096 chars
  RECALL_QUERY="${RECALL_QUERY:0:4096}"

  # Step 3.7.3: Read config params
  RECALL_LIMIT=$(jq -r '.knowledge.recall.planning_limit // 5' .nightgauge/config.yaml 2>/dev/null || echo 5)
  RECALL_THRESHOLD=$(jq -r '.knowledge.recall.planning_threshold // 0' .nightgauge/config.yaml 2>/dev/null || echo 0)

  # Step 3.7.4: Invoke recall
  RECALL_RESULT=$("$BINARY" knowledge recall "$RECALL_QUERY" \
    --json \
    --limit "$RECALL_LIMIT" \
    --scopes "local,cross-repo,workspace" \
    2>/dev/null || echo '{"hits":[],"total_hits":0,"query_id":""}')

  RECALL_HITS=$(echo "$RECALL_RESULT" | jq -c '.hits // []')
  RECALL_QUERY_ID=$(echo "$RECALL_RESULT" | jq -r '.query_id // ""')

  # Step 3.7.5: Filter by threshold (skip filter when threshold is 0)
  THRESH_NONZERO=$(awk "BEGIN{print ($RECALL_THRESHOLD > 0) ? 1 : 0}" 2>/dev/null || echo 0)
  if [ "$THRESH_NONZERO" = "1" ]; then
    RECALL_HITS=$(echo "$RECALL_HITS" | jq --argjson thresh "$RECALL_THRESHOLD" \
      '[.[] | select(.score >= $thresh)]')
  fi

  RECALL_HIT_COUNT=$(echo "$RECALL_HITS" | jq 'length')

  if [ "$RECALL_HIT_COUNT" -gt 0 ]; then
    echo "Phase 3.7: Recall found $RECALL_HIT_COUNT prior decisions above threshold"

    # Step 3.7.6: Emit telemetry (fire-and-forget)
    "$BINARY" knowledge telemetry record \
      --type recall \
      --recall-id "$RECALL_QUERY_ID" \
      --result-count "$RECALL_HIT_COUNT" \
      --stage feature-planning \
      2>/dev/null || true

    echo "$RECALL_HITS" | jq -c '.[]' | while IFS= read -r hit; do
      HIT_RANK=$(echo "$hit" | jq -r '.rank')
      HIT_PATH=$(echo "$hit" | jq -r '.path')
      "$BINARY" knowledge telemetry record \
        --type recall_hit \
        --path "$HIT_PATH" \
        --recall-id "$RECALL_QUERY_ID" \
        --hit-index "$HIT_RANK" \
        --stage feature-planning \
        2>/dev/null || true
    done

    # Step 3.7.7: Read full content of top-ranked decision files
    echo "=== Prior Decisions (full content) ==="
    echo "$RECALL_HITS" | jq -r '.[].path' | while IFS= read -r hit_path; do
      if [ -f "$hit_path" ]; then
        echo "--- $hit_path ---"
        cat "$hit_path"
        echo ""
      fi
    done
    echo "=== End Prior Decisions ==="
  else
    echo "Phase 3.7: No prior decisions above threshold — continuing without recall context"
    RECALL_HITS="[]"
  fi
fi
```

**RECALL_HITS** is now set for use in:

- Phase 4 (Produce Plan): include "## Prior Decisions" section when non-empty
- Phase 5 (Write Planning Context): populate `recalled_decisions` field in JSON
