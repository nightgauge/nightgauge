import { describe, it, expect } from "vitest";
import { resolve } from "node:path";
import { FailurePatternDetector } from "../../src/analysis/FailurePatternDetector.js";
import type { ExecutionHistoryRecord } from "../../src/analysis/types.js";

// ── Test fixtures ──────────────────────────────────────────────────

const TAXONOMY_PATH = resolve(__dirname, "../../src/analysis/failure-taxonomy.yaml");

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

// ── Tests ──────────────────────────────────────────────────────────

describe("FailurePatternDetector", () => {
  describe("constructor", () => {
    it("loads default taxonomy without error", () => {
      expect(() => new FailurePatternDetector({ taxonomyPath: TAXONOMY_PATH })).not.toThrow();
    });

    it("accepts custom config", () => {
      const detector = new FailurePatternDetector({
        taxonomyPath: TAXONOMY_PATH,
        recurringThreshold: 5,
        costRates: { inputPerMillion: 10, outputPerMillion: 30 },
      });
      expect(detector).toBeDefined();
    });
  });

  describe("analyze", () => {
    it("returns empty result for empty records", () => {
      const detector = new FailurePatternDetector({
        taxonomyPath: TAXONOMY_PATH,
      });
      const result = detector.analyze([]);

      expect(result.recordsAnalyzed).toBe(0);
      expect(result.totalFailures).toBe(0);
      expect(result.findings).toHaveLength(0);
      expect(result.summary.topCategory).toBeNull();
      expect(result.summary.failureRate).toBe(0);
      expect(result.summary.overallTrend).toBe("stable");
    });

    it("returns empty result when all records are successful", () => {
      const detector = new FailurePatternDetector({
        taxonomyPath: TAXONOMY_PATH,
      });
      const records = Array.from({ length: 5 }, (_, i) =>
        makeRecord({ issueNumber: i + 1, success: true })
      );

      const result = detector.analyze(records as ExecutionHistoryRecord[]);

      expect(result.recordsAnalyzed).toBe(5);
      expect(result.totalFailures).toBe(0);
      expect(result.findings).toHaveLength(0);
      expect(result.summary.failureRate).toBe(0);
    });

    it("detects build failures and produces findings", () => {
      const detector = new FailurePatternDetector({
        taxonomyPath: TAXONOMY_PATH,
      });
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
        makeRecord({ success: true, issueNumber: 3 }),
      ];

      const result = detector.analyze(records as ExecutionHistoryRecord[]);

      expect(result.recordsAnalyzed).toBe(3);
      expect(result.totalFailures).toBe(2);
      expect(result.findings.length).toBeGreaterThanOrEqual(1);
      expect(result.summary.failureRate).toBeCloseTo(2 / 3);

      const buildFinding = result.findings.find((f) => f.category === "build-failure");
      expect(buildFinding).toBeDefined();
      expect(buildFinding!.occurrenceCount).toBe(2);
      expect(buildFinding!.severity).toBe("auto-fixable");
    });

    it("detects multiple failure categories", () => {
      const detector = new FailurePatternDetector({
        taxonomyPath: TAXONOMY_PATH,
      });
      const records = [
        makeRecord({
          success: false,
          errorText: "error TS2345",
          issueNumber: 1,
        }),
        makeRecord({
          success: false,
          errorText: "FAIL tests/foo.test.ts",
          issueNumber: 2,
        }),
        makeRecord({
          success: false,
          errorText: "timeout exceeded",
          issueNumber: 3,
        }),
        makeRecord({ success: true, issueNumber: 4 }),
      ];

      const result = detector.analyze(records as ExecutionHistoryRecord[]);

      expect(result.totalFailures).toBe(3);
      const categories = result.findings.map((f) => f.category);
      expect(categories).toContain("build-failure");
      expect(categories).toContain("test-failure");
      expect(categories).toContain("timeout-transient");
    });

    it("sorts findings by occurrence count descending", () => {
      const detector = new FailurePatternDetector({
        taxonomyPath: TAXONOMY_PATH,
      });
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
        makeRecord({
          success: false,
          errorText: "error TS2345",
          issueNumber: 4,
        }),
      ];

      const result = detector.analyze(records as ExecutionHistoryRecord[]);

      expect(result.findings[0].occurrenceCount).toBeGreaterThanOrEqual(
        result.findings[result.findings.length - 1].occurrenceCount
      );
    });

    it("computes summary statistics correctly", () => {
      const detector = new FailurePatternDetector({
        taxonomyPath: TAXONOMY_PATH,
      });
      const records = [
        makeRecord({
          success: false,
          errorText: "error TS2345",
          costUsd: 0.5,
          issueNumber: 1,
        }),
        makeRecord({
          success: false,
          errorText: "error TS1005",
          costUsd: 0.3,
          issueNumber: 2,
        }),
        makeRecord({ success: true, issueNumber: 3 }),
        makeRecord({ success: true, issueNumber: 4 }),
      ];

      const result = detector.analyze(records as ExecutionHistoryRecord[]);

      expect(result.summary.failureRate).toBeCloseTo(0.5);
      expect(result.summary.totalFailureCostUsd).toBeGreaterThan(0);
      expect(result.summary.topCategory).toBe("build-failure");
    });

    it("populates recommendations for findings", () => {
      const detector = new FailurePatternDetector({
        taxonomyPath: TAXONOMY_PATH,
      });
      const records = [
        makeRecord({
          success: false,
          errorText: "error TS2345",
          issueNumber: 1,
        }),
        makeRecord({
          success: false,
          errorText: "error TS1005",
          issueNumber: 2,
        }),
      ];

      const result = detector.analyze(records as ExecutionHistoryRecord[]);

      for (const finding of result.findings) {
        expect(finding.recommendation).not.toBe("");
      }
    });

    it("sets analyzedAt to a valid ISO 8601 timestamp", () => {
      const detector = new FailurePatternDetector({
        taxonomyPath: TAXONOMY_PATH,
      });
      const result = detector.analyze([]);

      expect(() => new Date(result.analyzedAt)).not.toThrow();
      expect(result.analyzedAt).toMatch(/\d{4}-\d{2}-\d{2}T/);
    });

    it("respects custom recurringThreshold config", () => {
      const detector = new FailurePatternDetector({
        taxonomyPath: TAXONOMY_PATH,
        recurringThreshold: 2,
      });
      const records = [
        makeRecord({
          success: false,
          errorText: "error TS2345 in file X",
          issueNumber: 1,
        }),
        makeRecord({
          success: false,
          errorText: "error TS2345 in file Y",
          issueNumber: 2,
        }),
      ];

      const result = detector.analyze(records as ExecutionHistoryRecord[]);
      // With threshold 2, the recurring pattern should be detected
      expect(result.findings.length).toBeGreaterThanOrEqual(1);
    });
  });
});
