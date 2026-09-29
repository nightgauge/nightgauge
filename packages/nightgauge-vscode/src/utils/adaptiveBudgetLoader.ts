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

import { pipelineStateDir } from "./cloneLayout";
import * as fs from "node:fs/promises";
import * as path from "node:path";
import * as readline from "node:readline";
import { createReadStream } from "node:fs";
import { execFile } from "node:child_process";
import { promisify } from "node:util";

const execFileAsync = promisify(execFile);

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
 * Resolve the main repo root from a path that may be a worktree.
 *
 * THERE ARE TWO WORKTREE LAYOUTS AND THIS KNEW ABOUT ONE (#1017). The exact
 * lesson `internal/execution/issue_context_paths.go` records for #994, in a
 * file nobody re-checked:
 *
 *   - the VSCode extension writes `<repoRoot>/.worktrees/issue-N`
 *   - the Go manager writes `<repoRoot>/.nightgauge/worktrees/{repo}-issue-N`
 *
 * Missing the second means this returns the WORKTREE path unchanged, so every
 * history read calibrates against the worktree's own near-empty, gitignored
 * history rather than the repo's. That is how a repo whose corpus holds
 * eighty-seven costed issues reports a four-sample cohort — and a four-sample
 * cohort is how a pre-flight estimate lands ~2x under actual.
 *
 * String-stripping is not the ideal shape; the caller usually knows the real
 * root. It stays for the two in-tree layouts because both callers currently
 * receive a worktree path.
 *
 * Since #2038 the Go manager's worktrees live OUTSIDE the working tree
 * (`<worktree base>/{repo}-issue-N`, ADR-024 § 9), where no marker names the
 * repository. For such a path git answers instead
 * ({@link resolveMainRepoRootAsync}): a linked worktree's `--git-common-dir`
 * is `<main checkout>/.git`, so its parent is the repo root. This synchronous
 * form returns that answer once the async form has cached it, and the path
 * unchanged before then; it never spawns (the extension host must not block).
 */
export function resolveMainRepoRoot(workspaceRoot: string): string {
  return stripWorktreeMarker(workspaceRoot) ?? mainRootCache.get(workspaceRoot) ?? workspaceRoot;
}

/**
 * {@link resolveMainRepoRoot}, asking git when no in-tree marker applies. A
 * path that is not a linked worktree (or not a checkout at all) is returned
 * unchanged. Answers are cached per path.
 */
export async function resolveMainRepoRootAsync(workspaceRoot: string): Promise<string> {
  const marked = stripWorktreeMarker(workspaceRoot);
  if (marked !== undefined) return marked;
  return (await linkedWorktreeMainRoot(workspaceRoot)) ?? workspaceRoot;
}

function stripWorktreeMarker(workspaceRoot: string): string | undefined {
  // Longest marker first: the Go layout contains a path separator that the
  // extension marker would otherwise match inside it.
  for (const marker of [
    `${path.sep}.nightgauge${path.sep}worktrees${path.sep}`,
    `${path.sep}.worktrees${path.sep}`,
  ]) {
    const idx = workspaceRoot.indexOf(marker);
    if (idx >= 0) {
      return workspaceRoot.substring(0, idx);
    }
  }
  return undefined;
}

/** dir → its main checkout, for linked worktrees only. */
const mainRootCache = new Map<string, string>();
/** Paths git has already said are not linked worktrees. */
const notLinked = new Set<string>();

/**
 * The main checkout of the linked worktree at `dir`, or undefined when `dir`
 * is a main checkout, not a checkout, or git is unavailable.
 */
async function linkedWorktreeMainRoot(dir: string): Promise<string | undefined> {
  if (!dir || !path.isAbsolute(dir)) return undefined;
  const cached = mainRootCache.get(dir);
  if (cached !== undefined) return cached;
  if (notLinked.has(dir)) return undefined;
  try {
    const { stdout } = await execFileAsync(
      "git",
      ["rev-parse", "--path-format=absolute", "--git-dir", "--git-common-dir"],
      { cwd: dir, encoding: "utf8", timeout: 5_000 }
    );
    const [gitDir, commonDir] = stdout.trim().split("\n");
    if (gitDir && commonDir && path.resolve(gitDir) !== path.resolve(commonDir)) {
      const root = path.dirname(path.resolve(commonDir));
      mainRootCache.set(dir, root);
      return root;
    }
  } catch {
    // Not a checkout, or no git: leave the path as it is.
  }
  notLinked.add(dir);
  return undefined;
}

/**
 * Compute the p75 of a sorted number array (nearest-rank method).
 *
 * Exported so the pre-flight cost calibration (#112) shares one definition of
 * the statistic rather than growing a second, subtly different one.
 * Callers must pass an ascending-sorted array.
 */
export function p75(sorted: number[]): number {
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
    const mainRoot = await resolveMainRepoRootAsync(workspaceRoot);
    const exitRecordsDir = path.join(pipelineStateDir(mainRoot), "exit-records");

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
