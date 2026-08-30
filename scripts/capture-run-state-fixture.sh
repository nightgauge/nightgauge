#!/usr/bin/env bash
# capture-run-state-fixture.sh — capture a REAL `run-state.json` written by
# `RunStateManager` as the fixture for the #468 read-back refusal tests.
#
# Issue #468: `run-state.json` read-back validated `run_id` with
# `z.string().uuid()`, which accepts the exact set Go's run-identity authority
# (`internal/runstate/identity.go`) refuses — any UUID version, uppercase hex.
# The refusal tests must run against a file the product actually wrote, not a
# hand-authored one (#166): a hand-shaped fixture proves the schema refuses a
# string, not that the READ-BACK PATH refuses a file, and it silently stops
# tracking the writer the day the writer changes.
#
# What it captures: `markRunning` → `markStageComplete` → `markPaused`, i.e.
# the exact shape `RunStateManager.resume()` reads. Nothing is hand-authored;
# every field is whatever the manager wrote.
#
# ---------------------------------------------------------------------------
# REDACTION — this is a PUBLIC repository, and a real capture carries machine
# identity. Three classes are overwritten with stable placeholders AFTER the
# manager writes them, and the substitution is recorded in the fixture's own
# `_capture` header so a reader never has to guess which bytes are real:
#
#   - `host_id`   — a machine UUID on the capturing host.
#   - `pid`       — the capturing process.
#   - timestamps  — `created_at` / `updated_at` / `started_at`, which otherwise
#                   make the fixture churn on every re-capture.
#
# `run_id` is NOT redacted: it is the field under test, and it must be a real
# `uuidV7()` mint so the accepting arm proves the writer and the read-back
# validator agree. The refusal arms edit ONLY this field, in memory, in the
# test — the on-disk fixture stays exactly as the manager wrote it.
#
# The issue number and branch are fictional by construction (`acme/*`
# convention), so nothing identifying is captured in the first place.
#
# Re-running reproduces a byte-identical fixture except for `run_id`.
# ---------------------------------------------------------------------------
#
# Usage (from the repository root):
#
#     bash scripts/capture-run-state-fixture.sh
#
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
sdk="$repo_root/packages/nightgauge-sdk"
out="$sdk/src/__tests__/integration/fixtures/captured-run-state-paused.json"

# The manager uses TypeScript parameter properties, which Node's type-stripping
# cannot erase — so the capture runs against the compiled SDK, i.e. the same
# artifact consumers import.
echo "==> building @nightgauge/sdk"
npm --prefix "$repo_root" run build -w @nightgauge/sdk >/dev/null

echo "==> capturing a real RunStateManager write"
node --input-type=module -e '
import { mkdtemp, readFile, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import path from "node:path";
import { RunStateManager } from "'"$sdk"'/dist/context/RunStateManager.js";

const dir = await mkdtemp(path.join(tmpdir(), "capture-run-state-"));
const mgr = new RunStateManager(dir);

// The real lifecycle a resume() reads back: a run that started, finished one
// stage, and was paused by the stop button.
await mgr.markRunning({ issue_number: 4242, branch: "feat/acme-platform-widget" });
await mgr.markStageComplete("issue-pickup");
await mgr.markPaused("captured by scripts/capture-run-state-fixture.sh");

const captured = JSON.parse(await readFile(path.join(dir, "run-state.json"), "utf-8"));

// ── redaction (see the script header) ──────────────────────────────────
const STAMP = "2026-01-01T00:00:00.000Z";
captured.created_at = STAMP;
captured.updated_at = STAMP;
for (const attempt of captured.attempts) {
  attempt.started_at = STAMP;
  attempt.pid = 1;
  attempt.host_id = "00000000-0000-0000-0000-000000000000";
}

// `_capture` rides along because RunStateSchema is `.passthrough()`. JSON has
// no comment syntax, so this IS the fixture header the #166 rule asks for.
const withHeader = {
  _capture: {
    script: "scripts/capture-run-state-fixture.sh",
    command: "bash scripts/capture-run-state-fixture.sh",
    produced_by:
      "RunStateManager.markRunning -> markStageComplete(issue-pickup) -> markPaused, " +
      "read back verbatim from the file the manager wrote",
    issue: "468 — run-state.json read-back validated run_id with z.string().uuid()",
    redacted:
      "host_id, pid and all timestamps replaced with stable placeholders after the " +
      "write; every other field is exactly what RunStateManager produced",
    run_id_note:
      "run_id is a REAL uuidV7() mint and is NOT edited on disk. The refusal arms " +
      "of src/__tests__/RunStateManager.test.ts edit ONLY this field, in memory, " +
      "to each non-canonical form in the Go refusal table.",
  },
  ...captured,
};

await writeFile("'"$out"'", JSON.stringify(withHeader, null, 2) + "\n");
'

# The repo's Prettier gate covers checked-in JSON, and Prettier collapses short
# arrays where JSON.stringify expands them. Formatting HERE — rather than
# letting a human run `prettier --write` afterwards — is what keeps re-running
# this script byte-reproducible against the checked-in fixture.
echo "==> formatting to the repo's Prettier style"
npx --prefix "$repo_root" prettier --write "$out" >/dev/null

echo "==> wrote $out"
