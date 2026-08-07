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
# internal/ipc/server_learning_outcome_test.go — the shapes under test are
# captured from real runs, never hand-authored (#166).
#
# Reproducible: re-running against the same roots reproduces byte-identical
# fixtures (selection is deterministic — first match in sorted root/file/line
# order).
#
# ------------------------------------------------------------------------
# REDACTION — deny by default. This is a PUBLIC repository and the live corpus
# these fixtures come from mixes PRIVATE repositories' telemetry.
#
# An allowlist of "fields we know are sensitive" is the wrong shape: it lets
# every unlisted string through (stage `error`, `punt_reason`, and anything the
# record schema grows later), which is how a real `PR #187` from a source
# workspace survived into a public fixture. So the rule is inverted — every
# STRING VALUE anywhere in the tree is dropped unless it is a bare machine
# token:
#
#     ^[A-Za-z0-9][A-Za-z0-9._:+-]*$      (no spaces, no slashes, no '#', no '@')
#
# That keeps exactly the shape the tests read — enums, statuses, stage names,
# model ids, adapters, UUIDs, ISO timestamps, schema versions — and drops
# everything prose-shaped: issue titles, bodies, stage errors, multi-word punt
# reasons, paths, repo slugs, branch names, e-mail addresses, forge references.
# Identity fields are then overwritten with stable placeholders so the fixture
# still reads like a real record.
#
# Issue-context capture goes further and is a strict PROJECTION: only the four
# fields loadIssueClassification reads are emitted at all.
#
# A final verification pass re-walks the emitted JSON and ABORTS if any string
# is neither a bare token nor one of this script's own placeholders. The
# publication-boundary guard does not inspect fixture string contents, so this
# check is the only mechanical gate there is — it must fail closed.
# ------------------------------------------------------------------------
#
# Usage:
#   scripts/capture-outcome-gap-fixture.sh [WORKSPACE_ROOT ...]
#
# Roots default to the git repository root. Pass several roots for a multi-repo
# workspace (run history and issue context files often live in different
# repos). Fixtures are written to internal/ipc/testdata/outcome-gap/ relative to
# the repository this script lives in.

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT_DIR="$REPO_ROOT/internal/ipc/testdata/outcome-gap"

if [ "$#" -eq 0 ]; then
  set -- "$REPO_ROOT"
fi

mkdir -p "$OUT_DIR"

OUT_DIR="$OUT_DIR" python3 - "$@" <<'PY'
import glob
import json
import os
import re
import sys
from datetime import datetime, timezone

out_dir = os.environ["OUT_DIR"]
roots = sorted(os.path.abspath(r) for r in sys.argv[1:])

# --- redaction -----------------------------------------------------------

# The ONLY string values that survive. Everything else is prose by definition.
SAFE_TOKEN = re.compile(r"^[A-Za-z0-9][A-Za-z0-9._:+-]*$")

REDACTED = "REDACTED"
PLACEHOLDER_REPO = "acme/widget"
PLACEHOLDER_TITLE = "REDACTED — real issue title removed for public fixture"
PLACEHOLDER_BASE_BRANCH = "main"

# Label namespaces with closed, product-defined vocabularies. Anything else
# (component:*, area:*, team:*) can name private subsystems and is dropped.
SAFE_LABEL_NAMESPACES = ("type:", "size:", "priority:", "status:")


def placeholder_branch(issue_number):
    return "feat/%d-redacted" % issue_number


def scrub(value):
    """Deny-by-default scrub of every string in an arbitrary JSON value."""
    if isinstance(value, str):
        return value if SAFE_TOKEN.match(value) else REDACTED
    if isinstance(value, list):
        return [scrub(v) for v in value]
    if isinstance(value, dict):
        return {k: scrub(v) for k, v in value.items()}
    return value


def safe_labels(labels):
    """Keep only closed-vocabulary label namespaces, as bare tokens."""
    if not isinstance(labels, list):
        return []
    out = []
    for label in labels:
        if not isinstance(label, str) or not SAFE_TOKEN.match(label):
            continue
        if label.lower().startswith(SAFE_LABEL_NAMESPACES):
            out.append(label)
    return out


def redact_run(record, issue_number):
    r = scrub(dict(record))
    # Identity fields read as REDACTED after the scrub; give them stable,
    # record-shaped placeholders so the fixture still looks like a run record.
    r["repo"] = PLACEHOLDER_REPO
    r["issue_number"] = issue_number
    r["title"] = PLACEHOLDER_TITLE
    r["branch"] = placeholder_branch(issue_number)
    r["base_branch"] = PLACEHOLDER_BASE_BRANCH
    r.pop("body", None)
    if "labels" in r:
        r["labels"] = safe_labels(record.get("labels"))
    return r


def redact_outcome(record, issue_number):
    o = scrub(dict(record))
    o["repo"] = PLACEHOLDER_REPO
    o["issueNumber"] = issue_number
    return o


def project_issue_context(ctx, issue_number):
    """Strict projection: ONLY the fields loadIssueClassification reads.

    A projection cannot leak a field it does not name, so unlike the run-record
    scrub this needs no verification of what it left behind — the issue context
    is the richest private artifact in the workspace (issue body, acceptance
    criteria, routing rationale) and none of it is emitted.
    """
    routing = ctx.get("routing") or {}
    pickup = routing.get("pickup_recommendation") or {}
    issue_type = ctx.get("type")
    return {
        "issue_number": issue_number,
        "type": issue_type if isinstance(issue_type, str) and SAFE_TOKEN.match(issue_type) else "",
        "labels": safe_labels(ctx.get("labels")),
        "routing": {
            "complexity_score": int(routing.get("complexity_score") or 0),
            "pickup_recommendation": {
                "dev_model": pickup.get("dev_model")
                if isinstance(pickup.get("dev_model"), str)
                and SAFE_TOKEN.match(pickup.get("dev_model"))
                else "",
            },
        },
    }


APPROVED_PLACEHOLDERS = {
    REDACTED,
    PLACEHOLDER_REPO,
    PLACEHOLDER_TITLE,
    PLACEHOLDER_BASE_BRANCH,
    "",
}


class Unredacted(Exception):
    pass


def verify(name, value, path="$"):
    """Fail closed: raise unless every string is a bare token or a placeholder."""
    if isinstance(value, str):
        if value in APPROVED_PLACEHOLDERS or SAFE_TOKEN.match(value):
            return
        if re.match(r"^(feat|fix|docs)/\d+-redacted$", value):
            return
        raise Unredacted("%s%s would publish an un-redacted string: %r" % (name, path, value))
    elif isinstance(value, list):
        for i, v in enumerate(value):
            verify(name, v, "%s[%d]" % (path, i))
    elif isinstance(value, dict):
        for k, v in value.items():
            verify(name, v, "%s.%s" % (path, k))


def self_test_verifier():
    """Prove the gate rejects before trusting it to accept.

    A verifier that silently stopped matching — a widened SAFE_TOKEN, a typo in
    a regex — would pass every fixture and publish everything. These are the
    exact shapes that escaped the previous allowlist redaction: a forge
    reference buried in a stage error, a real repo slug, a home path.
    """
    poison = {
        "stages": {"pr-merge": {"error": "pr-merge reported success but PR #187 is not merged"}},
        "repo": "RealOrg/private-service",
        "path": "/Users/someone/Repositories/private-service",
        "email": "someone@example.com",
    }
    for key, value in poison.items():
        try:
            verify("self-test", {key: value})
        except Unredacted:
            continue
        print(
            "error: redaction verifier FAILED ITS OWN SELF-TEST — it accepted %r. "
            "Refusing to emit fixtures." % {key: value},
            file=sys.stderr,
        )
        sys.exit(2)


# --- capture -------------------------------------------------------------


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


def read_jsonl(path):
    rows = []
    with open(path) as fh:
        for line in fh:
            line = line.strip()
            if not line:
                continue
            try:
                rows.append(json.loads(line))
            except json.JSONDecodeError:
                continue
    return rows


daily_files = []
outcome_files = []
context_files = []
for root in roots:
    history_dir = os.path.join(root, ".nightgauge", "pipeline", "history")
    daily_files.extend(
        sorted(
            f
            for f in glob.glob(os.path.join(history_dir, "*.jsonl"))
            if not f.endswith("outcomes.jsonl")
        )
    )
    outcomes_path = os.path.join(history_dir, "outcomes.jsonl")
    if os.path.exists(outcomes_path):
        outcome_files.append(outcomes_path)
    context_files.extend(
        sorted(
            glob.glob(os.path.join(root, ".nightgauge", "pipeline", "issue-*.json")),
            key=lambda p: (
                int(re.sub(r"\D", "", os.path.basename(p)) or 0),
                p,
            ),
        )
    )

if not daily_files:
    print(
        "error: no pipeline history under %s — nothing to capture\n"
        "       pass workspace roots that have run pipelines: "
        "scripts/capture-outcome-gap-fixture.sh <ROOT> [ROOT ...]"
        % ", ".join(roots),
        file=sys.stderr,
    )
    sys.exit(1)

runs = []
for path in daily_files:
    runs.extend(read_jsonl(path))

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
for path in outcome_files:
    outcomes.extend(read_jsonl(path))
if not outcomes:
    print(
        "error: no outcomes.jsonl rows under %s — no scheduler-path corpus sample to capture"
        % ", ".join(roots),
        file=sys.stderr,
    )
    sys.exit(1)

# The issue context fixture must exercise ALL THREE classification fields the
# outcome depends on: a real complexity score, a real predicted dev model, and
# a real size:* label. A context missing any of them cannot prove the
# extension-path outcome is non-degenerate.
issue_context = None
for path in context_files:
    try:
        with open(path) as fh:
            ctx = json.load(fh)
    except (OSError, json.JSONDecodeError):
        continue
    routing = ctx.get("routing") or {}
    pickup = routing.get("pickup_recommendation") or {}
    labels = safe_labels(ctx.get("labels"))
    if (
        int(routing.get("complexity_score") or 0) >= 1
        and pickup.get("dev_model")
        and any(l.lower().startswith("size:") for l in labels)
    ):
        issue_context = ctx
        break
if issue_context is None:
    print(
        "error: no issue-{N}.json under %s carries routing.complexity_score, "
        "routing.pickup_recommendation.dev_model AND a size:* label — the "
        "classification fixture would not prove anything" % ", ".join(roots),
        file=sys.stderr,
    )
    sys.exit(1)


def write_json(name, obj):
    try:
        verify(name, obj)
    except Unredacted as exc:
        print("error: %s" % exc, file=sys.stderr)
        sys.exit(2)
    with open(os.path.join(out_dir, name), "w") as fh:
        json.dump(obj, fh, indent=2, sort_keys=True)
        fh.write("\n")


self_test_verifier()
write_json("run-record.json", redact_run(success, 1001))
write_json("run-record-failed.json", redact_run(failed, 1002))
write_json("outcome.json", redact_outcome(outcomes[0], 1003))
write_json("issue-context.json", project_issue_context(issue_context, 1001))

model_less = sum(1 for o in outcomes if not o.get("predictedModel"))
size_small = sum(1 for o in outcomes if o.get("predictedSize") == "small")
score_zero = sum(1 for o in outcomes if not o.get("complexityScore"))
no_actual_size = sum(1 for o in outcomes if not o.get("actualSize"))
last_outcome = max(o.get("completedAt", "") for o in outcomes)
first_day = os.path.basename(daily_files[0])[:10]
last_day = os.path.basename(daily_files[-1])[:10]

readme = """# `outcome-gap` fixtures — provenance

Captured by `scripts/capture-outcome-gap-fixture.sh` from the real pipeline
telemetry of a live Nightgauge workspace. Nothing here is hand-authored: the
shapes under test in `internal/ipc/server_learning_outcome_test.go` are
redacted copies of records this machine's pipeline actually wrote.

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
- **{size_small}/{n_outcomes}** have `predictedSize: "small"`, because the pre-#304 writer
  ran an unknown complexity score straight through `SizeBucketForScore`, which
  maps 0 onto the same label as a genuinely small issue.
- **{score_zero}/{n_outcomes}** have `complexityScore: 0`.
- **{no_actual_size}/{n_outcomes}** have no `actualSize` at all — the field every
  calibration consumer compares `predictedSize` against had no production
  writer, so size accuracy had never once been measured.

`run-record.json` and `run-record-failed.json` are the counter-evidence: a real
extension-path run record already carries `stages[*].model_selection.model`,
per-stage cost, token totals and duration. `issue-context.json` carries the
routing prediction — complexity score, predicted dev model, size label — that
the same run's outcome was written without.

## Files

- `run-record.json` — real extension-path run record, `outcome: complete`.
- `run-record-failed.json` — real extension-path run record, `outcome: failed`.
- `outcome.json` — real scheduler-path learning outcome, the degenerate shape.
- `issue-context.json` — real `issue-{{N}}.json` routing classification,
  projected down to the four fields `loadIssueClassification` reads.

## Redaction — what is and is NOT preserved

This is a public repository, and the live corpus these fixtures come from mixes
private repositories' telemetry. Redaction is therefore **deny-by-default on
string values**, not an allowlist of known-sensitive field names:

- Every string anywhere in a captured record is dropped to `"REDACTED"` unless
  it is a bare machine token — `^[A-Za-z0-9][A-Za-z0-9._:+-]*$`, i.e. no
  spaces, slashes, `#` or `@`.
- **Free-text diagnostic fields are NOT preserved verbatim.** Stage `error`
  strings, multi-word `punt_reason` values, issue titles and bodies are prose
  and are replaced. An earlier allowlist-shaped version of this script kept
  them, and a real `PR #187` from the source workspace shipped in the failed
  run record as a result.
- Identity fields are then overwritten with stable placeholders: `acme/widget`,
  issue `1001`/`1002`/`1003`, `feat/<n>-redacted`, `main`.
- `labels` keep only closed-vocabulary namespaces (`type:`, `size:`,
  `priority:`, `status:`); `component:`/`area:` labels can name private
  subsystems and are dropped.
- `issue-context.json` is a strict **projection** — only `issue_number`,
  `type`, `labels` and `routing.{{complexity_score,pickup_recommendation.dev_model}}`
  are emitted; the issue body, acceptance criteria and routing rationale are
  never read into the output at all.

What survives is exactly the record **shape** the tests assert on: enums,
statuses, stage names, model ids, adapters, run ids, ISO timestamps, and every
numeric field (tokens, per-stage cost, durations, complexity score).

The script re-walks each emitted document and **aborts** if any string is
neither a bare token nor one of its own placeholders. The publication-boundary
guard scans the tracked tree but does not inspect fixture string contents, so
this check is the only mechanical gate — it fails closed by design, and it
self-tests against a poison document (a forge reference inside a stage error, a
real repo slug, a home path, an e-mail) before it is trusted to accept anything.

## Regenerating

```bash
scripts/capture-outcome-gap-fixture.sh [WORKSPACE_ROOT ...]
```

Pass several roots for a multi-repo workspace — run history and issue context
files commonly live in different repos. Selection is deterministic (first match
in sorted root/file/line order) and the output is Prettier-normalized before it
lands, so re-running against the same roots reproduces byte-identical fixtures
that pass `npm run format:check`. Against a different workspace the numbers
above change — update this README from the script's output rather than editing
it by hand.
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
    no_actual_size=no_actual_size,
)

with open(os.path.join(out_dir, "README.md"), "w") as fh:
    fh.write(readme)

print(
    "captured %d extension-path run records (of %d total), %d outcome records "
    "and 1 issue context from %d root(s)"
    % (len(ext_runs), len(all_runs), len(outcomes), len(roots))
)
print(
    "wrote run-record.json, run-record-failed.json, outcome.json, "
    "issue-context.json, README.md to %s" % out_dir
)
PY

# Normalize with the repo's Prettier so a regeneration does not fail
# `npm run format:check`. Prettier is deterministic, so this preserves the
# byte-identical-reproduction property; skipping it silently would not, which is
# why an absent Prettier is a hard failure rather than a warning.
if ! npx --no-install prettier --write "$OUT_DIR" >/dev/null; then
  echo "error: prettier is unavailable — fixtures were written but are NOT format-normalized." >&2
  echo "       run 'npm install' and re-run this script, or 'npm run format' before committing." >&2
  exit 3
fi
echo "normalized fixture formatting with prettier"
