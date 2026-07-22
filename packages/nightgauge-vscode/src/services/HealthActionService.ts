/**
 * HealthActionService - Automatic interventions based on health score trends
 *
 * Evaluates the latest health score and triggers appropriate actions:
 * - Warning (score < 70): Log warning to output window
 * - Critical (score < 50): Suggest running pipeline audit, warn about routing
 * - Trend declining: Flag in post-pipeline summary
 *
 * All actions are informational/advisory — never blocks pipeline execution.
 * Non-critical: all operations wrapped in try/catch.
 *
 * @see Issue #1045 - Health-gated tier actions and learning system monitoring
 */

import { HealthScoreHistoryReader } from "../utils/healthScoreHistory";
import { IpcClient } from "./IpcClient";
import type { Logger } from "../utils/logger";
import type { PipelinePolicyOverrides, HealthPolicyTier } from "./PipelinePolicyOverrides";

/** Default thresholds */
const DEFAULT_WARNING_THRESHOLD = 70;
const DEFAULT_CRITICAL_THRESHOLD = 50;
const DEFAULT_EMERGENCY_THRESHOLD = 30;

/**
 * A single health-triggered action
 */
export interface HealthAction {
  level: "info" | "warning" | "critical";
  message: string;
  suggestion?: string;
  autoApplied: boolean;
}

/**
 * Complete health evaluation result
 */
export interface HealthEvaluation {
  score: number;
  status: string;
  trend: "improving" | "stable" | "declining";
  actions: HealthAction[];
}

export class HealthActionService {
  /**
   * Evaluate current health state and compute actions.
   *
   * Reads the latest health score snapshots, computes trend,
   * and generates appropriate health actions.
   *
   * @param workspaceRoot - Absolute path to repo root
   * @param logger - Logger instance for output
   * @returns Health evaluation with actions, or null if no data
   */
  static async evaluate(workspaceRoot: string, logger: Logger): Promise<HealthEvaluation | null> {
    try {
      const snapshots = await HealthScoreHistoryReader.readAll(workspaceRoot);

      if (snapshots.length === 0) {
        return {
          score: 100,
          status: "excellent",
          trend: "stable",
          actions: [],
        };
      }

      // Get latest score
      const sorted = [...snapshots].sort(
        (a, b) => new Date(b.timestamp).getTime() - new Date(a.timestamp).getTime()
      );
      const latest = sorted[0];
      const score = latest.score;
      const status = latest.status;

      // Compute trend: compare recent 3-run average to prior 7-run average
      const trend = this.computeTrend(sorted);

      // Read thresholds from config via IPC
      const { warningThreshold, criticalThreshold, actionsEnabled } =
        await this.getHealthThresholds();

      if (!actionsEnabled) {
        return { score, status, trend, actions: [] };
      }

      const actions: HealthAction[] = [];

      // Score-based actions
      if (score < criticalThreshold) {
        actions.push({
          level: "critical",
          message: `Critical health score: ${score}. Auto-routing effectiveness may be degraded.`,
          suggestion: "Consider running /nightgauge:backlog-groom to audit the pipeline.",
          autoApplied: false,
        });
      } else if (score < warningThreshold) {
        actions.push({
          level: "warning",
          message: `Health declining (score: ${score}). Consider running pipeline audit.`,
          suggestion: "Review recent failure patterns and model routing effectiveness.",
          autoApplied: false,
        });
      } else {
        actions.push({
          level: "info",
          message: `Pipeline health: ${score} (${status})`,
          autoApplied: false,
        });
      }

      // Trend-based actions
      if (trend === "declining") {
        const recentAvg = this.getAverage(sorted.slice(0, 3));
        const priorAvg = this.getAverage(sorted.slice(3, 10));
        const drop = priorAvg > 0 ? Math.round(((priorAvg - recentAvg) / priorAvg) * 100) : 0;
        actions.push({
          level: "warning",
          message: `Health trend declining over last 3 runs (${drop}% drop)`,
          autoApplied: false,
        });
      } else if (trend === "improving") {
        const recentAvg = this.getAverage(sorted.slice(0, 3));
        const priorAvg = this.getAverage(sorted.slice(3, 10));
        const gain = priorAvg > 0 ? Math.round(((recentAvg - priorAvg) / priorAvg) * 100) : 0;
        if (gain > 0) {
          actions.push({
            level: "info",
            message: `Health trend improving (+${gain}%)`,
            autoApplied: false,
          });
        }
      }

      return { score, status, trend, actions };
    } catch (err) {
      logger.warn("Health evaluation failed", {
        err: err instanceof Error ? err.message : String(err),
      });
      return null;
    }
  }

  /**
   * Evaluate health and compute policy overrides for the current pipeline run.
   *
   * Returns both the standard evaluation (actions/trend) and a
   * PipelinePolicyOverrides object that the HeadlessOrchestrator applies
   * as temporary per-run adjustments.
   *
   * Policy tiers:
   * - none (score >= warning): No overrides
   * - warning (score < 70): Retry budget +1
   * - critical (score < 50): Retry budget +2, escalate all stages
   * - emergency (score < 30): Retry budget +2, escalate, pause auto-routing
   *
   * @see Issue #1395 - Health-gated pipeline policies
   */
  static async evaluateWithPolicies(
    workspaceRoot: string,
    logger: Logger
  ): Promise<{
    evaluation: HealthEvaluation;
    policies: PipelinePolicyOverrides;
  }> {
    const evaluation = await this.evaluate(workspaceRoot, logger);

    // Default: no policies
    const noPolicies: PipelinePolicyOverrides = {
      tier: "none",
      retryBudgetIncrease: 0,
      escalateAllStages: false,
      pauseAutoRouting: false,
      reasons: [],
      score: evaluation?.score ?? 100,
      timestamp: new Date().toISOString(),
    };

    if (!evaluation) {
      return {
        evaluation: {
          score: 100,
          status: "excellent",
          trend: "stable",
          actions: [],
        },
        policies: noPolicies,
      };
    }

    // Read all thresholds and flags via IPC
    const { policiesEnabled, emergencyThreshold, warningThreshold, criticalThreshold } =
      await this.getHealthThresholds();

    if (!policiesEnabled) {
      return {
        evaluation,
        policies: { ...noPolicies, score: evaluation.score },
      };
    }

    const score = evaluation.score;
    let tier: HealthPolicyTier = "none";
    let retryBudgetIncrease = 0;
    let escalateAllStages = false;
    let pauseAutoRouting = false;
    const reasons: string[] = [];
    const policyActions: HealthAction[] = [];

    if (score < emergencyThreshold) {
      tier = "emergency";
      retryBudgetIncrease = 2;
      escalateAllStages = true;
      pauseAutoRouting = true;
      reasons.push(
        `Health score ${score} is below emergency threshold (${emergencyThreshold}) — retry budget +2, model escalation active, auto-routing paused`
      );
      policyActions.push(
        {
          level: "critical",
          message: `Emergency policy: retry budget increased by 2`,
          autoApplied: true,
        },
        {
          level: "critical",
          message: `Emergency policy: all stages escalated by one model tier`,
          autoApplied: true,
        },
        {
          level: "critical",
          message: `Emergency policy: auto-routing paused (using default model)`,
          autoApplied: true,
        }
      );
    } else if (score < criticalThreshold) {
      tier = "critical";
      retryBudgetIncrease = 2;
      escalateAllStages = true;
      reasons.push(
        `Health score ${score} is below critical threshold (${criticalThreshold}) — retry budget +2, model escalation active`
      );
      policyActions.push(
        {
          level: "critical",
          message: `Critical policy: retry budget increased by 2`,
          autoApplied: true,
        },
        {
          level: "critical",
          message: `Critical policy: all stages escalated by one model tier`,
          autoApplied: true,
        }
      );
    } else if (score < warningThreshold) {
      tier = "warning";
      retryBudgetIncrease = 1;
      reasons.push(
        `Health score ${score} is below warning threshold (${warningThreshold}) — retry budget +1`
      );
      policyActions.push({
        level: "warning",
        message: `Warning policy: retry budget increased by 1`,
        autoApplied: true,
      });
    }

    // Append policy actions to evaluation
    const augmentedEvaluation: HealthEvaluation = {
      ...evaluation,
      actions: [...evaluation.actions, ...policyActions],
    };

    const policies: PipelinePolicyOverrides = {
      tier,
      retryBudgetIncrease,
      escalateAllStages,
      pauseAutoRouting,
      reasons,
      score,
      timestamp: new Date().toISOString(),
    };

    return { evaluation: augmentedEvaluation, policies };
  }

  /**
   * Warn-once tracking for threshold-IPC fallbacks: the fallback runs on
   * every health evaluation and its cause (binary missing / IPC not
   * connected) rarely changes between calls — one line per distinct cause is
   * the signal, per-call repetition is noise. Unbounded growth is impossible
   * in practice (a handful of distinct error strings per session), and a NEW
   * cause still surfaces immediately.
   */
  private static warnedThresholdFallbacks = new Set<string>();

  /**
   * Read all health thresholds and flags via the Go IPC binary.
   * Falls back to compiled defaults if the IPC call fails.
   */
  private static async getHealthThresholds(): Promise<{
    warningThreshold: number;
    criticalThreshold: number;
    emergencyThreshold: number;
    actionsEnabled: boolean;
    policiesEnabled: boolean;
  }> {
    try {
      const result = await IpcClient.getInstance().configGetHealthThresholds();
      return {
        warningThreshold: result.warningThreshold,
        criticalThreshold: result.criticalThreshold,
        emergencyThreshold: result.emergencyThreshold,
        actionsEnabled: result.actionsEnabled,
        policiesEnabled: result.policiesEnabled,
      };
    } catch (err) {
      const cause = String(err);
      if (!HealthActionService.warnedThresholdFallbacks.has(cause)) {
        HealthActionService.warnedThresholdFallbacks.add(cause);
        console.warn(
          `[HealthActionService] getHealthThresholds IPC call failed, using defaults: ${cause}`
        );
      }
      return {
        warningThreshold: DEFAULT_WARNING_THRESHOLD,
        criticalThreshold: DEFAULT_CRITICAL_THRESHOLD,
        emergencyThreshold: DEFAULT_EMERGENCY_THRESHOLD,
        actionsEnabled: true,
        policiesEnabled: true,
      };
    }
  }

  /**
   * Compute trend by comparing recent 3-run average to prior 7-run average.
   * Uses >10% drop/gain as the threshold for declining/improving.
   */
  private static computeTrend(
    sortedSnapshots: Array<{ score: number }>
  ): "improving" | "stable" | "declining" {
    if (sortedSnapshots.length < 4) return "stable";

    const recentAvg = this.getAverage(sortedSnapshots.slice(0, 3));
    const priorAvg = this.getAverage(sortedSnapshots.slice(3, 10));

    if (priorAvg === 0) return "stable";

    const percentChange = ((recentAvg - priorAvg) / priorAvg) * 100;

    if (percentChange < -10) return "declining";
    if (percentChange > 10) return "improving";
    return "stable";
  }

  /**
   * Compute average score from an array of snapshots.
   */
  private static getAverage(snapshots: Array<{ score: number }>): number {
    if (snapshots.length === 0) return 0;
    return snapshots.reduce((sum, s) => sum + s.score, 0) / snapshots.length;
  }
}
