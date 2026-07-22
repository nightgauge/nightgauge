import { describe, it, expect } from "vitest";
import { TokenEfficiencyAnalyzer } from "../../src/analysis/TokenEfficiencyAnalyzer.js";
import type {
  ExecutionHistoryRecord,
  ExecutionHistoryRecordExtended,
} from "../../src/analysis/types.js";

// --- Test data factories ---

function makeRecord(overrides: Partial<ExecutionHistoryRecord> = {}): ExecutionHistoryRecord {
  return {
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
    ...overrides,
  };
}

function makeExtendedRecord(
  overrides: Partial<ExecutionHistoryRecordExtended> = {}
): ExecutionHistoryRecordExtended {
  return {
    ...makeRecord(overrides),
    filesRead: overrides.filesRead,
    filesWritten: overrides.filesWritten,
    toolCalls: overrides.toolCalls,
    contextWindowUtilization: overrides.contextWindowUtilization,
  };
}

function makeRunRecords(
  issueNumber: number,
  stages: string[],
  filesRead?: Record<string, string[]>
): ExecutionHistoryRecordExtended[] {
  return stages.map((stage) =>
    makeExtendedRecord({
      issueNumber,
      stage,
      filesRead: filesRead?.[stage],
    })
  );
}

// --- Tests ---

describe("TokenEfficiencyAnalyzer", () => {
  describe("percentile", () => {
    it("returns 0 for empty array", () => {
      expect(TokenEfficiencyAnalyzer.percentile([], 50)).toBe(0);
    });

    it("returns the single value for a single-element array", () => {
      expect(TokenEfficiencyAnalyzer.percentile([42], 50)).toBe(42);
    });

    it("computes 50th percentile (median) correctly", () => {
      expect(TokenEfficiencyAnalyzer.percentile([1, 2, 3, 4, 5], 50)).toBe(3);
    });

    it("computes 90th percentile with interpolation", () => {
      const values = [10, 20, 30, 40, 50, 60, 70, 80, 90, 100];
      const p90 = TokenEfficiencyAnalyzer.percentile(values, 90);
      expect(p90).toBeCloseTo(91, 0); // interpolated between 90 and 100
    });

    it("returns min at 0th percentile", () => {
      expect(TokenEfficiencyAnalyzer.percentile([5, 10, 15], 0)).toBe(5);
    });

    it("returns max at 100th percentile", () => {
      expect(TokenEfficiencyAnalyzer.percentile([5, 10, 15], 100)).toBe(15);
    });

    it("handles unsorted input", () => {
      expect(TokenEfficiencyAnalyzer.percentile([30, 10, 20], 50)).toBe(20);
    });
  });

  describe("classifySeverity", () => {
    const cases = [
      { wasteRatio: 0.6, savingsUsd: 0, expected: "critical" },
      { wasteRatio: 0, savingsUsd: 1.5, expected: "critical" },
      { wasteRatio: 0.3, savingsUsd: 0, expected: "high" },
      { wasteRatio: 0, savingsUsd: 0.5, expected: "high" },
      { wasteRatio: 0.15, savingsUsd: 0, expected: "medium" },
      { wasteRatio: 0, savingsUsd: 0.15, expected: "medium" },
      { wasteRatio: 0.05, savingsUsd: 0.01, expected: "low" },
      { wasteRatio: 0, savingsUsd: 0, expected: "info" },
    ] as const;

    cases.forEach(({ wasteRatio, savingsUsd, expected }) => {
      it(`classifies (ratio=${wasteRatio}, savings=$${savingsUsd}) as "${expected}"`, () => {
        expect(TokenEfficiencyAnalyzer.classifySeverity(wasteRatio, savingsUsd)).toBe(expected);
      });
    });
  });

  describe("detectRedundantFileReads", () => {
    it("detects files read across 3+ stages in the same run", () => {
      const analyzer = new TokenEfficiencyAnalyzer();
      const records = makeRunRecords(
        42,
        ["issue-pickup", "feature-planning", "feature-dev", "pr-create"],
        {
          "issue-pickup": ["PLAN.md", "README.md"],
          "feature-planning": ["PLAN.md", "ARCHITECTURE.md"],
          "feature-dev": ["PLAN.md", "README.md"],
          "pr-create": ["PLAN.md"],
        }
      );

      const patterns = analyzer.detectRedundantFileReads(records);

      expect(patterns.length).toBeGreaterThanOrEqual(1);
      const planPattern = patterns.find((p) =>
        (p.evidence.redundantFiles as Array<{ file: string }>).some((f) => f.file === "PLAN.md")
      );
      expect(planPattern).toBeDefined();
      expect(planPattern!.category).toBe("redundant-file-reads");
      expect(planPattern!.wastedTokens).toBeGreaterThan(0);
    });

    it("returns empty when filesRead is missing on all records", () => {
      const analyzer = new TokenEfficiencyAnalyzer();
      const records = [makeRecord(), makeRecord({ stage: "pr-create" })];

      const patterns = analyzer.detectRedundantFileReads(records);
      expect(patterns).toHaveLength(0);
    });

    it("returns empty for single-stage runs", () => {
      const analyzer = new TokenEfficiencyAnalyzer();
      const records = [
        makeExtendedRecord({
          issueNumber: 1,
          stage: "feature-dev",
          filesRead: ["PLAN.md", "README.md"],
        }),
      ];

      const patterns = analyzer.detectRedundantFileReads(records);
      expect(patterns).toHaveLength(0);
    });

    it("does not flag files appearing in fewer than threshold stages", () => {
      const analyzer = new TokenEfficiencyAnalyzer();
      const records = [
        makeExtendedRecord({
          issueNumber: 1,
          stage: "feature-dev",
          filesRead: ["PLAN.md"],
        }),
        makeExtendedRecord({
          issueNumber: 1,
          stage: "pr-create",
          filesRead: ["PLAN.md"],
        }),
      ];

      // Default threshold is 3, file appears in only 2 stages
      const patterns = analyzer.detectRedundantFileReads(records);
      expect(patterns).toHaveLength(0);
    });

    it("respects custom redundantReadMinOccurrences threshold", () => {
      const analyzer = new TokenEfficiencyAnalyzer({
        thresholds: { redundantReadMinOccurrences: 2 },
      });
      const records = [
        makeExtendedRecord({
          issueNumber: 1,
          stage: "feature-dev",
          filesRead: ["PLAN.md"],
        }),
        makeExtendedRecord({
          issueNumber: 1,
          stage: "pr-create",
          filesRead: ["PLAN.md"],
        }),
      ];

      const patterns = analyzer.detectRedundantFileReads(records);
      expect(patterns).toHaveLength(1);
    });
  });

  describe("detectOversizedContext", () => {
    it("flags stages with token usage above the 90th percentile", () => {
      const analyzer = new TokenEfficiencyAnalyzer();
      const records = [
        // 9 normal records
        ...Array.from({ length: 9 }, (_, i) =>
          makeRecord({
            issueNumber: i + 1,
            stage: "feature-dev",
            inputTokens: 1000,
          })
        ),
        // 1 outlier
        makeRecord({
          issueNumber: 10,
          stage: "feature-dev",
          inputTokens: 50000,
        }),
      ];

      const patterns = analyzer.detectOversizedContext(records);

      expect(patterns.length).toBeGreaterThanOrEqual(1);
      expect(patterns[0].category).toBe("oversized-context");
      expect(patterns[0].wastedTokens).toBeGreaterThan(0);
    });

    it("returns empty when all stages have identical token usage", () => {
      const analyzer = new TokenEfficiencyAnalyzer();
      const records = Array.from({ length: 10 }, (_, i) =>
        makeRecord({
          issueNumber: i + 1,
          stage: "feature-dev",
          inputTokens: 1000, // Identical values — no outlier possible
        })
      );

      const patterns = analyzer.detectOversizedContext(records);
      expect(patterns).toHaveLength(0);
    });

    it("returns empty when fewer than minSamples records per stage", () => {
      const analyzer = new TokenEfficiencyAnalyzer({
        minSamplesForOutliers: 5,
      });
      const records = [
        makeRecord({ stage: "feature-dev", inputTokens: 1000 }),
        makeRecord({ stage: "feature-dev", inputTokens: 50000 }),
      ];

      const patterns = analyzer.detectOversizedContext(records);
      expect(patterns).toHaveLength(0);
    });

    it("returns empty for empty input", () => {
      const analyzer = new TokenEfficiencyAnalyzer();
      const patterns = analyzer.detectOversizedContext([]);
      expect(patterns).toHaveLength(0);
    });

    it("skips patterns when median is zero (Issue #984)", () => {
      const analyzer = new TokenEfficiencyAnalyzer({
        minSamplesForOutliers: 5,
      });
      // All records have 0 inputTokens except one outlier
      const records = [
        ...Array.from({ length: 9 }, (_, i) =>
          makeRecord({
            issueNumber: i + 1,
            stage: "feature-dev",
            inputTokens: 0,
          })
        ),
        makeRecord({
          issueNumber: 10,
          stage: "feature-dev",
          inputTokens: 50000,
        }),
      ];

      const patterns = analyzer.detectOversizedContext(records);

      // Median is 0, so patterns should be skipped
      expect(patterns).toHaveLength(0);
    });

    it("includes input/output ratio in evidence", () => {
      const analyzer = new TokenEfficiencyAnalyzer();
      const records = [
        ...Array.from({ length: 9 }, (_, i) =>
          makeRecord({
            issueNumber: i + 1,
            stage: "feature-dev",
            inputTokens: 1000,
            outputTokens: 500,
          })
        ),
        makeRecord({
          issueNumber: 10,
          stage: "feature-dev",
          inputTokens: 50000,
          outputTokens: 100, // High input, low output
        }),
      ];

      const patterns = analyzer.detectOversizedContext(records);
      const outlierPattern = patterns.find((p) => (p.evidence.inputTokens as number) === 50000);
      expect(outlierPattern).toBeDefined();
      expect(outlierPattern!.evidence.inputOutputRatio).toBeGreaterThan(10);
    });
  });

  describe("detectCacheMissPatterns", () => {
    it("flags stages with low cache hit rate", () => {
      const analyzer = new TokenEfficiencyAnalyzer();
      const records = [
        makeRecord({
          stage: "feature-dev",
          inputTokens: 10000,
          cacheReadTokens: 100, // Very low cache read
        }),
        makeRecord({
          stage: "feature-dev",
          inputTokens: 10000,
          cacheReadTokens: 200,
        }),
      ];

      const patterns = analyzer.detectCacheMissPatterns(records);

      expect(patterns.length).toBeGreaterThanOrEqual(1);
      expect(patterns[0].category).toBe("cache-miss-patterns");
      expect(patterns[0].estimatedSavingsUsd).toBeGreaterThan(0);
    });

    it("returns empty when records have no cache fields", () => {
      const analyzer = new TokenEfficiencyAnalyzer();
      const records = [
        makeRecord({ cacheReadTokens: undefined }),
        makeRecord({ cacheReadTokens: undefined }),
      ];

      const patterns = analyzer.detectCacheMissPatterns(records);
      expect(patterns).toHaveLength(0);
    });

    it("returns empty when cache hit rate meets threshold", () => {
      const analyzer = new TokenEfficiencyAnalyzer();
      const records = [
        makeRecord({
          stage: "feature-dev",
          inputTokens: 1000,
          cacheReadTokens: 9000, // 90% cache hit rate
        }),
      ];

      const patterns = analyzer.detectCacheMissPatterns(records);
      expect(patterns).toHaveLength(0);
    });

    it("only analyzes records that have cache data", () => {
      const analyzer = new TokenEfficiencyAnalyzer();
      const records = [
        makeRecord({
          stage: "feature-dev",
          inputTokens: 10000,
          cacheReadTokens: undefined,
        }),
        makeRecord({
          stage: "feature-dev",
          inputTokens: 1000,
          cacheReadTokens: 9000, // Good cache rate
        }),
      ];

      const patterns = analyzer.detectCacheMissPatterns(records);
      expect(patterns).toHaveLength(0); // Only 1 record with cache, and it's good
    });

    it("includes cache hit rate in evidence", () => {
      const analyzer = new TokenEfficiencyAnalyzer();
      const records = [
        makeRecord({
          stage: "feature-dev",
          inputTokens: 10000,
          cacheReadTokens: 100,
        }),
      ];

      const patterns = analyzer.detectCacheMissPatterns(records);
      expect(patterns.length).toBeGreaterThanOrEqual(1);
      expect(patterns[0].evidence.cacheHitRate).toBeDefined();
      expect(patterns[0].evidence.cacheHitRate as number).toBeLessThan(0.5);
    });
  });

  describe("detectToolCallInefficiency", () => {
    it("flags stages with outlier tool call counts", () => {
      const analyzer = new TokenEfficiencyAnalyzer();
      const records = [
        // 9 normal records
        ...Array.from({ length: 9 }, (_, i) =>
          makeExtendedRecord({
            issueNumber: i + 1,
            stage: "feature-dev",
            toolCalls: 10,
          })
        ),
        // 1 outlier
        makeExtendedRecord({
          issueNumber: 10,
          stage: "feature-dev",
          toolCalls: 100,
        }),
      ];

      const patterns = analyzer.detectToolCallInefficiency(records);

      expect(patterns.length).toBeGreaterThanOrEqual(1);
      expect(patterns[0].category).toBe("tool-call-inefficiency");
      expect(patterns[0].wastedTokens).toBeGreaterThan(0);
    });

    it("skips patterns when median is zero (Issue #984)", () => {
      const analyzer = new TokenEfficiencyAnalyzer({
        minSamplesForOutliers: 5,
      });
      // All records have 0 toolCalls except one outlier
      const records = [
        ...Array.from({ length: 9 }, (_, i) =>
          makeExtendedRecord({
            issueNumber: i + 1,
            stage: "feature-dev",
            toolCalls: 0,
          })
        ),
        makeExtendedRecord({
          issueNumber: 10,
          stage: "feature-dev",
          toolCalls: 100,
        }),
      ];

      const patterns = analyzer.detectToolCallInefficiency(records);

      // Median is 0, so patterns should be skipped
      expect(patterns).toHaveLength(0);
    });

    it("returns empty when toolCalls is missing on all records", () => {
      const analyzer = new TokenEfficiencyAnalyzer();
      const records = [makeRecord(), makeRecord()];

      const patterns = analyzer.detectToolCallInefficiency(records);
      expect(patterns).toHaveLength(0);
    });

    it("returns empty when tool calls are uniform", () => {
      const analyzer = new TokenEfficiencyAnalyzer();
      const records = Array.from({ length: 10 }, (_, i) =>
        makeExtendedRecord({
          issueNumber: i + 1,
          stage: "feature-dev",
          toolCalls: 10,
        })
      );

      const patterns = analyzer.detectToolCallInefficiency(records);
      expect(patterns).toHaveLength(0);
    });

    it("returns empty with fewer than minSamples records", () => {
      const analyzer = new TokenEfficiencyAnalyzer({
        minSamplesForOutliers: 5,
      });
      const records = [
        makeExtendedRecord({ stage: "feature-dev", toolCalls: 10 }),
        makeExtendedRecord({ stage: "feature-dev", toolCalls: 100 }),
      ];

      const patterns = analyzer.detectToolCallInefficiency(records);
      expect(patterns).toHaveLength(0);
    });
  });

  describe("detectContextWindowUtilization", () => {
    it("flags stages with low utilization on expensive models", () => {
      const analyzer = new TokenEfficiencyAnalyzer();
      const records = [
        makeExtendedRecord({
          stage: "feature-dev",
          contextWindowUtilization: 0.1,
          costUsd: 0.5,
        }),
        makeExtendedRecord({
          stage: "feature-dev",
          contextWindowUtilization: 0.15,
          costUsd: 0.4,
        }),
      ];

      const patterns = analyzer.detectContextWindowUtilization(records);

      expect(patterns.length).toBeGreaterThanOrEqual(1);
      expect(patterns[0].category).toBe("context-window-utilization");
      expect(patterns[0].recommendation).toContain("expensive model");
    });

    it("returns empty when contextWindowUtilization is missing", () => {
      const analyzer = new TokenEfficiencyAnalyzer();
      const records = [makeRecord(), makeRecord()];

      const patterns = analyzer.detectContextWindowUtilization(records);
      expect(patterns).toHaveLength(0);
    });

    it("returns empty when utilization is above threshold", () => {
      const analyzer = new TokenEfficiencyAnalyzer();
      const records = [
        makeExtendedRecord({
          stage: "feature-dev",
          contextWindowUtilization: 0.8,
        }),
        makeExtendedRecord({
          stage: "feature-dev",
          contextWindowUtilization: 0.7,
        }),
      ];

      const patterns = analyzer.detectContextWindowUtilization(records);
      expect(patterns).toHaveLength(0);
    });

    it("uses generic message for low-cost models", () => {
      const analyzer = new TokenEfficiencyAnalyzer();
      const records = [
        makeExtendedRecord({
          stage: "feature-dev",
          contextWindowUtilization: 0.1,
          costUsd: 0.01,
        }),
        makeExtendedRecord({
          stage: "feature-dev",
          contextWindowUtilization: 0.15,
          costUsd: 0.02,
        }),
      ];

      const patterns = analyzer.detectContextWindowUtilization(records);
      expect(patterns.length).toBeGreaterThanOrEqual(1);
      expect(patterns[0].recommendation).toContain("smaller model");
      expect(patterns[0].recommendation).not.toContain("expensive model");
    });
  });

  describe("analyze (end-to-end)", () => {
    it("returns valid empty report for empty records", () => {
      const analyzer = new TokenEfficiencyAnalyzer();
      const result = analyzer.analyze([]);

      expect(result.recordsAnalyzed).toBe(0);
      expect(result.wastePatterns).toHaveLength(0);
      expect(result.summary.totalWastedTokens).toBe(0);
      expect(result.summary.totalEstimatedSavingsUsd).toBe(0);
      expect(result.summary.overallEfficiencyScore).toBe(100);
      expect(result.summary.topRecommendation).toContain("No waste patterns");
    });

    it("produces correct aggregate report with mixed records", () => {
      const analyzer = new TokenEfficiencyAnalyzer({
        minSamplesForOutliers: 5,
      });
      const records = [
        // 9 normal + 1 oversized context
        ...Array.from({ length: 9 }, (_, i) =>
          makeRecord({
            issueNumber: i + 1,
            stage: "feature-dev",
            inputTokens: 1000,
            outputTokens: 500,
          })
        ),
        makeRecord({
          issueNumber: 10,
          stage: "feature-dev",
          inputTokens: 50000,
          outputTokens: 100,
        }),
        // Cache miss records
        makeRecord({
          stage: "pr-create",
          inputTokens: 10000,
          cacheReadTokens: 100,
        }),
      ];

      const result = analyzer.analyze(records);

      expect(result.recordsAnalyzed).toBe(11);
      expect(result.wastePatterns.length).toBeGreaterThan(0);
      expect(result.summary.totalWastedTokens).toBeGreaterThan(0);
      expect(result.summary.totalEstimatedSavingsUsd).toBeGreaterThan(0);
      expect(result.summary.overallEfficiencyScore).toBeLessThan(100);
    });

    it("sorts patterns by estimatedSavingsUsd descending", () => {
      const analyzer = new TokenEfficiencyAnalyzer({
        minSamplesForOutliers: 5,
      });
      const records = [
        ...Array.from({ length: 9 }, (_, i) =>
          makeRecord({
            issueNumber: i + 1,
            stage: "feature-dev",
            inputTokens: 1000,
          })
        ),
        makeRecord({
          issueNumber: 10,
          stage: "feature-dev",
          inputTokens: 50000,
        }),
        makeRecord({
          stage: "pr-create",
          inputTokens: 10000,
          cacheReadTokens: 100,
        }),
      ];

      const result = analyzer.analyze(records);

      if (result.wastePatterns.length > 1) {
        for (let i = 1; i < result.wastePatterns.length; i++) {
          expect(result.wastePatterns[i - 1].estimatedSavingsUsd).toBeGreaterThanOrEqual(
            result.wastePatterns[i].estimatedSavingsUsd
          );
        }
      }
    });

    it("correctly aggregates categorySummary", () => {
      const analyzer = new TokenEfficiencyAnalyzer({
        minSamplesForOutliers: 5,
      });
      const records = [
        ...Array.from({ length: 9 }, (_, i) =>
          makeRecord({
            issueNumber: i + 1,
            stage: "feature-dev",
            inputTokens: 1000,
          })
        ),
        makeRecord({
          issueNumber: 10,
          stage: "feature-dev",
          inputTokens: 50000,
        }),
      ];

      const result = analyzer.analyze(records);

      // All categories should exist in summary
      expect(result.summary.categorySummary["redundant-file-reads"]).toBeDefined();
      expect(result.summary.categorySummary["oversized-context"]).toBeDefined();
      expect(result.summary.categorySummary["cache-miss-patterns"]).toBeDefined();
      expect(result.summary.categorySummary["tool-call-inefficiency"]).toBeDefined();
      expect(result.summary.categorySummary["context-window-utilization"]).toBeDefined();

      // Verify aggregation: sum of per-pattern savings equals category total
      const oversizedPatterns = result.wastePatterns.filter(
        (p) => p.category === "oversized-context"
      );
      const oversizedSummary = result.summary.categorySummary["oversized-context"];
      expect(oversizedSummary.patternCount).toBe(oversizedPatterns.length);

      const totalSavings = oversizedPatterns.reduce((sum, p) => sum + p.estimatedSavingsUsd, 0);
      expect(oversizedSummary.totalSavingsUsd).toBeCloseTo(totalSavings);
    });

    it("computes efficiency score correctly", () => {
      const analyzer = new TokenEfficiencyAnalyzer({
        minSamplesForOutliers: 5,
      });
      // Create data that will produce waste
      const records = [
        ...Array.from({ length: 9 }, (_, i) =>
          makeRecord({
            issueNumber: i + 1,
            stage: "feature-dev",
            inputTokens: 1000,
            outputTokens: 500,
          })
        ),
        makeRecord({
          issueNumber: 10,
          stage: "feature-dev",
          inputTokens: 50000,
          outputTokens: 500,
        }),
      ];

      const result = analyzer.analyze(records);

      expect(result.summary.overallEfficiencyScore).toBeGreaterThanOrEqual(0);
      expect(result.summary.overallEfficiencyScore).toBeLessThanOrEqual(100);
    });

    it("sets analyzedAt to a valid ISO 8601 timestamp", () => {
      const analyzer = new TokenEfficiencyAnalyzer();
      const result = analyzer.analyze([]);

      expect(() => new Date(result.analyzedAt)).not.toThrow();
      expect(result.analyzedAt).toMatch(/\d{4}-\d{2}-\d{2}T/);
    });

    it("respects date range filter", () => {
      const analyzer = new TokenEfficiencyAnalyzer({
        dateRange: { since: "2026-01-20", until: "2026-01-31" },
        minSamplesForOutliers: 2,
      });
      const records = [
        ...Array.from({ length: 5 }, (_, i) =>
          makeRecord({
            issueNumber: i + 1,
            timestamp: "2026-01-10T00:00:00Z", // excluded
            inputTokens: 50000,
          })
        ),
        ...Array.from({ length: 5 }, (_, i) =>
          makeRecord({
            issueNumber: i + 10,
            timestamp: "2026-01-25T00:00:00Z", // included
            inputTokens: 1000,
          })
        ),
      ];

      const result = analyzer.analyze(records);
      expect(result.recordsAnalyzed).toBe(5);
    });

    it("topRecommendation matches the highest-savings pattern", () => {
      const analyzer = new TokenEfficiencyAnalyzer({
        minSamplesForOutliers: 5,
      });
      const records = [
        ...Array.from({ length: 9 }, (_, i) =>
          makeRecord({
            issueNumber: i + 1,
            stage: "feature-dev",
            inputTokens: 1000,
          })
        ),
        makeRecord({
          issueNumber: 10,
          stage: "feature-dev",
          inputTokens: 50000,
        }),
      ];

      const result = analyzer.analyze(records);
      if (result.wastePatterns.length > 0) {
        expect(result.summary.topRecommendation).toBe(result.wastePatterns[0].recommendation);
      }
    });
  });

  describe("edge cases", () => {
    it("handles zero-cost stages", () => {
      const analyzer = new TokenEfficiencyAnalyzer({
        minSamplesForOutliers: 5,
      });
      const records = Array.from({ length: 5 }, (_, i) =>
        makeRecord({
          issueNumber: i + 1,
          stage: "feature-dev",
          inputTokens: 0,
          outputTokens: 0,
          costUsd: 0,
        })
      );

      const result = analyzer.analyze(records);
      expect(result.summary.overallEfficiencyScore).toBe(100);
    });

    it("handles single record input", () => {
      const analyzer = new TokenEfficiencyAnalyzer();
      const result = analyzer.analyze([makeRecord()]);

      expect(result.recordsAnalyzed).toBe(1);
      // Should not crash; most detectors need more samples
      expect(result.wastePatterns).toBeDefined();
    });

    it("handles all records for the same stage (no cross-stage comparison)", () => {
      const analyzer = new TokenEfficiencyAnalyzer();
      const records = Array.from({ length: 10 }, (_, i) =>
        makeExtendedRecord({
          issueNumber: 1,
          stage: "feature-dev",
          filesRead: ["PLAN.md"],
        })
      );

      // No cross-stage redundancy since all records are same stage
      const patterns = analyzer.detectRedundantFileReads(records);
      // All records are the same stage, so PLAN.md appears in only 1 unique stage
      expect(patterns).toHaveLength(0);
    });

    it("handles incomplete runs (partial stage data)", () => {
      const analyzer = new TokenEfficiencyAnalyzer();
      const records = [
        makeExtendedRecord({
          issueNumber: 1,
          stage: "feature-dev",
          filesRead: ["PLAN.md"],
          toolCalls: 5,
        }),
        // Missing other stages
      ];

      const result = analyzer.analyze(records);
      expect(result.recordsAnalyzed).toBe(1);
    });

    it("handles all graceful degradation paths simultaneously", () => {
      const analyzer = new TokenEfficiencyAnalyzer();
      // Records with no extended fields at all
      const records = Array.from({ length: 10 }, (_, i) => makeRecord({ issueNumber: i + 1 }));

      const result = analyzer.analyze(records);
      // Only oversized-context can detect patterns on base records (if enough samples)
      // Others should gracefully degrade
      expect(result.summary.categorySummary["redundant-file-reads"].patternCount).toBe(0);
      expect(result.summary.categorySummary["tool-call-inefficiency"].patternCount).toBe(0);
      expect(result.summary.categorySummary["context-window-utilization"].patternCount).toBe(0);
    });
  });

  describe("detectZeroChangeRuns", () => {
    it("detects verify-and-close runs as zero-change waste", () => {
      const analyzer = new TokenEfficiencyAnalyzer();
      const records = [
        makeRecord({
          issueNumber: 42,
          stage: "issue-pickup",
          costUsd: 0.5,
          outcomeType: "verify-and-close",
        }),
        makeRecord({
          issueNumber: 42,
          stage: "feature-dev",
          costUsd: 2.0,
          outcomeType: "verify-and-close",
        }),
      ];

      const patterns = analyzer.detectZeroChangeRuns(records);

      expect(patterns).toHaveLength(1);
      expect(patterns[0].category).toBe("zero-change-run");
      expect(patterns[0].estimatedSavingsUsd).toBeCloseTo(2.5);
      expect(patterns[0].description).toContain("zero file changes");
      expect(patterns[0].evidence.outcomeType).toBe("verify-and-close");
    });

    it("detects already-resolved runs as zero-change waste", () => {
      const analyzer = new TokenEfficiencyAnalyzer();
      const records = [
        makeRecord({
          issueNumber: 99,
          stage: "issue-pickup",
          costUsd: 0.1,
          outcomeType: "already-resolved",
        }),
      ];

      const patterns = analyzer.detectZeroChangeRuns(records);

      expect(patterns).toHaveLength(1);
      expect(patterns[0].description).toContain("already resolved");
      expect(patterns[0].recommendation).toContain("pre-pipeline checks");
    });

    it("returns empty when all records are productive", () => {
      const analyzer = new TokenEfficiencyAnalyzer();
      const records = [
        makeRecord({ outcomeType: "productive" }),
        makeRecord({ outcomeType: "productive" }),
      ];

      const patterns = analyzer.detectZeroChangeRuns(records);
      expect(patterns).toHaveLength(0);
    });

    it("returns empty when outcomeType is undefined", () => {
      const analyzer = new TokenEfficiencyAnalyzer();
      const records = [makeRecord(), makeRecord()];

      const patterns = analyzer.detectZeroChangeRuns(records);
      expect(patterns).toHaveLength(0);
    });

    it("groups multiple zero-change runs by issue number", () => {
      const analyzer = new TokenEfficiencyAnalyzer();
      const records = [
        makeRecord({
          issueNumber: 10,
          stage: "feature-dev",
          costUsd: 1.0,
          outcomeType: "verify-and-close",
        }),
        makeRecord({
          issueNumber: 20,
          stage: "feature-dev",
          costUsd: 2.0,
          outcomeType: "verify-and-close",
        }),
      ];

      const patterns = analyzer.detectZeroChangeRuns(records);
      expect(patterns).toHaveLength(2);
    });

    it("includes total cost in evidence", () => {
      const analyzer = new TokenEfficiencyAnalyzer();
      const records = [
        makeRecord({
          issueNumber: 42,
          stage: "issue-pickup",
          costUsd: 1.5,
          outcomeType: "verify-and-close",
        }),
        makeRecord({
          issueNumber: 42,
          stage: "feature-dev",
          costUsd: 3.5,
          outcomeType: "verify-and-close",
        }),
      ];

      const patterns = analyzer.detectZeroChangeRuns(records);
      expect(patterns[0].evidence.totalCostUsd).toBeCloseTo(5.0);
      expect(patterns[0].evidence.stageCount).toBe(2);
    });
  });

  describe("filterByOutcomeType", () => {
    it("filters records by single outcome type", () => {
      const analyzer = new TokenEfficiencyAnalyzer();
      const records = [
        makeRecord({ issueNumber: 1, outcomeType: "productive" }),
        makeRecord({ issueNumber: 2, outcomeType: "verify-and-close" }),
        makeRecord({ issueNumber: 3, outcomeType: "productive" }),
      ];

      const filtered = analyzer.filterByOutcomeType(records, ["verify-and-close"]);
      expect(filtered).toHaveLength(1);
      expect(filtered[0].issueNumber).toBe(2);
    });

    it("filters records by multiple outcome types", () => {
      const analyzer = new TokenEfficiencyAnalyzer();
      const records = [
        makeRecord({ issueNumber: 1, outcomeType: "productive" }),
        makeRecord({ issueNumber: 2, outcomeType: "verify-and-close" }),
        makeRecord({ issueNumber: 3, outcomeType: "already-resolved" }),
      ];

      const filtered = analyzer.filterByOutcomeType(records, [
        "verify-and-close",
        "already-resolved",
      ]);
      expect(filtered).toHaveLength(2);
    });

    it("excludes records with undefined outcomeType", () => {
      const analyzer = new TokenEfficiencyAnalyzer();
      const records = [
        makeRecord({ issueNumber: 1, outcomeType: "productive" }),
        makeRecord({ issueNumber: 2 }), // no outcomeType
      ];

      const filtered = analyzer.filterByOutcomeType(records, ["productive"]);
      expect(filtered).toHaveLength(1);
    });

    it("returns empty array when no records match", () => {
      const analyzer = new TokenEfficiencyAnalyzer();
      const records = [
        makeRecord({ outcomeType: "productive" }),
        makeRecord({ outcomeType: "productive" }),
      ];

      const filtered = analyzer.filterByOutcomeType(records, ["verify-and-close"]);
      expect(filtered).toHaveLength(0);
    });
  });

  describe("analyze includes zero-change-run category in summary", () => {
    it("includes zero-change-run in categorySummary", () => {
      const analyzer = new TokenEfficiencyAnalyzer();
      const result = analyzer.analyze([]);

      expect(result.summary.categorySummary["zero-change-run"]).toBeDefined();
      expect(result.summary.categorySummary["zero-change-run"].patternCount).toBe(0);
    });

    it("aggregates zero-change-run patterns in summary", () => {
      const analyzer = new TokenEfficiencyAnalyzer();
      const records = [
        makeRecord({
          issueNumber: 42,
          stage: "feature-dev",
          costUsd: 5.0,
          inputTokens: 10000,
          outputTokens: 5000,
          outcomeType: "verify-and-close",
        }),
      ];

      const result = analyzer.analyze(records);
      const summary = result.summary.categorySummary["zero-change-run"];

      expect(summary.patternCount).toBe(1);
      expect(summary.totalSavingsUsd).toBeGreaterThan(0);
      expect(summary.totalWastedTokens).toBeGreaterThan(0);
    });
  });

  describe("configuration", () => {
    it("uses default thresholds when none provided", () => {
      const analyzer = new TokenEfficiencyAnalyzer();
      // Should not throw
      const result = analyzer.analyze([]);
      expect(result).toBeDefined();
    });

    it("accepts partial threshold overrides", () => {
      const analyzer = new TokenEfficiencyAnalyzer({
        thresholds: {
          redundantReadMinOccurrences: 2,
          // Other thresholds use defaults
        },
      });

      // Should not throw
      const result = analyzer.analyze([]);
      expect(result).toBeDefined();
    });

    it("accepts custom cost rates", () => {
      const analyzer = new TokenEfficiencyAnalyzer({
        defaultCostRate: {
          inputPerMillion: 1.0,
          outputPerMillion: 5.0,
        },
      });

      const records = [
        makeRecord({
          stage: "feature-dev",
          inputTokens: 10000,
          cacheReadTokens: 100,
        }),
      ];

      const patterns = analyzer.detectCacheMissPatterns(records);
      // Should use custom cost rate for savings calculation
      expect(patterns.length).toBeGreaterThanOrEqual(1);
    });
  });

  describe("getCostRateForModel (Issue #725)", () => {
    it("returns Haiku cost rates for haiku model", () => {
      const analyzer = new TokenEfficiencyAnalyzer();
      const rate = analyzer.getCostRateForModel("haiku");

      expect(rate.inputPerMillion).toBe(1.0);
      expect(rate.outputPerMillion).toBe(5.0);
      expect(rate.cacheReadPerMillion).toBe(0.1);
      expect(rate.cacheCreationPerMillion).toBe(1.25);
    });

    it("returns Sonnet cost rates for sonnet model", () => {
      const analyzer = new TokenEfficiencyAnalyzer();
      const rate = analyzer.getCostRateForModel("sonnet");

      expect(rate.inputPerMillion).toBe(3.0);
      expect(rate.outputPerMillion).toBe(15.0);
      expect(rate.cacheReadPerMillion).toBe(0.3);
      expect(rate.cacheCreationPerMillion).toBe(3.75);
    });

    it("returns Opus cost rates for opus model", () => {
      const analyzer = new TokenEfficiencyAnalyzer();
      const rate = analyzer.getCostRateForModel("opus");

      expect(rate.inputPerMillion).toBe(5.0);
      expect(rate.outputPerMillion).toBe(25.0);
      expect(rate.cacheReadPerMillion).toBe(0.5);
      expect(rate.cacheCreationPerMillion).toBe(6.25);
    });

    it("falls back to defaultCostRate for unknown model", () => {
      const analyzer = new TokenEfficiencyAnalyzer();
      const rate = analyzer.getCostRateForModel("unknown-model");

      // Default cost rate (Sonnet pricing)
      expect(rate.inputPerMillion).toBe(3.0);
      expect(rate.outputPerMillion).toBe(15.0);
    });

    it("falls back to defaultCostRate when model is undefined", () => {
      const analyzer = new TokenEfficiencyAnalyzer();
      const rate = analyzer.getCostRateForModel(undefined);

      expect(rate.inputPerMillion).toBe(3.0);
      expect(rate.outputPerMillion).toBe(15.0);
    });

    it("respects custom costRates override", () => {
      const analyzer = new TokenEfficiencyAnalyzer({
        costRates: {
          haiku: {
            inputPerMillion: 0.5,
            outputPerMillion: 2.5,
          },
        },
      });

      const rate = analyzer.getCostRateForModel("haiku");
      expect(rate.inputPerMillion).toBe(0.5);
      expect(rate.outputPerMillion).toBe(2.5);
    });

    it("preserves default rates for models not in custom costRates", () => {
      const analyzer = new TokenEfficiencyAnalyzer({
        costRates: {
          haiku: {
            inputPerMillion: 0.5,
            outputPerMillion: 2.5,
          },
        },
      });

      // Sonnet and Opus should still have their defaults
      const sonnetRate = analyzer.getCostRateForModel("sonnet");
      expect(sonnetRate.inputPerMillion).toBe(3.0);

      const opusRate = analyzer.getCostRateForModel("opus");
      expect(opusRate.inputPerMillion).toBe(5.0);
    });
  });
});
