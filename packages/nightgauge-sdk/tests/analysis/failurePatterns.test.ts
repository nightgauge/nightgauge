import { describe, it, expect } from "vitest";
import {
  detectFailuresByCategory,
  detectRecurringFailures,
  correlateRootCauses,
  computeFailureTrends,
  generateRecommendations,
  computeLinearTrend,
} from "../../src/analysis/failurePatterns.js";
import type { ExecutionHistoryRecord, CostRates } from "../../src/analysis/types.js";
import type {
  FailureTaxonomy,
  FailureFinding,
  FailureCategory,
} from "../../src/analysis/failureTypes.js";

// ── Test fixtures ──────────────────────────────────────────────────

const TEST_COST_RATES: CostRates = {
  inputPerMillion: 3.0,
  outputPerMillion: 15.0,
};

function makeTaxonomy(): FailureTaxonomy {
  return {
    schemaVersion: "1",
    categories: new Map([
      [
        "build-failure",
        {
          category: "build-failure",
          displayName: "Build Failure",
          description: "Build failures",
          patterns: [/error TS\d+/i, /Build failed/i, /SyntaxError/i],
          autoFixable: true,
          typicalRootCauses: ["Type errors from code changes"],
        },
      ],
      [
        "test-failure",
        {
          category: "test-failure",
          displayName: "Test Failure",
          description: "Test failures",
          patterns: [/FAIL\s+.*\.test\./i, /AssertionError/i],
          autoFixable: true,
          typicalRootCauses: ["Implementation changed but tests not updated"],
        },
      ],
      [
        "timeout-transient",
        {
          category: "timeout-transient",
          displayName: "Timeout/Transient Failure",
          description: "Timeouts",
          patterns: [/timeout/i, /ETIMEDOUT/i],
          autoFixable: false,
          typicalRootCauses: ["Network instability"],
        },
      ],
    ]),
  };
}

function makeRecord(
  overrides: Partial<ExecutionHistoryRecord> & { errorText?: string } = {}
): ExecutionHistoryRecord & { errorText?: string } {
  const { errorText, ...rest } = overrides;
  const record: ExecutionHistoryRecord & { errorText?: string } = {
    issueNumber: 1,
    stage: "feature-dev",
    adapter: "claude",
    model: "sonnet",
    success: true,
    retries: 0,
    inputTokens: 1000,
    outputTokens: 500,
    costUsd: 0.1,
    durationMs: 5000,
    timestamp: "2026-01-15T12:00:00Z",
    ...rest,
  };
  if (errorText !== undefined) {
    record.errorText = errorText;
  }
  return record;
}

// ── detectFailuresByCategory ──────────────────────────────────────

describe("detectFailuresByCategory", () => {
  it("returns empty array for no records", () => {
    const taxonomy = makeTaxonomy();
    expect(detectFailuresByCategory([], taxonomy, TEST_COST_RATES)).toEqual([]);
  });

  it("returns empty array when all records are successful", () => {
    const taxonomy = makeTaxonomy();
    const records = [makeRecord({ success: true })] as ExecutionHistoryRecord[];
    expect(detectFailuresByCategory(records, taxonomy, TEST_COST_RATES)).toEqual([]);
  });

  it("classifies failures by taxonomy category", () => {
    const taxonomy = makeTaxonomy();
    const records = [
      makeRecord({
        success: false,
        errorText: "error TS2345: Argument of type",
        issueNumber: 1,
      }),
      makeRecord({
        success: false,
        errorText: "error TS1005: Missing semicolon",
        issueNumber: 2,
      }),
      makeRecord({
        success: false,
        errorText: "FAIL tests/foo.test.ts",
        issueNumber: 3,
      }),
    ] as ExecutionHistoryRecord[];

    const findings = detectFailuresByCategory(records, taxonomy, TEST_COST_RATES);

    expect(findings.length).toBe(2);
    const buildFinding = findings.find((f) => f.category === "build-failure");
    const testFinding = findings.find((f) => f.category === "test-failure");
    expect(buildFinding).toBeDefined();
    expect(buildFinding!.occurrenceCount).toBe(2);
    expect(testFinding).toBeDefined();
    expect(testFinding!.occurrenceCount).toBe(1);
  });

  it("sorts findings by occurrence count descending", () => {
    const taxonomy = makeTaxonomy();
    const records = [
      makeRecord({
        success: false,
        errorText: "FAIL tests/a.test.ts",
        issueNumber: 1,
      }),
      makeRecord({
        success: false,
        errorText: "FAIL tests/b.test.ts",
        issueNumber: 2,
      }),
      makeRecord({
        success: false,
        errorText: "FAIL tests/c.test.ts",
        issueNumber: 3,
      }),
      makeRecord({ success: false, errorText: "error TS2345", issueNumber: 4 }),
    ] as ExecutionHistoryRecord[];

    const findings = detectFailuresByCategory(records, taxonomy, TEST_COST_RATES);
    expect(findings[0].category).toBe("test-failure");
    expect(findings[0].occurrenceCount).toBe(3);
  });

  it("classifies errors without errorText as uncategorized", () => {
    const taxonomy = makeTaxonomy();
    const records = [makeRecord({ success: false })] as ExecutionHistoryRecord[];

    const findings = detectFailuresByCategory(records, taxonomy, TEST_COST_RATES);
    expect(findings.length).toBe(1);
    expect(findings[0].category).toBe("uncategorized");
  });

  it("tracks affected stages and runs", () => {
    const taxonomy = makeTaxonomy();
    const records = [
      makeRecord({
        success: false,
        errorText: "error TS2345",
        issueNumber: 1,
        stage: "feature-dev",
      }),
      makeRecord({
        success: false,
        errorText: "Build failed",
        issueNumber: 2,
        stage: "pr-create",
      }),
    ] as ExecutionHistoryRecord[];

    const findings = detectFailuresByCategory(records, taxonomy, TEST_COST_RATES);
    const buildFinding = findings.find((f) => f.category === "build-failure")!;
    expect(buildFinding.affectedStages).toContain("feature-dev");
    expect(buildFinding.affectedStages).toContain("pr-create");
    expect(buildFinding.affectedRuns).toContain(1);
    expect(buildFinding.affectedRuns).toContain(2);
  });

  it("uses record costUsd when available", () => {
    const taxonomy = makeTaxonomy();
    const records = [
      makeRecord({ success: false, errorText: "error TS2345", costUsd: 0.5 }),
    ] as ExecutionHistoryRecord[];

    const findings = detectFailuresByCategory(records, taxonomy, TEST_COST_RATES);
    expect(findings[0].estimatedCostUsd).toBe(0.5);
  });

  it("sets severity based on taxonomy autoFixable flag", () => {
    const taxonomy = makeTaxonomy();
    const records = [
      makeRecord({ success: false, errorText: "error TS2345" }),
      makeRecord({
        success: false,
        errorText: "timeout exceeded",
        issueNumber: 2,
      }),
    ] as ExecutionHistoryRecord[];

    const findings = detectFailuresByCategory(records, taxonomy, TEST_COST_RATES);
    const buildFinding = findings.find((f) => f.category === "build-failure")!;
    const timeoutFinding = findings.find((f) => f.category === "timeout-transient")!;
    expect(buildFinding.severity).toBe("auto-fixable");
    expect(timeoutFinding.severity).toBe("infrastructure");
  });
});

// ── detectRecurringFailures ───────────────────────────────────────

describe("detectRecurringFailures", () => {
  it("returns empty array for no records", () => {
    const taxonomy = makeTaxonomy();
    expect(detectRecurringFailures([], taxonomy, TEST_COST_RATES)).toEqual([]);
  });

  it("returns empty when no failures recur at threshold", () => {
    const taxonomy = makeTaxonomy();
    const records = [
      makeRecord({
        success: false,
        errorText: "error TS2345 in file A",
        issueNumber: 1,
      }),
      makeRecord({
        success: false,
        errorText: "error TS2345 in file B",
        issueNumber: 2,
      }),
    ] as ExecutionHistoryRecord[];

    // Default threshold is 3, only 2 distinct runs
    const findings = detectRecurringFailures(records, taxonomy, TEST_COST_RATES);
    expect(findings).toEqual([]);
  });

  it("flags patterns appearing in threshold+ distinct runs", () => {
    const taxonomy = makeTaxonomy();
    // Errors differ only by numbers/paths which get normalized
    const records = [
      makeRecord({
        success: false,
        errorText: "error TS2345 in /src/foo.ts line 42",
        issueNumber: 1,
      }),
      makeRecord({
        success: false,
        errorText: "error TS9999 in /src/bar.ts line 100",
        issueNumber: 2,
      }),
      makeRecord({
        success: false,
        errorText: "error TS1234 in /src/baz.ts line 7",
        issueNumber: 3,
      }),
    ] as ExecutionHistoryRecord[];

    const findings = detectRecurringFailures(records, taxonomy, TEST_COST_RATES, 3);
    expect(findings.length).toBe(1);
    expect(findings[0].occurrenceCount).toBe(3);
    expect(findings[0].category).toBe("build-failure");
  });

  it("respects custom threshold", () => {
    const taxonomy = makeTaxonomy();
    const records = [
      makeRecord({
        success: false,
        errorText: "error TS2345 in /src/foo.ts line 42",
        issueNumber: 1,
      }),
      makeRecord({
        success: false,
        errorText: "error TS9999 in /src/bar.ts line 100",
        issueNumber: 2,
      }),
    ] as ExecutionHistoryRecord[];

    const findings = detectRecurringFailures(records, taxonomy, TEST_COST_RATES, 2);
    expect(findings.length).toBe(1);
  });

  it("normalizes error text to group similar errors", () => {
    const taxonomy = makeTaxonomy();
    // Numbers and paths differ but normalized signature is the same
    const records = [
      makeRecord({
        success: false,
        errorText: "error TS2345 in /src/foo.ts line 42",
        issueNumber: 1,
      }),
      makeRecord({
        success: false,
        errorText: "error TS9999 in /src/bar.ts line 100",
        issueNumber: 2,
      }),
      makeRecord({
        success: false,
        errorText: "error TS1234 in /src/baz.ts line 7",
        issueNumber: 3,
      }),
    ] as ExecutionHistoryRecord[];

    const findings = detectRecurringFailures(records, taxonomy, TEST_COST_RATES, 3);
    expect(findings.length).toBe(1);
  });

  it("skips records without errorText", () => {
    const taxonomy = makeTaxonomy();
    const records = [
      makeRecord({ success: false, issueNumber: 1 }),
      makeRecord({ success: false, issueNumber: 2 }),
      makeRecord({ success: false, issueNumber: 3 }),
    ] as ExecutionHistoryRecord[];

    const findings = detectRecurringFailures(records, taxonomy, TEST_COST_RATES, 3);
    expect(findings).toEqual([]);
  });
});

// ── correlateRootCauses ───────────────────────────────────────────

describe("correlateRootCauses", () => {
  it("returns empty map for no records", () => {
    const taxonomy = makeTaxonomy();
    expect(correlateRootCauses([], taxonomy).size).toBe(0);
  });

  it("returns empty map when all records are successful", () => {
    const taxonomy = makeTaxonomy();
    const records = [makeRecord({ success: true })] as ExecutionHistoryRecord[];
    expect(correlateRootCauses(records, taxonomy).size).toBe(0);
  });

  it("detects stage correlation when failures concentrate in one stage", () => {
    const taxonomy = makeTaxonomy();
    // 10 records across stages, but build failures all in feature-dev
    const records = [
      ...Array.from({ length: 5 }, (_, i) =>
        makeRecord({ success: true, stage: "pr-create", issueNumber: i + 1 })
      ),
      ...Array.from({ length: 5 }, (_, i) =>
        makeRecord({
          success: false,
          errorText: "error TS2345",
          stage: "feature-dev",
          issueNumber: i + 10,
        })
      ),
    ] as ExecutionHistoryRecord[];

    const correlations = correlateRootCauses(records, taxonomy);
    const buildCorrelation = correlations.get("build-failure");
    expect(buildCorrelation).toBeDefined();
    // All build failures in feature-dev (100%) vs 50% baseline
    const stageFactor = buildCorrelation!.correlatedFactors.find((f) =>
      f.factor.startsWith("stage:")
    );
    expect(stageFactor).toBeDefined();
  });

  it("detects complexity correlation when failures cluster in high complexity", () => {
    const taxonomy = makeTaxonomy();
    const records = [
      ...Array.from({ length: 5 }, (_, i) =>
        makeRecord({
          success: true,
          complexityScore: 2,
          issueNumber: i + 1,
        })
      ),
      ...Array.from({ length: 5 }, (_, i) =>
        makeRecord({
          success: false,
          errorText: "error TS2345",
          complexityScore: 8,
          issueNumber: i + 10,
        })
      ),
    ] as ExecutionHistoryRecord[];

    const correlations = correlateRootCauses(records, taxonomy);
    const buildCorrelation = correlations.get("build-failure");
    expect(buildCorrelation).toBeDefined();
    const complexityFactor = buildCorrelation!.correlatedFactors.find((f) =>
      f.factor.startsWith("complexity:")
    );
    expect(complexityFactor).toBeDefined();
    expect(complexityFactor!.factor).toBe("complexity:high");
  });
});

// ── computeFailureTrends ──────────────────────────────────────────

describe("computeFailureTrends", () => {
  it("returns stable for empty records", () => {
    const taxonomy = makeTaxonomy();
    const result = computeFailureTrends([], taxonomy);
    expect(result.overall).toBe("stable");
    expect(result.perCategory.size).toBe(0);
  });

  it("returns stable for single week of data", () => {
    const taxonomy = makeTaxonomy();
    const records = [
      makeRecord({
        timestamp: "2026-01-15T12:00:00Z",
        success: false,
        errorText: "error TS2345",
      }),
    ] as ExecutionHistoryRecord[];
    const result = computeFailureTrends(records, taxonomy);
    expect(result.overall).toBe("stable");
  });

  it("detects worsening trend when failures increase over time", () => {
    const taxonomy = makeTaxonomy();
    const records: (ExecutionHistoryRecord & { errorText?: string })[] = [];
    // Week 1: 1 failure out of 5
    for (let i = 0; i < 5; i++) {
      records.push(
        makeRecord({
          timestamp: "2026-01-06T12:00:00Z",
          success: i >= 1,
          errorText: i < 1 ? "error TS2345" : undefined,
          issueNumber: i + 1,
        })
      );
    }
    // Week 2: 3 failures out of 5
    for (let i = 0; i < 5; i++) {
      records.push(
        makeRecord({
          timestamp: "2026-01-13T12:00:00Z",
          success: i >= 3,
          errorText: i < 3 ? "error TS2345" : undefined,
          issueNumber: i + 10,
        })
      );
    }
    // Week 3: 5 failures out of 5
    for (let i = 0; i < 5; i++) {
      records.push(
        makeRecord({
          timestamp: "2026-01-20T12:00:00Z",
          success: false,
          errorText: "error TS2345",
          issueNumber: i + 20,
        })
      );
    }

    const result = computeFailureTrends(records as ExecutionHistoryRecord[], taxonomy);
    expect(result.overall).toBe("worsening");
  });

  it("detects improving trend when failures decrease over time", () => {
    const taxonomy = makeTaxonomy();
    const records: (ExecutionHistoryRecord & { errorText?: string })[] = [];
    // Week 1: 5 failures out of 5
    for (let i = 0; i < 5; i++) {
      records.push(
        makeRecord({
          timestamp: "2026-01-06T12:00:00Z",
          success: false,
          errorText: "error TS2345",
          issueNumber: i + 1,
        })
      );
    }
    // Week 2: 2 failures out of 5
    for (let i = 0; i < 5; i++) {
      records.push(
        makeRecord({
          timestamp: "2026-01-13T12:00:00Z",
          success: i >= 2,
          errorText: i < 2 ? "error TS2345" : undefined,
          issueNumber: i + 10,
        })
      );
    }
    // Week 3: 0 failures out of 5
    for (let i = 0; i < 5; i++) {
      records.push(
        makeRecord({
          timestamp: "2026-01-20T12:00:00Z",
          success: true,
          issueNumber: i + 20,
        })
      );
    }

    const result = computeFailureTrends(records as ExecutionHistoryRecord[], taxonomy);
    expect(result.overall).toBe("improving");
  });
});

// ── generateRecommendations ───────────────────────────────────────

describe("generateRecommendations", () => {
  it("returns same array with recommendations populated", () => {
    const taxonomy = makeTaxonomy();
    const findings: FailureFinding[] = [
      {
        category: "build-failure",
        severity: "auto-fixable",
        title: "Build Failure (5 occurrences)",
        description: "5 failures",
        occurrenceCount: 5,
        affectedStages: ["feature-dev"],
        affectedRuns: [1, 2, 3, 4, 5],
        estimatedCostUsd: 1.0,
        rootCauseCorrelation: { correlatedFactors: [] },
        recommendation: "",
        trend: "stable",
        evidence: {},
      },
    ];

    const result = generateRecommendations(findings, taxonomy);
    expect(result).toBe(findings); // same reference
    expect(result[0].recommendation).not.toBe("");
    expect(result[0].recommendation).toContain("auto-fixable");
    expect(result[0].recommendation).toContain("Type errors");
  });

  it("includes urgency note for worsening trends", () => {
    const taxonomy = makeTaxonomy();
    const findings: FailureFinding[] = [
      {
        category: "build-failure",
        severity: "auto-fixable",
        title: "Build Failure",
        description: "5 failures",
        occurrenceCount: 5,
        affectedStages: ["feature-dev", "pr-create"],
        affectedRuns: [1, 2, 3, 4, 5],
        estimatedCostUsd: 1.0,
        rootCauseCorrelation: { correlatedFactors: [] },
        recommendation: "",
        trend: "worsening",
        evidence: {},
      },
    ];

    generateRecommendations(findings, taxonomy);
    expect(findings[0].recommendation).toContain("worsening");
    expect(findings[0].recommendation).toContain("urgently");
  });

  it("includes high frequency note for 10+ occurrences", () => {
    const taxonomy = makeTaxonomy();
    const findings: FailureFinding[] = [
      {
        category: "build-failure",
        severity: "auto-fixable",
        title: "Build Failure",
        description: "many failures",
        occurrenceCount: 15,
        affectedStages: ["feature-dev"],
        affectedRuns: Array.from({ length: 15 }, (_, i) => i + 1),
        estimatedCostUsd: 5.0,
        rootCauseCorrelation: { correlatedFactors: [] },
        recommendation: "",
        trend: "stable",
        evidence: {},
      },
    ];

    generateRecommendations(findings, taxonomy);
    expect(findings[0].recommendation).toContain("High frequency");
    expect(findings[0].recommendation).toContain("15 occurrences");
  });

  it("provides infrastructure advice for infrastructure severity", () => {
    const taxonomy = makeTaxonomy();
    const findings: FailureFinding[] = [
      {
        category: "timeout-transient",
        severity: "infrastructure",
        title: "Timeout",
        description: "timeouts",
        occurrenceCount: 3,
        affectedStages: ["feature-dev"],
        affectedRuns: [1, 2, 3],
        estimatedCostUsd: 0.5,
        rootCauseCorrelation: { correlatedFactors: [] },
        recommendation: "",
        trend: "stable",
        evidence: {},
      },
    ];

    generateRecommendations(findings, taxonomy);
    expect(findings[0].recommendation).toContain("infrastructure");
    expect(findings[0].recommendation).toContain("retry");
  });

  it("provides fallback recommendation for uncategorized findings", () => {
    const taxonomy = makeTaxonomy();
    const findings: FailureFinding[] = [
      {
        category: "uncategorized",
        severity: "manual-fix",
        title: "Unknown",
        description: "unknown failures",
        occurrenceCount: 2,
        affectedStages: ["feature-dev", "pr-create"],
        affectedRuns: [1, 2],
        estimatedCostUsd: 0.2,
        rootCauseCorrelation: { correlatedFactors: [] },
        recommendation: "",
        trend: "stable",
        evidence: {},
      },
    ];

    generateRecommendations(findings, taxonomy);
    expect(findings[0].recommendation).toContain("uncategorized");
  });
});

// ── computeLinearTrend ────────────────────────────────────────────

describe("computeLinearTrend", () => {
  const cases: Array<{ name: string; values: number[]; expected: string }> = [
    {
      name: "returns stable for single value",
      values: [0.5],
      expected: "stable",
    },
    { name: "returns stable for empty array", values: [], expected: "stable" },
    {
      name: "returns stable for flat values",
      values: [0.5, 0.5, 0.5, 0.5],
      expected: "stable",
    },
    {
      name: "returns worsening for increasing values",
      values: [0.0, 0.2, 0.4, 0.6, 0.8, 1.0],
      expected: "worsening",
    },
    {
      name: "returns improving for decreasing values",
      values: [1.0, 0.8, 0.6, 0.4, 0.2, 0.0],
      expected: "improving",
    },
    {
      name: "returns stable for small slope",
      values: [0.5, 0.51, 0.52, 0.5, 0.51],
      expected: "stable",
    },
  ];

  cases.forEach(({ name, values, expected }) => {
    it(name, () => {
      expect(computeLinearTrend(values)).toBe(expected);
    });
  });
});
