# Feature-Dev — Plan, Knowledge & Standards

Procedural detail for Phase 1 (Plan Verification), Phase 1.5 (Knowledge Base
Read), Phase 1.6 (Recall Architectural Constraints), and Phase 2 (Standards
Loading).

## Contents

- [Phase 1: Plan Verification](#phase-1-plan-verification)
- [Phase 1.5: Knowledge Base Read](#phase-15-knowledge-base-read)
- [Phase 1.6: Recall Architectural Constraints](#phase-16-recall-architectural-constraints)
- [Phase 2: Standards Loading](#phase-2-standards-loading)

---

## Phase 1: Plan Verification

### Step 1.0: Pre-load Context Files

If `files_to_read` is present in the planning context, read all listed files
before beginning implementation. This provides the agent with full context for
imports, patterns, and types without mid-implementation discovery.

For each file in `files_to_read`:

1. Read the file content
2. Note key exports, types, and patterns

If a listed file does not exist, log a warning and continue — the file may have
been renamed or removed since planning.

### Step 1.1: Infer Context and Locate Plan

Context is loaded from Phase 0. This step provides fallback for manual
invocation:

```bash
BRANCH=$(git branch --show-current)
ISSUE_NUMBER=$(echo "$BRANCH" | grep -oE '[0-9]+' | head -1)
```

### Step 1.2: Locate Plan by Issue Number

```bash
# Look for plan matching issue number first
if [ -n "$ISSUE_NUMBER" ]; then
  ls .nightgauge/plans/${ISSUE_NUMBER}-*.md 2>/dev/null
fi

# Fall back to common locations
ls PLAN.md .nightgauge/plans/*.md 2>/dev/null
```

If no plan found: decide to run `/feature-planning` first, implement without
plan, or accept a custom plan path.

### Step 1.3: Read and Validate Plan

Read PLAN.md and extract:

- Requirements and acceptance criteria
- Files to modify and create
- Test strategy
- Documented patterns to follow

### Step 1.4: Verify Branch Alignment

Confirm the plan's issue number matches current branch. If mismatch, warn before
proceeding.

---

## Phase 1.5: Knowledge Base Read

**No-op when `knowledge_path` is null or unset** — silently skip to Phase 2.

```bash
# Read knowledge_path from planning context, fall back to issue context
KNOWLEDGE_PATH=$(jq -r '.knowledge_path // empty' ".nightgauge/pipeline/planning-${ISSUE_NUMBER}.json" 2>/dev/null)
if [ -z "$KNOWLEDGE_PATH" ]; then
  KNOWLEDGE_PATH=$(jq -r '.knowledge_path // empty' ".nightgauge/pipeline/issue-${ISSUE_NUMBER}.json" 2>/dev/null)
fi

if [ -n "$KNOWLEDGE_PATH" ] && [ -d "$KNOWLEDGE_PATH" ]; then
  echo "=== Reading knowledge base from: $KNOWLEDGE_PATH ==="

  PRD_FILE="${KNOWLEDGE_PATH}/PRD.md"
  DECISIONS_FILE="${KNOWLEDGE_PATH}/decisions.md"
  KNOWLEDGE_FILES_READ="[]"
  HAS_SUBSTANTIVE=false

  # Check each file for substantive content before feeding it to the agent.
  # Files that are pure boilerplate (only TODO placeholders and empty tables)
  # are skipped to avoid wasting tokens on template noise.
  if [ -f "$PRD_FILE" ]; then
    # Strip HTML comments, headings, empty table rows, status checkboxes, and whitespace.
    # If <30 chars remain, the file is boilerplate.
    PRD_STRIPPED=$(sed -e 's/<!--.*-->//g' -e '/^#/d' -e '/^|[\s\-|]*|$/d' -e '/^- \[.\]/d' "$PRD_FILE" | tr -s '[:space:]' ' ' | sed 's/^ *//;s/ *$//')
    if [ ${#PRD_STRIPPED} -ge 30 ]; then
      echo "--- PRD.md ---"
      cat "$PRD_FILE"
      echo ""
      HAS_SUBSTANTIVE=true
    else
      echo "--- PRD.md: skipped (boilerplate template, no extracted content) ---"
    fi
  fi

  if [ -f "$DECISIONS_FILE" ]; then
    DEC_STRIPPED=$(sed -e 's/<!--.*-->//g' -e '/^#/d' -e '/^|[\s\-|]*|$/d' "$DECISIONS_FILE" | tr -s '[:space:]' ' ' | sed 's/^ *//;s/ *$//')
    if [ ${#DEC_STRIPPED} -ge 30 ]; then
      echo "--- decisions.md ---"
      cat "$DECISIONS_FILE"
      echo ""
      HAS_SUBSTANTIVE=true
    else
      echo "--- decisions.md: skipped (empty template, no decisions recorded) ---"
    fi
  fi

  if [ "$HAS_SUBSTANTIVE" = "true" ]; then
    echo "Use the above knowledge base content as additional context for implementation."
    echo "Build on these requirements and decisions rather than re-deriving them."
  else
    echo "Knowledge directory exists but contains only boilerplate templates."
    echo "Proceeding without knowledge base context — derive requirements from the plan."
  fi

  # List knowledge files for dev context (file paths only, not inlined further)
  KNOWLEDGE_FILES_READ=$(ls "$KNOWLEDGE_PATH"/*.md 2>/dev/null | jq -R . | jq -s .)
else
  echo "No knowledge_path found — skipping knowledge base read."
  KNOWLEDGE_FILES_READ="[]"
fi
```

### Cross-Repo Knowledge Pre-Load

When `cross_repo_knowledge` is present in `planning-{N}.json` and non-empty,
pre-load the referenced sibling-repo knowledge files before implementing.

```bash
CROSS_REPO=$(jq -r '.cross_repo_knowledge // []' ".nightgauge/pipeline/planning-${ISSUE_NUMBER}.json" 2>/dev/null)
REPO_COUNT=$(echo "$CROSS_REPO" | jq 'length' 2>/dev/null || echo "0")

if [ "$REPO_COUNT" -gt 0 ]; then
  echo "=== Cross-Repo Knowledge Context ==="
  echo "$CROSS_REPO" | jq -r '.[] | "--- Repo: \(.repo) ---\nPath: \(.path)\nEntries: \(.entries | join(", "))"'
  echo ""
  echo "Read the decisions.md and PRD.md files from the above sibling repositories."
  echo "These contain architecture decisions that may constrain the implementation approach."
  echo "If a sibling repo path is not accessible, log a warning and continue without it."
fi
```

For each accessible entry in `cross_repo_knowledge`, read the file at
`{path}/{entry}`. Prioritize `decisions.md` files — they contain architecture
decisions that may directly constrain implementation choices. If a path does not
exist locally, skip with a warning log and continue.

### Optional: Write implementation-notes.md

For **complex implementations only** — when the implementation requires
significant design decisions, workarounds, or trade-offs that future maintainers
should understand — write an `implementation-notes.md` to the knowledge
directory. Skip this step for straightforward implementations.

```bash
# Only when: KNOWLEDGE_PATH is set AND implementation is complex (agent judgment)
if [ -n "$KNOWLEDGE_PATH" ] && [ -d "$KNOWLEDGE_PATH" ] && [ "$IMPLEMENTATION_IS_COMPLEX" = "true" ]; then
  NOTES_FILE="${KNOWLEDGE_PATH}/implementation-notes.md"
  # Agent writes implementation notes here covering:
  # - Key design decisions made during implementation
  # - Non-obvious code patterns and why they were chosen
  # - Integration points and how they were wired
  # - Known limitations or future work
  echo "implementation-notes.md written: $NOTES_FILE"
fi
```

**When to set `IMPLEMENTATION_IS_COMPLEX=true`**: When implementation required
architectural adaptations not covered by the plan, introduced new abstractions,
or touched significantly more code than anticipated. Do NOT set for routine
feature additions, single-file changes, or straightforward implementations.

---

## Phase 1.6: Recall Architectural Constraints

**No-op conditions** (skip silently, set `ARCH_CONSTRAINTS="[]"`):

- `knowledge.enabled != true` in config
- No knowledge index exists or recall binary missing
- `files_to_modify` and `files_to_create` are both empty
- Recall exits with an error (log warning, continue — no-op safe)

```bash
ARCH_CONSTRAINTS="[]"
ARCH_CONSTRAINT_COUNT=0

KNOWLEDGE_ENABLED=$(jq -r '.knowledge.enabled // false' .nightgauge/config.yaml 2>/dev/null || echo "false")
if [ "$KNOWLEDGE_ENABLED" != "true" ]; then
  echo "Phase 1.6: knowledge.enabled=false — skipping recall"
else
  # Build query from files being modified: join paths with spaces (lexical substring match)
  ALL_FILES=$(echo "${FILES_TO_MODIFY:-[]}" | jq -r '.[]' 2>/dev/null; echo "${FILES_TO_CREATE:-[]}" | jq -r '.[]' 2>/dev/null)
  RECALL_QUERY=$(echo "$ALL_FILES" | tr '\n' ' ' | xargs | cut -c1-4096)

  if [ -z "$RECALL_QUERY" ]; then
    echo "Phase 1.6: no files to query against — skipping recall"
  else
    DEV_LIMIT=$(jq -r '.knowledge.recall.dev_limit // 5' .nightgauge/config.yaml 2>/dev/null || echo 5)
    DEV_THRESHOLD=$(jq -r '.knowledge.recall.dev_threshold // 1.5' .nightgauge/config.yaml 2>/dev/null || echo 1.5)

    RECALL_RESULT=$("$BINARY" knowledge recall "$RECALL_QUERY" \
      --json \
      --limit "$DEV_LIMIT" \
      --scopes "local,cross-repo,workspace" \
      2>/dev/null || echo '{"hits":[],"total_hits":0,"query_id":""}')

    RAW_HITS=$(echo "$RECALL_RESULT" | jq -c '.hits // []')
    RECALL_QUERY_ID=$(echo "$RECALL_RESULT" | jq -r '.query_id // ""')

    # Filter by dev_threshold (higher than planning's default to reduce noise)
    THRESH_NONZERO=$(awk "BEGIN{print ($DEV_THRESHOLD > 0) ? 1 : 0}" 2>/dev/null || echo 0)
    if [ "$THRESH_NONZERO" -eq 1 ]; then
      ARCH_CONSTRAINTS=$(echo "$RAW_HITS" | jq --argjson thresh "$DEV_THRESHOLD" \
        '[.[] | select(.score >= $thresh)]')
    else
      ARCH_CONSTRAINTS="$RAW_HITS"
    fi

    ARCH_CONSTRAINT_COUNT=$(echo "$ARCH_CONSTRAINTS" | jq 'length')

    if [ "$ARCH_CONSTRAINT_COUNT" -gt 0 ]; then
      echo "Phase 1.6: Recall found $ARCH_CONSTRAINT_COUNT architectural constraints above threshold ($DEV_THRESHOLD)"

      # Emit knowledge.recall telemetry event
      "$BINARY" telemetry emit \
        --type knowledge.recall \
        --recall-id "$RECALL_QUERY_ID" \
        --result-count "$ARCH_CONSTRAINT_COUNT" \
        --stage "feature-dev" 2>/dev/null || true

      # Emit knowledge.recall_hit per constraint
      echo "$ARCH_CONSTRAINTS" | jq -c '.[]' | while IFS= read -r hit; do
        HIT_PATH=$(echo "$hit" | jq -r '.path // ""')
        "$BINARY" telemetry emit \
          --type knowledge.recall_hit \
          --path "$HIT_PATH" \
          --recall-id "$RECALL_QUERY_ID" \
          --stage "feature-dev" 2>/dev/null || true
      done

      # Truncate if total snippet content exceeds ~2000 chars to stay within budget
      TOTAL_SNIPPET_LEN=$(echo "$ARCH_CONSTRAINTS" | jq '[.[].snippet | length] | add // 0')
      if [ "$TOTAL_SNIPPET_LEN" -gt 2000 ]; then
        "$BINARY" telemetry emit \
          --type knowledge.recall_truncated \
          --stage "feature-dev" 2>/dev/null || true
        # Keep highest-ranked hits that fit; drop lowest
        ARCH_CONSTRAINTS=$(echo "$ARCH_CONSTRAINTS" | jq '
          reduce .[] as $h (
            {"hits": [], "chars": 0};
            if (.chars + ($h.snippet | length)) <= 2000
            then {"hits": (.hits + [$h]), "chars": (.chars + ($h.snippet | length))}
            else .
            end
          ) | .hits')
        ARCH_CONSTRAINT_COUNT=$(echo "$ARCH_CONSTRAINTS" | jq 'length')
      fi

      echo ""
      echo "## Architectural Constraints"
      echo ""
      echo "The following prior architectural decisions affect the files you are"
      echo "about to modify. You MUST respect these constraints unless there is a"
      echo "documented reason to diverge (which must be added to decisions.md)."
      echo ""
      echo "| Rank | Issue | Path | Key Decision |"
      echo "| ---- | ----- | ---- | ------------ |"
      echo "$ARCH_CONSTRAINTS" | jq -r '.[] | "| \(.rank) | #\(.issue_number // "?") | \(.path) | \(.snippet | split("\n")[0] | .[0:80]) |"'
      echo ""

      # Read full content of each decision file for deep context
      echo "$ARCH_CONSTRAINTS" | jq -r '.[].path' | while IFS= read -r hit_path; do
        if [ -f "$hit_path" ]; then
          echo "---"
          echo "### $hit_path"
          cat "$hit_path"
          echo ""
        fi
      done
    else
      echo "Phase 1.6: No architectural constraints above threshold ($DEV_THRESHOLD) — continuing without recall context"
      ARCH_CONSTRAINTS="[]"
    fi
  fi
fi
```

When `ARCH_CONSTRAINT_COUNT > 0`, the above output serves as the "Architectural
Constraints" block prepended to the implementation context. The agent MUST read
it before writing any code. When `ARCH_CONSTRAINT_COUNT = 0`, no block is
emitted (no empty header).

---

## Phase 2: Standards Loading

### Step 2.1: Load Code Standards

```bash
# Load code standards with graceful fallback for greenfield repos (#1320)
if [ -f docs/CODE_STANDARDS.md ]; then
  cat docs/CODE_STANDARDS.md
elif [ -f CLAUDE.md ]; then
  echo "=== CODE STANDARDS: Extracted from CLAUDE.md (no docs/CODE_STANDARDS.md found) ==="
  cat CLAUDE.md
  echo "=== END FALLBACK: Apply TypeScript defaults: ESM, strict mode, named exports ==="
else
  echo "=== CODE STANDARDS: No standards file found. Using language defaults ==="
  echo "TypeScript defaults: ESM modules, strict: true, named exports, camelCase vars, PascalCase types"
fi
```

Extract: naming conventions, file structure patterns, error handling approach,
documentation requirements.

### Step 2.2: Load Security Standards

```bash
# Load security standards with graceful fallback for greenfield repos (#1320)
if [ -f docs/SECURITY_AND_ERROR_HANDLING.md ]; then
  cat docs/SECURITY_AND_ERROR_HANDLING.md
elif [ -f docs/SECURITY.md ]; then
  cat docs/SECURITY.md
elif [ -f CLAUDE.md ]; then
  echo "=== SECURITY STANDARDS: Extracted from CLAUDE.md (no docs/SECURITY*.md found) ==="
  grep -A5 -i "security\|secret\|inject\|validate\|auth" CLAUDE.md 2>/dev/null || \
    echo "No security section in CLAUDE.md. Apply defaults: no hardcoded secrets, validate all inputs, parameterized queries."
else
  echo "=== SECURITY STANDARDS: Using defaults: no hardcoded secrets, validate inputs, no eval ==="
fi
```

Extract: input validation rules, authentication/authorization patterns, error
handling without exposing internals, logging requirements.

### Step 2.3: Load Testing Standards

```bash
cat docs/TESTING.md 2>/dev/null
```

Extract: test naming conventions, coverage requirements, mocking strategies,
test file organization.
