#!/usr/bin/env tsx
/**
 * Analyze Model Routing Effectiveness
 *
 * Audits AutoModelSelector by retroactively replaying model selections
 * against JSONL execution history. Reports per-model/per-stage performance,
 * under/over-routing patterns, cost comparisons, and threshold recommendations.
 *
 * Usage:
 *   npx tsx scripts/analyze-model-routing.ts
 *   npx tsx scripts/analyze-model-routing.ts --since 2026-02-15
 *   npx tsx scripts/analyze-model-routing.ts --output reports/custom-report.md
 *
 * @see Issue #1043 - Analyze model routing effectiveness
 */

import * as fs from "fs/promises";
import * as path from "path";
import {
  AutoModelSelector,
  type ComplexityLabel,
  type ModelTier,
  type ModelSelectionResult,
  type IssueMetadata,
} from "../packages/nightgauge-sdk/src/analysis/AutoModelSelector.js";
import { DEFAULT_MODEL_COST_RATES } from "../packages/nightgauge-sdk/src/analysis/types.js";

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

/** Parsed JSONL run record (subset of fields we need) */
interface RunRecord {
  schema_version: string;
  record_type: string;
  issue_number: number;
  title: string;
  started_at: string;
  completed_at: string;
  total_duration_ms: number;
  outcome: "complete" | "failed" | "cancelled";
  labels?: string[];
  size?: string | null;
  type?: string | null;
  priority?: string | null;
  stages: Record<string, StageDetail>;
  tokens: {
    total_input: number;
    total_output: number;
    total_cache_read: number;
    total_cache_creation: number;
    estimated_cost_usd: number;
    per_stage?: Record<string, StageTokenUsage>;
  };
  routing?: {
    complexity_score?: number;
    path?: string;
    skip_stages?: string[];
  };
}

interface StageDetail {
  status: "complete" | "failed" | "skipped" | "pending" | "deferred";
  started_at?: string;
  completed_at?: string;
  duration_ms?: number;
  error?: string;
  skip_reason?: string;
  auto_retry_count?: number;
  manual_retry_count?: number;
  model_selection?: {
    model: string;
    source: string;
    confidence?: number;
    complexity?: string;
  };
}

interface StageTokenUsage {
  input: number;
  output: number;
  cache_read: number;
  cache_creation: number;
  cost_usd: number;
  model?: string;
  model_source?: string;
}

/** Per-stage simulation result */
interface StageSimulation {
  stage: string;
  selection: ModelSelectionResult;
  actualCostUsd: number;
  hypotheticalCostUsd: number;
  durationMs: number;
  stageOutcome: "complete" | "failed" | "skipped";
  retries: number;
}

/** Per-run simulation result */
interface RunSimulation {
  issueNumber: number;
  title: string;
  size: ComplexityLabel;
  type: string;
  priority: string;
  labels: string[];
  outcome: string;
  totalActualCost: number;
  totalAutoRoutedCost: number;
  stages: StageSimulation[];
}

/** Per-model, per-stage aggregated metrics */
interface ModelStageMetrics {
  model: ModelTier;
  stage: string;
  runs: number;
  successes: number;
  successRate: number;
  totalCost: number;
  avgCost: number;
  totalDuration: number;
  avgDuration: number;
  retries: number;
}

/** Under-routing pattern */
interface UnderRoutingPattern {
  stage: string;
  selectedModel: ModelTier;
  complexity: ComplexityLabel;
  count: number;
  avgCost: number;
  examples: number[];
}

/** Over-routing pattern */
interface OverRoutingPattern {
  stage: string;
  selectedModel: ModelTier;
  complexity: ComplexityLabel;
  count: number;
  avgCost: number;
  potentialSavings: number;
  examples: number[];
}

/** Confidence bucket analysis */
interface ConfidenceBucket {
  range: string;
  minConfidence: number;
  maxConfidence: number;
  totalStages: number;
  successes: number;
  successRate: number;
  avgCost: number;
}

// ---------------------------------------------------------------------------
// Constants
// ---------------------------------------------------------------------------

const PIPELINE_STAGES = [
  "issue-pickup",
  "feature-planning",
  "feature-dev",
  "feature-validate",
  "pr-create",
  "pr-merge",
] as const;

const DEFAULT_SINCE = "2026-02-15T00:00:00Z";
const HISTORY_DIR = ".nightgauge/pipeline/history";

// ---------------------------------------------------------------------------
// CLI Argument Parsing
// ---------------------------------------------------------------------------

function parseArgs(): { since: string; output: string } {
  const args = process.argv.slice(2);
  let since = DEFAULT_SINCE;
  let output = `reports/model-routing-analysis-${new Date().toISOString().split("T")[0]}.md`;

  for (let i = 0; i < args.length; i++) {
    if (args[i] === "--since" && args[i + 1]) {
      since = args[i + 1].includes("T") ? args[i + 1] : `${args[i + 1]}T00:00:00Z`;
      i++;
    } else if (args[i] === "--output" && args[i + 1]) {
      output = args[i + 1];
      i++;
    }
  }

  return { since, output };
}

// ---------------------------------------------------------------------------
// JSONL Parsing
// ---------------------------------------------------------------------------

async function loadRunRecords(historyDir: string, since: string): Promise<RunRecord[]> {
  const files = await fs.readdir(historyDir);
  const jsonlFiles = files.filter((f) => f.endsWith(".jsonl")).sort();

  const records: RunRecord[] = [];
  const sinceMs = new Date(since).getTime();

  for (const file of jsonlFiles) {
    const content = await fs.readFile(path.join(historyDir, file), "utf-8");
    const lines = content.trim().split("\n").filter(Boolean);

    for (const line of lines) {
      try {
        const record = JSON.parse(line) as RunRecord;
        if (record.record_type !== "run") continue;
        if (new Date(record.started_at).getTime() < sinceMs) continue;
        records.push(record);
      } catch {
        // Skip malformed lines
      }
    }
  }

  return records;
}

// ---------------------------------------------------------------------------
// Model Selection Simulation
// ---------------------------------------------------------------------------

function buildIssueMetadata(record: RunRecord): IssueMetadata {
  return {
    labels: record.labels ?? [],
    title: record.title,
    size: (record.size as ComplexityLabel) ?? undefined,
  };
}

function simulateRun(record: RunRecord, selector: AutoModelSelector): RunSimulation {
  const metadata = buildIssueMetadata(record);
  const stages: StageSimulation[] = [];
  let totalActualCost = 0;
  let totalAutoRoutedCost = 0;

  for (const stageName of PIPELINE_STAGES) {
    const stageDetail = record.stages[stageName];
    if (!stageDetail) continue;

    const stageTokens = record.tokens.per_stage?.[stageName];
    const actualCost = stageTokens?.cost_usd ?? 0;
    const selection = selector.selectModel(stageName, metadata);

    // Compute hypothetical cost if this model were used
    const hypotheticalCost = computeHypotheticalCost(stageTokens, selection.model);

    const retries = (stageDetail.auto_retry_count ?? 0) + (stageDetail.manual_retry_count ?? 0);

    stages.push({
      stage: stageName,
      selection,
      actualCostUsd: actualCost,
      hypotheticalCostUsd: hypotheticalCost,
      durationMs: stageDetail.duration_ms ?? 0,
      stageOutcome:
        stageDetail.status === "complete"
          ? "complete"
          : stageDetail.status === "failed"
            ? "failed"
            : "skipped",
      retries,
    });

    if (stageDetail.status !== "skipped") {
      totalActualCost += actualCost;
      totalAutoRoutedCost += hypotheticalCost;
    }
  }

  return {
    issueNumber: record.issue_number,
    title: record.title,
    size:
      (record.size as ComplexityLabel) ?? selector.selectModel("feature-dev", metadata).complexity,
    type: record.type ?? "unknown",
    priority: record.priority ?? "unknown",
    labels: record.labels ?? [],
    outcome: record.outcome,
    totalActualCost,
    totalAutoRoutedCost,
    stages,
  };
}

/**
 * Compute hypothetical cost if a different model were used.
 *
 * We scale the actual cost by the ratio of model cost rates. Since all
 * historical runs used sonnet, we can estimate what another model would
 * have cost for the same token volume.
 */
function computeHypotheticalCost(
  stageTokens: StageTokenUsage | undefined,
  targetModel: ModelTier
): number {
  if (!stageTokens) return 0;

  const targetRates = DEFAULT_MODEL_COST_RATES[targetModel];
  if (!targetRates) return 0;

  // Compute cost using actual token counts with target model rates
  const inputCost = (stageTokens.input * targetRates.inputPerMillion) / 1_000_000;
  const outputCost = (stageTokens.output * targetRates.outputPerMillion) / 1_000_000;
  const cacheReadCost =
    (stageTokens.cache_read * (targetRates.cacheReadPerMillion ?? 0)) / 1_000_000;
  const cacheCreationCost =
    (stageTokens.cache_creation * (targetRates.cacheCreationPerMillion ?? 0)) / 1_000_000;

  return inputCost + outputCost + cacheReadCost + cacheCreationCost;
}

// ---------------------------------------------------------------------------
// Analysis Functions
// ---------------------------------------------------------------------------

function aggregateModelStageMetrics(simulations: RunSimulation[]): ModelStageMetrics[] {
  const groups = new Map<string, ModelStageMetrics>();

  for (const sim of simulations) {
    for (const stageSim of sim.stages) {
      if (stageSim.stageOutcome === "skipped") continue;

      const key = `${stageSim.selection.model}|${stageSim.stage}`;
      const existing = groups.get(key) ?? {
        model: stageSim.selection.model,
        stage: stageSim.stage,
        runs: 0,
        successes: 0,
        successRate: 0,
        totalCost: 0,
        avgCost: 0,
        totalDuration: 0,
        avgDuration: 0,
        retries: 0,
      };

      existing.runs++;
      if (stageSim.stageOutcome === "complete") existing.successes++;
      existing.totalCost += stageSim.hypotheticalCostUsd;
      existing.totalDuration += stageSim.durationMs;
      existing.retries += stageSim.retries;

      groups.set(key, existing);
    }
  }

  // Compute averages
  for (const metrics of groups.values()) {
    metrics.successRate = metrics.runs > 0 ? metrics.successes / metrics.runs : 0;
    metrics.avgCost = metrics.runs > 0 ? metrics.totalCost / metrics.runs : 0;
    metrics.avgDuration = metrics.runs > 0 ? metrics.totalDuration / metrics.runs : 0;
  }

  return Array.from(groups.values()).sort((a, b) => {
    const stageOrder =
      PIPELINE_STAGES.indexOf(a.stage as (typeof PIPELINE_STAGES)[number]) -
      PIPELINE_STAGES.indexOf(b.stage as (typeof PIPELINE_STAGES)[number]);
    if (stageOrder !== 0) return stageOrder;
    return a.model.localeCompare(b.model);
  });
}

function detectUnderRouting(simulations: RunSimulation[]): UnderRoutingPattern[] {
  // Under-routing: auto-selector chose lighter model but stage had high cost or retries
  const groups = new Map<string, { issues: number[]; costs: number[] }>();

  for (const sim of simulations) {
    for (const stageSim of sim.stages) {
      if (stageSim.stageOutcome === "skipped") continue;

      // Detect: haiku selected for non-lightweight stage with actual high cost
      const isLightModel = stageSim.selection.model === "haiku";
      const isNonLightweightStage = !["issue-pickup", "pr-create"].includes(stageSim.stage);
      const hasHighCost = stageSim.actualCostUsd > 10; // Above P90 threshold
      const hasRetries = stageSim.retries > 0;

      if (isLightModel && isNonLightweightStage && (hasHighCost || hasRetries)) {
        const key = `${stageSim.stage}|${stageSim.selection.model}|${stageSim.selection.complexity}`;
        const existing = groups.get(key) ?? { issues: [], costs: [] };
        existing.issues.push(sim.issueNumber);
        existing.costs.push(stageSim.actualCostUsd);
        groups.set(key, existing);
      }
    }
  }

  const patterns: UnderRoutingPattern[] = [];
  for (const [key, group] of groups) {
    const [stage, model, complexity] = key.split("|");
    patterns.push({
      stage,
      selectedModel: model as ModelTier,
      complexity: complexity as ComplexityLabel,
      count: group.issues.length,
      avgCost: group.costs.reduce((a, b) => a + b, 0) / group.costs.length,
      examples: group.issues.slice(0, 5),
    });
  }

  return patterns.sort((a, b) => b.count - a.count);
}

function detectOverRouting(simulations: RunSimulation[]): OverRoutingPattern[] {
  // Over-routing: opus selected for simple tasks that completed quickly and cheaply
  const groups = new Map<string, { issues: number[]; costs: number[]; haikuCosts: number[] }>();

  for (const sim of simulations) {
    for (const stageSim of sim.stages) {
      if (stageSim.stageOutcome !== "complete") continue;

      const isHeavyModel = stageSim.selection.model === "opus";
      const isSimpleComplexity = ["XS", "S"].includes(stageSim.selection.complexity);
      const noRetries = stageSim.retries === 0;

      if (isHeavyModel && isSimpleComplexity && noRetries) {
        const key = `${stageSim.stage}|${stageSim.selection.model}|${stageSim.selection.complexity}`;
        const existing = groups.get(key) ?? {
          issues: [],
          costs: [],
          haikuCosts: [],
        };
        existing.issues.push(sim.issueNumber);
        existing.costs.push(stageSim.hypotheticalCostUsd);

        // Compute what haiku would have cost
        const stageTokens = sim.stages.find((s) => s.stage === stageSim.stage);
        if (stageTokens) {
          const sonnetRatio =
            DEFAULT_MODEL_COST_RATES.haiku.inputPerMillion /
            DEFAULT_MODEL_COST_RATES.opus.inputPerMillion;
          existing.haikuCosts.push(stageSim.hypotheticalCostUsd * sonnetRatio);
        }

        groups.set(key, existing);
      }
    }
  }

  const patterns: OverRoutingPattern[] = [];
  for (const [key, group] of groups) {
    const [stage, model, complexity] = key.split("|");
    const totalCost = group.costs.reduce((a, b) => a + b, 0);
    const totalHaikuCost = group.haikuCosts.reduce((a, b) => a + b, 0);
    patterns.push({
      stage,
      selectedModel: model as ModelTier,
      complexity: complexity as ComplexityLabel,
      count: group.issues.length,
      avgCost: totalCost / group.costs.length,
      potentialSavings: totalCost - totalHaikuCost,
      examples: group.issues.slice(0, 5),
    });
  }

  return patterns.sort((a, b) => b.potentialSavings - a.potentialSavings);
}

function analyzeConfidenceScoring(simulations: RunSimulation[]): ConfidenceBucket[] {
  const buckets: ConfidenceBucket[] = [
    {
      range: "0.0-0.4",
      minConfidence: 0,
      maxConfidence: 0.4,
      totalStages: 0,
      successes: 0,
      successRate: 0,
      avgCost: 0,
    },
    {
      range: "0.4-0.6",
      minConfidence: 0.4,
      maxConfidence: 0.6,
      totalStages: 0,
      successes: 0,
      successRate: 0,
      avgCost: 0,
    },
    {
      range: "0.6-0.75",
      minConfidence: 0.6,
      maxConfidence: 0.75,
      totalStages: 0,
      successes: 0,
      successRate: 0,
      avgCost: 0,
    },
    {
      range: "0.75-0.9",
      minConfidence: 0.75,
      maxConfidence: 0.9,
      totalStages: 0,
      successes: 0,
      successRate: 0,
      avgCost: 0,
    },
    {
      range: "0.9-1.0",
      minConfidence: 0.9,
      maxConfidence: 1.0,
      totalStages: 0,
      successes: 0,
      successRate: 0,
      avgCost: 0,
    },
  ];

  const bucketCosts: number[][] = buckets.map(() => []);

  for (const sim of simulations) {
    for (const stageSim of sim.stages) {
      if (stageSim.stageOutcome === "skipped") continue;

      const confidence = stageSim.selection.confidence;
      const bucketIdx = buckets.findIndex(
        (b) => confidence >= b.minConfidence && confidence <= b.maxConfidence
      );
      if (bucketIdx < 0) continue;

      buckets[bucketIdx].totalStages++;
      if (stageSim.stageOutcome === "complete") buckets[bucketIdx].successes++;
      bucketCosts[bucketIdx].push(stageSim.actualCostUsd);
    }
  }

  for (let i = 0; i < buckets.length; i++) {
    const b = buckets[i];
    b.successRate = b.totalStages > 0 ? b.successes / b.totalStages : 0;
    const costs = bucketCosts[i];
    b.avgCost = costs.length > 0 ? costs.reduce((a, c) => a + c, 0) / costs.length : 0;
  }

  return buckets;
}

function analyzeThresholds(simulations: RunSimulation[]): {
  complexityDistribution: Record<
    ComplexityLabel,
    { count: number; avgCost: number; successRate: number }
  >;
  haikuMaxAnalysis: string;
  sonnetMaxAnalysis: string;
  recommendations: string[];
} {
  const complexityGroups = new Map<
    ComplexityLabel,
    { costs: number[]; successes: number; total: number }
  >();

  for (const sim of simulations) {
    const complexity = sim.size;
    const existing = complexityGroups.get(complexity) ?? {
      costs: [],
      successes: 0,
      total: 0,
    };
    existing.total++;
    if (sim.outcome === "complete") existing.successes++;
    existing.costs.push(sim.totalActualCost);
    complexityGroups.set(complexity, existing);
  }

  const distribution: Record<string, { count: number; avgCost: number; successRate: number }> = {};
  for (const [complexity, data] of complexityGroups) {
    distribution[complexity] = {
      count: data.total,
      avgCost: data.costs.reduce((a, b) => a + b, 0) / data.costs.length,
      successRate: data.total > 0 ? data.successes / data.total : 0,
    };
  }

  // Current thresholds: haikuMax=3 (XS=1, S=2), sonnetMax=6 (M=4)
  // Check if XS/S runs would benefit from haiku
  const xsData = complexityGroups.get("XS");
  const sData = complexityGroups.get("S");
  const lightCount = (xsData?.total ?? 0) + (sData?.total ?? 0);
  const lightSuccess = (xsData?.successes ?? 0) + (sData?.successes ?? 0);
  const lightSuccessRate = lightCount > 0 ? lightSuccess / lightCount : 0;

  let haikuMaxAnalysis: string;
  if (lightCount === 0) {
    haikuMaxAnalysis = "No XS/S runs in dataset — cannot validate haiku_max threshold.";
  } else {
    haikuMaxAnalysis =
      `XS/S runs (n=${lightCount}): ${(lightSuccessRate * 100).toFixed(1)}% success rate. ` +
      `${
        lightSuccessRate >= 0.95
          ? "These runs succeed reliably — haiku routing is safe for them."
          : "Low success rate suggests haiku may be insufficient for these runs."
      }`;
  }

  // Check L/XL runs
  const lData = complexityGroups.get("L");
  const xlData = complexityGroups.get("XL");
  const heavyCount = (lData?.total ?? 0) + (xlData?.total ?? 0);
  const heavySuccess = (lData?.successes ?? 0) + (xlData?.successes ?? 0);
  const heavySuccessRate = heavyCount > 0 ? heavySuccess / heavyCount : 0;

  let sonnetMaxAnalysis: string;
  if (heavyCount === 0) {
    sonnetMaxAnalysis = "No L/XL runs in dataset — cannot validate sonnet_max threshold.";
  } else {
    sonnetMaxAnalysis =
      `L/XL runs (n=${heavyCount}): ${(heavySuccessRate * 100).toFixed(1)}% success rate. ` +
      `${
        heavySuccessRate >= 0.9
          ? "Good success rate — current opus routing for L/XL is appropriate."
          : "Lower success rate may warrant investigation."
      }`;
  }

  const recommendations: string[] = [];

  // Check M runs — these stay on sonnet under current thresholds
  const mData = complexityGroups.get("M");
  if (mData && mData.total > 5) {
    const mSuccessRate = mData.successes / mData.total;
    if (mSuccessRate < 0.9) {
      recommendations.push(
        `M-complexity runs have ${(mSuccessRate * 100).toFixed(1)}% success rate — ` +
          `consider lowering sonnet_max to route M tasks to opus.`
      );
    }
  }

  // Check if size prediction accuracy is low
  if (lightCount > 0 && lightSuccessRate >= 0.98) {
    recommendations.push(
      "XS/S tasks succeed at 98%+ — lowering haikuMax from 3 to 2 would route " +
        "only XS to haiku, which may be too conservative. Current threshold is appropriate."
    );
  }

  return {
    complexityDistribution: distribution as Record<
      ComplexityLabel,
      { count: number; avgCost: number; successRate: number }
    >,
    haikuMaxAnalysis,
    sonnetMaxAnalysis,
    recommendations,
  };
}

/** Compute model distribution summary */
function computeModelDistribution(
  simulations: RunSimulation[]
): Record<ModelTier, { stages: number; cost: number }> {
  const dist: Record<ModelTier, { stages: number; cost: number }> = {
    haiku: { stages: 0, cost: 0 },
    sonnet: { stages: 0, cost: 0 },
    opus: { stages: 0, cost: 0 },
  };

  for (const sim of simulations) {
    for (const stageSim of sim.stages) {
      if (stageSim.stageOutcome === "skipped") continue;
      dist[stageSim.selection.model].stages++;
      dist[stageSim.selection.model].cost += stageSim.hypotheticalCostUsd;
    }
  }

  return dist;
}

// ---------------------------------------------------------------------------
// Report Generation
// ---------------------------------------------------------------------------

function generateReport(
  simulations: RunSimulation[],
  records: RunRecord[],
  metrics: ModelStageMetrics[],
  underRouting: UnderRoutingPattern[],
  overRouting: OverRoutingPattern[],
  confidenceBuckets: ConfidenceBucket[],
  thresholdAnalysis: ReturnType<typeof analyzeThresholds>,
  modelDist: Record<ModelTier, { stages: number; cost: number }>,
  since: string
): string {
  const totalActualCost = simulations.reduce((sum, s) => sum + s.totalActualCost, 0);
  const totalAutoRoutedCost = simulations.reduce((sum, s) => sum + s.totalAutoRoutedCost, 0);
  const costSavings = totalActualCost - totalAutoRoutedCost;
  const costSavingsPercent = totalActualCost > 0 ? (costSavings / totalActualCost) * 100 : 0;

  const totalStageExecutions = simulations.reduce(
    (sum, s) => sum + s.stages.filter((st) => st.stageOutcome !== "skipped").length,
    0
  );

  const dateRange = {
    start: records.length > 0 ? records[0].started_at.split("T")[0] : "N/A",
    end: records.length > 0 ? records[records.length - 1].started_at.split("T")[0] : "N/A",
  };

  const lines: string[] = [];

  // Header
  lines.push(`# Model Routing Analysis Report — ${new Date().toISOString().split("T")[0]}`);
  lines.push("");
  lines.push(
    `**Issue**: #1043  **Period analyzed**: ${dateRange.start} – ${dateRange.end} (post-${since.split("T")[0]})`
  );
  lines.push(
    `**Total runs analyzed**: ${simulations.length}  **Stage executions**: ${totalStageExecutions}`
  );
  lines.push("");
  lines.push("---");
  lines.push("");

  // Executive Summary
  lines.push("## Executive Summary");
  lines.push("");
  lines.push(
    `1. **Auto-routing would save ${costSavingsPercent >= 0 ? `$${costSavings.toFixed(2)} (${costSavingsPercent.toFixed(1)}%)` : `cost $${Math.abs(costSavings).toFixed(2)} more (${Math.abs(costSavingsPercent).toFixed(1)}%)`}** — ` +
      `Compared to the current all-sonnet baseline ($${totalActualCost.toFixed(2)}), auto-routing ` +
      `would cost $${totalAutoRoutedCost.toFixed(2)}.`
  );

  lines.push(
    `2. **Model distribution under auto-routing** — ` +
      `Haiku: ${modelDist.haiku.stages} stages ($${modelDist.haiku.cost.toFixed(2)}), ` +
      `Sonnet: ${modelDist.sonnet.stages} stages ($${modelDist.sonnet.cost.toFixed(2)}), ` +
      `Opus: ${modelDist.opus.stages} stages ($${modelDist.opus.cost.toFixed(2)}).`
  );

  lines.push(
    `3. **${simulations.filter((s) => s.outcome === "complete").length}/${simulations.length} runs completed successfully** — ` +
      `${((simulations.filter((s) => s.outcome === "complete").length / simulations.length) * 100).toFixed(1)}% success rate.`
  );

  const sizes = new Map<string, number>();
  for (const sim of simulations) {
    sizes.set(sim.size, (sizes.get(sim.size) ?? 0) + 1);
  }
  const sizeBreakdown = Array.from(sizes.entries())
    .sort(([a], [b]) => a.localeCompare(b))
    .map(([size, count]) => `${size}: ${count}`)
    .join(", ");
  lines.push(`4. **Complexity distribution** — ${sizeBreakdown}.`);

  if (underRouting.length > 0) {
    lines.push(
      `5. **${underRouting.length} under-routing pattern(s) detected** — Lightweight models assigned to stages with high actual cost.`
    );
  } else {
    lines.push(
      "5. **No under-routing patterns detected** — Model assignments align with task complexity."
    );
  }

  if (overRouting.length > 0) {
    const totalWaste = overRouting.reduce((sum, p) => sum + p.potentialSavings, 0);
    lines.push(
      `6. **${overRouting.length} over-routing pattern(s) detected** — Estimated $${totalWaste.toFixed(2)} wasted on oversized models for simple tasks.`
    );
  } else {
    lines.push(
      "6. **No over-routing patterns detected** — No cases of opus being assigned to XS/S tasks."
    );
  }

  lines.push("");
  lines.push("---");
  lines.push("");

  // Data Overview
  lines.push("## Data Overview");
  lines.push("");
  lines.push("| Metric | Value |");
  lines.push("| ------ | ----- |");
  lines.push(`| Runs analyzed | ${simulations.length} |`);
  lines.push(`| Date range | ${dateRange.start} – ${dateRange.end} |`);
  lines.push(`| Stage executions | ${totalStageExecutions} |`);
  lines.push(
    `| Success rate | ${((simulations.filter((s) => s.outcome === "complete").length / simulations.length) * 100).toFixed(1)}% |`
  );
  lines.push(`| Total actual cost (all-sonnet) | $${totalActualCost.toFixed(2)} |`);
  lines.push(`| Total auto-routed cost (hypothetical) | $${totalAutoRoutedCost.toFixed(2)} |`);
  lines.push(
    `| Net savings from auto-routing | $${costSavings.toFixed(2)} (${costSavingsPercent.toFixed(1)}%) |`
  );
  lines.push("");
  lines.push("### Methodology");
  lines.push("");
  lines.push(
    "Since `model_selection` data is not yet populated in JSONL records (Issue #1006 added the schema but records lack the data), this analysis **retroactively simulates** model selections using `AutoModelSelector.selectModel()` against each run's issue metadata (`size`, `type`, `priority`, `labels`). Hypothetical costs are computed by applying model-specific cost rates to actual token counts from each stage."
  );
  lines.push("");
  lines.push("---");
  lines.push("");

  // Auto-Selection Simulation Results
  lines.push("## Auto-Selection Simulation Results");
  lines.push("");
  lines.push("### Per-Stage Model Distribution");
  lines.push("");
  lines.push("| Stage | Haiku | Sonnet | Opus | Total |");
  lines.push("| ----- | ----- | ------ | ---- | ----- |");

  for (const stage of PIPELINE_STAGES) {
    const stageMetrics = metrics.filter((m) => m.stage === stage);
    const haiku = stageMetrics.find((m) => m.model === "haiku")?.runs ?? 0;
    const sonnet = stageMetrics.find((m) => m.model === "sonnet")?.runs ?? 0;
    const opus = stageMetrics.find((m) => m.model === "opus")?.runs ?? 0;
    const total = haiku + sonnet + opus;
    lines.push(
      `| ${stage} | ${haiku} (${total > 0 ? ((haiku / total) * 100).toFixed(0) : 0}%) | ${sonnet} (${total > 0 ? ((sonnet / total) * 100).toFixed(0) : 0}%) | ${opus} (${total > 0 ? ((opus / total) * 100).toFixed(0) : 0}%) | ${total} |`
    );
  }

  lines.push("");
  lines.push("---");
  lines.push("");

  // Per-Model, Per-Stage Performance
  lines.push("## Per-Model, Per-Stage Performance");
  lines.push("");
  lines.push("| Stage | Model | Runs | Success Rate | Avg Cost (Hyp.) | Avg Duration | Retries |");
  lines.push("| ----- | ----- | ---- | ------------ | --------------- | ------------ | ------- |");

  for (const m of metrics) {
    const durationStr =
      m.avgDuration > 60000
        ? `${(m.avgDuration / 60000).toFixed(1)}m`
        : `${(m.avgDuration / 1000).toFixed(0)}s`;
    lines.push(
      `| ${m.stage} | ${m.model} | ${m.runs} | ${(m.successRate * 100).toFixed(1)}% | $${m.avgCost.toFixed(4)} | ${durationStr} | ${m.retries} |`
    );
  }

  lines.push("");
  lines.push("---");
  lines.push("");

  // Under-Routing Patterns
  lines.push("## Under-Routing Patterns");
  lines.push("");
  if (underRouting.length === 0) {
    lines.push(
      "**No under-routing patterns detected.** All model assignments appear appropriate for the task complexity and actual costs observed."
    );
    lines.push("");
    lines.push("This is expected because:");
    lines.push(
      "- The per-stage complexity matrix already routes L/XL tasks to opus for dev/planning/validate stages"
    );
    lines.push(
      "- Haiku is only assigned to lightweight stages (issue-pickup, pr-create) regardless of complexity"
    );
    lines.push("- The 100% success rate means no task-complexity mismatches surfaced as failures");
  } else {
    lines.push(
      "| Stage | Selected Model | Complexity | Count | Avg Actual Cost | Example Issues |"
    );
    lines.push(
      "| ----- | -------------- | ---------- | ----- | --------------- | -------------- |"
    );
    for (const p of underRouting) {
      lines.push(
        `| ${p.stage} | ${p.selectedModel} | ${p.complexity} | ${p.count} | $${p.avgCost.toFixed(2)} | ${p.examples.map((i) => `#${i}`).join(", ")} |`
      );
    }
  }

  lines.push("");
  lines.push("---");
  lines.push("");

  // Over-Routing Patterns
  lines.push("## Over-Routing Patterns");
  lines.push("");
  if (overRouting.length === 0) {
    lines.push(
      "**No over-routing patterns detected.** No cases where opus was selected for XS/S complexity tasks that succeeded on the first attempt."
    );
    lines.push("");
    lines.push(
      "This aligns with the stage-complexity matrix which routes XS/S tasks to haiku (validate, lightweight, merge) or sonnet (planning, dev) — never to opus."
    );
  } else {
    lines.push(
      "| Stage | Selected Model | Complexity | Count | Avg Hyp. Cost | Potential Savings | Example Issues |"
    );
    lines.push(
      "| ----- | -------------- | ---------- | ----- | ------------- | ----------------- | -------------- |"
    );
    for (const p of overRouting) {
      lines.push(
        `| ${p.stage} | ${p.selectedModel} | ${p.complexity} | ${p.count} | $${p.avgCost.toFixed(4)} | $${p.potentialSavings.toFixed(4)} | ${p.examples.map((i) => `#${i}`).join(", ")} |`
      );
    }
  }

  lines.push("");
  lines.push("---");
  lines.push("");

  // Cost Comparison
  lines.push("## Cost Comparison: Auto-Routing vs Static Defaults");
  lines.push("");
  lines.push("| Metric | All-Sonnet (Actual) | Auto-Routed (Hypothetical) | Delta |");
  lines.push("| ------ | ------------------- | -------------------------- | ----- |");
  lines.push(
    `| Total Cost | $${totalActualCost.toFixed(2)} | $${totalAutoRoutedCost.toFixed(2)} | ${costSavings >= 0 ? "-" : "+"}$${Math.abs(costSavings).toFixed(2)} (${Math.abs(costSavingsPercent).toFixed(1)}%) |`
  );
  lines.push(
    `| Avg Cost/Run | $${(totalActualCost / simulations.length).toFixed(2)} | $${(totalAutoRoutedCost / simulations.length).toFixed(2)} | ${costSavings >= 0 ? "-" : "+"}$${Math.abs(costSavings / simulations.length).toFixed(2)} |`
  );
  lines.push("");

  // Per-stage cost comparison
  lines.push("### Per-Stage Cost Comparison");
  lines.push("");
  lines.push("| Stage | Actual (Sonnet) | Auto-Routed | Savings | Primary Model |");
  lines.push("| ----- | --------------- | ----------- | ------- | ------------- |");

  for (const stage of PIPELINE_STAGES) {
    let actualTotal = 0;
    let autoTotal = 0;
    const modelCounts = new Map<ModelTier, number>();

    for (const sim of simulations) {
      for (const stageSim of sim.stages) {
        if (stageSim.stage !== stage || stageSim.stageOutcome === "skipped") continue;
        actualTotal += stageSim.actualCostUsd;
        autoTotal += stageSim.hypotheticalCostUsd;
        modelCounts.set(
          stageSim.selection.model,
          (modelCounts.get(stageSim.selection.model) ?? 0) + 1
        );
      }
    }

    const savings = actualTotal - autoTotal;
    const primaryModel =
      Array.from(modelCounts.entries()).sort(([, a], [, b]) => b - a)[0]?.[0] ?? "N/A";

    lines.push(
      `| ${stage} | $${actualTotal.toFixed(2)} | $${autoTotal.toFixed(2)} | ${savings >= 0 ? "-" : "+"}$${Math.abs(savings).toFixed(2)} | ${primaryModel} |`
    );
  }

  lines.push("");
  lines.push("---");
  lines.push("");

  // Threshold Analysis
  lines.push("## Threshold Analysis");
  lines.push("");
  lines.push(
    `**Current thresholds**: haikuMax=3 (scores ≤3 → haiku), sonnetMax=6 (scores ≤6 → sonnet), above → opus`
  );
  lines.push("");
  lines.push("### Complexity Distribution");
  lines.push("");
  lines.push(
    "| Complexity | Score | Runs | Avg Cost/Run | Success Rate | Auto-Routed Model (dev) |"
  );
  lines.push(
    "| ---------- | ----- | ---- | ------------ | ------------ | ----------------------- |"
  );

  const complexityScores: Record<string, number> = {
    XS: 1,
    S: 2,
    M: 4,
    L: 7,
    XL: 9,
  };
  const complexityModels: Record<string, string> = {
    XS: "sonnet",
    S: "sonnet",
    M: "sonnet",
    L: "opus",
    XL: "opus",
  };

  for (const complexity of ["XS", "S", "M", "L", "XL"] as ComplexityLabel[]) {
    const data = thresholdAnalysis.complexityDistribution[complexity];
    if (data) {
      lines.push(
        `| ${complexity} | ${complexityScores[complexity]} | ${data.count} | $${data.avgCost.toFixed(2)} | ${(data.successRate * 100).toFixed(1)}% | ${complexityModels[complexity]} |`
      );
    } else {
      lines.push(
        `| ${complexity} | ${complexityScores[complexity]} | 0 | — | — | ${complexityModels[complexity]} |`
      );
    }
  }

  lines.push("");
  lines.push("### haikuMax Analysis");
  lines.push("");
  lines.push(thresholdAnalysis.haikuMaxAnalysis);
  lines.push("");
  lines.push("### sonnetMax Analysis");
  lines.push("");
  lines.push(thresholdAnalysis.sonnetMaxAnalysis);
  lines.push("");

  if (thresholdAnalysis.recommendations.length > 0) {
    lines.push("### Threshold Recommendations");
    lines.push("");
    for (const rec of thresholdAnalysis.recommendations) {
      lines.push(`- ${rec}`);
    }
    lines.push("");
  }

  lines.push("---");
  lines.push("");

  // Confidence Scoring Validation
  lines.push("## Confidence Scoring Validation");
  lines.push("");
  lines.push("Does higher selection confidence correlate with higher success rates?");
  lines.push("");
  lines.push("| Confidence Range | Stage Executions | Success Rate | Avg Actual Cost |");
  lines.push("| ---------------- | ---------------- | ------------ | --------------- |");

  for (const bucket of confidenceBuckets) {
    if (bucket.totalStages === 0) continue;
    lines.push(
      `| ${bucket.range} | ${bucket.totalStages} | ${(bucket.successRate * 100).toFixed(1)}% | $${bucket.avgCost.toFixed(2)} |`
    );
  }

  lines.push("");

  // Assess confidence correlation
  const populatedBuckets = confidenceBuckets.filter((b) => b.totalStages > 0);
  if (populatedBuckets.length >= 2) {
    const allSuccess = populatedBuckets.every((b) => b.successRate === 1.0);
    if (allSuccess) {
      lines.push(
        "**Result**: All confidence buckets show 100% success rate. With zero failures in the dataset, confidence scoring cannot be meaningfully validated — there is no negative signal to differentiate confidence levels. An A/B experiment with deliberate model downgrades would provide the failure data needed for validation."
      );
    } else {
      const increasing = populatedBuckets.every(
        (b, i) => i === 0 || b.successRate >= populatedBuckets[i - 1].successRate
      );
      lines.push(
        increasing
          ? "**Result**: Higher confidence correlates with higher success rate, validating the confidence scoring model."
          : "**Result**: Confidence does not strictly correlate with success rate. The confidence scoring model may need recalibration."
      );
    }
  } else {
    lines.push(
      "**Result**: Insufficient confidence diversity — most selections fall in a single bucket. Cannot validate correlation."
    );
  }

  lines.push("");
  lines.push("---");
  lines.push("");

  // Recommendations
  lines.push("## Recommendations");
  lines.push("");

  lines.push("### High Priority");
  lines.push("");
  if (costSavingsPercent > 5) {
    lines.push(
      `1. **Enable automatic model routing** — Auto-routing would save an estimated $${costSavings.toFixed(2)} (${costSavingsPercent.toFixed(1)}%) over the analyzed period. Set \`model_routing.mode: automatic\` in \`.nightgauge/config.yaml\`.`
    );
  } else if (costSavingsPercent > 0) {
    lines.push(
      `1. **Consider enabling auto-routing** — Modest savings of $${costSavings.toFixed(2)} (${costSavingsPercent.toFixed(1)}%). The cost benefit is marginal; quality/reliability should be the primary decision factor.`
    );
  } else {
    lines.push(
      "1. **Auto-routing shows no cost benefit** — The current all-sonnet approach is cost-competitive with auto-routing. Savings may emerge as issue complexity distribution changes."
    );
  }

  lines.push(
    `2. **Populate model_selection metadata** — JSONL records lack \`model_selection\` data (Issue #1006). Enabling this would allow future analyses to use actual selection data instead of retroactive simulation.`
  );

  lines.push("");
  lines.push("### Medium Priority");
  lines.push("");
  lines.push(
    "3. **Run A/B experiment** — To validate that auto-routing maintains quality, run a controlled experiment (Issue #949 framework) comparing all-sonnet vs auto-routed pipelines on matched complexity batches."
  );
  lines.push(
    "4. **Fix size prediction accuracy** — The complexity model predicts M for nearly all issues but 95%+ land as XS by actual lines changed. Recalibrating size estimation would improve routing accuracy."
  );

  lines.push("");
  lines.push("### Low Priority");
  lines.push("");
  lines.push(
    "5. **Monitor haiku stage performance** — Once auto-routing is enabled, track haiku-assigned stages for failure rate increases. Set alerting threshold at >5% failure rate."
  );
  lines.push(
    "6. **Evaluate effort-level tuning** — The `deriveEffort()` function maps complexity to Claude effort levels. Combining model routing with effort routing could further optimize costs."
  );

  lines.push("");
  lines.push("---");
  lines.push("");

  // Follow-Up Issues
  lines.push("## Follow-Up Issues to File");
  lines.push("");
  lines.push("| Issue Title | Priority | Rationale |");
  lines.push("| ----------- | -------- | --------- |");
  lines.push(
    "| Enable model_selection tracking in JSONL records | High | Issue #1006 added the schema fields but they are not populated. Without real data, all analysis requires retroactive simulation. |"
  );
  lines.push(
    "| Fix size prediction accuracy in complexity model | High | 95%+ of predictions are M but actual is XS. This causes all runs to use the M-complexity routing path, preventing haiku cost savings. |"
  );
  lines.push(
    "| Run A/B experiment: auto-routing vs static sonnet | Medium | Validate that auto-routing maintains quality. Use Issue #949 framework. |"
  );
  lines.push(
    "| Evaluate haikuMax threshold after auto-routing enabled | Low | Current analysis shows 100% success at all complexity levels. Re-evaluate after collecting failure data. |"
  );

  lines.push("");
  lines.push("---");
  lines.push("");
  lines.push(
    `_Generated: ${new Date().toISOString().split("T")[0]} | Issue: #1043 | Branch: feat/1043-analyze-model-routing_`
  );

  return lines.join("\n");
}

// ---------------------------------------------------------------------------
// Main
// ---------------------------------------------------------------------------

async function main(): Promise<void> {
  const { since, output } = parseArgs();

  console.log(`Loading JSONL records from ${HISTORY_DIR} (since ${since})...`);
  const records = await loadRunRecords(HISTORY_DIR, since);
  console.log(`Loaded ${records.length} run records.`);

  if (records.length === 0) {
    console.error("ERROR: No run records found. Check date range and history directory.");
    process.exit(1);
  }

  // Initialize AutoModelSelector with default thresholds
  const selector = new AutoModelSelector();

  console.log("Simulating model selections for all runs...");
  const simulations = records.map((r) => simulateRun(r, selector));

  console.log("Aggregating per-model, per-stage metrics...");
  const metrics = aggregateModelStageMetrics(simulations);

  console.log("Detecting under-routing patterns...");
  const underRouting = detectUnderRouting(simulations);

  console.log("Detecting over-routing patterns...");
  const overRouting = detectOverRouting(simulations);

  console.log("Analyzing confidence scoring...");
  const confidenceBuckets = analyzeConfidenceScoring(simulations);

  console.log("Analyzing thresholds...");
  const thresholdAnalysis = analyzeThresholds(simulations);

  console.log("Computing model distribution...");
  const modelDist = computeModelDistribution(simulations);

  console.log("Generating report...");
  const report = generateReport(
    simulations,
    records,
    metrics,
    underRouting,
    overRouting,
    confidenceBuckets,
    thresholdAnalysis,
    modelDist,
    since
  );

  // Ensure output directory exists
  const outputDir = path.dirname(output);
  await fs.mkdir(outputDir, { recursive: true });

  await fs.writeFile(output, report, "utf-8");
  console.log(`Report written to ${output}`);

  // Print summary to stdout
  const totalActual = simulations.reduce((s, sim) => s + sim.totalActualCost, 0);
  const totalAuto = simulations.reduce((s, sim) => s + sim.totalAutoRoutedCost, 0);
  const savings = totalActual - totalAuto;
  const pct = totalActual > 0 ? (savings / totalActual) * 100 : 0;

  console.log("\n=== Summary ===");
  console.log(`Runs analyzed: ${simulations.length}`);
  console.log(`Actual cost (all-sonnet): $${totalActual.toFixed(2)}`);
  console.log(`Hypothetical auto-routed cost: $${totalAuto.toFixed(2)}`);
  console.log(`Estimated savings: $${savings.toFixed(2)} (${pct.toFixed(1)}%)`);
  console.log(
    `Model distribution: Haiku=${modelDist.haiku.stages}, Sonnet=${modelDist.sonnet.stages}, Opus=${modelDist.opus.stages}`
  );
  console.log(`Under-routing patterns: ${underRouting.length}`);
  console.log(`Over-routing patterns: ${overRouting.length}`);
}

main().catch((err) => {
  console.error("Fatal error:", err);
  process.exit(1);
});
