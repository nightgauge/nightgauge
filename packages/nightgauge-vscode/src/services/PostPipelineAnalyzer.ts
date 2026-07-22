/**
 * PostPipelineAnalyzer - Post-pipeline model routing analysis
 *
 * Static utility class (matching ExecutionHistoryWriter pattern) that:
 * 1. Reads execution history from JSONL files
 * 2. Adapts JSONL records to SDK's ExecutionHistoryRecord format
 * 3. Runs ModelPerformanceAnalyzer.analyze()
 * 4. Runs FailurePatternDetector.analyze() for failure pattern detection
 * 5. Stores analysis results in .nightgauge/analysis/
 * 6. Generates self-check summary for output window
 *
 * Non-critical: all operations wrapped in try/catch, failures log warnings
 * but never break the pipeline.
 *
 * @see Issue #943 - Integrate ModelPerformanceAnalyzer into post-pipeline feedback loop
 * @see Issue #1045 - Post-pipeline outcome analysis and learning system
 * @see packages/nightgauge-sdk/src/analysis/ModelPerformanceAnalyzer.ts
 */

import * as fs from "node:fs/promises";
import * as path from "node:path";
import { exec } from "node:child_process";
import { promisify } from "node:util";
import {
  ModelPerformanceAnalyzer,
  type ModelRoutingAnalysis,
  type ExecutionHistoryRecord as SdkExecutionHistoryRecord,
  type FailureAnalysisResult,
  SkillSelfAssessmentSynthesizer,
  AssessmentRecordSchema,
  type SynthesisResult,
  type ExecutionOutcome,
  foldWorkflowOutcomes,
  summarizeWorkflowOutcomes,
  type WorkflowEvent,
  type WorkflowCalibrationSignal,
} from "@nightgauge/sdk";

/** Per-stage retry statistics */
export interface StageRetryStats {
  stage: string;
  totalRuns: number;
  runsWithRetries: number;
  retryRate: number;
  totalRetryCount: number;
}

/** Per-stage duration percentiles */
export interface StageDurationStats {
  stage: string;
  totalRuns: number;
  p95DurationMs: number;
  maxDurationMs: number;
  medianDurationMs: number;
}

/** Aggregate stage execution statistics */
export interface StageExecutionStats {
  retryStats: StageRetryStats[];
  durationStats: StageDurationStats[];
}
import { ExecutionHistoryReader, type IssueCostAggregation } from "../utils/executionHistoryReader";
import type {
  ExecutionHistoryRunRecord,
  ExecutionHistoryRunRecordV2,
  HistoryStageDetail,
} from "../schemas/executionHistory";
import type { Logger } from "../utils/logger";
import { GateMetricsWriter } from "../utils/gateMetricsWriter";
import { SkillEffectivenessWriter } from "../utils/SkillEffectivenessWriter";
import type { HealthEvaluation } from "./HealthActionService";

/** Directory for analysis result storage */
const ANALYSIS_DIR = ".nightgauge/analysis";

/** Maximum number of analysis files to retain */
const MAX_ANALYSIS_FILES = 20;

/**
 * Result returned from post-pipeline analysis
 */
export interface PostPipelineAnalysisResult {
  /** Path to stored analysis JSON file */
  analysisFile: string;
  /** Number of routing recommendations generated */
  recommendationCount: number;
  /** Total potential savings from all recommendations */
  totalPotentialSavingsUsd: number;
  /** Cost savings from auto-selection vs static defaults */
  costSavingsVsStaticUsd: number;
  /** Human-readable overall recommendation */
  overallRecommendation: string;
  /** Top failure patterns detected (Issue #1045) */
  failurePatterns: {
    totalFailures: number;
    topFindings: Array<{
      category: string;
      occurrenceCount: number;
      trend: string;
      recommendation: string;
    }>;
    overallTrend: string;
  } | null;
  /** Cost aggregated per issue across all runs (last 20 by activity) — Issue #1410 */
  costPerIssue: IssueCostAggregation[] | null;
  /** Gate effectiveness summary from gate-metrics.jsonl (Issue #1412) */
  gateEffectiveness: {
    totalInvocations: number;
    byGate: Array<{
      gateName: string;
      invocations: number;
      catches: number;
      passes: number;
      /** catches / invocations; 0 when invocations === 0 */
      hitRate: number;
    }>;
  } | null;
  /**
   * Aggregate V4 workflow-orchestration calibration signal folded from the
   * canonical schemaVersion-4 WorkflowEvent journals in `.nightgauge/pipeline/`
   * (Issue #3915, epic #3899). `null` when no workflow run was recorded — the
   * consumer no longer references the deleted flat event shape.
   */
  workflowCalibration: WorkflowCalibrationSignal | null;
  /** Whether the calibration table was updated this run (Issue #1589) */
  calibrationUpdated: boolean;
  /** Whether a complexity model outcome was recorded this run (Issue #1395) */
  outcomeRecorded: boolean;
  /** Skill self-assessment synthesis summary (Issue #1986) */
  selfAssessmentSynthesis: {
    recordsAnalyzed: number;
    proposalCount: number;
    topProposals: Array<{
      skillFile: string;
      findingPattern: string;
      occurrenceCount: number;
      severity: string;
      proposedChange: string;
    }>;
  } | null;
  /** Skill file change effectiveness deltas (Issue #1414) */
  skillEffectiveness: {
    skillChangesFound: number;
    entries: Array<{
      skillFile: string;
      stage: string;
      commitHash: string;
      changedAt: string;
      beforeWindow: { sampleCount: number; successRate: number };
      afterWindow: { sampleCount: number; successRate: number };
      delta: number;
      classification: "effective" | "regression" | "neutral" | "insufficient_data";
      confidence: "insufficient_data" | "low" | "moderate";
    }>;
  } | null;
}

/**
 * Stored analysis file format
 */
interface StoredAnalysis {
  issue_number: number;
  pipeline_completion_time: string;
  analysis: ModelRoutingAnalysis;
  failure_analysis?: FailureAnalysisResult;
  created_at: string;
}

export class PostPipelineAnalyzer {
  /**
   * Run post-pipeline analysis after successful completion.
   *
   * Reads execution history, runs ModelPerformanceAnalyzer and
   * FailurePatternDetector, stores results. All errors are caught and logged.
   *
   * @param workspaceRoot - Absolute path to repo root
   * @param issueNumber - The completed issue number
   * @param logger - Logger instance for output
   * @returns Analysis summary or null on failure
   */
  static async analyze(
    workspaceRoot: string,
    issueNumber: number,
    logger: Logger
  ): Promise<PostPipelineAnalysisResult | null> {
    try {
      // Read all execution history records
      const rawRecords = await ExecutionHistoryReader.readAll(workspaceRoot);

      if (rawRecords.length === 0) {
        logger.info("Post-pipeline analysis skipped: no execution history", {
          issueNumber,
        });
        return null;
      }

      // Adapt JSONL run records to SDK analysis format
      const analysisRecords = this.adaptRecords(rawRecords);

      if (analysisRecords.length === 0) {
        logger.info("Post-pipeline analysis skipped: no stage-level records with model data", {
          issueNumber,
        });
        return null;
      }

      // Run model performance analysis
      const analyzer = new ModelPerformanceAnalyzer();
      const analysis = analyzer.analyze(analysisRecords);

      // Run failure pattern detection (non-critical)
      let failureAnalysis: FailureAnalysisResult | null = null;
      try {
        const { FailurePatternDetector } = await import("@nightgauge/sdk");
        const detector = new FailurePatternDetector();
        failureAnalysis = detector.analyze(analysisRecords);
      } catch (err) {
        logger.info("Failure pattern detection skipped", {
          err: err instanceof Error ? err.message : String(err),
        });
      }

      // Store analysis results
      const analysisFile = await this.storeAnalysis(
        workspaceRoot,
        issueNumber,
        analysis,
        failureAnalysis
      );

      // Enforce retention
      await this.enforceRetention(workspaceRoot);

      // Build failure patterns summary
      let failurePatterns: PostPipelineAnalysisResult["failurePatterns"] = null;
      if (failureAnalysis && failureAnalysis.totalFailures > 0) {
        failurePatterns = {
          totalFailures: failureAnalysis.totalFailures,
          topFindings: failureAnalysis.findings.slice(0, 5).map((f) => ({
            category: f.category,
            occurrenceCount: f.occurrenceCount,
            trend: f.trend,
            recommendation: f.recommendation,
          })),
          overallTrend: failureAnalysis.summary.overallTrend,
        };
      }

      // Compute cost-per-issue aggregations (non-critical) — Issue #1410
      let costPerIssue: IssueCostAggregation[] | null = null;
      try {
        costPerIssue = await ExecutionHistoryReader.getCostByIssue(workspaceRoot, 20);
      } catch (err) {
        logger.warn("Cost-per-issue aggregation failed (non-critical)", {
          err: err instanceof Error ? err.message : String(err),
        });
      }

      // Gate effectiveness analysis (non-critical) — Issue #1412
      let gateEffectiveness: PostPipelineAnalysisResult["gateEffectiveness"] = null;
      try {
        const gateRecords = await GateMetricsWriter.readAll(workspaceRoot);
        if (gateRecords.length > 0) {
          const byGate = new Map<string, { invocations: number; catches: number }>();
          for (const r of gateRecords) {
            const entry = byGate.get(r.gate_name) ?? {
              invocations: 0,
              catches: 0,
            };
            entry.invocations++;
            if (r.result === "catch") entry.catches++;
            byGate.set(r.gate_name, entry);
          }
          gateEffectiveness = {
            totalInvocations: gateRecords.length,
            byGate: Array.from(byGate.entries()).map(([gateName, stats]) => ({
              gateName,
              invocations: stats.invocations,
              catches: stats.catches,
              passes: stats.invocations - stats.catches,
              hitRate: stats.invocations > 0 ? stats.catches / stats.invocations : 0,
            })),
          };
        }
      } catch (err) {
        logger.info("Gate effectiveness analysis skipped", {
          err: err instanceof Error ? err.message : String(err),
        });
      }

      // Skill effectiveness tracking (non-critical) — Issue #1414
      let skillEffectiveness: PostPipelineAnalysisResult["skillEffectiveness"] = null;
      try {
        const rawSkillChanges = await this.getRecentSkillChanges(workspaceRoot);
        if (rawSkillChanges.length > 0) {
          const { SkillEffectivenessAnalyzer } = await import("@nightgauge/sdk");
          const skillChanges = rawSkillChanges.map((c) => ({
            skillFile: c.skillFile,
            commitHash: c.commitHash,
            changedAt: c.changedAt,
            stage: c.skillFile.replace(/^skills\/nightgauge-/, "").replace(/\/SKILL\.md$/, ""),
          }));
          const result = SkillEffectivenessAnalyzer.analyze(skillChanges, analysisRecords);
          if (result.entries.length > 0) {
            skillEffectiveness = {
              skillChangesFound: result.skillChangesFound,
              entries: result.entries.map((e) => {
                // Normalize SDK enum values to schema expectations
                const normalizedClassification =
                  e.classification === "insufficient_data"
                    ? "insufficient_data"
                    : (e.classification as string);
                const normalizedConfidence =
                  e.confidence === "insufficient_data"
                    ? "insufficient_data"
                    : (e.confidence as string);

                return {
                  skillFile: e.skillFile,
                  stage: e.stage,
                  commitHash: e.commitHash,
                  changedAt: e.changedAt,
                  beforeWindow: e.beforeWindow,
                  afterWindow: e.afterWindow,
                  delta: e.delta,
                  classification: normalizedClassification as
                    "effective" | "regression" | "neutral" | "insufficient_data",
                  confidence: normalizedConfidence as "insufficient_data" | "low" | "moderate",
                };
              }),
            };
            // Persist to JSONL
            for (const entry of result.entries) {
              // Normalize SDK enum values to schema expectations
              const classification =
                entry.classification === "insufficient_data"
                  ? "insufficient_data"
                  : (entry.classification as string);
              const confidence =
                entry.confidence === "insufficient_data"
                  ? "insufficient_data"
                  : (entry.confidence as string);

              await SkillEffectivenessWriter.appendRecord(workspaceRoot, {
                schema_version: "1",
                skill_file: entry.skillFile,
                stage: entry.stage,
                commit_hash: entry.commitHash,
                changed_at: entry.changedAt,
                before_sample_count: entry.beforeWindow.sampleCount,
                before_success_rate: entry.beforeWindow.successRate,
                after_sample_count: entry.afterWindow.sampleCount,
                after_success_rate: entry.afterWindow.successRate,
                delta: entry.delta,
                classification: classification as
                  "effective" | "regression" | "neutral" | "insufficient_data",
                confidence: confidence as "insufficient_data" | "low" | "moderate",
                analyzed_at: entry.analyzedAt,
              });
            }
          }
        }
      } catch (err) {
        logger.warn("Skill effectiveness tracking failed (non-critical)", {
          issueNumber,
          err: err instanceof Error ? err.message : String(err),
        });
      }

      // Skill self-assessment synthesis (non-critical) — Issue #1986
      let selfAssessmentSynthesis: PostPipelineAnalysisResult["selfAssessmentSynthesis"] = null;
      try {
        const assessmentsDir = path.join(workspaceRoot, ".nightgauge/pipeline/assessments");
        let assessmentFiles: string[] = [];
        try {
          assessmentFiles = (await fs.readdir(assessmentsDir)).filter(
            (f) => f.endsWith(".json") && !f.startsWith("synthesis")
          );
        } catch {
          // Directory doesn't exist yet — no assessments
        }

        if (assessmentFiles.length > 0) {
          const records = [];
          for (const file of assessmentFiles) {
            try {
              const content = await fs.readFile(path.join(assessmentsDir, file), "utf-8");
              const parsed = AssessmentRecordSchema.safeParse(JSON.parse(content));
              if (parsed.success) {
                records.push(parsed.data);
              }
            } catch {
              // Skip malformed files
            }
          }

          if (records.length > 0) {
            const synthesis = SkillSelfAssessmentSynthesizer.synthesize(records);
            selfAssessmentSynthesis = {
              recordsAnalyzed: synthesis.records_analyzed,
              proposalCount: synthesis.proposals.length,
              topProposals: synthesis.proposals.slice(0, 5).map((p) => ({
                skillFile: p.skill_file,
                findingPattern: p.finding_pattern,
                occurrenceCount: p.occurrence_count,
                severity: p.severity,
                proposedChange: p.proposed_change,
              })),
            };

            // Write synthesis result
            await fs.writeFile(
              path.join(assessmentsDir, "synthesis.json"),
              JSON.stringify(synthesis, null, 2),
              "utf-8"
            );

            // Clean up expired records
            const expired = SkillSelfAssessmentSynthesizer.findExpiredRecords(records);
            for (const exp of expired) {
              const expFile = `${exp.skill}-${exp.issue_number}.json`;
              try {
                await fs.unlink(path.join(assessmentsDir, expFile));
              } catch {
                // Non-critical
              }
            }

            // Auto-create GitHub issues for recurring findings (Issue #2321)
            if (synthesis.proposals.length > 0) {
              try {
                await this.createSkillDriftIssues(workspaceRoot, synthesis, logger);
              } catch (err) {
                logger.info("Skill drift issue creation skipped (non-critical)", {
                  err: err instanceof Error ? err.message : String(err),
                });
              }
            }
          }
        }
      } catch (err) {
        logger.info("Skill self-assessment synthesis skipped", {
          err: err instanceof Error ? err.message : String(err),
        });
      }

      // Calibration table update (non-critical) — Issue #1589
      let calibrationUpdated = false;
      try {
        const { CalibrationService } = await import("@nightgauge/sdk");
        const runRecords = rawRecords.filter(
          (r) => r.record_type === "run" && r.outcome === "complete"
        ) as ExecutionHistoryRunRecordV2[];
        const calibrationInputs = runRecords
          .filter((r) => r.size != null)
          .map((r) => ({
            outcome: r.outcome,
            size: r.size ?? null,
            cost_usd: r.tokens.estimated_cost_usd,
            duration_ms: r.total_duration_ms,
            total_tokens: r.tokens.total_input + r.tokens.total_output,
            pipeline_mode: r.performance_mode ?? (r.is_supercharge ? "supercharge" : null),
          }));

        if (calibrationInputs.length >= 3) {
          const table = CalibrationService.buildFromHistory(calibrationInputs);
          const calPath = CalibrationService.getDefaultPath(workspaceRoot);
          await CalibrationService.save(calPath, table);
          calibrationUpdated = true;
          logger.info("Calibration table updated", {
            issueNumber,
            totalRuns: table.total_runs_analyzed,
            buckets: Object.keys(table.buckets),
          });
        }
      } catch (err) {
        logger.debug("Calibration table update skipped", {
          err: err instanceof Error ? err.message : String(err),
        });
      }

      // V4 workflow-orchestration calibration (non-critical) — Issue #3915.
      // Fold the canonical schemaVersion-4 WorkflowEvent journals into the
      // WORKFLOW-level learning signal (judge-rejection rate, fan-out
      // efficiency, native-vs-fanout cost delta). Forward-only: this reads the
      // nested agents[]/judgeVerdict tree, never the deleted flat event shape.
      let workflowCalibration: WorkflowCalibrationSignal | null = null;
      try {
        const events = await this.readWorkflowJournals(workspaceRoot);
        if (events.length > 0) {
          const outcomes = foldWorkflowOutcomes(events);
          if (outcomes.length > 0) {
            workflowCalibration = summarizeWorkflowOutcomes(outcomes);
            logger.info("Workflow calibration folded from V4 journals", {
              issueNumber,
              runCount: workflowCalibration.runCount,
              meanJudgeRejectionRate: workflowCalibration.meanJudgeRejectionRate,
              meanFanoutEfficiency: workflowCalibration.meanFanoutEfficiency,
            });
          }
        }
      } catch (err) {
        logger.debug("Workflow calibration fold skipped (non-critical)", {
          issueNumber,
          err: err instanceof Error ? err.message : String(err),
        });
      }

      // Complexity model outcome recording (non-critical) — Issue #1395
      // Wires OutcomeRecorder into the post-pipeline flow so that each
      // completed pipeline run updates prediction accuracy, type modifiers,
      // and pattern confidence in complexity-model.yaml.
      let outcomeRecorded = false;
      try {
        // Cast to V2 — the reader normalizes V1 records to V2 shape before
        // returning, so V2 fields like `is_supercharge` are always available
        // regardless of the original schema version on disk.
        const thisIssueRun = rawRecords.find(
          (r) =>
            r.record_type === "run" && r.issue_number === issueNumber && r.outcome === "complete"
        ) as ExecutionHistoryRunRecordV2 | undefined;

        if (thisIssueRun) {
          // Map complexity_score (Fibonacci 1/2/3/5/8) to predicted size label
          const complexityScore =
            (thisIssueRun as unknown as Record<string, unknown>).routing != null
              ? (
                  (thisIssueRun as unknown as Record<string, unknown>).routing as Record<
                    string,
                    unknown
                  >
                ).complexity_score
              : 2;
          const score = typeof complexityScore === "number" ? complexityScore : 2;
          let predictedSize: "XS" | "S" | "M" | "L" | "XL";
          if (score <= 1) predictedSize = "XS";
          else if (score <= 2) predictedSize = "S";
          else if (score <= 4) predictedSize = "M";
          else if (score <= 6) predictedSize = "L";
          else predictedSize = "XL";

          // Infer issue type from labels
          const labels: string[] = Array.isArray(thisIssueRun.labels)
            ? (thisIssueRun.labels as string[])
            : [];
          const issueType =
            labels.find((l) => ["bug", "feature", "docs", "refactor", "chore"].includes(l)) ??
            "feature";

          // Read PR number from context file
          let prNumber: number | null = null;
          try {
            const prCtxPath = path.join(
              workspaceRoot,
              `.nightgauge/pipeline/pr-${issueNumber}.json`
            );
            const prCtx = JSON.parse(await fs.readFile(prCtxPath, "utf-8"));
            prNumber = typeof prCtx.pr_number === "number" ? prCtx.pr_number : null;
          } catch {
            /* context file not yet available */
          }

          // Get actual lines changed from GitHub (additions + deletions)
          let actualLinesChanged = 0;
          if (prNumber !== null) {
            try {
              const execAsync = promisify(exec);
              const { stdout } = await execAsync(
                `gh pr view ${prNumber} --json additions,deletions`,
                { cwd: workspaceRoot }
              );
              const prData = JSON.parse(stdout.trim()) as {
                additions?: number;
                deletions?: number;
              };
              actualLinesChanged = (prData.additions ?? 0) + (prData.deletions ?? 0);
            } catch {
              /* gh CLI unavailable or PR not accessible */
            }
          }

          if (prNumber !== null) {
            const { OutcomeRecorder, ComplexityModelService } = await import("@nightgauge/sdk");

            const stagesObj = thisIssueRun.stages as Record<string, { status: string } | undefined>;
            const stagesRun = Object.entries(stagesObj)
              .filter(([, s]) => s?.status === "complete" || s?.status === "skipped")
              .map(([name]) => name);
            const stagesFailed = Object.entries(stagesObj)
              .filter(([, s]) => s?.status === "failed")
              .map(([name]) => name);

            const perStage = (thisIssueRun.tokens as Record<string, unknown>).per_stage as
              Record<string, { model?: string }> | undefined;
            const modelUsed = perStage?.["feature-dev"]?.model ?? "sonnet";

            const outcome: ExecutionOutcome = {
              issue_number: issueNumber,
              issue_type: issueType,
              pr_number: prNumber,
              predicted_size: predictedSize,
              actual_lines_changed: actualLinesChanged,
              actual_tokens_total:
                thisIssueRun.tokens.total_input + thisIssueRun.tokens.total_output,
              actual_cost_usd: thisIssueRun.tokens.estimated_cost_usd,
              actual_duration_ms: thisIssueRun.total_duration_ms,
              stages_run: stagesRun,
              stages_failed: stagesFailed,
              model_used: modelUsed,
              completed_at: thisIssueRun.completed_at ?? new Date().toISOString(),
              outcome: "success",
              patterns_matched: null,
              build_passed_first: null,
              tests_passed_first: null,
              pr_review_iterations: null,
              // Propagate the performance mode into the calibration outcome so
              // OutcomeRecorder can skip prediction-accuracy / type-modifier /
              // pattern-confidence updates for non-baseline runs (efficiency /
              // maximum). Falls back to the legacy `is_supercharge` flag when
              // the new field is absent — preserves correct classification of
              // runs recorded before #3009 (Issues #2433, #3009).
              pipeline_mode:
                thisIssueRun.performance_mode ??
                (thisIssueRun.is_supercharge ? "supercharge" : "normal"),
            };

            const modelService = new ComplexityModelService(workspaceRoot);
            const recorder = new OutcomeRecorder(modelService);
            const result = await recorder.recordOutcome(outcome);
            outcomeRecorded = !result.skipped;
            logger.info("Complexity model outcome recording complete", {
              issueNumber,
              predictedSize,
              actualLinesChanged,
              skipped: result.skipped,
            });
          }
        }
      } catch (err) {
        logger.debug("Complexity model outcome recording skipped (non-critical)", {
          issueNumber,
          err: err instanceof Error ? err.message : String(err),
        });
      }

      return {
        analysisFile,
        recommendationCount: analysis.recommendations.length,
        totalPotentialSavingsUsd: analysis.summary.totalPotentialSavingsUsd,
        costSavingsVsStaticUsd: analysis.autoSelectionAnalysis?.costSavingsVsStaticUsd ?? 0,
        overallRecommendation: analysis.summary.overallRecommendation,
        failurePatterns,
        costPerIssue,
        gateEffectiveness,
        skillEffectiveness,
        workflowCalibration,
        calibrationUpdated,
        selfAssessmentSynthesis,
        outcomeRecorded,
      };
    } catch (err) {
      logger.warn("Post-pipeline analysis failed", {
        issueNumber,
        err: err instanceof Error ? err.message : String(err),
      });
      return null;
    }
  }

  /**
   * Format a self-check summary for the output window.
   *
   * Produces a structured text block showing health, routing, failures,
   * cost, and anomalies.
   *
   * @param analysisResult - Post-pipeline analysis result
   * @param healthEvaluation - Health evaluation from HealthActionService
   * @param costUsd - Total pipeline run cost
   * @param avgCostUsd - Historical average cost per run (0 if unknown)
   */
  static formatSelfCheck(
    analysisResult: PostPipelineAnalysisResult | null,
    healthEvaluation: HealthEvaluation | null,
    costUsd: number,
    avgCostUsd: number
  ): string {
    const lines: string[] = [];
    lines.push("┌─────────────────────────────────────────┐");
    lines.push("│  Pipeline Self-Check                    │");
    lines.push("├─────────────────────────────────────────┤");

    // Health score + trend
    if (healthEvaluation) {
      const trendIcon =
        healthEvaluation.trend === "improving"
          ? "+"
          : healthEvaluation.trend === "declining"
            ? "-"
            : "=";
      lines.push(
        `│  Health: ${healthEvaluation.score} (${trendIcon}) — ${healthEvaluation.status.charAt(0).toUpperCase() + healthEvaluation.status.slice(1)}`.padEnd(
          42
        ) + "│"
      );
    } else {
      lines.push("│  Health: N/A                            │");
    }

    // Model routing status
    if (analysisResult) {
      const routingStatus =
        analysisResult.recommendationCount === 0 ? "optimal" : "has recommendations";
      lines.push(`│  Model routing: ${routingStatus}`.padEnd(42) + "│");
    } else {
      lines.push("│  Model routing: N/A                     │");
    }

    // Failure patterns
    const newRecurring = analysisResult?.failurePatterns?.topFindings?.length ?? 0;
    lines.push(`│  Failure patterns: ${newRecurring} detected`.padEnd(42) + "│");

    // Cost assessment
    const costStr = `$${costUsd.toFixed(2)}`;
    let costNote = "";
    if (avgCostUsd > 0) {
      const ratio = costUsd / avgCostUsd;
      if (ratio > 2) {
        costNote = " (HIGH)";
      } else if (ratio > 1.5) {
        costNote = " (above avg)";
      } else {
        costNote = " (normal)";
      }
    }
    lines.push(`│  Cost: ${costStr}${costNote}`.padEnd(42) + "│");

    // Anomaly detection
    const anomalies: string[] = [];
    if (avgCostUsd > 0 && costUsd > avgCostUsd * 2) {
      anomalies.push("cost spike");
    }
    if (healthEvaluation && healthEvaluation.trend === "declining" && healthEvaluation.score < 50) {
      anomalies.push("critical health");
    }
    if (anomalies.length > 0) {
      lines.push(`│  Anomalies: ${anomalies.join(", ")}`.padEnd(42) + "│");
    } else {
      lines.push("│  No anomalies detected                  │");
    }

    // Gate hit-rates (Issue #1412)
    const gateEff = analysisResult?.gateEffectiveness;
    if (gateEff && gateEff.byGate.length > 0) {
      const top3 = [...gateEff.byGate].sort((a, b) => b.hitRate - a.hitRate).slice(0, 3);
      const hitRateStr = top3
        .map(
          (g) => `${g.gateName}: ${Math.round(g.hitRate * 100)}% (${g.catches}/${g.invocations})`
        )
        .join("  ");
      lines.push(`│  Gates: ${hitRateStr}`.padEnd(42) + "│");
    } else {
      lines.push("│  Gates: N/A                             │");
    }

    lines.push("└─────────────────────────────────────────┘");

    // V4 workflow-orchestration calibration (Issue #3915)
    const workflow = analysisResult?.workflowCalibration;
    if (workflow && workflow.runCount > 0) {
      lines.push("");
      const parts: string[] = [`Workflow fan-out: ${workflow.runCount} run(s)`];
      if (workflow.meanJudgeRejectionRate !== null) {
        parts.push(`judge-reject ${Math.round(workflow.meanJudgeRejectionRate * 100)}%`);
      }
      if (workflow.meanFanoutEfficiency !== null) {
        parts.push(`fan-out eff ${Math.round(workflow.meanFanoutEfficiency * 100)}%`);
      }
      if (workflow.nativeVsFanoutCostDeltaUsd !== null) {
        const delta = workflow.nativeVsFanoutCostDeltaUsd;
        parts.push(`native−fanout Δ$${delta.toFixed(4)}`);
      }
      lines.push(parts.join(" · "));
    }

    // Skill drift from self-assessment (Issue #1986)
    const selfAssess = analysisResult?.selfAssessmentSynthesis;
    if (selfAssess && selfAssess.proposalCount > 0) {
      lines.push("");
      lines.push(
        `Skill drift: ${selfAssess.proposalCount} recurring finding(s) from ${selfAssess.recordsAnalyzed} assessment(s):`
      );
      for (const p of selfAssess.topProposals) {
        lines.push(
          `    [${p.severity}] ${p.skillFile}: ${p.findingPattern} (${p.occurrenceCount}x)`
        );
      }
    }

    // Skill effectiveness deltas (Issue #1414)
    const skillEff = analysisResult?.skillEffectiveness;
    if (skillEff && skillEff.entries.length > 0) {
      const effectiveCount = skillEff.entries.filter(
        (e) => e.classification === "effective"
      ).length;
      const regressionCount = skillEff.entries.filter(
        (e) => e.classification === "regression"
      ).length;
      if (regressionCount > 0) {
        lines.push("");
        lines.push(`⚠️  Skill regression detected (${regressionCount} skill(s)):`);
        for (const e of skillEff.entries.filter((e) => e.classification === "regression")) {
          const pct = Math.round(e.delta * 100);
          lines.push(
            `    ${e.skillFile}: ${pct}% delta [${e.confidence}] — ${e.beforeWindow.sampleCount}→${e.afterWindow.sampleCount} samples`
          );
        }
      }
      if (effectiveCount > 0) {
        lines.push("");
        lines.push(`✓  Effective skill edits (${effectiveCount}):`);
        for (const e of skillEff.entries.filter((e) => e.classification === "effective")) {
          const pct = Math.round(e.delta * 100);
          lines.push(`    ${e.skillFile}: +${pct}% delta [${e.confidence}]`);
        }
      }
    }

    return lines.join("\n");
  }

  /**
   * Create GitHub issues for recurring skill drift findings.
   *
   * Only creates issues when config.yaml has `self_assessment.action_mode: create_issues`
   * and proposals exceed the configured thresholds. Deduplicates against existing
   * open issues with the `skill-drift` label.
   *
   * @see Issue #2321 — Automated GitHub issue creation for recurring skill drift findings
   */
  private static async createSkillDriftIssues(
    workspaceRoot: string,
    synthesis: SynthesisResult,
    logger: Logger
  ): Promise<void> {
    // Read config to check action_mode
    const configPath = path.join(workspaceRoot, ".nightgauge/config.yaml");
    let configContent: string;
    try {
      configContent = await fs.readFile(configPath, "utf-8");
    } catch {
      return; // No config file — skip
    }

    // Simple YAML parsing for self_assessment block
    const actionModeMatch = configContent.match(/self_assessment:[\s\S]*?action_mode:\s*(\S+)/);
    const actionMode = actionModeMatch?.[1] ?? "display";
    if (actionMode !== "create_issues") return;

    const thresholdMatch = configContent.match(/self_assessment:[\s\S]*?issue_threshold:\s*(\d+)/);
    const issueThreshold = parseInt(thresholdMatch?.[1] ?? "3", 10);

    const severityMatch = configContent.match(
      /self_assessment:[\s\S]*?severity_threshold:\s*(\S+)/
    );
    const severityThreshold = severityMatch?.[1] ?? "medium";
    const severityOrder: Record<string, number> = {
      high: 3,
      medium: 2,
      low: 1,
    };
    const minSeverity = severityOrder[severityThreshold] ?? 2;

    // Filter proposals that meet thresholds
    const eligible = synthesis.proposals.filter(
      (p) => p.occurrence_count >= issueThreshold && (severityOrder[p.severity] ?? 0) >= minSeverity
    );

    if (eligible.length === 0) return;

    // Check for existing open skill-drift issues to avoid duplicates
    const execAsync = promisify(exec);
    let existingTitles: Set<string>;
    try {
      const { stdout } = await execAsync(
        `gh issue list --label skill-drift --state open --json title --limit 100`,
        { cwd: workspaceRoot, maxBuffer: 512 * 1024 }
      );
      const existing = JSON.parse(stdout) as Array<{ title: string }>;
      existingTitles = new Set(existing.map((i) => i.title.toLowerCase()));
    } catch {
      existingTitles = new Set();
    }

    // Create issues for eligible proposals
    let created = 0;
    for (const proposal of eligible) {
      const skillName = proposal.skill_file
        .replace("skills/nightgauge-", "")
        .replace("/SKILL.md", "");
      const title = `Skill drift: ${skillName} — ${proposal.finding_pattern}`;

      // Deduplicate
      if (existingTitles.has(title.toLowerCase())) continue;

      const body = [
        `## Recurring Skill Friction`,
        "",
        `**Skill:** \`${proposal.skill_file}\``,
        `**Pattern:** ${proposal.finding_pattern}`,
        `**Severity:** ${proposal.severity}`,
        `**Occurrences:** ${proposal.occurrence_count} distinct issues (${proposal.affected_issues.join(", ")})`,
        `**First seen:** ${proposal.first_seen}`,
        `**Last seen:** ${proposal.last_seen}`,
        "",
        `## Proposed Fix`,
        "",
        proposal.proposed_change,
        "",
        `---`,
        `*Auto-created by skill self-assessment synthesis (Issue #1986)*`,
      ].join("\n");

      try {
        await execAsync(
          `gh issue create --title "${title.replace(/"/g, '\\"')}" --body "${body.replace(/"/g, '\\"')}" --label skill-drift --label "type:fix" --label "size:S"`,
          { cwd: workspaceRoot, maxBuffer: 512 * 1024 }
        );
        created++;
        logger.info("Created skill-drift issue", { skillName, title });
      } catch (err) {
        logger.info("Failed to create skill-drift issue", {
          skillName,
          err: err instanceof Error ? err.message : String(err),
        });
      }
    }

    if (created > 0) {
      logger.info(`Created ${created} skill-drift issue(s)`, {
        total: eligible.length,
        created,
      });
    }
  }

  /**
   * Get SKILL.md changes from git log within the last N days.
   *
   * Runs git log to find commits that touched any skills SKILL.md file.
   * Returns an empty array if `skills/` doesn't exist or git is unavailable.
   *
   * @param workspaceRoot - Absolute path to repository root
   * @param lookbackDays - How far back to search (default: 30)
   */
  private static async getRecentSkillChanges(
    workspaceRoot: string,
    lookbackDays = 30
  ): Promise<Array<{ skillFile: string; commitHash: string; changedAt: string }>> {
    try {
      const execAsync = promisify(exec);
      const { stdout } = await execAsync(
        `git -C "${workspaceRoot}" log --name-only --format="COMMIT %H %aI" --since="${lookbackDays} days ago" -- "skills/*/SKILL.md"`,
        { maxBuffer: 1024 * 1024 }
      );

      const results: Array<{
        skillFile: string;
        commitHash: string;
        changedAt: string;
      }> = [];

      let currentHash = "";
      let currentDate = "";

      for (const line of stdout.split("\n")) {
        const trimmed = line.trim();
        if (!trimmed) continue;

        const commitMatch = trimmed.match(/^COMMIT\s+([0-9a-f]{40})\s+(.+)$/);
        if (commitMatch) {
          currentHash = commitMatch[1];
          currentDate = commitMatch[2];
          continue;
        }

        if (currentHash && trimmed.startsWith("skills/") && trimmed.endsWith("SKILL.md")) {
          results.push({
            skillFile: trimmed,
            commitHash: currentHash,
            changedAt: currentDate,
          });
        }
      }

      return results;
    } catch {
      // git unavailable or skills/ not found — non-critical
      return [];
    }
  }

  /**
   * Adapt JSONL execution history records to SDK ExecutionHistoryRecord format.
   *
   * The JSONL records (from ExecutionHistoryReader) are run-level records with
   * nested `stages` objects. The SDK's ModelPerformanceAnalyzer expects flat
   * per-stage records. This function flattens run records into per-stage records.
   *
   * @param rawRecords - Records from ExecutionHistoryReader.readAll()
   * @returns Flat per-stage records for ModelPerformanceAnalyzer
   */
  static adaptRecords(
    rawRecords: Array<{ record_type: string; [key: string]: unknown }>
  ): SdkExecutionHistoryRecord[] {
    const result: SdkExecutionHistoryRecord[] = [];

    for (const record of rawRecords) {
      if (record.record_type !== "run") continue;

      const runRecord = record as unknown as ExecutionHistoryRunRecord;
      if (!runRecord.stages) continue;

      for (const [stageName, stageDetail] of Object.entries(runRecord.stages)) {
        const stage = stageDetail as HistoryStageDetail;

        // Skip stages without model selection data
        if (!stage.model_selection) continue;

        // Get per-stage token usage if available
        const perStageTokens =
          runRecord.tokens?.per_stage?.[stageName as keyof typeof runRecord.tokens.per_stage];

        result.push({
          issueNumber: runRecord.issue_number,
          stage: stageName,
          adapter: "claude",
          model: stage.model_selection.model,
          success: stage.status === "complete",
          retries: (stage.auto_retry_count ?? 0) + (stage.manual_retry_count ?? 0),
          inputTokens: perStageTokens?.input ?? 0,
          outputTokens: perStageTokens?.output ?? 0,
          cacheReadTokens: perStageTokens?.cache_read ?? 0,
          cacheCreationTokens: perStageTokens?.cache_creation ?? 0,
          costUsd: perStageTokens?.cost_usd ?? 0,
          durationMs: stage.duration_ms ?? 0,
          timestamp: stage.started_at ?? runRecord.started_at,
          selectionSource: stage.model_selection.source,
          selectedModel: stage.model_selection.model,
          modelSelectionMode: stage.model_selection.mode,
          autoSelectorConfidence: stage.model_selection.confidence,
          autoSelectorComplexity: stage.model_selection.complexity,
          failure_category: stage.failure_category,
        });
      }
    }

    return result;
  }

  /**
   * Read every canonical schemaVersion-4 workflow event journal under
   * `.nightgauge/pipeline/` and return the concatenated emission stream.
   *
   * Journals are named `workflow-{runId}.jsonl` (one node emission per line).
   * The fold ({@link foldWorkflowOutcomes}) buckets a multi-run stream back into
   * one outcome per run, so concatenating every journal here is safe. Malformed
   * lines are skipped — a single bad record never breaks the non-critical
   * post-pipeline loop.
   *
   * @see Issue #3915 — V4 outcome-recording consumer
   */
  static async readWorkflowJournals(workspaceRoot: string): Promise<WorkflowEvent[]> {
    const pipelineDir = path.join(workspaceRoot, ".nightgauge/pipeline");
    let files: string[];
    try {
      files = (await fs.readdir(pipelineDir)).filter(
        (f) => f.startsWith("workflow-") && f.endsWith(".jsonl")
      );
    } catch {
      return []; // directory absent — no workflow runs recorded
    }

    const events: WorkflowEvent[] = [];
    for (const file of files) {
      let content: string;
      try {
        content = await fs.readFile(path.join(pipelineDir, file), "utf-8");
      } catch {
        continue; // unreadable journal — skip
      }
      for (const line of content.split("\n")) {
        const trimmed = line.trim();
        if (!trimmed) continue;
        try {
          events.push(JSON.parse(trimmed) as WorkflowEvent);
        } catch {
          // Malformed JSONL line — skip rather than abort the whole fold.
        }
      }
    }
    return events;
  }

  /**
   * Store analysis results to .nightgauge/analysis/ directory.
   *
   * Creates timestamped file and overwrites latest.json.
   * Returns the path to the timestamped analysis file.
   */
  private static async storeAnalysis(
    workspaceRoot: string,
    issueNumber: number,
    analysis: ModelRoutingAnalysis,
    failureAnalysis?: FailureAnalysisResult | null
  ): Promise<string> {
    const analysisDir = path.join(workspaceRoot, ANALYSIS_DIR);
    await fs.mkdir(analysisDir, { recursive: true });

    const now = new Date().toISOString();
    const stored: StoredAnalysis = {
      issue_number: issueNumber,
      pipeline_completion_time: now,
      analysis,
      created_at: now,
    };

    if (failureAnalysis) {
      stored.failure_analysis = failureAnalysis;
    }

    const content = JSON.stringify(stored, null, 2);

    // Write timestamped file
    const timestamp = now.replace(/[:.]/g, "-");
    const filename = `analysis-${timestamp}.json`;
    const filePath = path.join(analysisDir, filename);
    await fs.writeFile(filePath, content, "utf-8");

    // Write latest.json (overwrite)
    const latestPath = path.join(analysisDir, "latest.json");
    await fs.writeFile(latestPath, content, "utf-8");

    return filePath;
  }

  /**
   * Enforce retention: keep only the last MAX_ANALYSIS_FILES timestamped files.
   */
  private static async enforceRetention(workspaceRoot: string): Promise<void> {
    const analysisDir = path.join(workspaceRoot, ANALYSIS_DIR);

    let entries: string[];
    try {
      entries = await fs.readdir(analysisDir);
    } catch {
      return;
    }

    // Filter to timestamped analysis files (not latest.json)
    const analysisFiles = entries
      .filter((e) => e.startsWith("analysis-") && e.endsWith(".json"))
      .sort();

    if (analysisFiles.length <= MAX_ANALYSIS_FILES) return;

    // Delete oldest files
    const toDelete = analysisFiles.slice(0, analysisFiles.length - MAX_ANALYSIS_FILES);
    for (const file of toDelete) {
      try {
        await fs.unlink(path.join(analysisDir, file));
      } catch {
        // Non-critical: skip deletion failures
      }
    }
  }

  /**
   * Compute per-stage retry and duration statistics from execution history.
   *
   * Iterates run records, accumulating retry counts and durations per stage.
   * Returns StageExecutionStats with retry rates and duration percentiles.
   *
   * @param rawRecords - Execution history run records
   * @returns StageExecutionStats for analysis display
   *
   * @see Issue #1573 - Stage execution stats computation
   */
  static computeStageExecutionStats(
    rawRecords: Array<{
      record_type?: string;
      stages?: unknown;
      [key: string]: unknown;
    }>
  ): StageExecutionStats {
    const retryMap = new Map<
      string,
      { totalRuns: number; runsWithRetries: number; totalRetryCount: number }
    >();
    const durationMap = new Map<string, number[]>();

    for (const record of rawRecords) {
      // Only process run records (skip outcome records)
      if (record.record_type && record.record_type !== "run") continue;
      if (!record.stages) continue;

      const stages = record.stages as Record<string, HistoryStageDetail>;
      for (const [stageName, detail] of Object.entries(stages)) {
        const stageDetail = detail as HistoryStageDetail;
        if (stageDetail.status !== "complete" && stageDetail.status !== "failed") continue;

        // Retry stats
        const autoRetries = stageDetail.auto_retry_count ?? 0;
        const manualRetries = stageDetail.manual_retry_count ?? 0;
        const totalRetries = autoRetries + manualRetries;

        const existing = retryMap.get(stageName) ?? {
          totalRuns: 0,
          runsWithRetries: 0,
          totalRetryCount: 0,
        };
        existing.totalRuns++;
        if (totalRetries > 0) existing.runsWithRetries++;
        existing.totalRetryCount += totalRetries;
        retryMap.set(stageName, existing);

        // Duration stats
        if (stageDetail.duration_ms != null && stageDetail.duration_ms > 0) {
          const durations = durationMap.get(stageName) ?? [];
          durations.push(stageDetail.duration_ms);
          durationMap.set(stageName, durations);
        }
      }
    }

    const retryStats: StageRetryStats[] = [];
    for (const [stage, data] of retryMap) {
      retryStats.push({
        stage,
        totalRuns: data.totalRuns,
        runsWithRetries: data.runsWithRetries,
        retryRate: data.totalRuns > 0 ? data.runsWithRetries / data.totalRuns : 0,
        totalRetryCount: data.totalRetryCount,
      });
    }

    const durationStats: StageDurationStats[] = [];
    for (const [stage, durations] of durationMap) {
      if (durations.length === 0) continue;

      const sorted = [...durations].sort((a, b) => a - b);
      const p95Index = Math.ceil(0.95 * sorted.length) - 1;
      const medianIndex = Math.floor(sorted.length / 2);

      durationStats.push({
        stage,
        totalRuns: sorted.length,
        p95DurationMs: sorted[Math.max(0, p95Index)],
        maxDurationMs: sorted[sorted.length - 1],
        medianDurationMs: sorted[medianIndex],
      });
    }

    return { retryStats, durationStats };
  }
}
