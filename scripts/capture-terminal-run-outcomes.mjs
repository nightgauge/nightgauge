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
 * every terminal run (`history/index.json` in the clone's pipeline directory,
 * `<git-common-dir>/nightgauge/pipeline`, ADR-024 § 7). Redaction
 * drops every free-text and identity field (title, branch, labels, run_id) and
 * keeps only the numeric/structural fields the tests read, so nothing
 * repo-private can reach a public fixture.
 *
 * Usage (from the repo root, with a populated local history):
 *   node scripts/capture-terminal-run-outcomes.mjs [pathToIndexJson]
 */
import { execFileSync } from "node:child_process";
import { readFileSync, realpathSync, writeFileSync } from "node:fs";
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

// GIT_LOCATION_ENV redirect which repository git reads; cleared for the lookup.
const GIT_LOCATION_ENV = [
  "GIT_DIR",
  "GIT_WORK_TREE",
  "GIT_COMMON_DIR",
  "GIT_INDEX_FILE",
  "GIT_OBJECT_DIRECTORY",
];

// defaultSource is history/index.json in the cwd's clone pipeline directory
// (ADR-024 § 7). Outside a git repository it fails rather than guessing.
function defaultSource() {
  const env = { ...process.env };
  for (const k of GIT_LOCATION_ENV) delete env[k];
  let common;
  try {
    common = execFileSync("git", ["rev-parse", "--path-format=absolute", "--git-common-dir"], {
      env,
      encoding: "utf-8",
      stdio: ["ignore", "pipe", "pipe"],
    }).trim();
  } catch {
    throw new Error(`not a git repository: ${process.cwd()} (pass the index.json path)`);
  }
  return path.join(realpathSync(common), "nightgauge", "pipeline", "history", "index.json");
}

const source = process.argv[2] ?? defaultSource();
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
    "pipeline history/index.json written by the Go binary. Free-text and " +
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
