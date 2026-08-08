#!/usr/bin/env bash
# capture-attention-fixture.sh — capture the REAL DecisionRequest envelope
# grammar from a live `.nightgauge/attention/` store, as the evidence corpus
# for the #305 run-scoped raise verb.
#
# Issue #166's evidence rule: a test must not hand-author the shape it claims to
# pin. The three run-scoped producers the IPC raise verb (#305) exposes must
# write records that are indistinguishable, envelope-wise, from what the
# shipping store has always written — that is the whole "no parallel second
# attention system" claim. The only honest way to test it is against records
# the shipping store actually wrote.
#
# ------------------------------------------------------------------------
# WHAT IS CAPTURED — the ENVELOPE, never the payload.
#
# For each `dr_*.json` record: schema_version, kind, severity, standing,
# default_action, lifecycle state, which lifecycle sub-records are present,
# which context FIELD NAMES are populated, and each option's verb. Plus the
# journal's action vocabulary.
#
# Every one of those is a machine token drawn from a closed registry that lives
# in this public repository (internal/attention/schema.go, verbs.go,
# store.go/standing.go action constants). Producer NAMES are captured for the
# same reason: they are constants in internal/orchestrator/attention_wiring.go
# and internal/attention/sweep/, not user data.
#
# ------------------------------------------------------------------------
# REDACTION — deny by default.
#
# NOTHING free-form is captured. Titles, bodies, blocker prose, URLs,
# idempotency keys, repo slugs, issue/PR numbers, fingerprints, ids, actors,
# steer text, notes, and every timestamp are DROPPED — not masked, not
# truncated, dropped. A context field contributes only its NAME to a sorted
# list; its value never leaves the machine. This is a public repository and the
# source store is a multi-repo workspace that mixes private repositories' runs,
# so the rule is allowlist-only: a field is captured because it is enumerated
# below, never because it looked safe.
#
# The one derived number kept per envelope is `count` — how many records share
# that exact envelope — which is a property of the corpus, not of any record.
#
# ------------------------------------------------------------------------
# REPRODUCIBLE.
#
# The output is a pure function of the input store: envelopes are deduped and
# emitted in sorted order, and no field is taken from the wall clock. Re-running
# on the same store on a different day produces a byte-identical file. The
# provenance block records WHICH store, WHICH commit, and WHEN — those three are
# written to the README, deliberately not into the JSON, so the fixture itself
# never churns.
#
# Usage:
#   scripts/capture-attention-fixture.sh [SOURCE_STORE_DIR] [OUT_FILE]
#
# Defaults:
#   SOURCE_STORE_DIR  .nightgauge/attention  (relative to the repo root)
#   OUT_FILE          internal/attention/testdata/captured-envelopes.json
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SRC="${1:-$REPO_ROOT/.nightgauge/attention}"
OUT="${2:-$REPO_ROOT/internal/attention/testdata/captured-envelopes.json}"

if [[ ! -d "$SRC" ]]; then
  echo "capture-attention-fixture: no attention store at $SRC" >&2
  echo "  Point this at a workspace that has run the pipeline." >&2
  exit 1
fi

mkdir -p "$(dirname "$OUT")"

SRC="$SRC" OUT="$OUT" python3 - <<'PY'
import collections
import glob
import json
import os

src = os.environ["SRC"]
out = os.environ["OUT"]

# ALLOWLIST. A key absent from here is never read, so a schema that grows a
# free-form field does not silently start leaking it into a public fixture.
CONTEXT_FIELDS = [
    "repo", "issue", "pr", "run_id", "stage", "cost_so_far_usd",
    "blocker", "url", "trace_ref",
]
LIFECYCLE_RECORDS = ["acknowledged", "resolved", "expired", "auto_resolved", "muted"]

envelopes = collections.Counter()
records = 0
run_scoped = 0

for path in sorted(glob.glob(os.path.join(src, "dr_*.json"))):
    try:
        with open(path, encoding="utf-8") as fh:
            rec = json.load(fh)
    except (OSError, ValueError):
        continue  # a partial/torn record is not evidence
    records += 1
    ctx = rec.get("context") or {}
    if ctx.get("run_id"):
        run_scoped += 1
    lifecycle = rec.get("lifecycle") or {}
    envelope = (
        rec.get("schema_version"),
        rec.get("kind"),
        rec.get("severity"),
        bool(rec.get("standing")),
        bool(rec.get("fingerprint")),
        rec.get("default_action"),
        lifecycle.get("state"),
        tuple(k for k in LIFECYCLE_RECORDS if lifecycle.get(k)),
        tuple(k for k in CONTEXT_FIELDS if ctx.get(k) not in (None, "", 0)),
        tuple(sorted({(o or {}).get("verb") for o in (rec.get("options") or [])})),
        rec.get("producer"),
        bool(rec.get("steer")),
    )
    envelopes[envelope] += 1

journal_actions = collections.Counter()
journal_path = os.path.join(src, "journal.jsonl")
if os.path.exists(journal_path):
    with open(journal_path, encoding="utf-8") as fh:
        for line in fh:
            line = line.strip()
            if not line:
                continue
            try:
                entry = json.loads(line)
            except ValueError:
                continue
            action = entry.get("action")
            if action:
                journal_actions[action] += 1

rows = []
for env, count in envelopes.items():
    rows.append({
        "producer": env[10],
        "schema_version": env[0],
        "kind": env[1],
        "severity": env[2],
        "standing": env[3],
        "has_fingerprint": env[4],
        "default_action": env[5],
        "lifecycle_state": env[6],
        "lifecycle_records": list(env[7]),
        "context_fields": list(env[8]),
        "option_verbs": [v for v in env[9] if v],
        "has_steer": env[11],
        "count": count,
    })
rows.sort(key=lambda r: (r["producer"], r["kind"], r["severity"],
                         r["lifecycle_state"], json.dumps(r, sort_keys=True)))

doc = {
    "_comment": (
        "CAPTURED, NOT AUTHORED (#166). Redacted envelope grammar of real "
        "DecisionRequest records written by the shipping attention store. "
        "Every value here is a machine token from a closed registry in this "
        "repository; no title, body, repo slug, issue number, timestamp, or "
        "free-form text is present. Regenerate with "
        "scripts/capture-attention-fixture.sh. See README.md for provenance."
    ),
    "records_scanned": records,
    "run_scoped_records": run_scoped,
    "journal_actions": dict(sorted(journal_actions.items())),
    "envelopes": rows,
}

with open(out, "w", encoding="utf-8") as fh:
    json.dump(doc, fh, indent=2, sort_keys=False)
    fh.write("\n")

print(f"capture-attention-fixture: {records} record(s) -> "
      f"{len(rows)} distinct envelope(s), {run_scoped} run-scoped -> {out}")
PY
