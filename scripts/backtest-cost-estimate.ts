/**
 * Back-test the pre-flight whole-run cost estimate against completed runs (#3364).
 *
 * Replays every completed run since `--since` against its PRIOR completed runs
 * only, using the same cohort rule as the pre-flight gate, and reports the
 * share of estimates within 2x of the actual and the median estimate/actual.
 * Exits 1 when the share is below 60% or the median is outside 0.8–1.5.
 *
 * Reads the clone's `<git-common-dir>/nightgauge/pipeline/history/*.jsonl`
 * (ADR-024 § 7; override with --history).
 *
 * Usage:
 *   npx tsx scripts/backtest-cost-estimate.ts [--since 2026-11-01] [--history <dir>] [--json]
 */

import * as fs from "fs";
import * as path from "path";
import { cloneClassDir } from "../packages/nightgauge-sdk/src/context/cloneLayout.js";
import {
  backtestWholeRunCalibration,
  type CompletedRunCost,
} from "../packages/nightgauge/src/utils/wholeRunCalibration.js";

const MIN_WITHIN_TWO_X = 0.6;
const MEDIAN_RATIO_MIN = 0.8;
const MEDIAN_RATIO_MAX = 1.5;

function usage(msg: string): never {
  console.error(msg);
  console.error("usage: backtest-cost-estimate.ts [--since <ISO date>] [--history <dir>] [--json]");
  process.exit(2);
}

function parseArgs(argv: string[]): { since?: string; historyDir: string; json: boolean } {
  let since: string | undefined;
  let historyDir: string | undefined;
  let json = false;
  for (let i = 2; i < argv.length; i++) {
    const a = argv[i];
    if (a === "--since" && argv[i + 1]) since = argv[++i];
    else if (a === "--history" && argv[i + 1]) historyDir = argv[++i];
    else if (a === "--json") json = true;
    else usage(`unknown or incomplete argument: ${a}`);
  }
  if (since !== undefined && Number.isNaN(Date.parse(since))) {
    usage(`--since must be an ISO date, got: ${since}`);
  }
  return { since, json, historyDir: historyDir ?? path.join(cloneClassDir("pipeline"), "history") };
}

function loadCompletedRuns(historyDir: string): CompletedRunCost[] {
  const runs: CompletedRunCost[] = [];
  if (!fs.existsSync(historyDir)) usage(`history directory not found: ${historyDir}`);
  let skipped = 0;
  for (const file of fs.readdirSync(historyDir).filter((f) => f.endsWith(".jsonl"))) {
    for (const line of fs.readFileSync(path.join(historyDir, file), "utf8").split("\n")) {
      if (!line.trim()) continue;
      let r: Record<string, any>;
      try {
        r = JSON.parse(line);
      } catch {
        skipped++;
        continue;
      }
      if (r.record_type !== "run" || r.outcome !== "complete") continue;
      const cost = r.tokens?.estimated_cost_usd;
      if (typeof cost !== "number" || !Number.isFinite(cost) || cost <= 0) continue;
      if (typeof r.recorded_at !== "string" || r.recorded_at === "") {
        skipped++;
        continue;
      }
      const stageModels: Record<string, string> = {};
      for (const [stage, u] of Object.entries<any>(r.tokens?.per_stage ?? {})) {
        if (typeof u?.model === "string" && u.model) stageModels[stage] = u.model;
      }
      runs.push({
        totalCostUsd: cost,
        sizeLabel: typeof r.size === "string" ? r.size : null,
        stageModels,
        recordedAt: r.recorded_at,
      });
    }
  }
  if (skipped > 0) console.error(`skipped ${skipped} malformed history record(s)`);
  return runs;
}

const args = parseArgs(process.argv);
const result = backtestWholeRunCalibration(loadCompletedRuns(args.historyDir), {
  since: args.since,
});
const ok =
  result.paired > 0 &&
  result.withinTwoX >= MIN_WITHIN_TWO_X &&
  result.medianRatio !== null &&
  result.medianRatio >= MEDIAN_RATIO_MIN &&
  result.medianRatio <= MEDIAN_RATIO_MAX;

if (args.json) {
  console.log(JSON.stringify({ ...result, since: args.since ?? null, ok }));
} else {
  console.log(`paired runs:            ${result.paired}`);
  console.log(`within 2x:              ${(result.withinTwoX * 100).toFixed(1)}%`);
  console.log(`median estimate/actual: ${result.medianRatio?.toFixed(2) ?? "n/a"}`);
  console.log(
    ok
      ? "PASS"
      : `FAIL (need ≥ ${MIN_WITHIN_TWO_X * 100}% within 2x and median in ` +
          `${MEDIAN_RATIO_MIN}–${MEDIAN_RATIO_MAX})`
  );
}
process.exit(ok ? 0 : 1);
