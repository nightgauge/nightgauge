# Phases 2.8–2.9: Cross-Repo Reality Check & Scope Gates — Procedural Detail

Detail bodies for Phase 2.8 (Cross-Repo Reality Check), Phase 2.85 (Oversized-Scope Hard-Gate), and Phase 2.9 (Epic Decomposition Hard-Gate) of the `nightgauge-issue-create` skill. The blocking-check summary for each gate stays inline in SKILL.md; this file holds the detection logic and gate-decision shell.

## Contents

- [Phase 2.8: Cross-Repo Reality Check](#phase-28-cross-repo-reality-check)
- [Phase 2.85: Oversized-Scope Hard-Gate](#phase-285-oversized-scope-hard-gate)
- [Phase 2.9: Epic Decomposition Hard-Gate](#phase-29-epic-decomposition-hard-gate)

## Phase 2.8: Cross-Repo Reality Check (Recommended for API Issues)

**Gate**: Runs when the issue references platform API endpoints, cross-repo
dependencies, or integration with companion repositories. Skip for purely
internal issues.

Before creating issues that assume API endpoints exist or that companion repos
provide specific functionality, verify those assumptions against reality:

1. **API endpoint verification**: If the issue references platform API endpoints
   (e.g., "calls GET /v1/pipeline-runs"), check if those endpoints actually
   exist:

   ```bash
   # Quick probe — does the endpoint respond with something other than 404?
   STATUS=$(curl -s -o /dev/null -w "%{http_code}" http://localhost:3000/v1/<path>)
   ```

   - If `404`: the endpoint doesn't exist. Add a `blockedBy` note referencing
     the platform epic that will create it, or flag the assumption.
   - If `401/403`: the endpoint exists but may need a different auth method.
   - If Docker is not running, skip with a warning rather than failing.

2. **Companion repo verification**: If the issue depends on code in another
   repo (e.g., "platform provides /v1/auth/me"), verify:

   ```bash
   # Check if the referenced endpoint/file exists in the companion repo
   grep -r "auth/me" ../acme-platform/packages/api/src/routes/
   ```

   - If not found: flag as a cross-repo dependency that needs tracking.

3. **Existing backlog check**: Before creating the issue, check if a similar
   issue already exists in the target repo or companion repos:

   ```bash
   nightgauge issue list --owner nightgauge --repo <repo> --search "<keywords>" --limit 5
   ```

   - If a match exists: reference it rather than creating a duplicate.

4. **Doc freshness check**: If the issue will change API surface, auth flows,
   or integration patterns, note which docs need updating:
   - `docs/ECOSYSTEM.md` (cross-repo truth)
   - `docs/ARCHITECTURE.md` (per-repo architecture)
   - `CLAUDE.md` (AI agent instructions)
   - Platform OpenAPI spec

5. **Output**: Add a `## Cross-Repo Dependencies` section to the issue body
   listing verified/unverified assumptions:

   ```markdown
   ## Cross-Repo Dependencies

   - ✅ GET /v1/analytics/health — verified (200)
   - ❌ GET /v1/pipeline-runs — not found (404), blocked by platform #491
   - ⚠️ Docker not running — endpoint verification skipped
   ```

**Why this phase exists**: Issues created with unverified API assumptions
produce code that works against mocks but fails against the real platform. This
phase catches those gaps at creation time, not at integration time.

## Phase 2.85: Oversized-Scope Hard-Gate

**Gate**: Runs UNCONDITIONALLY for **every issue type** (feature, bug, refactor,
chore, docs, spike, AND epic — unlike Phase 2.9, this gate does NOT skip
non-epics). Cannot be bypassed without an explicit
`<!-- nightgauge:oversized-scope-accepted -->` marker (or the phrase
`oversized scope accepted`) in the issue body.

**Why this phase exists**: Issue #3811's feature-dev burned **$112.77** churning
on a single issue that secretly meant "refactor ~18 oversized skills". It sailed
through as a `size:L` standalone issue because the size predictor only labels
(XL routes to a stronger model — worse, not blocked) and the Phase 2.9
decomposition gate explicitly skips all non-epic issues (line 878). Every
runtime runaway defense then conflated activity with progress and never stopped
it. This gate is the cheapest place to stop the whole class: an issue bundling
many independent refactors must be decomposed into sub-issues under an epic
**before** any GitHub mutation, not discovered at $112 of feature-dev churn.

**Reuses the file/size signals already computed in the sizing phase** (Size
Prediction & File-Based Sizing Heuristics, lines ~200-258): the set of distinct
top-level target files referenced in the technical notes, and the predicted size
label.

**Detection logic** (reuses `ISSUE_BODY`, `TYPE_LABEL`, and the size signals):

```bash
# --- Signal 1: distinct CHANGE-TARGET files (shared deterministic extractor) ---
# `nightgauge issue extract-targets` is the SAME implementation the epic wave
# planner uses (#79), so gate counting can never drift from wave planning. It
# counts the change surface, not the bibliography: markdown-link destinations
# are citations and never count, and an explicit `file_ownership` list in the
# `nightgauge:dependency-metadata` block replaces prose inference entirely.
# Authors: cite evidence as markdown links (or declare file_ownership);
# bare code paths in prose still count as targets.
DISTINCT_TARGETS=$(printf '%s' "${ISSUE_BODY}" \
  | nightgauge issue extract-targets --json 2>/dev/null \
  | jq -r '.count' 2>/dev/null || echo 0)

# --- Signal 2: predicted size == XL (data-driven, from the sizing phase) ---
# PREDICTED_SIZE is the SizeLabel resolved earlier (complexity model or
# `nightgauge size predict <num> --json`). Default to the heuristic label.
PREDICTED_SIZE="${PREDICTED_SIZE:-${SIZE_LABEL:-M}}"

# --- Signal 3: independent acceptance-criteria groups ---
# A bundled multi-refactor issue enumerates many independent groups (e.g. one
# bullet per skill/file to refactor). Count distinct top-level list items whose
# verb signals an independent unit of refactor/migration work.
AC_GROUP_COUNT=$(printf '%s\n' "${ISSUE_BODY}" \
  | grep -ciE '^[[:space:]]*([-*]|[0-9]+\.)[[:space:]]+(refactor|migrate|convert|split|rewrite|extract|decompose|reduce|trim)[[:space:]]')

# --- Override marker (mirrors the Phase 2.9 marker pattern) ---
SCOPE_OVERRIDE=false
if printf '%s' "${ISSUE_BODY}" | grep -qi "nightgauge:oversized-scope-accepted\|oversized scope accepted"; then
  SCOPE_OVERRIDE=true
fi

# --- Thresholds: ≥6 distinct targets, OR size==XL, OR ≥6 independent AC groups ---
OVERSIZED=false
TRIGGERS=""
if [ "${DISTINCT_TARGETS:-0}" -ge 6 ]; then
  OVERSIZED=true
  TRIGGERS="${TRIGGERS} ${DISTINCT_TARGETS} distinct target files (>=6);"
fi
if echo "${PREDICTED_SIZE}" | grep -qiE '^XL$'; then
  OVERSIZED=true
  TRIGGERS="${TRIGGERS} predicted size == XL;"
fi
if [ "${AC_GROUP_COUNT:-0}" -ge 6 ]; then
  OVERSIZED=true
  TRIGGERS="${TRIGGERS} ${AC_GROUP_COUNT} independent acceptance-criteria groups (>=6);"
fi
```

**Gate decision**:

```bash
if [ "$OVERSIZED" = "true" ]; then

  # An epic that is BEING decomposed now (sub-issues planned in Phase 2) is the
  # CORRECT shape for oversized scope — that is exactly what the gate wants.
  if echo "${TYPE_LABEL}" | grep -qi "epic" && [ "${SUB_ISSUE_COUNT:-0}" -gt 0 ]; then
    echo "Phase 2.85: PASS — oversized scope is decomposed into ${SUB_ISSUE_COUNT} sub-issues under an epic"

  elif [ "$SCOPE_OVERRIDE" = "true" ]; then
    echo "Phase 2.85: PASS — oversized-scope override marker present (triggers:${TRIGGERS})"
    echo "  WARNING: Operator explicitly accepted oversized scope. Recurrence risk of #3811-style runaway."

  else
    # Oversized AND not decomposed AND no override — reject before any mutation.
    cat >&2 << GATE_ERROR
ERROR: oversized-scope-gate

  This issue is OVERSIZED for a single executable ticket.
  Triggered by:${TRIGGERS}

  A single issue bundling many independent targets/refactors is the root cause
  of pipeline runaways (incident #3811: \$112.77 of feature-dev churn on one
  issue that meant "refactor ~18 skills"). Choose one:

  Path A — Decompose into an epic (recommended):
    Create a type:epic parent and split the work into one sub-issue per
    independent target. Re-run /nightgauge:issue-create with the
    decomposition; Phase 2 will create the sub-issues and Phase 2.9 will
    verify the epic shape.

  Path B — Explicit override (use only when the scope is genuinely atomic):
    Add this marker anywhere in the issue body:
      <!-- nightgauge:oversized-scope-accepted -->
    OR include the phrase "oversized scope accepted".
    Use this ONLY when the many file references are a single cohesive change
    (e.g. a mechanical rename touching N files in one PR), NOT N independent
    refactors. The runtime runaway defenses are your only backstop after this.

  Refused to create the issue until it is decomposed or explicitly overridden.
GATE_ERROR
    exit 1
  fi

else
  echo "Phase 2.85: PASS — scope within single-ticket bounds (targets=${DISTINCT_TARGETS:-0}, size=${PREDICTED_SIZE}, ac_groups=${AC_GROUP_COUNT:-0})"
fi
```

## Phase 2.9: Epic Decomposition Hard-Gate

**Gate**: Runs UNCONDITIONALLY when `TYPE_LABEL=epic`. Skipped for all
non-epic issues. Cannot be bypassed without an explicit `--accept-empty-epic`
operator flag.

**Why this phase exists**: Issue #3313 was created as a bare epic with no
sub-issues and only decomposed days later, leaving the pipeline unable to pick
it up. Silent half-decomposed epics corrupt the project board's "Ready"
semantics. This gate enforces that every epic creation falls into one of three
explicit shapes before any GitHub mutation.

**Detection logic**:

```bash
IS_EPIC=false
if echo "${TYPE_LABEL}" | grep -qi "epic"; then
  IS_EPIC=true
fi

if [ "$IS_EPIC" = "false" ]; then
  # Not an epic — gate does not apply
  echo "Phase 2.9: skipped (not a type:epic issue)"
  # Continue to next phase
else

  # Detect which shape the epic falls into
  HAS_SUB_ISSUES=false
  HAS_PLACEHOLDER_MARKER=false
  HAS_STANDALONE_MARKER=false

  # Shape A: sub-issues were planned in Phase 2
  if [ "${SUB_ISSUE_COUNT:-0}" -gt 0 ]; then
    HAS_SUB_ISSUES=true
  fi

  # Shape B: explicit placeholder marker in issue body
  if echo "${ISSUE_BODY}" | grep -qi "nightgauge:decompose-later\|placeholder.*decompose later\|decompose later.*placeholder"; then
    HAS_PLACEHOLDER_MARKER=true
  fi

  # Shape C: standalone epic declaration
  if echo "${ISSUE_BODY}" | grep -qi "nightgauge:standalone-epic\|standalone epic\|intentionally.*no sub-issues"; then
    HAS_STANDALONE_MARKER=true
  fi

fi
```

**Gate decision**:

```bash
if [ "$IS_EPIC" = "true" ]; then

  if [ "$HAS_SUB_ISSUES" = "true" ]; then
    echo "Phase 2.9: PASS — epic has ${SUB_ISSUE_COUNT} sub-issues planned (Path A)"

  elif [ "$HAS_PLACEHOLDER_MARKER" = "true" ]; then
    echo "Phase 2.9: PASS — placeholder marker detected (Path B)"
    echo "  → Creating follow-up type:chore issue for decomposition tracking"
    PLACEHOLDER_CHORE_TITLE="chore: decompose epic — ${EPIC_TITLE}"
    PLACEHOLDER_CHORE_BODY="## Epic Decomposition Chore

This issue tracks the decomposition of the parent epic into executable sub-issues.

Parent epic: #${EPIC_NUMBER}

### When to close this chore

Close this chore when the parent epic has been fully decomposed into sub-issues
and all sub-issues have been added to the project board with appropriate
priority/size/status metadata.

### Decomposition checklist

- [ ] Sub-issues identified and documented
- [ ] Sub-issues created via /nightgauge-issue-create
- [ ] All sub-issues linked to parent epic via addSubIssue mutation
- [ ] All sub-issues added to project board with Status set
- [ ] This chore closed
"
    # Create the follow-up chore — body written to temp file (hook-safe, per ADR-002)
    CHORE_BODY_FILE=$(mktemp)
    printf '%s' "$PLACEHOLDER_CHORE_BODY" > "$CHORE_BODY_FILE"
    CHORE_NUMBER=$(gh issue create \
      --repo "${REPO}" \
      --title "$PLACEHOLDER_CHORE_TITLE" \
      --body-file "$CHORE_BODY_FILE" \
      --label "type:chore,priority:medium,size:S" \
      --json number --jq '.number')
    rm -f "$CHORE_BODY_FILE"
    if [ -z "$CHORE_NUMBER" ]; then
      echo "ERROR: Path B — follow-up chore creation failed (gh issue create returned empty number)" >&2
      echo "  Epic will NOT be created. Fix the chore creation error and re-run." >&2
      exit 1
    fi
    echo "  → Follow-up chore created: #${CHORE_NUMBER}"
    echo "  → Epic will be created without sub-issues; decomposition tracked by #${CHORE_NUMBER}"

  elif [ "$HAS_STANDALONE_MARKER" = "true" ]; then
    echo "Phase 2.9: PASS — standalone epic marker detected (Path C)"
    echo "  → Epic will be created without sub-issues per explicit declaration"

  else
    # No valid shape — reject before any GitHub mutation
    cat >&2 << 'GATE_ERROR'
ERROR: epic-decomposition-gate

  A type:epic issue cannot be created without one of the three valid shapes:

  Path A — Decompose now:
    Include sub-issues in the issue body. The skill creates them in Phase 3.
    (This is the standard path for well-understood epics.)

  Path B — Placeholder, decompose later:
    Add this marker anywhere in the epic body:
      <!-- nightgauge:decompose-later -->
    OR include the phrase "placeholder, decompose later" in the body.
    The skill will create a follow-up type:chore issue to track decomposition.
    (Use this when the epic scope is clear but sub-issue breakdown needs more
    thought. References incident #3313 — created bare, decomposed days later.)

  Path C — Standalone epic (no sub-issues intentional):
    Add this marker anywhere in the epic body:
      <!-- nightgauge:standalone-epic -->
    OR include the phrase "standalone epic" or "intentionally no sub-issues".
    (Use this only for single-deliverable epics tracked as one atomic PR.)

  Refused to create epic until one of the above shapes is declared.
GATE_ERROR
    exit 1
  fi

fi
```

When Path B produces a chore (`CHORE_NUMBER` is set), Phase 4.9's manifest
entry for the epic SHOULD include `"decomposition_chore": <CHORE_NUMBER>` as an
optional integer field so the terminal audit in Phase 6 can verify the chore
exists and was added to the board.
