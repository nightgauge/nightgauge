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

    # Seed the bundle index. `nightgauge knowledge index` owns this file from
    # here on and overwrites the seed on its first run — README.md is not a
    # filename an Open Knowledge Format bundle root can also use.
    cat > "$KNOWLEDGE_DIR/index.md" << 'READMEEOF'
---
type: index
title: Knowledge Base
status: draft
generated:
  by: process:knowledge-scaffold
---

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

The knowledge base is **on by default** (ADR-020). Opt out in
`.nightgauge/config.yaml` only for repo footprint (it writes and commits files
under `.nightgauge/knowledge/`) or per-run token cost:

```yaml
knowledge:
  enabled: false
```

While enabled, running `/nightgauge:issue-pickup` automatically creates a
`{N}-{slug}/` directory with `PRD.md` and `decisions.md` pre-populated from the
issue body.

## Documentation

See [docs/KNOWLEDGE_BASE.md](../../docs/KNOWLEDGE_BASE.md) for the full schema
reference, naming conventions, and pipeline integration details.
READMEEOF

    echo "  + created: .nightgauge/knowledge/"
    echo "  + created: .nightgauge/knowledge/epics/"
    echo "  + created: .nightgauge/knowledge/features/"
    echo "  + created: .nightgauge/knowledge/index.md"
  fi
fi
````

**Idempotency**: The entire block is guarded by `if [ -d "$KNOWLEDGE_DIR" ]`. If
the directory exists, it is skipped entirely — no files are overwritten.

---

## Phase 6.8: Bootstrap Complexity Model

Initialize `.nightgauge/complexity-model.yaml` through the supported Go command.
`nightgauge outcome init` owns the canonical universal baseline, creates the
`.nightgauge` directory when needed, and leaves an existing model untouched.
The Go outcome recorder calls this same initializer automatically, so a fresh
repository can also learn from its first completed run without a setup-only
dependency.

When `--seed-from` is provided, seed the model from an existing repo's model
file instead of using bootstrap defaults. The seed operation copies universal
calibration data (size_calibration averages, type_adjustments, patterns) while
zeroing out repo-specific data (recent_outcomes, per-model counts, prediction
accuracy). This gives a new repo the benefit of cross-repo learning without
polluting with another repo's history.

```bash
MODEL_PATH=".nightgauge/complexity-model.yaml"

if [ -L ".nightgauge" ]; then
  echo "ERROR: refusing symlinked .nightgauge directory" >&2
  exit 1
elif [ -e "$MODEL_PATH" ] || [ -L "$MODEL_PATH" ]; then
  echo "$MODEL_PATH already exists — preserving it"
elif [ -n "$SEED_FROM" ]; then
  # Cross-repo seeding (#1323): Python YAML transform (jq cannot parse YAML)
  echo "Seeding complexity model from $SEED_FROM..."
  TODAY=$(date +%Y-%m-%d)
  python3 - "$SEED_FROM" "$TODAY" "$MODEL_PATH" << 'PYEOF'
import os, sys, tempfile, yaml

source_path = sys.argv[1]
today = sys.argv[2]
target_path = sys.argv[3]

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

temp_path = None
try:
    with tempfile.NamedTemporaryFile(
        mode='w', dir=os.path.dirname(target_path),
        prefix='.complexity-model-seed-', suffix='.yaml.tmp', delete=False,
    ) as f:
        temp_path = f.name
        yaml.dump(model, f, default_flow_style=False, allow_unicode=True)
        f.flush()
        os.fsync(f.fileno())
    try:
        os.link(temp_path, target_path)
        print(f"Seeded complexity model from {source_path}")
    except FileExistsError:
        print(f"{target_path} appeared during seeding — preserving it")
finally:
    if temp_path:
        try:
            os.unlink(temp_path)
        except FileNotFoundError:
            pass
PYEOF
  if [ $? -ne 0 ]; then
    echo "WARNING: Python seed transform failed. Using bootstrap defaults instead."
    SEED_FROM=""
  fi
fi

if [ ! -e "$MODEL_PATH" ] && [ ! -L "$MODEL_PATH" ]; then
  nightgauge outcome init
fi
```

The generated YAML is NOT committed to git (covered by
`.nightgauge/.gitignore`). It is populated with real data via the feedback loop
as pipeline runs accumulate.
