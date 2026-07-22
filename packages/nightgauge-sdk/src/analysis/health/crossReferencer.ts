/**
 * Cross-Referencing Engine
 *
 * Runs after all dimension analyzers complete. Examines findings across
 * dimensions to detect correlated patterns that no single dimension can
 * identify in isolation.
 *
 * Correlation rules:
 * 1. Cost spikes correlated with model routing changes
 * 2. Failure patterns correlated with specific stages/models
 * 3. Health score drops traced to specific dimension degradation
 * 4. Token waste correlated with cost anomalies
 *
 * @see Issue #1101 - Multi-Dimensional Health Analysis Engine
 */

import type { HealthDimension, DimensionResult, CrossReference, Severity } from "./types.js";

/**
 * Cross-reference findings across completed dimension results.
 *
 * Examines all dimension results for correlated patterns and produces
 * CrossReference entries linking related findings across dimensions.
 */
export function crossReference(
  dimensionResults: Map<HealthDimension, DimensionResult>
): CrossReference[] {
  const crossRefs: CrossReference[] = [];
  let refIndex = 0;

  // Rule 1: Cost spikes + model routing issues
  const costResult = dimensionResults.get("cost-health");
  const routingResult = dimensionResults.get("model-routing");

  if (costResult && routingResult) {
    const costFindings = costResult.findings.filter(
      (f) => f.severity === "high" || f.severity === "critical"
    );
    const routingFindings = routingResult.findings.filter(
      (f) => f.severity === "high" || f.severity === "critical"
    );

    if (costFindings.length > 0 && routingFindings.length > 0) {
      crossRefs.push({
        id: `xr-${++refIndex}`,
        dimensions: ["cost-health", "model-routing"],
        severity: maxSeverity(costFindings[0].severity, routingFindings[0].severity),
        title: "Cost spikes correlated with model routing issues",
        description:
          "Cost anomalies detected alongside model routing problems. " +
          "Misrouted models (under/over-routing) may be driving cost increases.",
        correlatedFindings: [...costFindings.map((f) => f.id), ...routingFindings.map((f) => f.id)],
        confidence: bothHaveEnoughData(costResult, routingResult) ? "medium" : "low",
        evidence: {
          costScore: costResult.score,
          routingScore: routingResult.score,
          costFindingCount: costFindings.length,
          routingFindingCount: routingFindings.length,
        },
      });
    }
  }

  // Rule 2: Failure patterns + specific stages
  const reliabilityResult = dimensionResults.get("reliability");
  const stageResult = dimensionResults.get("stage-effectiveness");

  if (reliabilityResult && stageResult) {
    const reliabilityFindings = reliabilityResult.findings.filter(
      (f) => f.severity === "high" || f.severity === "critical"
    );
    const stageFindings = stageResult.findings.filter(
      (f) => f.severity === "high" || f.severity === "critical"
    );

    if (reliabilityFindings.length > 0 && stageFindings.length > 0) {
      // Check for overlapping stages in evidence
      const reliabilityStages = new Set(
        reliabilityFindings.flatMap((f) => {
          const stages = f.evidence.stages;
          return Array.isArray(stages) ? (stages as string[]) : [];
        })
      );
      const stageNames = new Set(
        stageFindings.flatMap((f) => {
          const stage = f.evidence.stage;
          return typeof stage === "string" ? [stage] : [];
        })
      );

      const overlapping = [...reliabilityStages].filter((s) => stageNames.has(s));

      if (overlapping.length > 0) {
        crossRefs.push({
          id: `xr-${++refIndex}`,
          dimensions: ["reliability", "stage-effectiveness"],
          severity: maxSeverity(reliabilityFindings[0].severity, stageFindings[0].severity),
          title: "Failure patterns concentrated in specific stages",
          description:
            `Reliability issues and stage effectiveness problems overlap in stage(s): ${overlapping.join(", ")}. ` +
            "These stages are both unreliable and underperforming, suggesting a common root cause.",
          correlatedFindings: [
            ...reliabilityFindings.map((f) => f.id),
            ...stageFindings.map((f) => f.id),
          ],
          confidence: bothHaveEnoughData(reliabilityResult, stageResult) ? "high" : "medium",
          evidence: {
            overlappingStages: overlapping,
            reliabilityScore: reliabilityResult.score,
            stageScore: stageResult.score,
          },
        });
      }
    }
  }

  // Rule 3: Health score drops + dimension degradation
  const selfImprovementResult = dimensionResults.get("learning-effectiveness");

  if (selfImprovementResult) {
    const worseningFindings = selfImprovementResult.findings.filter((f) =>
      f.title.toLowerCase().includes("worsening")
    );

    if (worseningFindings.length > 0) {
      // Find which other dimensions are also degrading
      const degradingDimensions: HealthDimension[] = [];
      for (const [dim, result] of dimensionResults) {
        if (dim === "learning-effectiveness") continue;
        if (result.score < 50 || result.periodComparison?.direction === "degrading") {
          degradingDimensions.push(dim);
        }
      }

      if (degradingDimensions.length > 0) {
        crossRefs.push({
          id: `xr-${++refIndex}`,
          dimensions: ["learning-effectiveness", ...degradingDimensions],
          severity: degradingDimensions.length >= 3 ? "critical" : "high",
          title: "Health score decline linked to multiple dimension degradation",
          description:
            `Overall health scores are worsening while ${degradingDimensions.length} dimension(s) are degrading: ` +
            `${degradingDimensions.join(", ")}. Address the lowest-scoring dimensions to reverse the trend.`,
          correlatedFindings: worseningFindings.map((f) => f.id),
          confidence: degradingDimensions.length >= 2 ? "high" : "medium",
          evidence: {
            degradingDimensions,
            degradingDimensionCount: degradingDimensions.length,
            dimensionScores: Object.fromEntries(
              degradingDimensions.map((d) => [d, dimensionResults.get(d)!.score])
            ),
          },
        });
      }
    }
  }

  // Rule 4: Token waste + cost anomalies
  const tokenResult = dimensionResults.get("token-economics");

  if (tokenResult && costResult) {
    const tokenFindings = tokenResult.findings.filter(
      (f) => f.severity === "high" || f.severity === "critical"
    );
    const costAnomalyFindings = costResult.findings.filter((f) =>
      f.title.toLowerCase().includes("anomal")
    );

    if (tokenFindings.length > 0 && costAnomalyFindings.length > 0) {
      crossRefs.push({
        id: `xr-${++refIndex}`,
        dimensions: ["token-economics", "cost-health"],
        severity: maxSeverity(tokenFindings[0].severity, costAnomalyFindings[0].severity),
        title: "Token waste driving cost anomalies",
        description:
          "Token waste patterns detected alongside cost anomalies. " +
          "Addressing token inefficiency may directly reduce cost spikes.",
        correlatedFindings: [
          ...tokenFindings.map((f) => f.id),
          ...costAnomalyFindings.map((f) => f.id),
        ],
        confidence: bothHaveEnoughData(tokenResult, costResult) ? "medium" : "low",
        evidence: {
          tokenScore: tokenResult.score,
          costScore: costResult.score,
          tokenFindingCount: tokenFindings.length,
          costAnomalyCount: costAnomalyFindings.length,
        },
      });
    }
  }

  // Rule 5: Velocity degradation + reliability issues
  const velocityResult = dimensionResults.get("pipeline-velocity");

  if (velocityResult && reliabilityResult) {
    const velocityDegrading =
      velocityResult.periodComparison?.direction === "degrading" || velocityResult.score < 50;
    const reliabilityDegrading =
      reliabilityResult.periodComparison?.direction === "degrading" || reliabilityResult.score < 50;

    if (velocityDegrading && reliabilityDegrading) {
      crossRefs.push({
        id: `xr-${refIndex + 1}`,
        dimensions: ["pipeline-velocity", "reliability"],
        severity: "high",
        title: "Pipeline slowdown correlated with reliability decline",
        description:
          "Pipeline velocity is degrading alongside declining reliability. " +
          "Frequent failures and retries may be the root cause of slower throughput.",
        correlatedFindings: [
          ...velocityResult.findings.map((f) => f.id),
          ...reliabilityResult.findings.map((f) => f.id),
        ],
        confidence: bothHaveEnoughData(velocityResult, reliabilityResult) ? "medium" : "low",
        evidence: {
          velocityScore: velocityResult.score,
          reliabilityScore: reliabilityResult.score,
        },
      });
    }
  }

  return crossRefs;
}

// ── Helpers ─────────────────────────────────────────────────────

const SEVERITY_ORDER: Record<Severity, number> = {
  critical: 4,
  high: 3,
  medium: 2,
  low: 1,
  info: 0,
};

function maxSeverity(a: Severity, b: Severity): Severity {
  return SEVERITY_ORDER[a] >= SEVERITY_ORDER[b] ? a : b;
}

function bothHaveEnoughData(a: DimensionResult, b: DimensionResult): boolean {
  return a.hasEnoughData && b.hasEnoughData;
}
