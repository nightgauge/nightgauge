# Produce Plan File and Knowledge Base Enrichment (Phases 4, 5.5)

This reference carries the procedural detail for writing the plan file (Phase 4)
and enriching the knowledge base after planning (Phase 5.5). Follow the section
matching the phase you are currently in. The intervening Phase 5 (Write Planning
Context) stays inline in `SKILL.md` because it is the downstream output
contract.

## Contents

- [Phase 4: Produce Plan File](#phase-4-produce-plan-file)
- [Phase 5.5: Knowledge Base Enrichment](#phase-55-knowledge-base-enrichment)

## Phase 4: Produce Plan File

Write a concise plan that includes:

- Problem summary and scope boundaries
- Assumptions and constraints
- Files likely to change (existing + new)
- Files to read — existing files that feature-dev should pre-load at the start
  of implementation for context (imports, patterns, types)
- Step-by-step implementation plan
- Test/validation plan
- Integration points — for each new service, export, or data producer:
  - **Consumers**: who imports/calls it and what they do with the result
  - **Producers**: what existing data/services the new code depends on
  - **Wiring verification**: consumer code modified in THIS PR, or tracked in
    dependent issue #NNN
- Risks and mitigations
- Explicit out-of-scope items

Plans that create new services or exports without documented consumers in the
"Integration points" section are considered incomplete. If no consumer exists
yet, the plan must explicitly state this and reference a tracking issue.

**When IS_REVISION=true** (feedback context was loaded in Phase 0), the plan
file MUST additionally include:

```markdown
# PLAN.md — Attempt {revision_count + 1}
```

as the document title (instead of `# PLAN.md`), and a section:

```markdown
## What the Previous Plan Got Wrong
```

listing specific corrections keyed to each feedback evidence string. This
section is mandatory — omitting it causes quality review to fail.

**When `RECALL_HIT_COUNT > 0`** (Phase 3.7 found prior decisions), include a
`## Prior Decisions` section **before** `## Implementation Plan`. Format:

```markdown
## Prior Decisions

The following decisions from prior issues are relevant to this plan. The
implementation MUST respect these decisions unless there is a documented reason
to diverge.

| Rank | Issue | Path                 | Key Decision                  |
| ---- | ----- | -------------------- | ----------------------------- |
| 1    | #NNN  | path/to/decisions.md | Brief summary of the decision |
```

Fill each row from the `RECALL_HITS` array (rank, issue_number, path, first
sentence of snippet). If `RECALL_HIT_COUNT=0`, omit this section entirely.

The plan must be implementation-ready and specific enough for
`/nightgauge-feature-dev`. Write the plan file to
`.nightgauge/plans/{N}-*.md` and do not write `PLAN.md` at repository root.

## Phase 5.5: Knowledge Base Enrichment

**PURPOSE**: Enrich the scaffolded `PRD.md` with detailed requirements, approach
rationale, and scope boundaries determined during planning. Populate
`decisions.md` with key design decisions in ADR block format. This builds the
living knowledge base that feature-dev and future maintainers will use.

**Deferred scaffolding**: When `knowledge_path` is null or unset BUT
`knowledge.enabled` is true in `.nightgauge/config.yaml`, issue-pickup
deferred scaffolding because the issue body had no extractable sections. In this
case, feature-planning MUST scaffold the knowledge directory itself before
enriching, since planning has the richest context available. Use the same
directory naming convention: `.nightgauge/knowledge/features/{N}-{slug}/`.

**Skip when knowledge is disabled**: If `knowledge.enabled` is false in config
AND `knowledge_path` is null, silently skip to Phase 6.

**Decisions.md validation gate**: After enriching `decisions.md`, the skill
calls `nightgauge knowledge validate <issue>` when `knowledge.require_decisions: true`
is set in `.nightgauge/config.yaml`. If the plan contains 2+ distinct tradeoff
keywords (loaded from `configs/knowledge-tradeoff-keywords.yaml`) but `decisions.md`
lacks a valid ADR block, planning fails with a clear error pointing to the tradeoff
locations and an ADR template. To disable this gate: set `knowledge.require_decisions: false`.

**PRD.md enrichment rules**:

The scaffolded PRD is the single source of truth for the issue — enrich its
seeded sections **in place**, do NOT append a parallel "Planning Detail" section
and do NOT create separate TRD/QRD files. Technical requirements live in
`## Technical Approach` (the embedded TRD); quality and non-functional
requirements live in `## Quality & Non-Functional Requirements` (the embedded
QRD).

- Preserve the `## Summary`, `## User Story`, and `## Acceptance Criteria`
  sections written by issue-pickup (refine wording only if the plan clarified
  them; never delete acceptance criteria).
- Replace the placeholder comment under `## Technical Approach` with the design:
  approach rationale, key components/files, data flow, and constraints. Add
  `### Files to Change` and `### Key Constraints` subsections here when useful.
- Replace the placeholder comment under `## Quality & Non-Functional
Requirements` with the test strategy (unit/integration/e2e) plus any
  performance, security, accessibility, or reliability budgets. Write "None
  beyond the acceptance criteria" when there are genuinely none.
- Replace the placeholder comment under `## Out of Scope` with the scope
  boundaries the plan established.

**decisions.md format** (ADR block — one block per key decision):

```markdown
## ADR-001: [Decision Title]

**Status**: Proposed
**Context**: [Background and constraints that led to this decision]
**Decision**: [What was decided and why]
**Consequences**: [Expected impact, trade-offs, and follow-up actions]
```

### Decision Categories

Look for decisions in these categories and capture at least one per plan:

- **Architecture**: how components are structured or decomposed
- **Data model**: schema choices, field types, normalization decisions
- **Integration**: which existing service/API to reuse vs. build new
- **Format/protocol**: file format, serialization, naming convention choices
- **Scope boundary**: what was explicitly deferred and why
- **Algorithm/approach**: chosen implementation strategy when multiple existed

If a plan has no meaningful decisions (e.g. single-line bug fix), write one
entry explaining why no architectural choices were required.

```bash
KNOWLEDGE_PATH=$(jq -r '.knowledge_path // empty' ".nightgauge/pipeline/issue-${ISSUE_NUMBER}.json" 2>/dev/null)

# Deferred scaffolding: if issue-pickup skipped because the issue body had no
# extractable sections, scaffold now — planning has the richest context.
if [ -z "$KNOWLEDGE_PATH" ] || [ ! -d "$KNOWLEDGE_PATH" ]; then
  # Check if knowledge is enabled in config
  KNOWLEDGE_ENABLED=$(python3 -c "
import yaml, sys
try:
  cfg = yaml.safe_load(open('.nightgauge/config.yaml'))
  print('true' if cfg.get('knowledge', {}).get('enabled') else 'false')
except: print('false')
" 2>/dev/null || echo "false")

  if [ "$KNOWLEDGE_ENABLED" = "true" ]; then
    SLUG=$(echo "$TITLE" | tr '[:upper:]' '[:lower:]' | sed 's/[^a-z0-9]/-/g' | sed 's/--*/-/g;s/^-//;s/-$//' | cut -c1-50)
    KNOWLEDGE_PATH=".nightgauge/knowledge/features/${ISSUE_NUMBER}-${SLUG}"
    mkdir -p "$KNOWLEDGE_PATH"
    echo "Knowledge directory scaffolded by feature-planning (deferred from issue-pickup): $KNOWLEDGE_PATH"
  fi
fi

if [ -n "$KNOWLEDGE_PATH" ] && [ -d "$KNOWLEDGE_PATH" ]; then
  PRD_FILE="${KNOWLEDGE_PATH}/PRD.md"
  DECISIONS_FILE="${KNOWLEDGE_PATH}/decisions.md"
  TODAY=$(date -u +%Y-%m-%d)

  # Create PRD.md if it doesn't exist (deferred scaffolding case).
  # Seed the SAME structure as issue-pickup's scaffold so the deferred path is
  # identical to the normal path: requirements + embedded TRD (Technical
  # Approach) + embedded QRD (Quality & Non-Functional Requirements). The agent
  # fills these placeholders in place during enrichment below.
  if [ ! -f "$PRD_FILE" ]; then
    cat > "$PRD_FILE" << PRDNEWEOF
# PRD: #${ISSUE_NUMBER} — ${TITLE}

## Summary

<!-- TODO: 1-2 sentence problem statement — what is missing/broken and why it matters -->

## User Story

<!-- TODO: As a <role>, I want <capability> so that <benefit>. Omit for pure infra/chore work. -->

## Acceptance Criteria

<!-- TODO: Testable checkboxes — each one a behavior feature-validate can verify
- [ ] Criterion 1
- [ ] Criterion 2 -->

## Technical Approach

<!-- TODO (embedded TRD): design, key components/files, data flow, and implementation constraints.
     This IS the technical requirements doc — keep it here, do not split into a separate TRD file. -->

## Quality & Non-Functional Requirements

<!-- TODO (embedded QRD): test strategy (unit/integration/e2e) plus any performance, security,
     accessibility, or reliability budgets. "None beyond the acceptance criteria" is a valid answer. -->

## Out of Scope

<!-- TODO: What this issue explicitly will NOT do — names the boundary to prevent scope creep. -->

## Status

- [ ] Draft
- [ ] Reviewed
- [ ] Approved
PRDNEWEOF
  fi

  # Create decisions.md if it doesn't exist (deferred scaffolding case)
  if [ ! -f "$DECISIONS_FILE" ]; then
    cat > "$DECISIONS_FILE" << DECNEWEOF
# Decisions: #${ISSUE_NUMBER} — ${TITLE}

## Architecture Decisions
DECNEWEOF
  fi

  # --- Enrich PRD.md (in place) ---
  # AI: Do NOT append a new section. Edit the seeded PRD in place, replacing the
  # TODO placeholder comments with real planning content. Per the enrichment
  # rules above:
  #   - ## Technical Approach (embedded TRD): approach rationale, key
  #     components/files, data flow, constraints. Add ### Files to Change and
  #     ### Key Constraints subsections here when useful.
  #   - ## Quality & Non-Functional Requirements (embedded QRD): test strategy
  #     plus any performance/security/accessibility/reliability budgets, or
  #     "None beyond the acceptance criteria".
  #   - ## Out of Scope: the scope boundaries established during planning.
  # Preserve ## Summary, ## User Story, and ## Acceptance Criteria.
  echo "PRD.md ready for in-place enrichment: $PRD_FILE (fill Technical Approach, Quality & NFRs, Out of Scope)"

  # --- Populate decisions.md ---
  # Write key decisions from planning as ADR blocks.
  # AI: Replace the placeholder text below with actual decisions from your planning.
  # Add one ## ADR-NNN block per key decision. Do NOT leave placeholder text — replace it.
  # The validator requires at least one ADR block with Status, Context, Decision,
  # and Consequences fields when the plan contains 2+ tradeoff keywords.
  cat >> "$DECISIONS_FILE" << 'DECEOF'

## ADR-001: [Decision Title — replace this placeholder]

**Status**: Proposed
**Context**: [Background and constraints that led to this decision — replace this placeholder]
**Decision**: [What was decided and why — replace this placeholder]
**Consequences**: [Expected impact, trade-offs, and follow-up actions — replace this placeholder]
DECEOF

  echo "decisions.md populated: $DECISIONS_FILE"

  # --- Enrichment quality validation ---
  # Verify the AI replaced the seeded TODO placeholders with real content. The
  # embedded TRD/QRD markers only exist in unfilled scaffold comments, so their
  # presence means Technical Approach or Quality & NFRs was left empty.
  UNREPLACED_PRD=$(grep -c 'embedded TRD\|embedded QRD\|prevent scope creep' "$PRD_FILE" 2>/dev/null || echo "0")
  UNREPLACED_DEC=$(grep -c '{[A-Z_]\{3,\}}' "$DECISIONS_FILE" 2>/dev/null || echo "0")
  if [ "$UNREPLACED_PRD" -gt 0 ] || [ "$UNREPLACED_DEC" -gt 0 ]; then
    echo "WARNING: Knowledge enrichment left placeholders (PRD seeded-comments: $UNREPLACED_PRD, decisions: $UNREPLACED_DEC)."
    echo "The AI MUST replace the seeded TODO comments in Technical Approach, Quality & Non-Functional Requirements, and Out of Scope with real content."
    echo "Go back and write real content for each placeholder in $PRD_FILE and $DECISIONS_FILE."
  fi

  # Warn if ADR placeholder text was not replaced
  if grep -q '\[Decision Title — replace this placeholder\]\|\[Background and constraints\|replace this placeholder\]' "$DECISIONS_FILE" 2>/dev/null; then
    echo "WARNING: decisions.md still contains placeholder text. Replace all placeholder text in ADR blocks with actual decisions from your planning."
  fi

  # List all .md files in knowledge directory for knowledge_entries
  KNOWLEDGE_ENTRIES=$(ls "$KNOWLEDGE_PATH"/*.md 2>/dev/null | xargs -I{} basename {} | jq -R . | jq -s .)

  # Patch knowledge_path and knowledge_entries into planning context
  PLANNING_FILE=".nightgauge/pipeline/planning-${ISSUE_NUMBER}.json"
  tmp=$(mktemp)
  jq \
    --arg kp "$KNOWLEDGE_PATH" \
    --argjson ke "${KNOWLEDGE_ENTRIES:-[]}" \
    '.knowledge_path = $kp | .knowledge_entries = $ke' \
    "$PLANNING_FILE" > "$tmp"
  mv "$tmp" "$PLANNING_FILE"

  # Also patch knowledge_path into issue context if it was deferred
  ISSUE_FILE=".nightgauge/pipeline/issue-${ISSUE_NUMBER}.json"
  if [ -f "$ISSUE_FILE" ]; then
    EXISTING_KP=$(jq -r '.knowledge_path // empty' "$ISSUE_FILE" 2>/dev/null)
    if [ -z "$EXISTING_KP" ]; then
      tmp=$(mktemp)
      jq --arg kp "$KNOWLEDGE_PATH" '.knowledge_path = $kp' "$ISSUE_FILE" > "$tmp"
      mv "$tmp" "$ISSUE_FILE"
    fi
  fi

  echo "planning-${ISSUE_NUMBER}.json updated with knowledge_path and knowledge_entries"

  # --- Tradeoff validation gate ---
  # When knowledge.require_decisions is true, enforce that decisions.md contains
  # at least one ADR block when the plan has 2+ distinct tradeoff keywords.
  # Tradeoff keywords are loaded from configs/knowledge-tradeoff-keywords.yaml.
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
    VALIDATE_OUTPUT=$("$BINARY" knowledge validate "$ISSUE_NUMBER" --workdir "." 2>&1)
    VALIDATE_EXIT=$?
    if [ $VALIDATE_EXIT -ne 0 ]; then
      echo ""
      echo "ERROR: decisions.md validation failed — see details below."
      echo "$VALIDATE_OUTPUT"
      echo ""
      echo "The plan contains tradeoff signals that require documented decisions."
      echo "Add at least one ADR block to $DECISIONS_FILE:"
      echo ""
      echo "  ## ADR-001: [Decision Title]"
      echo "  **Status**: Proposed"
      echo "  **Context**: [Background and constraints]"
      echo "  **Decision**: [What was decided and why]"
      echo "  **Consequences**: [Impact and trade-offs]"
      echo ""
      echo "To disable this gate: set knowledge.require_decisions: false in .nightgauge/config.yaml"
      exit 1
    fi
  fi
else
  echo "No knowledge_path found and knowledge not enabled — skipping knowledge base enrichment."
fi
```

**AI-generated content for enrichment**: The `{APPROACH_SUMMARY}`,
`{IN_SCOPE_SUMMARY}`, `{OUT_OF_SCOPE_SUMMARY}`, `{FILES_TO_CHANGE_LIST}`, and
`{KEY_CONSTRAINTS}` placeholders MUST be replaced by the AI with actual content
derived from planning. The bash block writes the template; the AI determines and
writes the real content. **Do NOT leave `{PLACEHOLDER}` tokens in the final
files** — the enrichment validation above will flag them.

For `decisions.md`, replace the `Example:` row with actual decisions and add
additional rows as needed. The secondary validation above will warn if the
example row is not replaced.

**If no meaningful decisions were made during planning** (e.g., the issue is a
straightforward bugfix), write a single decision entry explaining why no
architectural choices were needed, rather than leaving the decisions file empty.
