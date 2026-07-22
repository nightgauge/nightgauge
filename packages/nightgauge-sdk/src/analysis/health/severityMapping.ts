/**
 * Severity Mapping - Finding-to-Issue Label Utilities
 *
 * Pure mapping functions for translating Finding severity and dimension values
 * into GitHub issue labels used by the Finding-to-Issue engine.
 *
 * @see Issue #1102 - Finding-to-Issue Generation Engine
 */

import type { Severity, HealthDimension, Finding } from "./types.js";

// ── Severity Order ─────────────────────────────────────────────────

export const SEVERITY_ORDER: Record<Severity, number> = {
  critical: 4,
  high: 3,
  medium: 2,
  low: 1,
  info: 0,
};

// ── Threshold Check ────────────────────────────────────────────────

/**
 * Returns true if `severity` is greater than or equal to `threshold`
 * in the SEVERITY_ORDER ranking.
 */
export function severityMeetsThreshold(severity: Severity, threshold: Severity): boolean {
  return SEVERITY_ORDER[severity] >= SEVERITY_ORDER[threshold];
}

// ── Label Mappers ──────────────────────────────────────────────────

export function severityToPriorityLabel(severity: Severity): string {
  switch (severity) {
    case "critical":
      return "priority:critical";
    case "high":
      return "priority:high";
    case "medium":
      return "priority:medium";
    case "low":
      return "priority:low";
    case "info":
      return "priority:low";
  }
}

export function severityToSizeLabel(severity: Severity): string {
  switch (severity) {
    case "critical":
      return "size:M";
    case "high":
      return "size:S";
    case "medium":
      return "size:S";
    case "low":
      return "size:XS";
    case "info":
      return "size:XS";
  }
}

export function dimensionToComponentLabel(dimension: HealthDimension): string {
  return `component:health-${dimension}`;
}

export function severityToTypeLabel(severity: Severity): string {
  switch (severity) {
    case "critical":
    case "high":
      return "type:fix";
    case "medium":
    case "low":
    case "info":
      return "type:chore";
  }
}

// ── Combined Label Builder ─────────────────────────────────────────

/**
 * Returns exactly 4 GitHub issue labels for a given finding:
 * [priorityLabel, sizeLabel, typeLabel, componentLabel]
 */
export function findingToLabels(finding: Finding): string[] {
  return [
    severityToPriorityLabel(finding.severity),
    severityToSizeLabel(finding.severity),
    severityToTypeLabel(finding.severity),
    dimensionToComponentLabel(finding.dimension),
  ];
}
