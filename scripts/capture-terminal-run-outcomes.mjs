#!/usr/bin/env node
/**
 * Capture + redact real pipeline run outcomes for the #307 force-clear tests.
 *
 * The abort-deadline tests need the shape a real `runPipeline` settlement
 * carries — duration, cost, token totals, stage count — for both a run that
 * COMPLETED and a run that was CANCELLED. Hand-authoring those numbers is
 * exactly what #166 forbids: the test then asserts against an invented shape
 * and stays green when the real one drifts.
 *
 * Source: the local pipeline history index, which the Go binary writes on
 * every terminal run (`.nightgauge/pipeline/history/index.json`). Redaction
 * drops every free-text and identity field (title, branch, labels, run_id) and
 * keeps only the numeric/structural fields the tests read, so nothing
 * repo-private can reach a public fixture.
 *
 * Usage (from the repo root, with a populated local history):
 *   node scripts/capture-terminal-run-outcomes.mjs [pathToIndexJson]
 */
import { readFileSync, writeFileSync } from "node:fs";
import path from "node:path";

const KEEP = [
  "outcome",
  "cost_usd",
  "total_input_tokens",
  "total_output_tokens",
  "total_cache_read_tokens",
  "total_cache_creation_tokens",
  "duration_ms",
  "stage_count",
];

const source = process.argv[2] ?? ".nightgauge/pipeline/history/index.json";
const index = JSON.parse(readFileSync(source, "utf-8"));
const entries = Array.isArray(index.entries) ? index.entries : [];

function latest(outcome) {
  const matches = entries.filter((e) => e.outcome === outcome);
  if (matches.length === 0) throw new Error(`no run with outcome=${outcome} in ${source}`);
  const raw = matches[matches.length - 1];
  const redacted = {};
  for (const key of KEEP) if (raw[key] !== undefined) redacted[key] = raw[key];
  return redacted;
}

const out = {
  _provenance:
    "Captured + redacted by scripts/capture-terminal-run-outcomes.mjs from a local " +
    ".nightgauge/pipeline/history/index.json written by the Go binary. Free-text and " +
    "identity fields (title, branch, labels, run_id, issue_number, timestamps) are dropped.",
  totalRunsInSource: index.total_runs ?? entries.length,
  complete: latest("complete"),
  cancelled: latest("cancelled"),
};

const dest =
  process.argv[3] ??
  path.join("packages/nightgauge-vscode/tests/fixtures/terminal/run-outcomes.json");
writeFileSync(dest, `${JSON.stringify(out, null, 2)}\n`);
console.log(`wrote ${dest} from ${entries.length} records in ${source}`);
