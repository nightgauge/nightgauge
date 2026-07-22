# Scaffold Knowledge & Bootstrap Complexity Model (Phases 6.7 and 6.8)

Procedural detail for Phase 6.7 (Scaffold Knowledge Directory) and Phase 6.8
(Bootstrap Complexity Model).

## Contents

- [Phase 6.7: Scaffold Knowledge Directory](#phase-67-scaffold-knowledge-directory)
- [Phase 6.8: Bootstrap Complexity Model](#phase-68-bootstrap-complexity-model)

---

## Phase 6.7: Scaffold Knowledge Directory

Create the knowledge base directory structure. Skip if `--skip-knowledge` was
passed.

````bash
if [ "$SKIP_KNOWLEDGE" = "true" ]; then
  echo "  -- Skipping knowledge directory (--skip-knowledge)"
else
  KNOWLEDGE_DIR=".nightgauge/knowledge"

  if [ -d "$KNOWLEDGE_DIR" ]; then
    echo "  ✓ exists: .nightgauge/knowledge/ (skipping — idempotent)"
  else
    # Create standard subdirectories
    mkdir -p "$KNOWLEDGE_DIR/epics" "$KNOWLEDGE_DIR/features"

    # Create README.md
    cat > "$KNOWLEDGE_DIR/README.md" << 'READMEEOF'
# .nightgauge/knowledge/

This directory stores persistent context for GitHub issues managed by the
Nightgauge pipeline.

## Structure

```text
knowledge/
├── epics/
│   └── {N}-{slug}/
│       ├── PRD.md        — Product requirements document
│       └── decisions.md  — Architectural decision log
└── features/
    └── {N}-{slug}/
        ├── PRD.md
        └── decisions.md
```

Issues with the `type:epic` label go under `epics/`. All others go under
`features/`.

## Activation

The knowledge base is **opt-in**. Enable it in `.nightgauge/config.yaml`:

```yaml
knowledge:
  enabled: true
  auto_scaffold: true
```

Once enabled, running `/nightgauge:issue-pickup` automatically creates a
`{N}-{slug}/` directory with `PRD.md` and `decisions.md` pre-populated from the
issue body.

## Documentation

See [docs/KNOWLEDGE_BASE.md](../../docs/KNOWLEDGE_BASE.md) for the full schema
reference, naming conventions, and pipeline integration details.
READMEEOF

    echo "  + created: .nightgauge/knowledge/"
    echo "  + created: .nightgauge/knowledge/epics/"
    echo "  + created: .nightgauge/knowledge/features/"
    echo "  + created: .nightgauge/knowledge/README.md"
  fi
fi
````

**Idempotency**: The entire block is guarded by `if [ -d "$KNOWLEDGE_DIR" ]`. If
the directory exists, it is skipped entirely — no files are overwritten.

---

## Phase 6.8: Bootstrap Complexity Model

Create `.nightgauge/complexity-model.yaml` with universal baseline
calibration defaults if it does not already exist. This ensures the pipeline can
run without a first-run crash in `ComplexityModelService.load()`.

When `--seed-from` is provided, seed the model from an existing repo's model
file instead of using bootstrap defaults. The seed operation copies universal
calibration data (size_calibration averages, type_adjustments, patterns) while
zeroing out repo-specific data (recent_outcomes, per-model counts, prediction
accuracy). This gives a new repo the benefit of cross-repo learning without
polluting with another repo's history.

```bash
if [ -n "$SEED_FROM" ]; then
  # Cross-repo seeding (#1323): Python YAML transform (jq cannot parse YAML)
  echo "Seeding complexity model from $SEED_FROM..."
  TODAY=$(date +%Y-%m-%d)
  python3 - "$SEED_FROM" "$TODAY" << 'PYEOF'
import sys, yaml, datetime

source_path = sys.argv[1]
today = sys.argv[2]

with open(source_path, 'r') as f:
    model = yaml.safe_load(f)

# Filter out repo-specific patterns; keep cross-project and untagged bootstrap patterns
def filter_patterns(patterns):
    return [p for p in (patterns or []) if p.get('source') != 'repo-specific']

# Reset repo-specific data
model['last_updated'] = today
model['bootstrap_date'] = today
model['seeded_from'] = source_path
model['total_observations'] = 0
model.setdefault('model_tracking', {})['observations_by_model'] = {}
model['learnings'] = [f"{today}: Model seeded from cross-repo baseline: {source_path}."]

# Zero sample counts but keep learned averages in size_calibration
for bucket in model.get('size_calibration', {}).values():
    bucket['sample_count'] = 0

# Reset prediction accuracy
model['prediction_accuracy'] = {
    'total_predictions': 0,
    'correct_predictions': 0,
    'by_type': {},
    'by_size': {},
    'recent_outcomes': [],
}

# Filter patterns
patterns = model.get('patterns', {})
for category in ['high_complexity', 'medium_complexity', 'low_complexity']:
    patterns[category] = filter_patterns(patterns.get(category, []))

with open('.nightgauge/complexity-model.yaml', 'w') as f:
    yaml.dump(model, f, default_flow_style=False, allow_unicode=True)

print(f"Seeded complexity model from {source_path}")
PYEOF
  if [ $? -ne 0 ]; then
    echo "WARNING: Python seed transform failed. Using bootstrap defaults instead."
    # Fall through to bootstrap creation below
    SEED_FROM=""
  fi
elif [ ! -f ".nightgauge/complexity-model.yaml" ]; then
  TODAY=$(date +%Y-%m-%d)
  cat > .nightgauge/complexity-model.yaml << YAML
schema_version: "1.0"
last_updated: "${TODAY}"
bootstrap_date: "${TODAY}"
total_observations: 0
decay:
  enabled: false
  half_life_days: 30
model_tracking:
  current_default: "claude-sonnet-4-6"
  observations_by_model: {}
patterns:
  high_complexity:
    - match: "refactor|redesign|rewrite"
      modifier: 1.5
      confidence: 0.45
      rationale: "Refactoring/redesign typically requires touching many files"
      observations: 0
    - match: "migrate|migration"
      modifier: 1.3
      confidence: 0.45
      rationale: "Migration work spans analysis, planning, and execution layers"
      observations: 0
    - match: "multi.?repo|workspace|cross.?repo"
      modifier: 1.5
      confidence: 0.50
      rationale: "Multi-repo features require coordination across boundaries"
      observations: 0
  medium_complexity:
    - match: "config|setting|option"
      modifier: 0
      confidence: 0.50
      rationale: "Configuration changes are moderate scope"
      observations: 0
    - match: "validation|schema|zod"
      modifier: 0
      confidence: 0.57
      rationale: "Schema/validation changes are moderate scope"
      observations: 0
  low_complexity:
    - match: "typo|spelling|wording"
      modifier: -1
      confidence: 0.70
      rationale: "Typo/spelling fixes are minimal scope"
      observations: 0
    - match: "readme|changelog|documentation"
      modifier: -0.8
      confidence: 0.65
      rationale: "Documentation-only changes are small scope"
      observations: 0
    - match: "bump|upgrade|version"
      modifier: -0.5
      confidence: 0.56
      rationale: "Version bumps are typically small"
      observations: 0
size_calibration:
  XS: { expected_lines: 50, actual_average_lines: 59, sample_count: 0 }
  S:  { expected_lines: 150, actual_average_lines: 213, sample_count: 0 }
  M:  { expected_lines: 500, actual_average_lines: 574, sample_count: 0 }
  L:  { expected_lines: 1200, actual_average_lines: 1476, sample_count: 0 }
  XL: { expected_lines: 2500, actual_average_lines: 2352, sample_count: 0 }
type_adjustments:
  feature:  { modifier: -1.45, observations: 0, rationale: "Seeded from cross-repo baseline (45 observations)" }
  bug:      { modifier: -0.6,  observations: 0, rationale: "Bugs tend toward smaller scope" }
  docs:     { modifier: -0.7,  observations: 0, rationale: "Documentation changes are typically smaller" }
  refactor: { modifier: 0.3,   observations: 0, rationale: "Refactors tend to touch more files" }
  chore:    { modifier: -0.3,  observations: 0, rationale: "Chores are typically small maintenance" }
priority_adjustments:
  critical: { modifier: 0.2,  rationale: "Critical issues often have broader scope",           observations: 0 }
  high:     { modifier: 0.1,  rationale: "High priority slightly correlates with complexity",  observations: 0 }
  medium:   { modifier: 0,    rationale: "Baseline priority",                                  observations: 0 }
  low:      { modifier: -0.1, rationale: "Low priority often simpler scope",                   observations: 0 }
lines_changed_thresholds:
  XS: 100
  S:  325
  M:  850
  L:  1850
  XL: 2500
learnings:
  - "${TODAY}: Bootstrap model created during repo-init with universal baseline calibration."
prediction_accuracy:
  total_predictions: 0
  correct_predictions: 0
  by_type: {}
  by_size: {}
  recent_outcomes: []
critical_files:
  description: "Files whose modification significantly increases issue complexity"
  registry: []
  per_file_modifier: 0.5
  max_modifier: 1.5
YAML
  echo "Created .nightgauge/complexity-model.yaml (bootstrap defaults)"
else
  echo ".nightgauge/complexity-model.yaml already exists — skipping"
fi
```

This YAML is NOT committed to git (covered by `.nightgauge/.gitignore`). It
is populated with real data via the feedback loop as pipeline runs accumulate.
