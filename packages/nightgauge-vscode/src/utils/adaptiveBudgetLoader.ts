/**
 * Adaptive Budget Loader — reads historical exit records to compute per-repo
 * p75 stage cost estimates and pass them as stageOverrides to BudgetEnforcer.
 *
 * The loader runs once at pipeline start (async), then the computed overrides
 * are passed into BudgetEnforcer's constructor so enforcement stays synchronous.
 * BudgetEnforcer itself is unchanged.
 *
 * Algorithm:
 * 1. Read exit-record JSONL from the last `limitDays` days.
 * 2. For each stage, collect cost_usd from records matching repo + size_label
 *    where success === true.
 * 3. If count >= minSamples, compute p75, clamp to [static × clampMin,
 *    static × clampMax].
 * 4. Only override when diff > 5% to avoid noisy micro-adjustments.
 * 5. Return overrides map and estimate_source labels for budget-overrun JSON.
 *
 * @see Issue #3667 — Adaptive per-repo stage-budget estimates
 * @see docs/CONFIGURATION.md — pipeline.adaptive_budget flag
 */

import * as fs from "node:fs/promises";
import * as path from "node:path";
import * as readline from "node:readline";
import { createReadStream } from "node:fs";

export type EstimateSource = "adaptive_p75" | "static_table";

export interface AdaptiveOverrides {
  /** Computed per-stage p75 costs (stage → override USD) */
  stageOverrides: Record<string, number>;
  /** Source label for each stage ("adaptive_p75" | "static_table") */
  estimateSources: Record<string, EstimateSource>;
  /** Diagnostic log lines emitted during loading */
  logLines: string[];
}

export interface AdaptiveBudgetLoaderParams {
  workspaceRoot: string;
  /** Canonical "owner/name" repo identifier */
  repo: string;
  /** Effective size label for the current issue (XS/S/M/L/XL) */
  sizeLabel: string;
  /** Static base budgets keyed by stage (stage → USD) */
  staticBudgets: Record<string, number>;
  /** Minimum successful samples before switching to adaptive path (default 5) */
  minSamples?: number;
  /** Minimum clamp multiplier relative to static (default 0.5) */
  clampMin?: number;
  /** Maximum clamp multiplier relative to static (default 3.0) */
  clampMax?: number;
  /** Number of history days to consider (default 30; 0 = all) */
  limitDays?: number;
  /** Master enable flag — when false, returns empty overrides (default true) */
  enabled?: boolean;
}

/**
 * Minimal shape of a StageExitRecord as written by the Go scheduler.
 * Only the fields we need for budget estimation.
 */
interface ExitRecord {
  repo?: string;
  stage?: string;
  size_label?: string;
  success?: boolean;
  tokens?: {
    cost_usd?: number;
  };
}

/**
 * Resolve the main repo root from a potential worktree path.
 * Exit records live in the main repo, not in the worktree.
 * Mirrors the logic in budgetIntelligence.ts.
 */
function resolveMainRepoRoot(workspaceRoot: string): string {
  const marker = `${path.sep}.worktrees${path.sep}`;
  const idx = workspaceRoot.indexOf(marker);
  return idx >= 0 ? workspaceRoot.substring(0, idx) : workspaceRoot;
}

/**
 * Compute the p75 of a sorted number array (nearest-rank method).
 */
function p75(sorted: number[]): number {
  if (sorted.length === 0) return 0;
  const idx = Math.floor(0.75 * (sorted.length - 1));
  return sorted[Math.min(idx, sorted.length - 1)];
}

/**
 * Read a single JSONL file line-by-line and collect matching cost samples.
 * Returns a map of stage → cost_usd[] for records matching repo + sizeLabel
 * where success === true.
 */
async function collectSamplesFromFile(
  filePath: string,
  repo: string,
  sizeLabel: string
): Promise<Map<string, number[]>> {
  const stageCosts = new Map<string, number[]>();
  try {
    const rl = readline.createInterface({
      input: createReadStream(filePath),
      crlfDelay: Infinity,
    });
    for await (const line of rl) {
      if (!line.trim()) continue;
      let rec: ExitRecord;
      try {
        rec = JSON.parse(line) as ExitRecord;
      } catch {
        continue;
      }
      if (
        rec.repo !== repo ||
        (rec.size_label ?? "") !== sizeLabel ||
        rec.success !== true ||
        !rec.stage ||
        !(rec.tokens?.cost_usd && rec.tokens.cost_usd > 0)
      ) {
        continue;
      }
      const existing = stageCosts.get(rec.stage) ?? [];
      existing.push(rec.tokens.cost_usd);
      stageCosts.set(rec.stage, existing);
    }
  } catch {
    // Non-fatal: file unreadable — skip
  }
  return stageCosts;
}

/**
 * Load adaptive budget overrides from historical exit records.
 *
 * Returns empty overrides (all static_table) when:
 * - `enabled` is false
 * - No exit records found
 * - Insufficient samples (< minSamples) for a stage
 * - Any read failure (non-fatal, logs a warning)
 */
export async function loadAdaptiveBudgetOverrides(
  params: AdaptiveBudgetLoaderParams
): Promise<AdaptiveOverrides> {
  const {
    workspaceRoot,
    repo,
    sizeLabel,
    staticBudgets,
    minSamples = 5,
    clampMin = 0.5,
    clampMax = 3.0,
    limitDays = 30,
    enabled = true,
  } = params;

  const empty: AdaptiveOverrides = { stageOverrides: {}, estimateSources: {}, logLines: [] };

  if (!enabled || !repo || !sizeLabel) {
    return empty;
  }

  try {
    const mainRoot = resolveMainRepoRoot(workspaceRoot);
    const exitRecordsDir = path.join(mainRoot, ".nightgauge", "pipeline", "exit-records");

    let entries: string[];
    try {
      const dirEntries = await fs.readdir(exitRecordsDir);
      entries = dirEntries.filter((e) => e.endsWith(".jsonl"));
    } catch {
      // Directory doesn't exist yet — cold start
      return empty;
    }

    // Filter by date window
    if (limitDays > 0) {
      const cutoff = new Date();
      cutoff.setDate(cutoff.getDate() - limitDays);
      const cutoffStr = cutoff.toISOString().slice(0, 10); // YYYY-MM-DD
      entries = entries.filter((e) => e.replace(".jsonl", "") >= cutoffStr);
    }

    if (entries.length === 0) {
      return empty;
    }

    // Collect samples from all files
    const allStageCosts = new Map<string, number[]>();
    await Promise.all(
      entries.map(async (name) => {
        const filePath = path.join(exitRecordsDir, name);
        const fileSamples = await collectSamplesFromFile(filePath, repo, sizeLabel);
        for (const [stage, costs] of fileSamples) {
          const existing = allStageCosts.get(stage) ?? [];
          allStageCosts.set(stage, existing.concat(costs));
        }
      })
    );

    const stageOverrides: Record<string, number> = {};
    const estimateSources: Record<string, EstimateSource> = {};
    const logLines: string[] = [];

    for (const [stage, staticBase] of Object.entries(staticBudgets)) {
      if (staticBase <= 0) continue;

      const costs = allStageCosts.get(stage);
      if (!costs || costs.length < minSamples) {
        estimateSources[stage] = "static_table";
        continue;
      }

      const sorted = [...costs].sort((a, b) => a - b);
      const rawP75 = p75(sorted);
      const clamped = Math.min(Math.max(rawP75, staticBase * clampMin), staticBase * clampMax);

      // Only override when diff > 5% to avoid noisy micro-adjustments
      const diffRatio = Math.abs(clamped - staticBase) / staticBase;
      if (diffRatio <= 0.05) {
        estimateSources[stage] = "static_table";
        continue;
      }

      stageOverrides[stage] = +clamped.toFixed(3);
      estimateSources[stage] = "adaptive_p75";
      logLines.push(
        `[adaptive-budget] ${stage} (${sizeLabel}): ` +
          `static=$${staticBase.toFixed(3)} → p75=$${rawP75.toFixed(3)} → ` +
          `clamped=$${clamped.toFixed(3)} (n=${costs.length}, diff=${(diffRatio * 100).toFixed(1)}%)`
      );
    }

    return { stageOverrides, estimateSources, logLines };
  } catch (err) {
    return {
      stageOverrides: {},
      estimateSources: {},
      logLines: [`[adaptive-budget] WARNING: failed to load exit records: ${String(err)}`],
    };
  }
}
