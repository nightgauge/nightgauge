#!/usr/bin/env bash
# capture-outcome-gap-fixture.sh — capture REAL pipeline telemetry as the
# fixtures for the #304 learning-outcome recording gap.
#
# Issue #304: the learning/calibration outcome corpus
# (.nightgauge/pipeline/history/outcomes.jsonl) had exactly one writer, the Go
# scheduler. Extension-path runs (ConcurrentPipelineManager →
# HeadlessOrchestrator → IPC pipeline.notifyComplete) never entered that loop,
# so in the mode the product is actually operated in nothing recorded an
# outcome. The fixtures this script emits are the evidence AND the input to
# internal/ipc/server_learning_outcome_test.go — the run-record shape under test
# is captured from real runs, never hand-authored (#166).
#
# Reproducible: re-running against the same history reproduces byte-identical
# fixtures (selection is deterministic — first match in sorted file/line order).
#
# Redaction (this is a PUBLIC repo): repo/owner, issue number, issue title,
# issue body, branch name, and any absolute /Users|/home path are replaced with
# stable placeholders. Only the record SHAPE — stages, model_selection, token
# totals, outcome, routing, size — is preserved, and only the shape is what the
# test asserts on.
#
# Usage:
#   scripts/capture-outcome-gap-fixture.sh [WORKSPACE_ROOT]
#
# WORKSPACE_ROOT defaults to the git repository root. Fixtures are written to
# internal/ipc/testdata/outcome-gap/ relative to the repository this script
# lives in.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORKSPACE_ROOT="${1:-$REPO_ROOT}"
OUT_DIR="$REPO_ROOT/internal/ipc/testdata/outcome-gap"

HISTORY_DIR="$WORKSPACE_ROOT/.nightgauge/pipeline/history"
if [ ! -d "$HISTORY_DIR" ]; then
  echo "error: no pipeline history at $HISTORY_DIR — nothing to capture" >&2
  echo "       pass a workspace root that has run pipelines: $0 <WORKSPACE_ROOT>" >&2
  exit 1
fi

mkdir -p "$OUT_DIR"

HISTORY_DIR="$HISTORY_DIR" OUT_DIR="$OUT_DIR" python3 - <<'PY'
import glob
import json
import os
import re
import sys
from datetime import datetime, timezone

history_dir = os.environ["HISTORY_DIR"]
out_dir = os.environ["OUT_DIR"]

daily = sorted(
    f for f in glob.glob(os.path.join(history_dir, "*.jsonl"))
    if not f.endswith("outcomes.jsonl")
)
outcomes_path = os.path.join(history_dir, "outcomes.jsonl")

HOME_PATH = re.compile(r"/(?:Users|home)/[^/\s\"]+")


def scrub_paths(value):
    """Replace every absolute home-directory path with a placeholder."""
    if isinstance(value, str):
        return HOME_PATH.sub("/REDACTED/HOME", value)
    if isinstance(value, list):
        return [scrub_paths(v) for v in value]
    if isinstance(value, dict):
        return {k: scrub_paths(v) for k, v in value.items()}
    return value


def is_extension_path(record):
    """A run record written by the extension funnel (pipeline.notifyComplete).

    Two independent signals, both required:
      - no outcome_prediction: the Go scheduler always threads one in via
        recordOutcome; the extension path never did (that IS the #304 defect).
      - at least one stage carries execution_path: only the TypeScript
        HeadlessOrchestrator's deterministic-first hooks produce that (#309),
        replayed onto the runtime by the notifyComplete handler.
    """
    if record.get("record_type") != "run":
        return False
    if record.get("outcome_prediction") is not None:
        return False
    stages = record.get("stages") or {}
    return any(isinstance(v, dict) and "execution_path" in v for v in stages.values())


def redact_run(record, issue_number):
    r = scrub_paths(dict(record))
    r["repo"] = "acme/widget"
    r["issue_number"] = issue_number
    r["title"] = "REDACTED — real issue title removed for public fixture"
    r["branch"] = "feat/%d-redacted" % issue_number
    r.pop("body", None)
    return r


def redact_outcome(record, issue_number):
    o = scrub_paths(dict(record))
    o["repo"] = "acme/widget"
    o["issueNumber"] = issue_number
    return o


runs = []
for path in daily:
    with open(path) as fh:
        for line in fh:
            line = line.strip()
            if not line:
                continue
            try:
                runs.append(json.loads(line))
            except json.JSONDecodeError:
                continue

all_runs = [r for r in runs if r.get("record_type") == "run"]
ext_runs = [r for r in runs if is_extension_path(r)]

success = next((r for r in ext_runs if r.get("outcome") == "complete"), None)
failed = next((r for r in ext_runs if r.get("outcome") == "failed"), None)
if success is None or failed is None:
    print(
        "error: need at least one complete AND one failed extension-path run record; "
        "found complete=%s failed=%s in %d extension-path records"
        % (success is not None, failed is not None, len(ext_runs)),
        file=sys.stderr,
    )
    sys.exit(1)

outcomes = []
if os.path.exists(outcomes_path):
    with open(outcomes_path) as fh:
        for line in fh:
            line = line.strip()
            if not line:
                continue
            try:
                outcomes.append(json.loads(line))
            except json.JSONDecodeError:
                continue
if not outcomes:
    print("error: outcomes.jsonl is empty — no scheduler-path corpus sample to capture", file=sys.stderr)
    sys.exit(1)


def write_json(name, obj):
    with open(os.path.join(out_dir, name), "w") as fh:
        json.dump(obj, fh, indent=2, sort_keys=True)
        fh.write("\n")


write_json("run-record.json", redact_run(success, 1001))
write_json("run-record-failed.json", redact_run(failed, 1002))
write_json("outcome.json", redact_outcome(outcomes[0], 1003))

model_less = sum(1 for o in outcomes if not o.get("predictedModel"))
size_small = sum(1 for o in outcomes if o.get("predictedSize") == "small")
score_zero = sum(1 for o in outcomes if not o.get("complexityScore"))
last_outcome = max(o.get("completedAt", "") for o in outcomes)
first_day = os.path.basename(daily[0])[:10] if daily else "n/a"
last_day = os.path.basename(daily[-1])[:10] if daily else "n/a"

readme = """# `outcome-gap` fixtures — provenance

Captured by `scripts/capture-outcome-gap-fixture.sh` from the real pipeline
telemetry of a live Nightgauge workspace. Nothing here is hand-authored: the
record shapes under test in `internal/ipc/server_learning_outcome_test.go` are
verbatim (redacted) copies of records this machine's pipeline actually wrote.

## Captured

- **Date (UTC)**: {captured_at}
- **History window scanned**: `{first_day}` … `{last_day}`
- **Run records in that window**: **{total_runs}**
- **…identified as extension-path**: **{ext_runs}**
- **Learning outcome records in the corpus, all time**: **{n_outcomes}**
- **Most recent outcome record**: `{last_outcome}`

Extension-path identification (both signals required, see the script):
no `outcome_prediction` (the Go scheduler always sets one) **and** at least one
stage carrying `execution_path` (only the TypeScript HeadlessOrchestrator
produces that, #309). The gap #304 fixes is the whole distance between those
last two numbers: every extension-path run wrote a run record and no outcome.

## The corpus was not just small — it was degenerate

Of the {n_outcomes} outcome records that exist:

- **{model_less}/{n_outcomes}** have `predictedModel: ""` — no model attribution at all,
  so the model-routing calibration had nothing to calibrate on.
- **{size_small}/{n_outcomes}** have `predictedSize: "small"`, because the scheduler's
  `predictedSizeLabel(0)` maps an unknown complexity score onto the same label
  as a genuinely small issue.
- **{score_zero}/{n_outcomes}** have `complexityScore: 0`.

`run-record.json` and `run-record-failed.json` are the counter-evidence: a real
extension-path run record already carries `stages[*].model_selection.model`,
token totals and cost, so the outcome derived from it at the IPC seam is
non-degenerate. That is what `TestLearningOutcomeFor_FromCapturedRunRecord`
asserts.

## Files

- `run-record.json` — real extension-path run record, `outcome: complete`.
- `run-record-failed.json` — real extension-path run record, `outcome: failed`.
- `outcome.json` — real scheduler-path learning outcome, the degenerate shape.

## Redaction

This is a public repository. The script replaces repo/owner, issue number,
issue title, issue body, branch name, and every absolute `/Users/...` or
`/home/...` path with stable placeholders (`acme/widget`, `1001`/`1002`/`1003`,
`feat/<n>-redacted`, `/REDACTED/HOME`). Record _shape_ — stages,
`model_selection`, token totals, `outcome`, `routing`, `size` — is preserved
untouched, and shape is the only thing the tests assert on.

## Regenerating

```bash
scripts/capture-outcome-gap-fixture.sh [WORKSPACE_ROOT]
```

Selection is deterministic (first match in sorted file/line order), so
re-running against the same history reproduces byte-identical fixtures. Against
a different workspace the numbers above change — update this README from the
script's output rather than editing it by hand.
""".format(
    captured_at=datetime.now(timezone.utc).strftime("%Y-%m-%d"),
    first_day=first_day,
    last_day=last_day,
    total_runs=len(all_runs),
    ext_runs=len(ext_runs),
    n_outcomes=len(outcomes),
    last_outcome=last_outcome,
    model_less=model_less,
    size_small=size_small,
    score_zero=score_zero,
)

with open(os.path.join(out_dir, "README.md"), "w") as fh:
    fh.write(readme)

print("captured %d extension-path run records (of %d total) and %d outcome records"
      % (len(ext_runs), len(all_runs), len(outcomes)))
print("wrote run-record.json, run-record-failed.json, outcome.json, README.md to %s" % out_dir)
PY
