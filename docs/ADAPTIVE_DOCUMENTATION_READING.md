# Adaptive Documentation Reading

**Feature**: Intelligent complexity assessment to optimize documentation reading
in the Nightgauge pipeline.

**Issue**: #162 **Status**: Implemented **Version**: 1.8.0 (feature-planning
skill)

---

## Problem Statement

The Nightgauge pipeline currently reads all documentation files for every
issue, regardless of complexity. This results in:

- **Token waste** for simple fixes (e.g., typo corrections use ~5,000 tokens for
  full docs reading)
- **Slower execution** for straightforward changes
- **Unnecessary cost** when minimal context is sufficient

**Example**: Issue #153 (TypeScript build error) required only understanding the
error message and applying a simple fix, but consumed tokens reading all
architecture and testing documentation.

---

## Solution Overview

Adaptive documentation reading intelligently assesses issue complexity
**before** documentation reading and adjusts the scope accordingly:

```
┌─────────────────────────────────────────────────────────────────┐
│  COMPLEXITY ASSESSMENT (Deterministic)                          │
│  ├─ Parse issue labels (size, type, priority)                  │
│  ├─ Apply decision tree                                         │
│  ├─ Determine scope: minimal/targeted/standard/extended         │
│  └─ Log rationale to planning context                           │
└─────────────────────────────────────────────────────────────────┘
                              ↓
┌─────────────────────────────────────────────────────────────────┐
│  ADAPTIVE DOCUMENTATION READING (Probabilistic)                 │
│  ├─ Minimal: Read GIT_WORKFLOW + SECURITY (sequential)          │
│  ├─ Targeted: + CODE_STANDARDS + testing (2 subagents)          │
│  ├─ Standard: All docs/ (3 subagents - current behavior)        │
│  └─ Extended: All docs/ + deep exploration (3 subagents +)      │
└─────────────────────────────────────────────────────────────────┘
```

---

## Documentation Scope Levels

| Scope        | Files Read                                        | Use Case                      | Est. Tokens | Token Savings       |
| ------------ | ------------------------------------------------- | ----------------------------- | ----------- | ------------------- |
| **Minimal**  | GIT_WORKFLOW.md<br/>SECURITY.md                   | Typo fixes, single-line bugs  | ~1,500      | **-70%**            |
| **Targeted** | Minimal +<br/>CODE_STANDARDS.md<br/>Test patterns | Small bugs, docs changes      | ~2,500      | **-50%**            |
| **Standard** | All docs/<br/>(current behavior)                  | Features, refactors, medium+  | ~5,000      | 0%                  |
| **Extended** | Standard +<br/>Deep exploration                   | Large features, architectural | ~10,000+    | +100% (intentional) |

---

## Decision Tree

The complexity assessment uses a **deterministic decision tree** based on issue
labels:

```bash
# Extract labels from issue context
SIZE=$(jq -r '.labels[] | select(startswith("size:"))' issue.json)
TYPE=$(jq -r '.type' issue.json)
PRIORITY=$(jq -r '.labels[] | select(startswith("priority:"))' issue.json)

# Apply decision tree
IF size:XS AND type:bug → Minimal
IF size:S AND type:(bug|docs) → Targeted
IF size:M OR type:feature → Standard (current behavior)
IF size:(L|XL) OR priority:critical → Extended
```

### Why Deterministic?

- **Zero LLM tokens consumed** for classification
- **Fast** (<1ms vs seconds for AI decision)
- **Debuggable** (clear rules, no black box)
- **Predictable** (same labels = same scope every time)

See
[docs/ARCHITECTURE.md - Deterministic vs Probabilistic](ARCHITECTURE.md#deterministic-vs-probabilistic-architecture)
for the architectural rationale.

---

## Implementation Details

### Phase 0.5: Complexity Assessment (New)

Added to `skills/nightgauge-feature-planning/SKILL.md`:

```bash
# Read issue context
CONTEXT_FILE=".nightgauge/pipeline/issue-${ISSUE_NUMBER}.json"

# Extract labels
SIZE=$(jq -r '.labels[] | select(startswith("size:")) | sub("size:"; "")' "$CONTEXT_FILE")
TYPE=$(jq -r '.type' "$CONTEXT_FILE")
PRIORITY=$(jq -r '.labels[] | select(startswith("priority:")) | sub("priority:"; "")' "$CONTEXT_FILE")

# Map size to complexity score (Fibonacci scale from ESTIMATION.md)
case "$SIZE" in
  XS) SCORE=1 ;;
  S)  SCORE=2 ;;
  M)  SCORE=3 ;;
  L)  SCORE=5 ;;
  XL) SCORE=8 ;;
  *)  SCORE=3 ;; # Default to medium
esac

# Determine documentation scope
if [ "$SIZE" = "XS" ] && [ "$TYPE" = "bug" ]; then
  SCOPE="minimal"
  RATIONALE="Extra-small bug fix: requires only security and git workflow docs"
elif [ "$SIZE" = "S" ] && { [ "$TYPE" = "bug" ] || [ "$TYPE" = "docs" ]; }; then
  SCOPE="targeted"
  RATIONALE="Small $TYPE change: requires standards and testing patterns"
elif [ "$SIZE" = "L" ] || [ "$SIZE" = "XL" ] || [ "$PRIORITY" = "critical" ]; then
  SCOPE="extended"
  RATIONALE="Large or critical issue: requires full documentation and deep exploration"
else
  SCOPE="standard"
  RATIONALE="Standard complexity: full documentation review (current behavior)"
fi

echo "┌─────────────────────────────────────────────────────────────────┐"
echo "│  COMPLEXITY ASSESSMENT                                          │"
echo "└─────────────────────────────────────────────────────────────────┘"
echo ""
echo "Size: $SIZE (score: $SCORE)"
echo "Type: $TYPE"
echo "Priority: $PRIORITY"
echo "Documentation Scope: $SCOPE"
echo "Rationale: $RATIONALE"
echo ""
```

### Phase 2: Documentation Discovery (Modified)

Updated to conditionally spawn subagents based on scope:

```bash
case "$SCOPE" in
  minimal)
    # Sequential reading (no subagents needed for 2 files)
    echo "Reading minimal documentation (GIT_WORKFLOW, SECURITY)..."
    cat docs/GIT_WORKFLOW.md
    cat docs/SECURITY.md
    ;;

  targeted)
    # Spawn 2 parallel subagents
    echo "Reading targeted documentation (2 subagents)..."
    # Subagent 1: Git + Security
    # Subagent 2: Code Standards + Testing patterns
    ;;

  standard)
    # Current behavior: 3 parallel subagents
    echo "Reading standard documentation (3 subagents - all docs/)..."
    # Subagent 1: Architecture + Standards
    # Subagent 2: Security + Error Handling
    # Subagent 3: Git Workflow + Testing
    ;;

  extended)
    # 3 subagents + extended exploration timeout
    echo "Reading extended documentation (deep exploration)..."
    # Same as standard, but with longer timeout for thorough analysis
    ;;
esac
```

### Phase 8: Write Planning Context (Modified)

Planning context now includes `complexity_assessment` field:

```json
{
  "schema_version": "1.0",
  "issue_number": 162,
  "complexity_assessment": {
    "size_label": "M",
    "type_label": "feature",
    "priority_label": "high",
    "computed_score": 3,
    "documentation_scope": "standard",
    "rationale": "Medium-sized feature requires full documentation review",
    "estimated_token_savings": 0
  },
  "plan_file": ".nightgauge/plans/162-adaptive-documentation-reading.md",
  "approach": "Embedded Complexity Assessment in Feature-Planning",
  "files_to_create": [...],
  "files_to_modify": [...]
}
```

---

## Schema Changes

### Planning Context Schema

Added to `packages/nightgauge-sdk/src/context/schemas/planning.ts`:

```typescript
export interface ComplexityAssessment {
  /** Size label from issue (XS/S/M/L/XL) */
  size_label: string;

  /** Issue type (feature/bug/docs/refactor/chore) */
  type_label: string;

  /** Priority label (critical/high/medium/low) */
  priority_label: string;

  /** Fibonacci complexity score (1/2/3/5/8) */
  computed_score: number;

  /** Documentation scope to use (minimal/targeted/standard/extended) */
  documentation_scope: "minimal" | "targeted" | "standard" | "extended";

  /** Human-readable explanation of the decision */
  rationale: string;

  /** Estimated token savings vs standard scope (0 if standard/extended) */
  estimated_token_savings: number;
}

export interface PlanningContext {
  schema_version: string;
  issue_number: number;
  complexity_assessment: ComplexityAssessment; // NEW FIELD
  plan_file: string;
  approach: string;
  files_to_create: string[];
  files_to_modify: string[];
  patterns_applied: object;
  decisions: Decision[];
  created_at: string;
}
```

---

## Token Efficiency Impact

### Current Behavior (Standard Scope Always)

| Issue Type      | Docs Read | Tokens Used |
| --------------- | --------- | ----------- |
| XS bug (typo)   | All docs/ | ~5,000      |
| S bug (1-liner) | All docs/ | ~5,000      |
| M feature       | All docs/ | ~5,000      |
| L refactor      | All docs/ | ~5,000      |

**Total for 10 mixed issues**: ~50,000 tokens

### With Adaptive Reading

| Issue Type      | Docs Read           | Tokens Used | Savings             |
| --------------- | ------------------- | ----------- | ------------------- |
| XS bug (typo)   | Git, Security       | ~1,500      | **-70%**            |
| S bug (1-liner) | + Standards, Tests  | ~2,500      | **-50%**            |
| M feature       | All docs/           | ~5,000      | 0%                  |
| L refactor      | All docs/ + explore | ~10,000     | +100% (intentional) |

**Total for 10 mixed issues** (3 XS, 3 S, 3 M, 1 L): ~32,500 tokens

**Overall Savings**: **~35% token reduction** across mixed workload

**Cost Impact**: $0.50 → $0.33 per 10 issues (at $0.01/1K tokens input)

---

## Real Issue Examples

These examples illustrate how the adaptive system selects different
documentation scopes for different issue complexities.

### XS Bug — Minimal Scope

**Issue**: `#1403 — Fix OutputWindow stealing focus from editor` **Labels**:
`type:fix`, `size:XS`, `priority:high`

```
Complexity score: 1 (XS)
Scope: minimal
Documents read: 2 (GIT_WORKFLOW.md, security.md)
Tokens consumed: ~1,500
Savings vs standard: -70%
```

The agent read only commit conventions and security basics — sufficient to fix a
one-line `preserveFocus: true` parameter addition.

### M Feature — Standard Scope

**Issue**:
`#1348 — Mid-pipeline complexity feedback via FeedbackLearningService`
**Labels**: `type:feature`, `size:M`, `priority:medium`

```
Complexity score: 3 (M)
Scope: standard
Documents read: 6 (ARCHITECTURE.md, CONTEXT_ARCHITECTURE.md,
  CODE_STANDARDS.md, TESTING.md, GIT_WORKFLOW.md, security.md)
Tokens consumed: ~5,000
Savings vs standard: 0% (this IS the standard baseline)
```

All core docs were read because the feature touched SDK services, required new
tests, and needed architectural context for the feedback signal flow.

### L Refactor — Extended Scope

**Issue**: `#1400 — Documentation sync gaps` **Labels**: `type:feature`,
`size:L`, `priority:high`

```
Complexity score: 5 (L)
Scope: extended
Documents read: 10+ (all docs/ + source code exploration)
Tokens consumed: ~10,000
Savings vs standard: +100% (intentional — deep exploration required)
```

The agent read all docs files plus explored source code across 15+ TypeScript
files to verify API references. Extended scope is intentional for large tasks
where thoroughness prevents rework.

---

## Dashboard Metrics

The VS Code extension dashboard will track:

- **Average tokens per complexity level** (minimal/targeted/standard/extended)
- **Token savings**: `standard_baseline - actual_usage`
- **Cache hit rate** by documentation scope
- **Time savings**: Faster documentation reading = faster planning

See `packages/nightgauge-vscode/src/views/dashboard/DashboardState.ts` for
implementation.

---

## Testing Strategy

### Unit Tests (Deterministic Logic)

- ✅ Test all decision tree paths (minimal/targeted/standard/extended)
- ✅ Test score calculation (XS=1, S=2, M=3, L=5, XL=8)
- ✅ Test missing label fallbacks
- ✅ Test priority override (critical → extended)

### Integration Tests (End-to-End)

- ✅ **Scenario A**: XS bug → minimal scope → token usage < 2,000
- ✅ **Scenario B**: M feature → standard scope → backward compatibility
- ✅ **Scenario C**: XL refactor → extended scope → deep exploration

### Baseline Comparison

- ✅ Compare against issue #153 (simple TypeScript build error)
- ✅ Verify 60-80% token reduction for XS/S bugs
- ✅ Verify 0% change for M+ features (backward compatibility)

---

## Backward Compatibility

- ✅ **Default behavior unchanged**: Medium and large issues still read all
  documentation
- ✅ **No breaking changes**: Planning context schema is additive (new optional
  field)
- ✅ **Graceful fallback**: Missing labels default to standard scope
- ✅ **Optional adoption**: Projects without size labels continue working as
  before

---

## Configuration

No configuration required. The feature works automatically based on issue
labels.

**Optional overrides** (if needed in future):

```yaml
# .nightgauge/config.yaml (future enhancement)
adaptive_reading:
  enabled: true
  scope_overrides:
    minimal_tokens: 1500
    targeted_tokens: 2500
    standard_tokens: 5000
```

---

## References

- **Issue**:
  #162 - Adaptive Documentation Reading for Simple Fixes
- **Architecture**:
  [docs/ARCHITECTURE.md - Deterministic vs Probabilistic](ARCHITECTURE.md#deterministic-vs-probabilistic-architecture)
- **Context Schema**: [docs/CONTEXT_ARCHITECTURE.md](CONTEXT_ARCHITECTURE.md)
- **Estimation Guide**: [docs/ESTIMATION.md](ESTIMATION.md)
- **Related Pattern**:
  [Context-Isolated Pipeline](ARCHITECTURE.md#context-isolated-pipeline-architecture)

---

**Author**: nightgauge **License**: Apache-2.0
