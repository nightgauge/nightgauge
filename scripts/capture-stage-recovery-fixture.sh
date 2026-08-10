#!/usr/bin/env bash
# capture-stage-recovery-fixture.sh — capture the REAL Go runtime snapshot for a
# run whose stage failed and then SUCCEEDED on retry (#407).
#
# Issue #166's evidence rule: a test must not hand-author the shape it claims to
# pin. The TypeScript assertions this fixture feeds are "the snapshot Go emits
# for a recovered stage makes the appliers render it complete, and the notifier
# call it plainly Complete" — so the snapshot has to come from Go, not from a
# TypeScript author's idea of Go.
#
# ------------------------------------------------------------------------
# HOW IT IS CAPTURED — the real binary, the real wire, the real handler.
#
# This builds `cmd/nightgauge` and runs `nightgauge serve` as a subprocess over
# stdin/stdout, exactly as the VSCode extension's IpcClient does. It sends the
# `pipeline.notifyStageTransition` sequence the HeadlessOrchestrator sends for a
# run where feature-validate fails and the retry succeeds, and keeps the LAST
# `pipeline.stateChanged` event the server emitted — verbatim.
#
# Nothing in the output is written by this script. The keys, the nesting, the
# presence/absence of `stageErrors` entries, the two `completedStages` entries
# for the retried stage, the time formats: all of it is produced by the Go
# marshaller in the shipping binary. The only authored material is the INPUT —
# the wire messages, which are themselves the production
# PipelineNotifyStageTransitionParams shape.
#
# ONE REQUEST AT A TIME, response awaited before the next is sent. The IPC
# server dispatches each request on its own goroutine, so a fire-and-forget
# driver races its own transitions (a `running` for stage N+1 can land before
# the `complete` for stage N and silently corrupt the capture). IpcClient awaits
# every response; so does this.
#
# ------------------------------------------------------------------------
# NO REDACTION NEEDED — the content is synthetic.
#
# The repo slug, issue number, error text and token/cost numbers are invented
# here and typed into a throwaway workspace under $TMPDIR. No real workspace,
# history store, or GitHub account is read. `serve` is started with NO project
# config, so it attaches no scheduler and makes no network calls: the capture is
# hermetic and runs offline.
#
# Timestamps and durations ARE captured verbatim, so regenerating produces a
# different-but-equivalent file. Nothing asserts on them; the fixture exists for
# its SHAPE.
#
# Usage:
#   scripts/capture-stage-recovery-fixture.sh [OUT_FILE]
#
# Default OUT_FILE:
#   packages/nightgauge-vscode/tests/fixtures/stage-recovery/recovered-stage-snapshot.json
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="${1:-$REPO_ROOT/packages/nightgauge-vscode/tests/fixtures/stage-recovery/recovered-stage-snapshot.json}"

WORK="$(mktemp -d "${TMPDIR:-/tmp}/ng-stage-recovery-capture.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT

BIN="$WORK/nightgauge"
echo "capture-stage-recovery-fixture: building the real binary…" >&2
(cd "$REPO_ROOT" && go build -o "$BIN" ./cmd/nightgauge/)

WS="$WORK/workspace"
mkdir -p "$WS"

# Machine-tier config isolation: never read the developer's ~/.nightgauge.
export NIGHTGAUGE_CONFIG_HOME="$WORK/config-home"
mkdir -p "$NIGHTGAUGE_CONFIG_HOME"

mkdir -p "$(dirname "$OUT")"

BIN="$BIN" WS="$WS" OUT="$OUT" python3 - <<'PY'
import json
import os
import subprocess
import sys
import time

BIN = os.environ["BIN"]
WS = os.environ["WS"]
OUT = os.environ["OUT"]


def mint_run_id():
    """A UUIDv7 in the ADR-017 Decision 1 layout.

    Not taken on faith: the server runs runstate.IsIdentity on every
    run-bearing verb (internal/ipc/run_registry.go) and answers run_id_invalid
    to anything non-canonical. `call` below raises on any JSON-RPC error, so a
    malformed id fails the capture loudly instead of producing a fixture.
    """
    b = bytearray(os.urandom(16))
    b[0:6] = int(time.time() * 1000).to_bytes(6, "big")
    b[6] = (b[6] & 0x0F) | 0x70  # version 7
    b[8] = (b[8] & 0x3F) | 0x80  # variant 10
    h = b.hex()
    return "-".join([h[0:8], h[8:12], h[12:16], h[16:20], h[20:32]])


RUN_ID = mint_run_id()

proc = subprocess.Popen(
    [BIN, "serve", "--workspace", WS],
    stdin=subprocess.PIPE,
    stdout=subprocess.PIPE,
    stderr=subprocess.DEVNULL,
    text=True,
    bufsize=1,
)

events = []
next_id = [0]


def call(params):
    """Send one request and drain stdout until ITS response arrives.

    This is the IpcClient discipline — one in-flight request — and it is what
    keeps the transitions ordered. Events seen along the way are collected.
    """
    next_id[0] += 1
    req_id = next_id[0]
    proc.stdin.write(json.dumps({
        "id": req_id,
        "method": "pipeline.notifyStageTransition",
        "params": params,
    }) + "\n")
    proc.stdin.flush()
    while True:
        line = proc.stdout.readline()
        if not line:
            sys.exit("capture-stage-recovery-fixture: serve closed stdout mid-capture")
        line = line.strip()
        if not line:
            continue
        msg = json.loads(line)
        if msg.get("error"):
            sys.exit("capture-stage-recovery-fixture: the server REFUSED a call — %s"
                     % json.dumps(msg["error"]))
        if msg.get("event"):
            events.append(msg)
            continue
        if msg.get("id") == req_id:
            return


def transition(stage, status, **extra):
    params = {
        "repo": "acme/widgets",
        "issueNumber": 407,
        "stage": stage,
        "status": status,
        "runId": RUN_ID,
        "adapter": "claude",
    }
    params.update(extra)
    call(params)


# The HeadlessOrchestrator's transition sequence for a run where
# feature-validate fails and its retry succeeds. `running` before each attempt
# is what advances the stage clock, exactly as the extension emits it.
transition("issue-pickup", "running", model="claude-haiku-4-5")
transition("issue-pickup", "complete", model="claude-haiku-4-5",
           inputTokens=4200, outputTokens=900, cacheReadTokens=1800, costUsd=0.031)

transition("feature-dev", "running", model="claude-sonnet-4-6")
transition("feature-dev", "complete", model="claude-sonnet-4-6",
           inputTokens=31000, outputTokens=7400, cacheReadTokens=18000, costUsd=0.412)

# THE RECOVERY: attempt 1 fails, the escalated attempt 2 succeeds.
transition("feature-validate", "running", model="claude-sonnet-4-6")
transition("feature-validate", "failed", model="claude-sonnet-4-6",
           error="exit 1: 2 tests failed",
           inputTokens=12000, outputTokens=2600, cacheReadTokens=6000, costUsd=0.188)
transition("feature-validate", "running", model="claude-opus-4-8")
transition("feature-validate", "complete", model="claude-opus-4-8",
           inputTokens=15500, outputTokens=3100, cacheReadTokens=7200, costUsd=0.244)

transition("pr-create", "running", model="claude-haiku-4-5")
transition("pr-create", "complete", model="claude-haiku-4-5",
           inputTokens=2100, outputTokens=600, cacheReadTokens=800, costUsd=0.014)

transition("pr-merge", "running", model="claude-haiku-4-5")
transition("pr-merge", "complete", model="claude-haiku-4-5",
           inputTokens=1900, outputTokens=400, cacheReadTokens=700, costUsd=0.011)

proc.stdin.close()
proc.wait(timeout=30)

state_events = [e for e in events if e.get("event") == "pipeline.stateChanged"]
if not state_events:
    sys.exit("capture-stage-recovery-fixture: the server emitted no pipeline.stateChanged event")
last = state_events[-1]
state = (last.get("data") or {}).get("state") or {}

# Fail loudly rather than committing a fixture that does not show the thing it
# exists to show. These are properties of the CAPTURE, not assertions moved out
# of the test — the test still makes its own.
errors = state.get("stageErrors") or {}
if "feature-validate" in errors:
    sys.exit("capture-stage-recovery-fixture: the recovered stage is still in stageErrors "
             "(%r) — capture aborted; the binary under capture does not carry #407" % errors)
completed = [s.get("stage") for s in (state.get("completedStages") or [])]
if completed.count("feature-validate") != 2:
    sys.exit("capture-stage-recovery-fixture: expected both feature-validate attempts in "
             "completedStages, got %r" % completed)
if completed.count("") or len(completed) != 6:
    sys.exit("capture-stage-recovery-fixture: transitions raced — completedStages is %r" % completed)

# Written verbatim — re-indented only so the committed file is reviewable in a
# diff. No key is added, removed, renamed or reordered.
with open(OUT, "w", encoding="utf-8") as fh:
    json.dump(last, fh, indent=2)
    fh.write("\n")

print("capture-stage-recovery-fixture: wrote %s" % OUT)
PY
