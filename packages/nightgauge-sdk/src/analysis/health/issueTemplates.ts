/**
 * Issue Templates - Finding-to-Issue Template Formatting
 *
 * Pure formatting functions that convert health analysis findings into
 * GitHub issue titles and bodies for the Finding-to-Issue engine.
 *
 * @see Issue #1102 - Finding-to-Issue Generation Engine
 */

import type {
  Finding,
  HealthDimension,
  CrossReference,
  FindingToIssueResult,
  GeneratedIssue,
} from "./types.js";

// ── Constants ──────────────────────────────────────────────────────

const ISSUE_TITLE_MAX_LENGTH = 256;

// ── Helpers ────────────────────────────────────────────────────────

/**
 * Formats a single evidence entry value as a string.
 * Objects and arrays are serialized with JSON.stringify; primitives use String().
 */
function formatEvidenceValue(value: unknown): string {
  if (value !== null && typeof value === "object") {
    return JSON.stringify(value);
  }
  return String(value);
}

/**
 * Builds the markdown evidence table rows from a Record<string, unknown>.
 */
function buildEvidenceRows(evidence: Record<string, unknown>): string {
  const entries = Object.entries(evidence);
  if (entries.length === 0) {
    return "| — | — |";
  }
  return entries.map(([key, value]) => `| ${key} | ${formatEvidenceValue(value)} |`).join("\n");
}

// ── Public Formatting Functions ────────────────────────────────────

/**
 * Formats a GitHub issue title for a given finding.
 * Format: `[HEALTH] {finding.title}` — truncated to 256 characters total.
 */
export function formatIssueTitle(finding: Finding): string {
  const prefix = "[HEALTH] ";
  const full = `${prefix}${finding.title}`;
  return full.length > ISSUE_TITLE_MAX_LENGTH ? full.slice(0, ISSUE_TITLE_MAX_LENGTH) : full;
}

/**
 * Formats a GitHub issue body for a given finding.
 * Optionally includes a health report reference and cross-references.
 */
export function formatIssueBody(
  finding: Finding,
  healthReportRef?: string,
  crossRefs?: CrossReference[]
): string {
  const evidenceRows = buildEvidenceRows(finding.evidence);
  const reportRef = healthReportRef ?? "N/A";

  const crossRefSection =
    crossRefs && crossRefs.length > 0
      ? [
          "",
          "### Cross-References",
          ...crossRefs.map((ref) => `- **${ref.title}**: ${ref.description}`),
        ].join("\n")
      : "";

  return [
    "## Summary",
    finding.description,
    "",
    "## Impact",
    finding.impact,
    "",
    "## Evidence",
    "| Metric | Value |",
    "|--------|-------|",
    evidenceRows,
    "",
    "## Acceptance Criteria",
    `- [ ] ${finding.recommendation}`,
    `- [ ] Verify improvement in ${finding.dimension} health score`,
    "",
    "## Technical Notes",
    `- Dimension: ${finding.dimension}`,
    `- Severity: ${finding.severity}`,
    `- Confidence: ${finding.confidence}`,
    `- Finding ID: ${finding.id}`,
    crossRefSection,
    "",
    "## Related",
    `Generated from health analysis report ${reportRef}`,
  ]
    .join("\n")
    .trimEnd();
}

/**
 * Formats a GitHub epic title for a dimension group.
 * Format: `[HEALTH] {dimension} improvements ({count} findings)`
 */
export function formatEpicTitle(dimension: HealthDimension, findingCount: number): string {
  return `[HEALTH] ${dimension} improvements (${findingCount} findings)`;
}

/**
 * Formats a GitHub epic body summarising all findings within a dimension.
 */
export function formatEpicBody(
  dimension: HealthDimension,
  findings: Finding[],
  healthReportRef?: string
): string {
  const reportRef = healthReportRef ?? "N/A";

  const findingsSections = findings
    .map(
      (f) =>
        `### ${f.title}\n**Severity:** ${f.severity} | **Confidence:** ${f.confidence}\n\n${f.description}`
    )
    .join("\n\n");

  return [
    "## Summary",
    `${findings.length} findings identified in the ${dimension} dimension requiring attention.`,
    "",
    "## Findings",
    findingsSections,
    "",
    "## Related",
    `Generated from health analysis report ${reportRef}`,
  ]
    .join("\n")
    .trimEnd();
}

/**
 * Formats a human-readable dry-run preview summary from a FindingToIssueResult.
 */
export function formatDryRunPreview(result: FindingToIssueResult): string {
  const issueLines = result.generatedIssues
    .map((issue: GeneratedIssue) => {
      const base = `- [${issue.severity}] ${issue.title}`;
      return issue.skipped ? `${base} (SKIP: ${issue.skipReason})` : base;
    })
    .join("\n");

  const epicLines = result.epicGroups
    .map((group) => {
      const subIssueLines = group.subIssues
        .map((sub: GeneratedIssue) => `  - [${sub.severity}] ${sub.title}`)
        .join("\n");
      return `#### ${group.epicTitle}\n${subIssueLines}`;
    })
    .join("\n\n");

  return [
    "## Dry Run Preview",
    "",
    `**Total findings:** ${result.totalFindings}`,
    `**After severity filter:** ${result.filteredFindings}`,
    `**Duplicates that would be skipped:** ${result.duplicatesSkipped}`,
    `**Issues that would be created:** ${result.issuesCreated}`,
    `**Epics that would be created:** ${result.epicsCreated}`,
    "",
    "### Issues",
    issueLines || "_None_",
    "",
    "### Epic Groups",
    epicLines || "_None_",
  ]
    .join("\n")
    .trimEnd();
}
