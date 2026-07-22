import { describe, it, expect } from "vitest";
import {
  formatIssueTitle,
  formatIssueBody,
  formatEpicTitle,
  formatEpicBody,
  formatDryRunPreview,
} from "../../../src/analysis/health/issueTemplates.js";
import type {
  Finding,
  CrossReference,
  FindingToIssueResult,
  GeneratedIssue,
  EpicGroup,
} from "../../../src/analysis/health/types.js";

// ── Helpers ────────────────────────────────────────────────────────

function makeFinding(overrides: Partial<Finding> = {}): Finding {
  return {
    id: "finding-001",
    dimension: "cost-health",
    severity: "high",
    title: "High cost per pipeline run",
    description: "Average cost per run exceeds acceptable threshold.",
    impact: "Budget overruns and reduced throughput.",
    recommendation: "Reduce input token usage by caching repeated context.",
    evidence: { avgCostUsd: 0.85, threshold: 0.5 },
    confidence: "high",
    ...overrides,
  };
}

function makeCrossRef(overrides: Partial<CrossReference> = {}): CrossReference {
  return {
    id: "xref-001",
    dimensions: ["cost-health", "token-economics"],
    severity: "high",
    title: "Token bloat driving cost spike",
    description: "High token usage correlates with elevated costs.",
    correlatedFindings: ["finding-001"],
    confidence: "medium",
    evidence: {},
    ...overrides,
  };
}

function makeGeneratedIssue(overrides: Partial<GeneratedIssue> = {}): GeneratedIssue {
  return {
    findingId: "finding-001",
    title: "[HEALTH] High cost per pipeline run",
    body: "## Summary\nAverage cost per run exceeds threshold.",
    labels: ["health", "cost-health"],
    severity: "high",
    dimension: "cost-health",
    ...overrides,
  };
}

function makeEpicGroup(overrides: Partial<EpicGroup> = {}): EpicGroup {
  const finding = makeFinding();
  const subIssue = makeGeneratedIssue();
  return {
    dimension: "cost-health",
    findings: [finding],
    epicTitle: "[HEALTH] cost-health improvements (1 findings)",
    epicBody: "## Summary\n1 findings identified.",
    epicLabels: ["health", "epic", "cost-health"],
    subIssues: [subIssue],
    ...overrides,
  };
}

function makeResult(overrides: Partial<FindingToIssueResult> = {}): FindingToIssueResult {
  return {
    totalFindings: 3,
    filteredFindings: 2,
    duplicatesSkipped: 1,
    issuesCreated: 2,
    epicsCreated: 1,
    generatedIssues: [],
    epicGroups: [],
    dryRun: true,
    ...overrides,
  };
}

// ── Tests ──────────────────────────────────────────────────────────

describe("formatIssueTitle", () => {
  it('prefixes with "[HEALTH] "', () => {
    const finding = makeFinding({ title: "High token usage" });
    const result = formatIssueTitle(finding);
    expect(result).toBe("[HEALTH] High token usage");
  });

  it("truncates long titles to 256 characters", () => {
    const longTitle = "A".repeat(300);
    const finding = makeFinding({ title: longTitle });
    const result = formatIssueTitle(finding);
    expect(result.length).toBe(256);
    expect(result.startsWith("[HEALTH] ")).toBe(true);
  });

  it("does not truncate short titles", () => {
    const finding = makeFinding({ title: "Short title" });
    const result = formatIssueTitle(finding);
    expect(result).toBe("[HEALTH] Short title");
    expect(result.length).toBeLessThan(256);
  });

  it("produces exactly 256 chars when prefix plus title equals exactly 256", () => {
    // '[HEALTH] ' is 9 chars, so we need 247 more to hit exactly 256
    const borderTitle = "B".repeat(247);
    const finding = makeFinding({ title: borderTitle });
    const result = formatIssueTitle(finding);
    expect(result.length).toBe(256);
    expect(result).toBe(`[HEALTH] ${borderTitle}`);
  });
});

describe("formatIssueBody", () => {
  it("includes a Summary section with the finding description", () => {
    const finding = makeFinding();
    const body = formatIssueBody(finding);
    expect(body).toContain("## Summary");
    expect(body).toContain(finding.description);
  });

  it("includes an Impact section with the finding impact", () => {
    const finding = makeFinding();
    const body = formatIssueBody(finding);
    expect(body).toContain("## Impact");
    expect(body).toContain(finding.impact);
  });

  it("includes an Evidence section with a markdown table", () => {
    const finding = makeFinding({
      evidence: { avgCostUsd: 0.85, threshold: 0.5 },
    });
    const body = formatIssueBody(finding);
    expect(body).toContain("## Evidence");
    expect(body).toContain("| Metric | Value |");
    expect(body).toContain("|--------|-------|");
  });

  it("renders evidence key-value pairs as table rows", () => {
    const finding = makeFinding({
      evidence: { avgCostUsd: 0.85, threshold: 0.5 },
    });
    const body = formatIssueBody(finding);
    expect(body).toContain("| avgCostUsd | 0.85 |");
    expect(body).toContain("| threshold | 0.5 |");
  });

  it("serializes object evidence values with JSON.stringify", () => {
    const finding = makeFinding({
      evidence: { breakdown: { input: 100, output: 200 } },
    });
    const body = formatIssueBody(finding);
    expect(body).toContain('| breakdown | {"input":100,"output":200} |');
  });

  it("renders a fallback row for empty evidence", () => {
    const finding = makeFinding({ evidence: {} });
    const body = formatIssueBody(finding);
    expect(body).toContain("| — | — |");
  });

  it("includes Acceptance Criteria section", () => {
    const finding = makeFinding();
    const body = formatIssueBody(finding);
    expect(body).toContain("## Acceptance Criteria");
    expect(body).toContain(`- [ ] ${finding.recommendation}`);
    expect(body).toContain(`- [ ] Verify improvement in ${finding.dimension} health score`);
  });

  it("includes Technical Notes section with dimension, severity, confidence, and id", () => {
    const finding = makeFinding();
    const body = formatIssueBody(finding);
    expect(body).toContain("## Technical Notes");
    expect(body).toContain(`- Dimension: ${finding.dimension}`);
    expect(body).toContain(`- Severity: ${finding.severity}`);
    expect(body).toContain(`- Confidence: ${finding.confidence}`);
    expect(body).toContain(`- Finding ID: ${finding.id}`);
  });

  it("includes Related section with health report ref when provided", () => {
    const finding = makeFinding();
    const body = formatIssueBody(finding, "health-report-2026-01");
    expect(body).toContain("## Related");
    expect(body).toContain("Generated from health analysis report health-report-2026-01");
  });

  it("shows N/A for health report ref when not provided", () => {
    const finding = makeFinding();
    const body = formatIssueBody(finding);
    expect(body).toContain("Generated from health analysis report N/A");
  });

  it("includes Cross-References section when crossRefs are provided", () => {
    const finding = makeFinding();
    const crossRef = makeCrossRef();
    const body = formatIssueBody(finding, undefined, [crossRef]);
    expect(body).toContain("### Cross-References");
    expect(body).toContain(`**${crossRef.title}**: ${crossRef.description}`);
  });

  it("omits Cross-References section when no crossRefs are provided", () => {
    const finding = makeFinding();
    const body = formatIssueBody(finding);
    expect(body).not.toContain("### Cross-References");
  });

  it("omits Cross-References section when crossRefs is an empty array", () => {
    const finding = makeFinding();
    const body = formatIssueBody(finding, undefined, []);
    expect(body).not.toContain("### Cross-References");
  });

  it("includes all required sections in the correct order", () => {
    const finding = makeFinding();
    const body = formatIssueBody(finding);
    const summaryIdx = body.indexOf("## Summary");
    const impactIdx = body.indexOf("## Impact");
    const evidenceIdx = body.indexOf("## Evidence");
    const acceptanceIdx = body.indexOf("## Acceptance Criteria");
    const technicalIdx = body.indexOf("## Technical Notes");
    const relatedIdx = body.indexOf("## Related");
    expect(summaryIdx).toBeLessThan(impactIdx);
    expect(impactIdx).toBeLessThan(evidenceIdx);
    expect(evidenceIdx).toBeLessThan(acceptanceIdx);
    expect(acceptanceIdx).toBeLessThan(technicalIdx);
    expect(technicalIdx).toBeLessThan(relatedIdx);
  });

  it("renders multiple cross-references as a list", () => {
    const finding = makeFinding();
    const ref1 = makeCrossRef({
      id: "xref-001",
      title: "First Cross Ref",
      description: "Desc A",
    });
    const ref2 = makeCrossRef({
      id: "xref-002",
      title: "Second Cross Ref",
      description: "Desc B",
    });
    const body = formatIssueBody(finding, undefined, [ref1, ref2]);
    expect(body).toContain("**First Cross Ref**: Desc A");
    expect(body).toContain("**Second Cross Ref**: Desc B");
  });
});

describe("formatEpicTitle", () => {
  it("returns the correct format with dimension and count", () => {
    const result = formatEpicTitle("cost-health", 5);
    expect(result).toBe("[HEALTH] cost-health improvements (5 findings)");
  });

  it("handles a count of 1 finding", () => {
    const result = formatEpicTitle("reliability", 1);
    expect(result).toBe("[HEALTH] reliability improvements (1 findings)");
  });

  it("handles a count of zero findings", () => {
    const result = formatEpicTitle("token-economics", 0);
    expect(result).toBe("[HEALTH] token-economics improvements (0 findings)");
  });

  it("works with all valid health dimensions", () => {
    const dimensions = [
      "token-economics",
      "cost-health",
      "stage-effectiveness",
      "model-routing",
      "reliability",
      "learning-effectiveness",
      "pipeline-velocity",
    ] as const;
    for (const dim of dimensions) {
      const result = formatEpicTitle(dim, 3);
      expect(result).toBe(`[HEALTH] ${dim} improvements (3 findings)`);
    }
  });
});

describe("formatEpicBody", () => {
  it("mentions finding count and dimension in the summary", () => {
    const findings = [makeFinding(), makeFinding({ id: "finding-002", title: "Second issue" })];
    const body = formatEpicBody("cost-health", findings);
    expect(body).toContain("## Summary");
    expect(body).toContain("2 findings");
    expect(body).toContain("cost-health");
  });

  it("includes a Findings section for each finding", () => {
    const f1 = makeFinding({ id: "f-001", title: "First finding" });
    const f2 = makeFinding({ id: "f-002", title: "Second finding" });
    const body = formatEpicBody("cost-health", [f1, f2]);
    expect(body).toContain("## Findings");
    expect(body).toContain("### First finding");
    expect(body).toContain("### Second finding");
  });

  it("includes severity and confidence for each finding subsection", () => {
    const finding = makeFinding({ severity: "critical", confidence: "high" });
    const body = formatEpicBody("cost-health", [finding]);
    expect(body).toContain("**Severity:** critical");
    expect(body).toContain("**Confidence:** high");
  });

  it("includes finding description in each subsection", () => {
    const finding = makeFinding({
      description: "Detailed description text here.",
    });
    const body = formatEpicBody("cost-health", [finding]);
    expect(body).toContain("Detailed description text here.");
  });

  it("includes Related section with health report ref when provided", () => {
    const findings = [makeFinding()];
    const body = formatEpicBody("cost-health", findings, "report-ref-xyz");
    expect(body).toContain("## Related");
    expect(body).toContain("Generated from health analysis report report-ref-xyz");
  });

  it("shows N/A for health report ref when not provided", () => {
    const findings = [makeFinding()];
    const body = formatEpicBody("cost-health", findings);
    expect(body).toContain("Generated from health analysis report N/A");
  });

  it("handles an empty findings array", () => {
    const body = formatEpicBody("reliability", []);
    expect(body).toContain("## Summary");
    expect(body).toContain("0 findings");
    expect(body).toContain("reliability");
  });
});

describe("formatDryRunPreview", () => {
  it("shows the correct header", () => {
    const result = makeResult();
    const preview = formatDryRunPreview(result);
    expect(preview).toContain("## Dry Run Preview");
  });

  it("shows correct counts from the result", () => {
    const result = makeResult({
      totalFindings: 10,
      filteredFindings: 7,
      duplicatesSkipped: 2,
      issuesCreated: 5,
      epicsCreated: 2,
    });
    const preview = formatDryRunPreview(result);
    expect(preview).toContain("**Total findings:** 10");
    expect(preview).toContain("**After severity filter:** 7");
    expect(preview).toContain("**Duplicates that would be skipped:** 2");
    expect(preview).toContain("**Issues that would be created:** 5");
    expect(preview).toContain("**Epics that would be created:** 2");
  });

  it("lists generated issues with severity prefix", () => {
    const issue = makeGeneratedIssue({
      severity: "high",
      title: "[HEALTH] High cost per run",
    });
    const result = makeResult({ generatedIssues: [issue] });
    const preview = formatDryRunPreview(result);
    expect(preview).toContain("### Issues");
    expect(preview).toContain("- [high] [HEALTH] High cost per run");
  });

  it("shows skipped issues with skip reason", () => {
    const skipped = makeGeneratedIssue({
      severity: "medium",
      title: "[HEALTH] Duplicate finding",
      skipped: true,
      skipReason: "Duplicate of #42",
    });
    const result = makeResult({ generatedIssues: [skipped] });
    const preview = formatDryRunPreview(result);
    expect(preview).toContain("(SKIP: Duplicate of #42)");
    expect(preview).toContain("- [medium] [HEALTH] Duplicate finding (SKIP: Duplicate of #42)");
  });

  it('shows "_None_" when there are no issues', () => {
    const result = makeResult({ generatedIssues: [] });
    const preview = formatDryRunPreview(result);
    expect(preview).toContain("_None_");
  });

  it("shows epic groups with sub-issues", () => {
    const subIssue = makeGeneratedIssue({
      severity: "high",
      title: "[HEALTH] Sub issue A",
    });
    const epicGroup = makeEpicGroup({
      epicTitle: "[HEALTH] cost-health improvements (1 findings)",
      subIssues: [subIssue],
    });
    const result = makeResult({ epicGroups: [epicGroup] });
    const preview = formatDryRunPreview(result);
    expect(preview).toContain("### Epic Groups");
    expect(preview).toContain("#### [HEALTH] cost-health improvements (1 findings)");
    expect(preview).toContain("  - [high] [HEALTH] Sub issue A");
  });

  it('shows "_None_" in Epic Groups section when no epics exist', () => {
    const result = makeResult({ epicGroups: [] });
    const preview = formatDryRunPreview(result);
    expect(preview).toContain("### Epic Groups");
    expect(preview).toContain("_None_");
  });

  it("renders multiple issues without skip annotations for non-skipped issues", () => {
    const issue1 = makeGeneratedIssue({
      severity: "critical",
      title: "[HEALTH] Critical issue",
    });
    const issue2 = makeGeneratedIssue({
      severity: "low",
      title: "[HEALTH] Low priority issue",
    });
    const result = makeResult({ generatedIssues: [issue1, issue2] });
    const preview = formatDryRunPreview(result);
    expect(preview).toContain("- [critical] [HEALTH] Critical issue");
    expect(preview).toContain("- [low] [HEALTH] Low priority issue");
    expect(preview).not.toContain("(SKIP:");
  });

  it("renders multiple epic groups", () => {
    const epic1 = makeEpicGroup({
      dimension: "cost-health",
      epicTitle: "[HEALTH] cost-health improvements (1 findings)",
      subIssues: [makeGeneratedIssue({ severity: "high", title: "[HEALTH] Cost issue" })],
    });
    const epic2 = makeEpicGroup({
      dimension: "reliability",
      epicTitle: "[HEALTH] reliability improvements (2 findings)",
      subIssues: [
        makeGeneratedIssue({
          severity: "critical",
          title: "[HEALTH] Reliability issue",
        }),
      ],
    });
    const result = makeResult({ epicGroups: [epic1, epic2] });
    const preview = formatDryRunPreview(result);
    expect(preview).toContain("#### [HEALTH] cost-health improvements (1 findings)");
    expect(preview).toContain("#### [HEALTH] reliability improvements (2 findings)");
    expect(preview).toContain("  - [high] [HEALTH] Cost issue");
    expect(preview).toContain("  - [critical] [HEALTH] Reliability issue");
  });
});
