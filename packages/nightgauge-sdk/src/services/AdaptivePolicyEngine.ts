/**
 * AdaptivePolicyEngine - Converts health and model analysis into typed policy decisions
 *
 * Consumes HealthAnalysisResult and ModelRoutingAnalysis, produces PolicyDecision[]
 * with confidence scoring, evidence chains, magnitude guardrails, and rollback triggers.
 *
 * This is a DETERMINISTIC operation — same inputs always produce the same decisions.
 * The engine is a pure producer with no side effects.
 *
 * @see Issue #1387 - Adaptive Policy Engine
 * @see Issue #1386 - Self-Improving Pipeline Engine (parent epic)
 */

import { z } from "zod";
import type {
  HealthAnalysisResult,
  Finding,
  Confidence,
  RecurringFinding,
} from "../analysis/health/types.js";
import type { ModelRoutingAnalysis } from "../analysis/types.js";

// ── Zod Schemas ──────────────────────────────────────────────────

export const PolicyDecisionTypeSchema = z.enum([
  "model-threshold-adjust",
  "budget-adjust",
  "escalation-policy-change",
  "routing-override",
  "retry-policy-adjust",
  "timeout-adjust",
]);

export const PolicyDecisionSchema = z.object({
  type: PolicyDecisionTypeSchema,
  field: z.string().min(1),
  current_value: z.union([z.number(), z.string(), z.boolean()]),
  proposed_value: z.union([z.number(), z.string(), z.boolean()]),
  confidence: z.enum(["high", "medium", "low"]),
  sample_size: z.number().int().min(0),
  evidence: z.array(z.string()),
  rollback_trigger: z.string().min(1),
});

export const PolicyEngineResultSchema = z.object({
  decisions: z.array(PolicyDecisionSchema),
  analyzed_at: z.string().datetime(),
  inputs_summary: z.object({
    health_overall_score: z.number(),
    health_overall_status: z.string(),
    model_records_analyzed: z.number(),
  }),
  guardrails_applied: z.number().int().min(0),
});

// ── Inferred Types ───────────────────────────────────────────────

export type PolicyDecisionType = z.infer<typeof PolicyDecisionTypeSchema>;
export type PolicyDecision = z.infer<typeof PolicyDecisionSchema>;
export type PolicyEngineResult = z.infer<typeof PolicyEngineResultSchema>;

// ── Stage Execution Stats ────────────────────────────────────────

export interface StageRetryStats {
  stage: string;
  totalRuns: number;
  runsWithRetries: number;
  retryRate: number; // runsWithRetries / totalRuns
  totalRetryCount: number; // sum of auto_retry_count + manual_retry_count
}

export interface StageDurationStats {
  stage: string;
  totalRuns: number;
  p95DurationMs: number;
  maxDurationMs: number;
  medianDurationMs: number;
}

export interface StageExecutionStats {
  retryStats: StageRetryStats[];
  durationStats: StageDurationStats[];
}

// ── Configuration ────────────────────────────────────────────────

export interface PolicyEngineConfig {
  minSampleSize: number;
  maxThresholdChange: number;
  maxBudgetChangePercent: number;
}

// ── Guardrail Constants ──────────────────────────────────────────

const GUARDRAILS = {
  MINIMUM_SAMPLE_SIZE: 10,
  MAX_THRESHOLD_CHANGE_PER_CYCLE: 1.0,
  MAX_BUDGET_CHANGE_PERCENT: 0.15,
} as const;

// ── Retry Policy Guardrails ──────────────────────────────────────

const RETRY_POLICY_GUARDRAILS = {
  MIN_RETRIES: 1,
  MAX_RETRIES: 5,
  DEFAULT_RETRIES: 2,
  MIN_RUNS_FOR_INCREASE: 20,
  MIN_RUNS_FOR_DECREASE: 50,
  RETRY_RATE_INCREASE_THRESHOLD: 0.1, // >10% retry rate triggers increase
  RETRY_RATE_DECREASE_THRESHOLD: 0.0, // 0% retry rate enables decrease
} as const;

// ── Timeout Guardrails ──────────────────────────────────────────

const TIMEOUT_GUARDRAILS = {
  MIN_TIMEOUT_MS: 60_000, // 60 seconds
  MAX_TIMEOUT_MS: 1_800_000, // 30 minutes
  MIN_RUNS_FOR_ADJUSTMENT: 30,
  P95_MULTIPLIER: 1.5,
  MAX_HEADROOM_MULTIPLIER: 1.5, // 50% headroom above observed max
  CHANGE_THRESHOLD: 0.2, // Only adjust when >20% change
} as const;

// ── Rebalancing Constants ────────────────────────────────────────

const REBALANCING = {
  MIN_STAGE_RUNS: 3, // minimum unique runs per stage before eligible
  MAX_SHIFT: 0.2, // max 20% change per stage per cycle
  CONCENTRATION_THRESHOLD: 0.6, // >60% share triggers rebalancing
  XL_COMPLEXITY_SCORE: 8, // complexityScore >= 8 = XL
} as const;

// ── Engine ───────────────────────────────────────────────────────

export class AdaptivePolicyEngine {
  private readonly minSampleSize: number;
  private readonly maxThresholdChange: number;
  private readonly maxBudgetChangePercent: number;

  constructor(config?: Partial<PolicyEngineConfig>) {
    this.minSampleSize = config?.minSampleSize ?? GUARDRAILS.MINIMUM_SAMPLE_SIZE;
    this.maxThresholdChange =
      config?.maxThresholdChange ?? GUARDRAILS.MAX_THRESHOLD_CHANGE_PER_CYCLE;
    this.maxBudgetChangePercent =
      config?.maxBudgetChangePercent ?? GUARDRAILS.MAX_BUDGET_CHANGE_PERCENT;
  }

  /**
   * Evaluate health and model analysis results, producing typed policy decisions.
   *
   * Calls five internal decision generators sequentially:
   * 1. Model threshold adjustments (from auto-selection analysis)
   * 2. Budget adjustments (from cost-health dimension)
   * 3. Escalation policy changes (from reliability findings)
   * 4. Routing overrides (from model routing recommendations)
   * 5. Recurring finding decisions (from RecommendationTracker, Issue #1397)
   *
   * Results are Zod-validated before returning.
   */
  evaluate(
    healthResult: HealthAnalysisResult,
    modelResult: ModelRoutingAnalysis,
    recurringFindings?: RecurringFinding[],
    stageStats?: StageExecutionStats
  ): PolicyEngineResult {
    let guardrailsApplied = 0;

    const thresholdDecisions = this.generateModelThresholdDecisions(modelResult);
    guardrailsApplied += thresholdDecisions.guardrailsApplied;

    const budgetDecisions = this.generateBudgetDecisions(healthResult);
    guardrailsApplied += budgetDecisions.guardrailsApplied;

    const escalationDecisions = this.generateEscalationDecisions(healthResult);

    const routingDecisions = this.generateRoutingOverrideDecisions(modelResult);

    const rebalancingDecisions = this.generateBudgetRebalancingDecisions(healthResult);

    const recurringDecisions =
      recurringFindings && recurringFindings.length > 0
        ? this.generateRecurringFindingDecisions(recurringFindings)
        : [];

    const retryPolicyDecisions = stageStats
      ? this.generateRetryPolicyDecisions(stageStats.retryStats)
      : [];

    const timeoutDecisions = stageStats
      ? this.generateTimeoutDecisions(stageStats.durationStats)
      : [];

    const decisions = [
      ...thresholdDecisions.decisions,
      ...budgetDecisions.decisions,
      ...rebalancingDecisions,
      ...escalationDecisions,
      ...routingDecisions,
      ...recurringDecisions,
      ...retryPolicyDecisions,
      ...timeoutDecisions,
    ];

    const result: PolicyEngineResult = {
      decisions,
      analyzed_at: new Date().toISOString(),
      inputs_summary: {
        health_overall_score: healthResult.overallScore,
        health_overall_status: healthResult.overallStatus,
        model_records_analyzed: modelResult.recordsAnalyzed,
      },
      guardrails_applied: guardrailsApplied,
    };

    return PolicyEngineResultSchema.parse(result);
  }

  /**
   * Compute a per-run budget multiplier from an efficiency trend.
   *
   * - Improving efficiency → reduce budgets (tasks complete under budget)
   * - Degrading efficiency → increase budgets (tasks need more room)
   * - No data → neutral (multiplier = 1.0)
   *
   * Adjustment is capped at ±15% per cycle.
   *
   * @see Issue #1392
   */
  // ── Decision Generators ──────────────────────────────────────

  private generateModelThresholdDecisions(modelResult: ModelRoutingAnalysis): {
    decisions: PolicyDecision[];
    guardrailsApplied: number;
  } {
    const decisions: PolicyDecision[] = [];
    let guardrailsApplied = 0;
    const recommendations = modelResult.autoSelectionAnalysis?.thresholdRecommendations ?? [];

    for (const rec of recommendations) {
      if (rec.evidence.sampleSize < this.minSampleSize) {
        continue;
      }

      let proposedValue = rec.suggestedValue;
      const changeMagnitude = Math.abs(proposedValue - rec.currentValue);
      if (changeMagnitude > this.maxThresholdChange) {
        const direction = proposedValue > rec.currentValue ? 1 : -1;
        proposedValue = rec.currentValue + direction * this.maxThresholdChange;
        guardrailsApplied++;
      }

      decisions.push({
        type: "model-threshold-adjust",
        field: rec.field,
        current_value: rec.currentValue,
        proposed_value: proposedValue,
        confidence: computeConfidence(rec.evidence.sampleSize, rec.confidence),
        sample_size: rec.evidence.sampleSize,
        evidence: [
          `Threshold "${rec.field}": current=${rec.currentValue}, suggested=${rec.suggestedValue}`,
          `Rationale: ${rec.rationale}`,
          `Affected stages: ${rec.evidence.affectedStages.join(", ")}`,
        ],
        rollback_trigger: `Revert if success rate drops >5% within 10 pipeline runs after threshold change on "${rec.field}"`,
      });
    }

    return { decisions, guardrailsApplied };
  }

  private generateBudgetDecisions(healthResult: HealthAnalysisResult): {
    decisions: PolicyDecision[];
    guardrailsApplied: number;
  } {
    const decisions: PolicyDecision[] = [];
    let guardrailsApplied = 0;
    const costHealth = healthResult.dimensions["cost-health"];

    if (!costHealth || !costHealth.hasEnoughData || costHealth.score >= 50) {
      return { decisions, guardrailsApplied };
    }

    // Cost health is poor/critical — compute reduction proportional to overspend
    const overspendFraction = (50 - costHealth.score) / 100;
    let reductionPercent = Math.min(overspendFraction, this.maxBudgetChangePercent);
    if (overspendFraction > this.maxBudgetChangePercent) {
      reductionPercent = this.maxBudgetChangePercent;
      guardrailsApplied++;
    }

    const avgCost = costHealth.metrics["avgCostPerRun"] ?? 0;
    const proposedCeiling = avgCost * (1 - reductionPercent);

    decisions.push({
      type: "budget-adjust",
      field: "pipeline.budget_ceiling_usd",
      current_value: avgCost,
      proposed_value: Math.round(proposedCeiling * 100) / 100,
      confidence: computeConfidence(costHealth.sampleSize, "medium"),
      sample_size: costHealth.sampleSize,
      evidence: [
        `Cost-health score: ${costHealth.score}/100 (${costHealth.status})`,
        `Average cost per run: $${avgCost.toFixed(2)}`,
        `Proposed reduction: ${(reductionPercent * 100).toFixed(1)}%`,
      ],
      rollback_trigger: `Revert if pipeline failure rate exceeds 20% after budget adjustment`,
    });

    return { decisions, guardrailsApplied };
  }

  private generateEscalationDecisions(healthResult: HealthAnalysisResult): PolicyDecision[] {
    const decisions: PolicyDecision[] = [];
    const reliability = healthResult.dimensions["reliability"];

    if (!reliability || !reliability.hasEnoughData) {
      return decisions;
    }

    const criticalFindings = reliability.findings.filter(
      (f: Finding) =>
        (f.severity === "critical" || f.severity === "high") && f.confidence === "high"
    );

    for (const finding of criticalFindings) {
      if (reliability.sampleSize < this.minSampleSize) {
        continue;
      }

      decisions.push({
        type: "escalation-policy-change",
        field: `reliability.${finding.id}`,
        current_value: finding.title,
        proposed_value: finding.recommendation,
        confidence: computeConfidence(reliability.sampleSize, finding.confidence),
        sample_size: reliability.sampleSize,
        evidence: [
          `Finding: ${finding.title} (${finding.severity})`,
          `Impact: ${finding.impact}`,
          `Dimension score: ${reliability.score}/100`,
        ],
        rollback_trigger: `Revert if reliability score does not improve by 10+ points within 20 pipeline runs`,
      });
    }

    return decisions;
  }

  private generateRoutingOverrideDecisions(modelResult: ModelRoutingAnalysis): PolicyDecision[] {
    const decisions: PolicyDecision[] = [];

    for (const rec of modelResult.recommendations) {
      if (rec.confidence !== "high") {
        continue;
      }

      const totalSamples = Object.values(rec.evidence.sampleSizes).reduce((sum, n) => sum + n, 0);
      if (totalSamples < this.minSampleSize) {
        continue;
      }

      decisions.push({
        type: "routing-override",
        field: `model_routing.${rec.stage}`,
        current_value: rec.currentModel,
        proposed_value: rec.suggestedModel,
        confidence: computeConfidence(totalSamples, rec.confidence),
        sample_size: totalSamples,
        evidence: [
          `Stage "${rec.stage}": ${rec.currentModel} → ${rec.suggestedModel} (${rec.type})`,
          `Success rate: ${(rec.evidence.currentSuccessRate * 100).toFixed(1)}% → ${(rec.evidence.suggestedSuccessRate * 100).toFixed(1)}%`,
          `Effective cost: $${rec.evidence.currentEffectiveCost.toFixed(2)} → $${rec.evidence.suggestedEffectiveCost.toFixed(2)}`,
          `Estimated savings: $${rec.estimatedSavingsUsd.toFixed(2)}/run`,
        ],
        rollback_trigger: `Revert routing for "${rec.stage}" if success rate drops below ${(rec.evidence.currentSuccessRate * 100).toFixed(0)}% within 10 runs`,
      });
    }

    return decisions;
  }

  /**
   * Generate policy decisions for recurring findings (Issue #1397).
   *
   * Only acts on findings with 3+ occurrences that are still open.
   * Maps dimension to decision type:
   *   model-routing  → model-threshold-adjust (haiku_max threshold)
   *   cost-health    → budget-adjust (pipeline.budget_ceiling_usd)
   *   reliability    → escalation-policy-change (occurrence counter)
   */
  private generateRecurringFindingDecisions(
    recurringFindings: RecurringFinding[]
  ): PolicyDecision[] {
    const decisions: PolicyDecision[] = [];

    for (const finding of recurringFindings) {
      if (finding.occurrence_count < 3 || finding.all_closed) continue;

      const evidence = [
        `Recurring finding: "${finding.finding_title}"`,
        `Occurrences: ${finding.occurrence_count}`,
        `First seen: ${finding.first_seen}`,
        `Last seen: ${finding.last_seen}`,
        `Dimension: ${finding.dimension}`,
        `Linked issues: ${finding.issue_numbers.join(", ") || "none"}`,
      ];

      switch (finding.dimension) {
        case "model-routing":
          decisions.push({
            type: "model-threshold-adjust",
            field: "complexity_thresholds.haiku_max",
            current_value: 3,
            proposed_value: 2,
            confidence: "medium",
            sample_size: finding.occurrence_count,
            evidence,
            rollback_trigger: `Revert if model cost increases >20% within 10 runs after threshold adjustment for recurring "${finding.finding_title}"`,
          });
          break;

        case "cost-health":
          decisions.push({
            type: "budget-adjust",
            field: "pipeline.budget_ceiling_usd",
            current_value: 1.0,
            proposed_value: 0.9,
            confidence: "medium",
            sample_size: finding.occurrence_count,
            evidence,
            rollback_trigger: `Revert if pipeline failure rate exceeds 20% within 10 runs after budget reduction for recurring "${finding.finding_title}"`,
          });
          break;

        case "reliability": {
          const safeKey = finding.finding_title
            .toLowerCase()
            .replace(/[^a-z0-9]+/g, "_")
            .slice(0, 50);
          decisions.push({
            type: "escalation-policy-change",
            field: `reliability.recurring.${safeKey}`,
            current_value: finding.occurrence_count - 1,
            proposed_value: finding.occurrence_count,
            confidence: "medium",
            sample_size: finding.occurrence_count,
            evidence,
            rollback_trigger: `Revert if reliability score improves by 15+ points within 15 runs after escalation for recurring "${finding.finding_title}"`,
          });
          break;
        }
      }
    }

    return decisions;
  }

  /**
   * Generate retry policy adjustment decisions from stage retry statistics.
   *
   * - Increase max retries when retry rate > 10% over 20+ runs
   * - Decrease max retries when retry rate = 0% over 50+ runs
   * - Clamp to [1, 5] guardrails
   *
   * @see Issue #1573 - Retry policy auto-tuning
   */
  private generateRetryPolicyDecisions(retryStats: StageRetryStats[]): PolicyDecision[] {
    const decisions: PolicyDecision[] = [];

    for (const stats of retryStats) {
      const currentRetries = RETRY_POLICY_GUARDRAILS.DEFAULT_RETRIES;

      // Check for increase: retry rate > 10% with 20+ runs
      if (
        stats.totalRuns >= RETRY_POLICY_GUARDRAILS.MIN_RUNS_FOR_INCREASE &&
        stats.retryRate > RETRY_POLICY_GUARDRAILS.RETRY_RATE_INCREASE_THRESHOLD
      ) {
        const proposed = Math.min(currentRetries + 1, RETRY_POLICY_GUARDRAILS.MAX_RETRIES);
        if (proposed !== currentRetries) {
          decisions.push({
            type: "retry-policy-adjust",
            field: `pipeline.retry_limits.${stats.stage}`,
            current_value: currentRetries,
            proposed_value: proposed,
            confidence: computeConfidence(stats.totalRuns, "medium"),
            sample_size: stats.totalRuns,
            evidence: [
              `Stage "${stats.stage}": retry rate ${(stats.retryRate * 100).toFixed(1)}% over ${stats.totalRuns} runs (threshold: >10%)`,
              `Runs with retries: ${stats.runsWithRetries}/${stats.totalRuns}`,
              `Total retry count: ${stats.totalRetryCount}`,
              `Proposing increase: ${currentRetries} → ${proposed} max retries`,
            ],
            rollback_trigger: `Revert if retry rate increases above 15% over next 20 runs for "${stats.stage}"`,
          });
        }
        continue;
      }

      // Check for decrease: retry rate = 0% with 50+ runs
      if (
        stats.totalRuns >= RETRY_POLICY_GUARDRAILS.MIN_RUNS_FOR_DECREASE &&
        stats.retryRate === RETRY_POLICY_GUARDRAILS.RETRY_RATE_DECREASE_THRESHOLD
      ) {
        const proposed = Math.max(currentRetries - 1, RETRY_POLICY_GUARDRAILS.MIN_RETRIES);
        if (proposed !== currentRetries) {
          decisions.push({
            type: "retry-policy-adjust",
            field: `pipeline.retry_limits.${stats.stage}`,
            current_value: currentRetries,
            proposed_value: proposed,
            confidence: computeConfidence(stats.totalRuns, "medium"),
            sample_size: stats.totalRuns,
            evidence: [
              `Stage "${stats.stage}": 0% retry rate over ${stats.totalRuns} runs`,
              `No retries needed in ${stats.totalRuns} consecutive runs`,
              `Proposing decrease: ${currentRetries} → ${proposed} max retries`,
            ],
            rollback_trigger: `Revert if retry rate increases above 5% over next 20 runs for "${stats.stage}"`,
          });
        }
      }
    }

    return decisions;
  }

  /**
   * Generate stage timeout adjustment decisions from duration statistics.
   *
   * - Timeout = max(P95 × 1.5, maxDuration × 1.5) clamped to [60s, 1800s]
   * - Only adjusts when proposed differs from current by >20%
   * - Requires 30+ runs for P95 confidence
   *
   * @see Issue #1573 - Stage timeout auto-tuning
   */
  private generateTimeoutDecisions(durationStats: StageDurationStats[]): PolicyDecision[] {
    const decisions: PolicyDecision[] = [];

    for (const stats of durationStats) {
      if (stats.totalRuns < TIMEOUT_GUARDRAILS.MIN_RUNS_FOR_ADJUSTMENT) {
        continue;
      }

      // Compute proposed: P95 × 1.5
      const p95Based = stats.p95DurationMs * TIMEOUT_GUARDRAILS.P95_MULTIPLIER;

      // Ensure 50% headroom above observed max
      const maxBased = stats.maxDurationMs * TIMEOUT_GUARDRAILS.MAX_HEADROOM_MULTIPLIER;

      // Take the larger of the two, then clamp
      const rawProposed = Math.max(p95Based, maxBased);
      const proposed = Math.round(
        Math.max(
          TIMEOUT_GUARDRAILS.MIN_TIMEOUT_MS,
          Math.min(TIMEOUT_GUARDRAILS.MAX_TIMEOUT_MS, rawProposed)
        )
      );

      // Use a reasonable default current timeout for comparison
      const currentTimeout = TIMEOUT_GUARDRAILS.MAX_TIMEOUT_MS; // default: 30 min

      // Skip if change is within 20% threshold
      const changeRatio = Math.abs(proposed - currentTimeout) / currentTimeout;
      if (changeRatio <= TIMEOUT_GUARDRAILS.CHANGE_THRESHOLD) {
        continue;
      }

      decisions.push({
        type: "timeout-adjust",
        field: `pipeline.stage_timeouts.${stats.stage}`,
        current_value: currentTimeout,
        proposed_value: proposed,
        confidence: computeConfidence(stats.totalRuns, "medium"),
        sample_size: stats.totalRuns,
        evidence: [
          `Stage "${stats.stage}": P95 duration ${stats.p95DurationMs}ms over ${stats.totalRuns} runs`,
          `Max observed: ${stats.maxDurationMs}ms, Median: ${stats.medianDurationMs}ms`,
          `Proposed timeout: ${proposed}ms (P95×1.5=${Math.round(p95Based)}ms, Max×1.5=${Math.round(maxBased)}ms)`,
          `Change from current: ${currentTimeout}ms → ${proposed}ms (${(changeRatio * 100).toFixed(1)}%)`,
        ],
        rollback_trigger: `Revert if stage timeout failures increase over next 10 runs for "${stats.stage}"`,
      });
    }

    return decisions;
  }

  private generateBudgetRebalancingDecisions(healthResult: HealthAnalysisResult): PolicyDecision[] {
    const costHealth = healthResult.dimensions["cost-health"];
    if (!costHealth?.hasEnoughData) return [];

    const concentrationFinding = costHealth.findings.find(
      (f: Finding) => f.title === "High Stage Cost Concentration"
    );
    if (!concentrationFinding) return [];

    const ev = concentrationFinding.evidence as {
      dominantStage: string;
      stageShare: number;
      allStageShares: Record<string, number>;
      stageRunCounts: Record<string, number>;
      stageHasXL: Record<string, boolean>;
    };

    const { dominantStage, allStageShares, stageRunCounts, stageHasXL } = ev;

    // XL guard: if dominant stage is feature-dev and had XL runs, skip
    if (dominantStage === "feature-dev" && stageHasXL["feature-dev"] === true) {
      return [];
    }

    // Collect eligible stages (>= MIN_STAGE_RUNS and has cost data)
    const eligibleStages = Object.entries(allStageShares).filter(
      ([stage]) => (stageRunCounts[stage] ?? 0) >= REBALANCING.MIN_STAGE_RUNS
    );

    if (eligibleStages.length < 2) return [];

    const dominantRunCount = stageRunCounts[dominantStage] ?? 0;
    if (dominantRunCount < REBALANCING.MIN_STAGE_RUNS) return [];

    // allStageShares is in percentages (e.g., 75.2)
    const dominantFraction = (allStageShares[dominantStage] ?? 0) / 100;
    if (dominantFraction <= REBALANCING.CONCENTRATION_THRESHOLD) return [];

    const numEligible = eligibleStages.length;
    const targetShare = 1 / numEligible;
    const concentrationExcess = dominantFraction - targetShare;

    const proposedReduction = Math.min(REBALANCING.MAX_SHIFT, concentrationExcess);
    const dominantMultiplier = parseFloat((1.0 - proposedReduction).toFixed(4));

    const nonDominantEligible = eligibleStages.filter(([s]) => s !== dominantStage);
    const increasePerStage =
      nonDominantEligible.length > 0
        ? Math.min(REBALANCING.MAX_SHIFT, proposedReduction / nonDominantEligible.length)
        : 0;

    const decisions: PolicyDecision[] = [];

    decisions.push({
      type: "budget-adjust",
      field: `stage_budget_multipliers.${dominantStage}`,
      current_value: 1.0,
      proposed_value: dominantMultiplier,
      confidence: computeConfidence(dominantRunCount, "medium"),
      sample_size: dominantRunCount,
      evidence: [
        `Stage "${dominantStage}" has ${(dominantFraction * 100).toFixed(1)}% of total cost (threshold: ${(REBALANCING.CONCENTRATION_THRESHOLD * 100).toFixed(0)}%)`,
        `Reducing budget by ${(proposedReduction * 100).toFixed(1)}% (max shift: ${(REBALANCING.MAX_SHIFT * 100).toFixed(0)}%)`,
        `All stage shares: ${JSON.stringify(allStageShares)}`,
        `Run counts: ${JSON.stringify(stageRunCounts)}`,
      ],
      rollback_trigger: `Revert if "${dominantStage}" failure rate increases by more than 10% within 10 pipeline runs`,
    });

    for (const [stage] of nonDominantEligible) {
      const multiplier = parseFloat((1.0 + increasePerStage).toFixed(4));
      decisions.push({
        type: "budget-adjust",
        field: `stage_budget_multipliers.${stage}`,
        current_value: 1.0,
        proposed_value: multiplier,
        confidence: computeConfidence(stageRunCounts[stage] ?? 0, "medium"),
        sample_size: stageRunCounts[stage] ?? 0,
        evidence: [
          `Redistributing ${(proposedReduction * 100).toFixed(1)}% savings from "${dominantStage}" to ${nonDominantEligible.length} under-budgeted stages`,
          `Increasing "${stage}" budget by ${(increasePerStage * 100).toFixed(1)}%`,
          `Stage "${stage}" current share: ${allStageShares[stage]?.toFixed(1) ?? "N/A"}%`,
        ],
        rollback_trigger: `Revert if overall pipeline success rate drops below 80% within 15 runs after rebalancing`,
      });
    }

    return decisions;
  }
}

// ── Helpers ──────────────────────────────────────────────────────

function computeConfidence(
  sampleSize: number,
  sourceConfidence: Confidence | "low" | "medium" | "high"
): "high" | "medium" | "low" {
  if (sampleSize >= 50 && sourceConfidence === "high") return "high";
  if (sampleSize >= 20 || sourceConfidence === "medium") return "medium";
  return "low";
}
