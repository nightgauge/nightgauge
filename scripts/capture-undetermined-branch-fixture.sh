#!/usr/bin/env bash
# capture-undetermined-branch-fixture.sh — capture the REAL history record the
# Go binary writes for a run whose feature branch could not be determined (#397).
#
# Issue #166's evidence rule: a test must not hand-author the shape it claims to
# pin. The TypeScript assertion this fixture feeds is "a record whose branch is
# undetermined survives the strict schema and is IMPORTED rather than dropped" —
# so the record has to come from Go, not from a TypeScript author's idea of Go.
# (The Runs tab's "(branch not determined)" label is a SEPARATE contract over a
# platform-served row, not over this record; see the test file.) Hand-writing
# `{"branch": ""}` would be asserting a belief about the writer; before #397 that
# belief was wrong in the most consequential possible way, because the writer
# emitted `"branch":"feat/{N}"` and every reader believed it.
#
# The distinction the fixture exists to carry — key PRESENT, value EMPTY — does
# not survive a round trip through a JSON decoder, so the file is the verbatim
# JSONL line, byte for byte as the writer emitted it.
#
# ------------------------------------------------------------------------
# HOW IT IS CAPTURED — the real binary, BOTH real production paths.
#
# #397 removed a `feat/{N}` fabrication from two writers, so this captures one
# record from each. Both come from the shipping binary; nothing in either output
# is written by this script.
#
#  1. completed-run-record.jsonl — `state.HistoryWriter.BuildV2Record`, the
#     primary site, reached over the real IPC wire. `nightgauge serve` is driven
#     over stdin/stdout exactly as the VSCode extension's IpcClient drives it:
#     the `pipeline.notifyStageTransition` sequence for a two-stage run, then
#     `pipeline.notifyComplete`, whose handler builds the authoritative record.
#     The run's branch is never named — the `initialized` transition carries an
#     EMPTY `branch`, which is production-shaped (`SeedRunContext` seeds the
#     field only when non-empty, so a caller with no branch leaves it unset) —
#     and pre-#397 BuildV2Record substituted `feat/{N}` at exactly that point.
#     ONE REQUEST AT A TIME, response awaited before the next is sent: the IPC
#     server dispatches each request on its own goroutine, so a fire-and-forget
#     driver races its own transitions. IpcClient awaits every response; so does
#     this.
#
#  2. crash-record.jsonl — `orchestrator.SynthesizeOrchestratorCrashRecord`, the
#     second site. The workspace holds one artifact: a
#     `.nightgauge/pipeline/current-run.json` sidecar naming a pid that is no
#     longer alive, which is exactly the on-disk state a killed orchestrator
#     leaves behind. Any command that constructs a Scheduler then runs the real
#     startup recovery (`loadQueue` → `recoverOrchestratorCrash` →
#     `SynthesizeOrchestratorCrashRecord` → `WriteRecord`); `queue list` is the
#     cheapest such command, and the record is written during construction,
#     before the queue is even printed.
#
# The only authored material is the INPUT: the wire messages and the sidecar
# (the latter field for field what `writeCurrentRunSidecar` stamps). The wire
# messages carry the fields each `PipelineStateService` emitter sends that reach
# the record, plus the per-stage `adapter` attribution `completeStage` carries —
# they are not a byte-for-byte replay of those emitters. This driver attaches
# `adapter` to every transition rather than only the terminal ones, and omits
# fields no assertion depends on (`completeStage`'s `stagePid`;
# `notifyComplete`'s `prMerged`, `deferred`, `stageExecutionPaths`,
# `stagePuntReasons`). Keys, ordering, the empty `branch`, the per-stage token
# blocks, the V3 `terminal_failure_kind`, the time formats: all produced by the
# Go marshaller.
#
# Both inputs deliberately carry a POSITIVE issue number. Pre-#397 both writers
# fabricated `feat/{IssueNumber}` for exactly that case, so a capture with
# issue_number <= 0 would sail past the defect these fixtures pin.
#
# ------------------------------------------------------------------------
# NO REDACTION NEEDED — the content is synthetic.
#
# The repo slug, issue number, title and run id are invented here and typed into
# a throwaway workspace under $TMPDIR. Three redirects keep the run out of the
# operator's environment, and each covers a different path:
#
#   - NIGHTGAUGE_CONFIG_HOME -> $WORK — the machine-tier config the binary reads.
#   - HOME -> $WORK/home — everything else $HOME-derived, which that variable
#     does NOT cover. `nightgauge serve` starts a serve sidecar whose claim
#     directory is `os.UserHomeDir()/.nightgauge/serve`; without this the capture
#     writes a claim file into the developer's real ~/.nightgauge (and a SIGKILL
#     mid-capture leaks one naming a dead pid, which `nightgauge doctor` reads).
#   - GITHUB_TOKEN / GH_TOKEN -> a placeholder, set UNCONDITIONALLY. Both paths
#     construct a forge client and a token must be resolvable for construction to
#     proceed; the Go chain reads config, then GITHUB_TOKEN, then shells out to
#     `gh auth token`. Inheriting either variable would resolve the operator's
#     real credential, and leaving them unset would shell out to the machine's
#     real `gh`. Neither path makes a request — that is verified, not assumed —
#     but the identity would be real.
#
# The workspaces have no project config, so no board or history store is read,
# and no network call is made.
#
# Timestamps and durations ARE captured verbatim (the binary stamps them from
# the wall clock), so regenerating produces a different-but-equivalent file.
# Nothing asserts on them; the fixture exists for its SHAPE.
#
# Usage:
#   scripts/capture-undetermined-branch-fixture.sh [OUT_DIR]
#
# Default OUT_DIR:
#   packages/nightgauge-vscode/tests/fixtures/undetermined-branch/
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT_DIR="${1:-$REPO_ROOT/packages/nightgauge-vscode/tests/fixtures/undetermined-branch}"

WORK="$(mktemp -d "${TMPDIR:-/tmp}/ng-undetermined-branch-capture.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT

BIN="$WORK/nightgauge"
echo "capture-undetermined-branch-fixture: building the real binary…" >&2
(cd "$REPO_ROOT" && go build -o "$BIN" ./cmd/nightgauge/)

# Everything below runs against the temp tree only — see the isolation block in
# the header for what each redirect covers and why one of them is not enough.
# (The build above deliberately precedes this: it wants the real Go build cache.)
export NIGHTGAUGE_CONFIG_HOME="$WORK/config-home"
mkdir -p "$NIGHTGAUGE_CONFIG_HOME"
export HOME="$WORK/home"
mkdir -p "$HOME"
export GITHUB_TOKEN="capture-placeholder-not-used"
export GH_TOKEN="capture-placeholder-not-used"

# All three outputs are staged here and copied to OUT_DIR only after EVERY guard
# on BOTH captures has passed — a half-regenerated fixture set (new completed
# run, stale crash record) is the one state this directory promises against.
STAGE_DIR="$WORK/staged"
mkdir -p "$STAGE_DIR"

# ---------------------------------------------------------------------------
# CAPTURE 1 — BuildV2Record, over the real IPC wire.
# ---------------------------------------------------------------------------

IPC_WS="$WORK/ipc-workspace"
mkdir -p "$IPC_WS"

echo "capture-undetermined-branch-fixture: driving the real IPC notifyComplete path…" >&2
SERVE_ERR="$WORK/serve.err"
if ! BIN="$BIN" WS="$IPC_WS" STAGE_DIR="$STAGE_DIR" SERVE_ERR="$SERVE_ERR" python3 - <<'PY'
import json
import os
import subprocess
import sys
import time

BIN = os.environ["BIN"]
WS = os.environ["WS"]
STAGE_DIR = os.environ["STAGE_DIR"]
SERVE_ERR = os.environ["SERVE_ERR"]


def mint_run_id():
    """A UUIDv7 in the ADR-017 Decision 1 layout.

    Not taken on faith: the server runs runstate.IsIdentity on every run-bearing
    verb and answers run_id_invalid to anything non-canonical. `call` below
    raises on any JSON-RPC error, so a malformed id fails the capture loudly
    instead of producing a fixture.
    """
    b = bytearray(os.urandom(16))
    b[0:6] = int(time.time() * 1000).to_bytes(6, "big")
    b[6] = (b[6] & 0x0F) | 0x70  # version 7
    b[8] = (b[8] & 0x3F) | 0x80  # variant 10
    h = b.hex()
    return "-".join([h[0:8], h[8:12], h[12:16], h[16:20], h[20:32]])


RUN_ID = mint_run_id()

# serve's stderr goes to a file, never to DEVNULL: when the server refuses to
# start, its message ("no GitHub token available…") is the ONLY useful
# diagnostic, and the driver would otherwise report just "closed stdout
# mid-capture". The caller tails this file on every failure path.
serve_err = open(SERVE_ERR, "w", encoding="utf-8")
proc = subprocess.Popen(
    [BIN, "serve", "--workspace", WS],
    stdin=subprocess.PIPE,
    stdout=subprocess.PIPE,
    stderr=serve_err,
    text=True,
    bufsize=1,
)

next_id = [0]


def call(method, params):
    """Send one request and drain stdout until ITS response arrives."""
    next_id[0] += 1
    req_id = next_id[0]
    proc.stdin.write(json.dumps({"id": req_id, "method": method, "params": params}) + "\n")
    proc.stdin.flush()
    while True:
        line = proc.stdout.readline()
        if not line:
            sys.exit("capture-undetermined-branch-fixture: serve closed stdout mid-capture")
        line = line.strip()
        if not line:
            continue
        msg = json.loads(line)
        if msg.get("error"):
            sys.exit("capture-undetermined-branch-fixture: the server REFUSED a call — %s"
                     % json.dumps(msg["error"]))
        if msg.get("event"):
            continue
        if msg.get("id") == req_id:
            return msg.get("result")


def transition(stage, status, **extra):
    params = {
        "repo": "acme/widgets",
        "issueNumber": 812,
        "stage": stage,
        "status": status,
        "runId": RUN_ID,
        "adapter": "claude",
    }
    params.update(extra)
    call("pipeline.notifyStageTransition", params)


# The `initialized` transition PipelineStateService.initializePipeline sends,
# field for field — including the EMPTY branch. That is what a caller with no
# branch to name produces, and SeedRunContext seeds the runtime's branch only
# when the field is non-empty, so the run reaches BuildV2Record with none.
transition("init", "initialized",
           title="Undetermined branch for the whole run",
           branch="", baseBranch="")

transition("issue-pickup", "running", model="claude-haiku-4-5")
transition("issue-pickup", "complete", model="claude-haiku-4-5",
           inputTokens=4200, outputTokens=900, cacheReadTokens=1800, costUsd=0.031)

transition("feature-dev", "running", model="claude-sonnet-4-6")
transition("feature-dev", "complete", model="claude-sonnet-4-6",
           inputTokens=31000, outputTokens=7400, cacheReadTokens=18000, costUsd=0.412)

# The terminal funnel: its handler builds the authoritative record via
# state.HistoryWriter.BuildV2Record and appends it to the daily JSONL.
call("pipeline.notifyComplete", {
    "repo": "acme/widgets",
    "issueNumber": 812,
    "success": True,
    "totalDurationMs": 600000,
    "stagesRun": ["issue-pickup", "feature-dev"],
    "runId": RUN_ID,
})

proc.stdin.close()
proc.wait(timeout=30)

hist_dir = os.path.join(WS, ".nightgauge", "pipeline", "history")
day_files = sorted(f for f in os.listdir(hist_dir)
                   if f.endswith(".jsonl") and f != "outcomes.jsonl")
if len(day_files) != 1:
    sys.exit("capture-undetermined-branch-fixture: expected one daily JSONL, got %r" % day_files)
lines = [ln for ln in open(os.path.join(hist_dir, day_files[0]), encoding="utf-8")
         .read().splitlines() if ln.strip()]
if len(lines) != 1:
    sys.exit("capture-undetermined-branch-fixture: expected exactly one run record, got %d"
             % len(lines))
line = lines[0]

# Fail loudly rather than committing a fixture that does not show the thing it
# exists to show. These are properties of the CAPTURE, not assertions moved out
# of the tests — the tests still make their own.
if '"branch":""' not in line:
    sys.exit("capture-undetermined-branch-fixture: BuildV2Record did not emit an EMPTY branch — "
             "the binary under capture does not carry #397.\nline was:\n%s" % line)
rec = json.loads(line)
if "branch" not in rec or rec["branch"] != "":
    sys.exit("capture-undetermined-branch-fixture: branch is %r, want a present, empty key"
             % rec.get("branch"))
if rec.get("issue_number", 0) <= 0:
    sys.exit("capture-undetermined-branch-fixture: issue_number must be positive — a non-positive "
             "one never reached the pre-#397 fabrication and would pin nothing")
if not rec.get("title"):
    sys.exit("capture-undetermined-branch-fixture: the record carries no title; the `initialized` "
             "transition did not seed the run context")
completed = [s for s, d in (rec.get("stages") or {}).items() if d.get("status") == "complete"]
if sorted(completed) != ["feature-dev", "issue-pickup"]:
    sys.exit("capture-undetermined-branch-fixture: transitions raced — completed stages are %r"
             % sorted(completed))
if (rec.get("tokens") or {}).get("estimated_cost_usd", 0) <= 0:
    sys.exit("capture-undetermined-branch-fixture: the run booked no cost, so it is "
             "indistinguishable from the phantom records readers skip")

with open(os.path.join(STAGE_DIR, "completed-run-record.jsonl"), "w", encoding="utf-8") as fh:
    fh.write(line + "\n")

# The index entry the same write produced. It is a SECOND surface carrying the
# branch (state.V2IndexEntry → HistoryIndexEntry → indexEntryToRunSummary), so
# the tests get real material for it rather than a derived guess.
index = json.load(open(os.path.join(hist_dir, "index.json"), encoding="utf-8"))
entries = index.get("entries") or []
if len(entries) != 1 or entries[0].get("branch") != "":
    sys.exit("capture-undetermined-branch-fixture: index entry is not a single empty-branch row: %r"
             % entries)
# Go already writes index.json 2-space indented, so this round trip reproduces
# its bytes and adds only the trailing newline Go omits.
with open(os.path.join(STAGE_DIR, "completed-run-index.json"), "w", encoding="utf-8") as fh:
    json.dump(index, fh, indent=2)
    fh.write("\n")

print("capture-undetermined-branch-fixture: staged completed-run-record.jsonl, "
      "completed-run-index.json")
PY
then
  echo "capture-undetermined-branch-fixture: the IPC capture failed" >&2
  if [ -s "$SERVE_ERR" ]; then
    echo "--- serve stderr (tail) ---" >&2
    tail -20 "$SERVE_ERR" >&2
  fi
  exit 1
fi

# ---------------------------------------------------------------------------
# CAPTURE 2 — SynthesizeOrchestratorCrashRecord, via startup crash recovery.
# ---------------------------------------------------------------------------

WS="$WORK/workspace"
mkdir -p "$WS/.nightgauge/pipeline"

# A pid that is definitively NOT alive: spawn a trivial process, reap it, reuse
# its id. The liveness guard in recoverOrchestratorCrashAt treats a live pid as
# a RUNNING run and writes no record at all — the verification below fails the
# capture loudly if that happens (pid reuse, an unexpectedly slow reap).
/usr/bin/true &
DEAD_PID=$!
wait "$DEAD_PID" 2>/dev/null || true

# Sidecar timestamps are relative to now, so the captured record's durations
# read like a run that crashed 42 minutes into feature-dev rather than a run
# stalled since some hard-coded date. Nothing asserts on them.
RUN_STARTED_AT="$(python3 -c 'import datetime;print((datetime.datetime.now(datetime.timezone.utc)-datetime.timedelta(minutes=42)).strftime("%Y-%m-%dT%H:%M:%SZ"))')"
STAGE_STARTED_AT="$(python3 -c 'import datetime;print((datetime.datetime.now(datetime.timezone.utc)-datetime.timedelta(minutes=7)).strftime("%Y-%m-%dT%H:%M:%SZ"))')"

cat > "$WS/.nightgauge/pipeline/current-run.json" <<JSON
{
  "issue_number": 397,
  "repo": "acme/widgets",
  "run_id": "01998f1a-2b3c-7d4e-8f01-2233445566aa",
  "item_id": "item-397",
  "title": "Crash mid-stage with no branch on record",
  "started_at": "$RUN_STARTED_AT",
  "stage": "feature-dev",
  "stage_started_at": "$STAGE_STARTED_AT",
  "pid": $DEAD_PID
}
JSON

echo "capture-undetermined-branch-fixture: driving the real crash-recovery path…" >&2
(cd "$WS" && "$BIN" queue list >"$WORK/queue.out" 2>"$WORK/queue.err") || {
  echo "capture-undetermined-branch-fixture: the binary exited non-zero" >&2
  cat "$WORK/queue.err" >&2
  exit 1
}

if ! grep -q "synthesized terminal-failure RunRecord for #397" "$WORK/queue.err"; then
  echo "capture-undetermined-branch-fixture: recovery never fired — no crash record was synthesized." >&2
  echo "(a LIVE pid in the sidecar, or a changed recovery path, both land here)" >&2
  cat "$WORK/queue.err" >&2
  exit 1
fi

HIST_DIR="$WS/.nightgauge/pipeline/history"
JSONL="$(find "$HIST_DIR" -name '*.jsonl' -type f | head -1)"
if [ -z "$JSONL" ]; then
  echo "capture-undetermined-branch-fixture: no daily JSONL was written" >&2
  exit 1
fi

JSONL="$JSONL" STAGE_DIR="$STAGE_DIR" python3 - <<'PY'
import json
import os
import sys

JSONL = os.environ["JSONL"]
STAGE_DIR = os.environ["STAGE_DIR"]

lines = [ln for ln in open(JSONL, encoding="utf-8").read().splitlines() if ln.strip()]
if len(lines) != 1:
    sys.exit("capture-undetermined-branch-fixture: expected exactly one record, got %d" % len(lines))
line = lines[0]

# Fail loudly rather than committing a fixture that does not show the thing it
# exists to show. These are properties of the CAPTURE, not assertions moved out
# of the tests — the tests still make their own.
if '"branch":""' not in line:
    sys.exit("capture-undetermined-branch-fixture: the record does not carry an EMPTY branch — "
             "the binary under capture does not carry #397.\nline was:\n%s" % line)
rec = json.loads(line)
# One guard, both failure modes — an omitted key makes `rec.get("branch")` None,
# so a separate `"branch" not in rec` check placed AFTER a `!= ""` test could
# never print its own diagnostic. Same form as capture 1.
if "branch" not in rec or rec["branch"] != "":
    sys.exit("capture-undetermined-branch-fixture: branch is %r, want a present, empty key — an "
             "OMITTED key is a different record contract, not \"undetermined\"" % rec.get("branch"))
if rec.get("issue_number", 0) <= 0:
    sys.exit("capture-undetermined-branch-fixture: issue_number must be positive — a non-positive "
             "one never reached the pre-#397 fabrication and would pin nothing")
if rec.get("schema_version") != "3" or rec.get("terminal_failure_kind") != "orchestrator_crash":
    sys.exit("capture-undetermined-branch-fixture: not the V3 orchestrator-crash record "
             "(schema_version=%r, terminal_failure_kind=%r)"
             % (rec.get("schema_version"), rec.get("terminal_failure_kind")))

# Verbatim — the trailing newline is the JSONL record separator the writer emits.
with open(os.path.join(STAGE_DIR, "crash-record.jsonl"), "w", encoding="utf-8") as fh:
    fh.write(line + "\n")

print("capture-undetermined-branch-fixture: staged crash-record.jsonl")
PY

# ---------------------------------------------------------------------------
# PUBLISH — only now, with every guard on BOTH captures passed.
# ---------------------------------------------------------------------------

for f in completed-run-record.jsonl completed-run-index.json crash-record.jsonl; do
  if [ ! -s "$STAGE_DIR/$f" ]; then
    echo "capture-undetermined-branch-fixture: $f was never staged — refusing to publish a partial set" >&2
    exit 1
  fi
done

mkdir -p "$OUT_DIR"
cp "$STAGE_DIR/completed-run-record.jsonl" \
   "$STAGE_DIR/completed-run-index.json" \
   "$STAGE_DIR/crash-record.jsonl" \
   "$OUT_DIR/"

echo "capture-undetermined-branch-fixture: wrote completed-run-record.jsonl, completed-run-index.json and crash-record.jsonl to $OUT_DIR" >&2
