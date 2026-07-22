/**
 * Unit tests for FindingToIssueEngine (Issue #1102)
 *
 * Tests the deterministic finding-to-issue conversion logic.
 * Mocks `node:child_process` via vi.mock to avoid real shell calls.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("node:child_process", () => ({
  execSync: vi.fn(),
}));

import { execSync } from "node:child_process";
const mockExecSync = vi.mocked(execSync);

import { FindingToIssueEngine } from "../../../src/analysis/health/FindingToIssueEngine.js";
import type {
  HealthAnalysisResult,
  HealthAnalysisConfig,
  DimensionResult,
  Finding,
  CrossReference,
} from "../../../src/analysis/health/types.js";
import { DEFAULT_HEALTH_CONFIG } from "../../../src/analysis/health/types.js";

// ── Factories ─────────────────────────────────────────────────────

function makeFinding(overrides: Partial<Finding> = {}): Finding {
  return {
    id: "finding-001",
    dimension: "token-economics",
    severity: "high",
    title: "High token usage detected",
    description: "Token usage is above expected thresholds.",
    impact: "Increased operational cost.",
    recommendation: "Optimize prompt length and context window usage.",
    evidence: { avgTokensPerRun: 5000, threshold: 3000 },
    confidence: "high",
    ...overrides,
  };
}

function makeDimensionResult(
  dimension: DimensionResult["dimension"],
  findings: Finding[]
): DimensionResult {
  return {
    dimension,
    score: 60,
    status: "fair",
    findings,
    metrics: {},
    hasEnoughData: true,
    sampleSize: 10,
  };
}

function makeAnalysisResult(
  findings: Finding[],
  crossRefs: CrossReference[] = []
): HealthAnalysisResult {
  // Group findings by dimension so each dimension slot holds its own findings
  const dimensionMap: Partial<Record<string, Finding[]>> = {};
  for (const f of findings) {
    if (!dimensionMap[f.dimension]) {
      dimensionMap[f.dimension] = [];
    }
    dimensionMap[f.dimension]!.push(f);
  }

  const dimensions: HealthAnalysisResult["dimensions"] = {};
  for (const [dim, dimFindings] of Object.entries(dimensionMap)) {
    dimensions[dim as DimensionResult["dimension"]] = makeDimensionResult(
      dim as DimensionResult["dimension"],
      dimFindings!
    );
  }

  return {
    dimensions,
    crossReferences: crossRefs,
    overallScore: 65,
    overallStatus: "fair",
    summary: "Test analysis summary.",
    analyzedAt: "2026-02-20T00:00:00Z",
    config: DEFAULT_HEALTH_CONFIG as HealthAnalysisConfig,
  };
}

// ── Dry-run mode ──────────────────────────────────────────────────

describe("FindingToIssueEngine — dry-run mode", () => {
  beforeEach(() => {
    mockExecSync.mockReset();
  });

  it("returns an empty result when there are no findings", async () => {
    const engine = new FindingToIssueEngine({ dryRun: true });
    const result = await engine.generate(makeAnalysisResult([]));

    expect(result.totalFindings).toBe(0);
    expect(result.filteredFindings).toBe(0);
    expect(result.generatedIssues).toHaveLength(0);
    expect(result.epicGroups).toHaveLength(0);
    expect(result.issuesCreated).toBe(0);
    expect(result.epicsCreated).toBe(0);
    expect(result.duplicatesSkipped).toBe(0);
    expect(result.dryRun).toBe(true);
  });

  it("filters out findings below the severity threshold", async () => {
    const engine = new FindingToIssueEngine({
      dryRun: true,
      severityThreshold: "high",
    });
    const belowThreshold = makeFinding({
      id: "finding-low",
      severity: "medium",
    });
    const result = await engine.generate(makeAnalysisResult([belowThreshold]));

    expect(result.totalFindings).toBe(1);
    expect(result.filteredFindings).toBe(0);
    expect(result.generatedIssues).toHaveLength(0);
  });

  it("includes findings that exactly meet the severity threshold", async () => {
    const engine = new FindingToIssueEngine({
      dryRun: true,
      severityThreshold: "high",
    });
    const atThreshold = makeFinding({ id: "finding-high", severity: "high" });
    const result = await engine.generate(makeAnalysisResult([atThreshold]));

    expect(result.filteredFindings).toBe(1);
    expect(result.generatedIssues).toHaveLength(1);
  });

  it("includes findings above the severity threshold (critical > high)", async () => {
    const engine = new FindingToIssueEngine({
      dryRun: true,
      severityThreshold: "high",
    });
    const critical = makeFinding({ id: "finding-crit", severity: "critical" });
    const result = await engine.generate(makeAnalysisResult([critical]));

    expect(result.filteredFindings).toBe(1);
    expect(result.generatedIssues).toHaveLength(1);
  });

  it("produces a single standalone GeneratedIssue for one above-threshold finding", async () => {
    const engine = new FindingToIssueEngine({
      dryRun: true,
      severityThreshold: "high",
    });
    const finding = makeFinding({ id: "finding-standalone", severity: "high" });
    const result = await engine.generate(makeAnalysisResult([finding]));

    expect(result.generatedIssues).toHaveLength(1);
    expect(result.epicGroups).toHaveLength(0);

    const issue = result.generatedIssues[0];
    expect(issue.findingId).toBe("finding-standalone");
    expect(issue.issueNumber).toBeUndefined();
    expect(issue.skipped).toBeUndefined();
    expect(issue.title).toMatch(/^\[HEALTH\]/);
    expect(issue.labels).toHaveLength(4);
    expect(issue.severity).toBe("high");
    expect(issue.dimension).toBe("token-economics");
  });

  it("creates an epic group when 3 or more findings share a dimension", async () => {
    const engine = new FindingToIssueEngine({
      dryRun: true,
      severityThreshold: "high",
      epicGroupingThreshold: 3,
    });
    const findings = [
      makeFinding({
        id: "f1",
        severity: "high",
        dimension: "cost-health",
        title: "Cost issue one",
      }),
      makeFinding({
        id: "f2",
        severity: "high",
        dimension: "cost-health",
        title: "Cost issue two",
      }),
      makeFinding({
        id: "f3",
        severity: "high",
        dimension: "cost-health",
        title: "Cost issue three",
      }),
    ];
    const result = await engine.generate(makeAnalysisResult(findings));

    expect(result.epicGroups).toHaveLength(1);
    // Sub-issues from epic groups are also pushed into generatedIssues
    expect(result.generatedIssues).toHaveLength(3);

    const group = result.epicGroups[0];
    expect(group.dimension).toBe("cost-health");
    expect(group.subIssues).toHaveLength(3);
    expect(group.epicNumber).toBeUndefined();
    expect(group.epicTitle).toMatch(/cost-health/);
    expect(group.epicLabels).toContain("type:epic");
    expect(group.epicLabels).toContain("component:health-cost-health");

    for (const sub of group.subIssues) {
      expect(sub.issueNumber).toBeUndefined();
      expect(sub.skipped).toBeUndefined();
      expect(sub.epicNumber).toBeUndefined();
    }
  });

  it("treats exactly epicGroupingThreshold count as an epic, one fewer as standalone", async () => {
    const threshold = 3;

    const engineEpic = new FindingToIssueEngine({
      dryRun: true,
      severityThreshold: "high",
      epicGroupingThreshold: threshold,
    });
    const threeFindings = [
      makeFinding({
        id: "a1",
        severity: "high",
        dimension: "reliability",
        title: "Rel A",
      }),
      makeFinding({
        id: "a2",
        severity: "high",
        dimension: "reliability",
        title: "Rel B",
      }),
      makeFinding({
        id: "a3",
        severity: "high",
        dimension: "reliability",
        title: "Rel C",
      }),
    ];
    const epicResult = await engineEpic.generate(makeAnalysisResult(threeFindings));
    expect(epicResult.epicGroups).toHaveLength(1);
    // Sub-issues from the epic are pushed into generatedIssues as well
    expect(epicResult.generatedIssues).toHaveLength(3);

    const engineStandalone = new FindingToIssueEngine({
      dryRun: true,
      severityThreshold: "high",
      epicGroupingThreshold: threshold,
    });
    const twoFindings = [
      makeFinding({
        id: "b1",
        severity: "high",
        dimension: "reliability",
        title: "Rel X",
      }),
      makeFinding({
        id: "b2",
        severity: "high",
        dimension: "reliability",
        title: "Rel Y",
      }),
    ];
    const standaloneResult = await engineStandalone.generate(makeAnalysisResult(twoFindings));
    expect(standaloneResult.epicGroups).toHaveLength(0);
    expect(standaloneResult.generatedIssues).toHaveLength(2);
  });

  it("handles mixed dimensions: some grouped as epics, some standalone", async () => {
    const engine = new FindingToIssueEngine({
      dryRun: true,
      severityThreshold: "high",
      epicGroupingThreshold: 3,
    });

    // Three in cost-health → epic
    const epicFindings = [
      makeFinding({
        id: "c1",
        severity: "high",
        dimension: "cost-health",
        title: "Cost A",
      }),
      makeFinding({
        id: "c2",
        severity: "high",
        dimension: "cost-health",
        title: "Cost B",
      }),
      makeFinding({
        id: "c3",
        severity: "high",
        dimension: "cost-health",
        title: "Cost C",
      }),
    ];

    // Two in reliability → standalone
    const standaloneFindings = [
      makeFinding({
        id: "r1",
        severity: "high",
        dimension: "reliability",
        title: "Rel A",
      }),
      makeFinding({
        id: "r2",
        severity: "high",
        dimension: "reliability",
        title: "Rel B",
      }),
    ];

    const result = await engine.generate(
      makeAnalysisResult([...epicFindings, ...standaloneFindings])
    );

    expect(result.epicGroups).toHaveLength(1);
    expect(result.epicGroups[0].dimension).toBe("cost-health");
    expect(result.epicGroups[0].subIssues).toHaveLength(3);

    // generatedIssues contains epic sub-issues (3) + standalone issues (2) = 5
    expect(result.generatedIssues).toHaveLength(5);

    // The standalone (non-epic) issues belong to reliability
    const standaloneIssues = result.generatedIssues.filter(
      (issue) => issue.dimension === "reliability"
    );
    expect(standaloneIssues).toHaveLength(2);
  });

  it("attaches related cross-references to finding issue bodies", async () => {
    const engine = new FindingToIssueEngine({
      dryRun: true,
      severityThreshold: "high",
    });

    const finding = makeFinding({
      id: "finding-with-crossref",
      severity: "high",
    });
    const crossRef: CrossReference = {
      id: "cr-001",
      dimensions: ["token-economics", "cost-health"],
      severity: "high",
      title: "Token-cost correlation",
      description: "High token usage correlates with cost spikes.",
      correlatedFindings: ["finding-with-crossref"],
      confidence: "medium",
      evidence: {},
    };

    const result = await engine.generate(makeAnalysisResult([finding], [crossRef]));

    expect(result.generatedIssues).toHaveLength(1);
    const issue = result.generatedIssues[0];
    expect(issue.body).toContain("Cross-References");
    expect(issue.body).toContain("Token-cost correlation");
  });

  it("does not attach cross-references that do not correlate to the finding", async () => {
    const engine = new FindingToIssueEngine({
      dryRun: true,
      severityThreshold: "high",
    });

    const finding = makeFinding({
      id: "finding-no-crossref",
      severity: "high",
    });
    const unrelatedCrossRef: CrossReference = {
      id: "cr-unrelated",
      dimensions: ["reliability"],
      severity: "medium",
      title: "Unrelated issue",
      description: "This cross-ref does not correlate to our finding.",
      correlatedFindings: ["some-other-finding-id"],
      confidence: "low",
      evidence: {},
    };

    const result = await engine.generate(makeAnalysisResult([finding], [unrelatedCrossRef]));

    expect(result.generatedIssues[0].body).not.toContain("Cross-References");
  });

  it("never calls execSync in dry-run mode", async () => {
    const engine = new FindingToIssueEngine({
      dryRun: true,
      severityThreshold: "high",
      epicGroupingThreshold: 3,
    });
    const findings = [
      makeFinding({
        id: "x1",
        severity: "high",
        dimension: "cost-health",
        title: "C1",
      }),
      makeFinding({
        id: "x2",
        severity: "high",
        dimension: "cost-health",
        title: "C2",
      }),
      makeFinding({
        id: "x3",
        severity: "high",
        dimension: "cost-health",
        title: "C3",
      }),
      makeFinding({
        id: "x4",
        severity: "critical",
        dimension: "reliability",
        title: "R1",
      }),
    ];
    await engine.generate(makeAnalysisResult(findings));

    expect(mockExecSync).not.toHaveBeenCalled();
  });

  it("tracks totalFindings vs filteredFindings correctly", async () => {
    const engine = new FindingToIssueEngine({
      dryRun: true,
      severityThreshold: "high",
    });
    const findings = [
      makeFinding({ id: "above1", severity: "critical" }),
      makeFinding({
        id: "above2",
        severity: "high",
        dimension: "reliability",
        title: "High finding",
      }),
      makeFinding({
        id: "below1",
        severity: "medium",
        dimension: "cost-health",
        title: "Medium finding",
      }),
      makeFinding({
        id: "below2",
        severity: "low",
        dimension: "model-routing",
        title: "Low finding",
      }),
    ];
    const result = await engine.generate(makeAnalysisResult(findings));

    expect(result.totalFindings).toBe(4);
    expect(result.filteredFindings).toBe(2);
  });

  it("attaches healthReportRef to the result", async () => {
    const engine = new FindingToIssueEngine({ dryRun: true });
    const result = await engine.generate(makeAnalysisResult([]), "health-report-2026-02-20.json");
    expect(result.healthReportRef).toBe("health-report-2026-02-20.json");
  });

  it("produces an issue body containing the healthReportRef", async () => {
    const engine = new FindingToIssueEngine({
      dryRun: true,
      severityThreshold: "high",
    });
    const finding = makeFinding({ id: "ref-test", severity: "high" });
    const result = await engine.generate(makeAnalysisResult([finding]), "my-report.json");
    expect(result.generatedIssues[0].body).toContain("my-report.json");
  });
});

// ── Non-dry-run mode ──────────────────────────────────────────────

describe("FindingToIssueEngine — non-dry-run mode", () => {
  beforeEach(() => {
    mockExecSync.mockReset();
  });

  /**
   * Default mock responses:
   *
   *   gh issue list --search  → '[]' (no duplicates)
   *   gh issue create         → 'https://github.com/owner/repo/issues/123'
   *   add-to-project.sh       → '{}'
   *   create-sub-issue.sh     → JSON with issue_number / issue_url
   */
  function setupDefaultMocks(): void {
    mockExecSync.mockImplementation((cmd: unknown) => {
      const command = String(cmd);
      if (command.includes("gh issue list --search")) {
        return "[]";
      }
      if (command.includes("gh issue create")) {
        return "https://github.com/owner/repo/issues/123\n";
      }
      if (command.includes("create-sub-issue.sh")) {
        return JSON.stringify({
          issue_number: 124,
          issue_url: "https://github.com/owner/repo/issues/124",
          parent_number: 123,
          sync_status: "synced",
        });
      }
      if (command.includes("add-to-project.sh")) {
        return "{}";
      }
      if (command.includes("gh repo view")) {
        return "https://github.com/owner/repo";
      }
      return "";
    });
  }

  it("creates a standalone issue and calls gh issue create + add-to-project.sh", async () => {
    setupDefaultMocks();

    const engine = new FindingToIssueEngine({
      dryRun: false,
      severityThreshold: "high",
      hooksDir: "/fake/hooks",
      repository: "owner/repo",
    });
    const finding = makeFinding({ id: "standalone-issue", severity: "high" });
    const result = await engine.generate(makeAnalysisResult([finding]));

    expect(result.generatedIssues).toHaveLength(1);
    expect(result.issuesCreated).toBe(1);
    expect(result.dryRun).toBe(false);

    const issue = result.generatedIssues[0];
    expect(issue.issueNumber).toBe(123);
    expect(issue.issueUrl).toContain("/issues/123");
    expect(issue.skipped).toBeUndefined();

    // Verify gh issue create was called
    const calls = mockExecSync.mock.calls.map((c) => String(c[0]));
    expect(calls.some((c) => c.includes("gh issue create"))).toBe(true);
    // Verify add-to-project.sh was called
    expect(calls.some((c) => c.includes("add-to-project.sh"))).toBe(true);
  });

  it("creates an epic and sub-issues calling gh issue create + create-sub-issue.sh", async () => {
    let issueCreateCallCount = 0;
    mockExecSync.mockImplementation((cmd: unknown) => {
      const command = String(cmd);
      if (command.includes("gh issue list --search")) {
        return "[]";
      }
      if (command.includes("gh issue create")) {
        issueCreateCallCount++;
        // First call = epic (123), subsequent calls won't happen for sub-issues
        return "https://github.com/owner/repo/issues/123\n";
      }
      if (command.includes("create-sub-issue.sh")) {
        const subNum = 130 + issueCreateCallCount;
        return JSON.stringify({
          issue_number: subNum,
          issue_url: `https://github.com/owner/repo/issues/${subNum}`,
          parent_number: 123,
          sync_status: "synced",
        });
      }
      if (command.includes("add-to-project.sh")) {
        return "{}";
      }
      if (command.includes("gh repo view")) {
        return "https://github.com/owner/repo";
      }
      return "";
    });

    const engine = new FindingToIssueEngine({
      dryRun: false,
      severityThreshold: "high",
      epicGroupingThreshold: 3,
      hooksDir: "/fake/hooks",
      repository: "owner/repo",
    });

    const findings = [
      makeFinding({
        id: "e1",
        severity: "high",
        dimension: "cost-health",
        title: "Cost A",
      }),
      makeFinding({
        id: "e2",
        severity: "high",
        dimension: "cost-health",
        title: "Cost B",
      }),
      makeFinding({
        id: "e3",
        severity: "high",
        dimension: "cost-health",
        title: "Cost C",
      }),
    ];

    const result = await engine.generate(makeAnalysisResult(findings));

    expect(result.epicGroups).toHaveLength(1);
    const group = result.epicGroups[0];
    expect(group.epicNumber).toBe(123);
    expect(group.subIssues).toHaveLength(3);
    expect(result.epicsCreated).toBe(1);

    const calls = mockExecSync.mock.calls.map((c) => String(c[0]));
    // Epic creation uses gh issue create
    expect(calls.some((c) => c.includes("gh issue create"))).toBe(true);
    // Sub-issues use create-sub-issue.sh
    expect(calls.some((c) => c.includes("create-sub-issue.sh"))).toBe(true);
  });

  it("skips a standalone issue when the deduplication check finds an existing match", async () => {
    mockExecSync.mockImplementation((cmd: unknown) => {
      const command = String(cmd);
      if (command.includes("gh issue list --search")) {
        return JSON.stringify([{ number: 99, title: "[HEALTH] High token usage detected" }]);
      }
      if (command.includes("gh repo view")) {
        return "https://github.com/owner/repo";
      }
      return "";
    });

    const engine = new FindingToIssueEngine({
      dryRun: false,
      severityThreshold: "high",
      hooksDir: "/fake/hooks",
      repository: "owner/repo",
    });
    const finding = makeFinding({ id: "dup-finding", severity: "high" });
    const result = await engine.generate(makeAnalysisResult([finding]));

    expect(result.duplicatesSkipped).toBe(1);
    expect(result.issuesCreated).toBe(0);
    expect(result.generatedIssues).toHaveLength(1);

    const issue = result.generatedIssues[0];
    expect(issue.skipped).toBe(true);
    expect(issue.skipReason).toMatch(/[Dd]uplicate/);
    expect(issue.issueNumber).toBeUndefined();

    // gh issue create should NOT have been called
    const calls = mockExecSync.mock.calls.map((c) => String(c[0]));
    expect(calls.some((c) => c.includes("gh issue create"))).toBe(false);
  });

  it("skips a sub-issue when deduplication finds an existing match for it", async () => {
    let searchCallCount = 0;
    mockExecSync.mockImplementation((cmd: unknown) => {
      const command = String(cmd);
      if (command.includes("gh issue list --search")) {
        searchCallCount++;
        // First sub-issue → duplicate; remaining → no duplicate
        if (searchCallCount === 1) {
          return JSON.stringify([{ number: 77, title: "[HEALTH] Cost A" }]);
        }
        return "[]";
      }
      if (command.includes("gh issue create")) {
        return "https://github.com/owner/repo/issues/123\n";
      }
      if (command.includes("create-sub-issue.sh")) {
        return JSON.stringify({
          issue_number: 200,
          issue_url: "https://github.com/owner/repo/issues/200",
          parent_number: 123,
          sync_status: "synced",
        });
      }
      if (command.includes("add-to-project.sh")) {
        return "{}";
      }
      if (command.includes("gh repo view")) {
        return "https://github.com/owner/repo";
      }
      return "";
    });

    const engine = new FindingToIssueEngine({
      dryRun: false,
      severityThreshold: "high",
      epicGroupingThreshold: 3,
      hooksDir: "/fake/hooks",
      repository: "owner/repo",
    });

    const findings = [
      makeFinding({
        id: "d1",
        severity: "high",
        dimension: "cost-health",
        title: "Cost A",
      }),
      makeFinding({
        id: "d2",
        severity: "high",
        dimension: "cost-health",
        title: "Cost B",
      }),
      makeFinding({
        id: "d3",
        severity: "high",
        dimension: "cost-health",
        title: "Cost C",
      }),
    ];

    const result = await engine.generate(makeAnalysisResult(findings));

    expect(result.epicGroups).toHaveLength(1);
    const group = result.epicGroups[0];
    // One sub-issue should be skipped
    const skipped = group.subIssues.filter((s) => s.skipped);
    const created = group.subIssues.filter((s) => !s.skipped);
    expect(skipped).toHaveLength(1);
    expect(created).toHaveLength(2);
    expect(result.duplicatesSkipped).toBe(1);
  });

  it("is resilient when gh issue list --search throws — proceeds without dedup", async () => {
    mockExecSync.mockImplementation((cmd: unknown) => {
      const command = String(cmd);
      if (command.includes("gh issue list --search")) {
        throw new Error("gh: command not found");
      }
      if (command.includes("gh issue create")) {
        return "https://github.com/owner/repo/issues/123\n";
      }
      if (command.includes("add-to-project.sh")) {
        return "{}";
      }
      if (command.includes("gh repo view")) {
        return "https://github.com/owner/repo";
      }
      return "";
    });

    const engine = new FindingToIssueEngine({
      dryRun: false,
      severityThreshold: "high",
      hooksDir: "/fake/hooks",
      repository: "owner/repo",
    });
    const finding = makeFinding({ id: "resilient", severity: "high" });

    // Should not throw; issue should still be created
    const result = await engine.generate(makeAnalysisResult([finding]));
    expect(result.generatedIssues).toHaveLength(1);
    expect(result.generatedIssues[0].issueNumber).toBe(123);
  });

  it("is resilient when gh issue create throws — returns issue without number", async () => {
    mockExecSync.mockImplementation((cmd: unknown) => {
      const command = String(cmd);
      if (command.includes("gh issue list --search")) {
        return "[]";
      }
      if (command.includes("gh issue create")) {
        throw new Error("gh: API error");
      }
      if (command.includes("gh repo view")) {
        return "https://github.com/owner/repo";
      }
      return "";
    });

    const engine = new FindingToIssueEngine({
      dryRun: false,
      severityThreshold: "high",
      hooksDir: "/fake/hooks",
      repository: "owner/repo",
    });
    const finding = makeFinding({ id: "create-fails", severity: "high" });

    const result = await engine.generate(makeAnalysisResult([finding]));
    expect(result.generatedIssues).toHaveLength(1);
    // Issue number should be absent when creation fails
    expect(result.generatedIssues[0].issueNumber).toBeUndefined();
    expect(result.issuesCreated).toBe(0);
  });

  it("passes --repo flag to gh commands when repository config is set", async () => {
    setupDefaultMocks();

    const engine = new FindingToIssueEngine({
      dryRun: false,
      severityThreshold: "high",
      hooksDir: "/fake/hooks",
      repository: "myorg/myrepo",
    });
    const finding = makeFinding({ id: "repo-flag", severity: "high" });
    await engine.generate(makeAnalysisResult([finding]));

    const calls = mockExecSync.mock.calls.map((c) => String(c[0]));
    const ghCreateCall = calls.find((c) => c.includes("gh issue create"));
    expect(ghCreateCall).toBeDefined();
    expect(ghCreateCall).toContain("--repo myorg/myrepo");
  });

  it("uses the configured hooksDir for script paths", async () => {
    setupDefaultMocks();

    const engine = new FindingToIssueEngine({
      dryRun: false,
      severityThreshold: "high",
      hooksDir: "/custom/hooks/dir",
    });
    const finding = makeFinding({ id: "hooks-dir", severity: "high" });
    await engine.generate(makeAnalysisResult([finding]));

    const calls = mockExecSync.mock.calls.map((c) => String(c[0]));
    const projectCall = calls.find((c) => c.includes("add-to-project.sh"));
    expect(projectCall).toBeDefined();
    expect(projectCall).toContain("/custom/hooks/dir/add-to-project.sh");
  });

  it("counts issuesCreated and epicsCreated correctly in a mixed scenario", async () => {
    let subIssueCounter = 200;
    mockExecSync.mockImplementation((cmd: unknown) => {
      const command = String(cmd);
      if (command.includes("gh issue list --search")) {
        return "[]";
      }
      if (command.includes("gh issue create")) {
        return "https://github.com/owner/repo/issues/123\n";
      }
      if (command.includes("create-sub-issue.sh")) {
        subIssueCounter++;
        return JSON.stringify({
          issue_number: subIssueCounter,
          issue_url: `https://github.com/owner/repo/issues/${subIssueCounter}`,
          parent_number: 123,
          sync_status: "synced",
        });
      }
      if (command.includes("add-to-project.sh")) {
        return "{}";
      }
      if (command.includes("gh repo view")) {
        return "https://github.com/owner/repo";
      }
      return "";
    });

    const engine = new FindingToIssueEngine({
      dryRun: false,
      severityThreshold: "high",
      epicGroupingThreshold: 3,
      hooksDir: "/fake/hooks",
      repository: "owner/repo",
    });

    // 3 in cost-health → epic; 1 in reliability → standalone
    const findings = [
      makeFinding({
        id: "e1",
        severity: "high",
        dimension: "cost-health",
        title: "Cost A",
      }),
      makeFinding({
        id: "e2",
        severity: "high",
        dimension: "cost-health",
        title: "Cost B",
      }),
      makeFinding({
        id: "e3",
        severity: "high",
        dimension: "cost-health",
        title: "Cost C",
      }),
      makeFinding({
        id: "r1",
        severity: "high",
        dimension: "reliability",
        title: "Rel A",
      }),
    ];

    const result = await engine.generate(makeAnalysisResult(findings));

    expect(result.epicsCreated).toBe(1);
    // 3 sub-issues + 1 standalone = 4 issues created
    expect(result.issuesCreated).toBe(4);
  });
});
