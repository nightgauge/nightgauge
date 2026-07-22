#!/usr/bin/env tsx
/**
 * Audit Stage Cost Distribution — Compute per-stage cost percentiles from
 * pipeline run history.
 *
 * Reads `.nightgauge/pipeline/history/*.jsonl`, extracts
 * `tokens.per_stage[<stage>].cost_usd` for every recorded run, and prints
 * p50 / p95 / p99 per stage along with sample size and recommended
 * `pipeline.stage_cost_caps` defaults (target = p95 × 2 rounded to nearest
 * dollar, suggested only when n >= 20 — otherwise keep the current default).
 *
 * Filters:
 *   - outcome ∈ { complete, cancelled }   (skip orchestrator_crash etc.)
 *   - last 90 days (configurable via --days)
 *   - cost_usd > 0                        (skip stages that never ran)
 *
 * Usage:
 *   npx tsx scripts/audit-stage-cost-distribution.ts
 *   npx tsx scripts/audit-stage-cost-distribution.ts --days 30
 *   npx tsx scripts/audit-stage-cost-distribution.ts --history /path/to/history
 *
 * @see Issue #3208 — Tune per-stage cost cap defaults
 */

import * as fs from "fs";
import * as path from "path";

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

interface PerStageTokens {
  input?: number;
  output?: number;
  cost_usd?: number;
  cache_read?: number;
  cache_creation?: number;
}

interface RunRecord {
  schema_version?: string;
  record_type?: string;
  issue_number?: number;
  started_at?: string;
  completed_at?: string;
  outcome?: string;
  tokens?: {
    estimated_cost_usd?: number;
    per_stage?: Record<string, PerStageTokens>;
  };
}

interface StageStats {
  stage: string;
  n: number;
  p50: number;
  p95: number;
  p99: number;
  max: number;
  mean: number;
}

// ---------------------------------------------------------------------------
// Args
// ---------------------------------------------------------------------------

function parseArgs(argv: string[]): { days: number; historyDir: string } {
  let days = 90;
  let historyDir = path.resolve(process.cwd(), ".nightgauge", "pipeline", "history");
  for (let i = 2; i < argv.length; i++) {
    const a = argv[i];
    if (a === "--days" && argv[i + 1]) {
      const parsed = Number.parseInt(argv[++i], 10);
      if (!Number.isNaN(parsed) && parsed > 0) days = parsed;
    } else if (a === "--history" && argv[i + 1]) {
      historyDir = argv[++i];
    }
  }
  return { days, historyDir };
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function percentile(sorted: number[], p: number): number {
  if (sorted.length === 0) return 0;
  if (sorted.length === 1) return sorted[0];
  // Nearest-rank method (no interpolation): index = ceil(p/100 * n) - 1
  const idx = Math.min(sorted.length - 1, Math.max(0, Math.ceil((p / 100) * sorted.length) - 1));
  return sorted[idx];
}

function fmtUsd(n: number): string {
  return `$${n.toFixed(2)}`;
}

// Round p95 × 2 to the nearest dollar, with a $1 floor when there's any
// productive cost so the cap is meaningful.
function recommendCap(p95: number): number {
  const target = p95 * 2;
  return Math.max(1, Math.round(target));
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

function main(): void {
  const { days, historyDir } = parseArgs(process.argv);

  if (!fs.existsSync(historyDir)) {
    console.error(`History directory not found: ${historyDir}`);
    process.exit(1);
  }

  const cutoff = new Date(Date.now() - days * 24 * 60 * 60 * 1000);
  const files = fs
    .readdirSync(historyDir)
    .filter((f) => f.endsWith(".jsonl"))
    .sort();

  // stage -> array of cost_usd values
  const byStage = new Map<string, number[]>();
  let totalRunsConsidered = 0;
  let totalRunsKept = 0;

  for (const f of files) {
    const dateMatch = f.match(/^(\d{4}-\d{2}-\d{2})\.jsonl$/);
    if (dateMatch) {
      const fileDate = new Date(`${dateMatch[1]}T00:00:00Z`);
      if (fileDate < cutoff) continue;
    }
    const full = path.join(historyDir, f);
    const content = fs.readFileSync(full, "utf-8");
    const lines = content.split("\n").filter((l) => l.trim().length > 0);
    for (const line of lines) {
      let rec: RunRecord;
      try {
        rec = JSON.parse(line) as RunRecord;
      } catch {
        continue;
      }
      if (rec.record_type && rec.record_type !== "run") continue;
      totalRunsConsidered++;
      // Only keep complete or cancelled (productive cost). Cancelled runs
      // include user-cancelled and cost-cap kills, both of which represent
      // real spend we care about. Skip orchestrator crashes.
      if (rec.outcome !== "complete" && rec.outcome !== "cancelled") continue;
      if (rec.started_at) {
        const started = new Date(rec.started_at);
        if (!Number.isNaN(started.getTime()) && started < cutoff) continue;
      }
      const perStage = rec.tokens?.per_stage;
      if (!perStage) continue;
      totalRunsKept++;
      for (const [stage, t] of Object.entries(perStage)) {
        const cost = t?.cost_usd;
        if (typeof cost !== "number" || !Number.isFinite(cost) || cost <= 0) continue;
        let arr = byStage.get(stage);
        if (!arr) {
          arr = [];
          byStage.set(stage, arr);
        }
        arr.push(cost);
      }
    }
  }

  // Build stats
  const stats: StageStats[] = [];
  for (const [stage, costs] of byStage.entries()) {
    const sorted = [...costs].sort((a, b) => a - b);
    const sum = sorted.reduce((a, b) => a + b, 0);
    stats.push({
      stage,
      n: sorted.length,
      p50: percentile(sorted, 50),
      p95: percentile(sorted, 95),
      p99: percentile(sorted, 99),
      max: sorted[sorted.length - 1],
      mean: sum / sorted.length,
    });
  }

  // Stable order: pipeline order if known, otherwise alphabetical
  const order = [
    "pipeline-start",
    "issue-pickup",
    "feature-planning",
    "feature-dev",
    "feature-validate",
    "pr-create",
    "pr-merge",
  ];
  stats.sort((a, b) => {
    const ia = order.indexOf(a.stage);
    const ib = order.indexOf(b.stage);
    if (ia !== -1 && ib !== -1) return ia - ib;
    if (ia !== -1) return -1;
    if (ib !== -1) return 1;
    return a.stage.localeCompare(b.stage);
  });

  // ---------------------------------------------------------------------------
  // Output
  // ---------------------------------------------------------------------------
  console.log(`# Stage Cost Distribution Audit`);
  console.log("");
  console.log(`- History dir: \`${historyDir}\``);
  console.log(`- Window: last ${days} days (cutoff ${cutoff.toISOString()})`);
  console.log(
    `- Records considered: ${totalRunsConsidered}; kept (complete|cancelled): ${totalRunsKept}`
  );
  console.log("");
  console.log(`| Stage | n | p50 | p95 | p99 | max | mean | recommended cap (p95×2) |`);
  console.log(`| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |`);
  for (const s of stats) {
    const rec = s.n >= 20 ? fmtUsd(recommendCap(s.p95)) : `n<20 — keep current`;
    console.log(
      `| ${s.stage} | ${s.n} | ${fmtUsd(s.p50)} | ${fmtUsd(s.p95)} | ${fmtUsd(
        s.p99
      )} | ${fmtUsd(s.max)} | ${fmtUsd(s.mean)} | ${rec} |`
    );
  }
  console.log("");
  console.log(
    `Recommendation rule: base cap = round(p95 × 2) when n ≥ 20; otherwise leave default unchanged.`
  );
  console.log(`Effective cap = base × COST_CAP_MODEL_SCALE[model:effort] (e.g. opus:high = 5.0×).`);
}

main();
