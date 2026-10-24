/**
 * HealthWidget.audit.test.ts
 *
 * Audit tests for the health headline, trending, and edge cases.
 *
 * Since #1898 the widget computes no score of its own: the headline is the
 * latest entry of the one health series (`health/trends.jsonl`), written by
 * the SDK engine once per run. The legacy 4-component formula these tests
 * once pinned is retired.
 *
 * @see Issue #1044 - Audit: Validate health score calculation, trending, and actionability
 * @see Issue #1898 - one health series
 */

import { describe, it, expect, vi } from "vitest";

// Mock SDK analysis modules to simulate unavailability
vi.mock("@nightgauge/sdk/dist/analysis/FailurePatternDetector", () => {
  throw new Error("SDK not available in test");
});
vi.mock("@nightgauge/sdk/dist/analysis/TokenEfficiencyAnalyzer", () => {
  throw new Error("SDK not available in test");
});

vi.mock("vscode", () => ({
  workspace: {
    getConfiguration: vi.fn(() => ({
      get: vi.fn((_key: string, defaultValue: unknown) => defaultValue),
    })),
  },
}));

import type { HealthTrendEntry } from "@nightgauge/sdk";
import { HealthWidgetService } from "../../../src/views/dashboard/HealthWidget";
import { getHealthStatus } from "../../../src/views/dashboard/HealthWidgetTypes";
import { aggregateHealthByDay, analyzeHealthTrend } from "../../../src/utils/healthSeries";

// ---------------------------------------------------------------------------
// Mock factories
// ---------------------------------------------------------------------------

const createMockDashboardState = (overrides?: Record<string, unknown>) => ({
  getHistory: vi.fn().mockReturnValue([]),
  getEfficiencyTrend: vi.fn().mockReturnValue({
    improving: true,
    percentChange: 0,
    hasEnoughData: false,
  }),
  getCostTrend: vi.fn().mockReturnValue({
    improving: true,
    percentChange: 0,
    hasEnoughData: false,
  }),
  getTokenTrend: vi.fn().mockReturnValue({
    direction: "stable",
    percentChange: 0,
    hasEnoughData: false,
  }),
  getAggregates: vi.fn().mockReturnValue({ successRate: 1.0, totalRuns: 10 }),
  getHistoricalData: vi.fn().mockReturnValue([]),
  getVelocityInsights: vi.fn().mockResolvedValue(null),
  getAccuracyTrend: vi.fn().mockResolvedValue(null),
  ...overrides,
});

function createEntry(overrides: Partial<HealthTrendEntry> = {}): HealthTrendEntry {
  return {
    schema_version: "2",
    timestamp: "2026-02-15T10:00:00Z",
    run_id: "r",
    issue_number: 42,
    overall_score: 75,
    status: "good",
    dimensions: { reliability: 75 },
    excluded_dimensions: {},
    significant_findings: [],
    ...overrides,
  };
}

// ---------------------------------------------------------------------------
// Audit Tests
// ---------------------------------------------------------------------------

describe("Health Score Audit (#1044, #1898)", () => {
  describe("getHealthStatus thresholds", () => {
    const cases: [number, string][] = [
      [100, "excellent"],
      [90, "excellent"],
      [89, "good"],
      [70, "good"],
      [69, "fair"],
      [50, "fair"],
      [49, "poor"],
      [30, "poor"],
      [29, "critical"],
      [0, "critical"],
    ];

    cases.forEach(([score, expected]) => {
      it(`score ${score} maps to "${expected}"`, () => {
        expect(getHealthStatus(score)).toBe(expected);
      });
    });
  });

  describe("Edge cases", () => {
    it("0 runs: getData returns isEmpty state with no score — not 0, not critical", async () => {
      const state = createMockDashboardState();
      const service = new HealthWidgetService(state as any, "/workspace");

      const data = await service.getData();

      expect(data.isEmpty).toBe(true);
      expect(data.summary.score).toBeNull();
      expect(data.summary.status).toBe("no-data");
      expect(data.summary.contributing).toBe(0);
    });
  });

  describe("Trend analysis verification", () => {
    // Use deterministic UTC date strings to avoid DST boundary collisions.
    // new Date() + setDate() + toISOString() can produce duplicate UTC dates
    // when a DST transition falls within the 14-day window (e.g., US Central
    // DST on March 8 causes i=7 and i=6 to both map to "2026-03-08" UTC).
    function makeDays(
      count: number,
      scoreFn: (i: number) => number
    ): Array<{ date: string; avgScore: number; count: number }> {
      const days = [];
      for (let i = count - 1; i >= 0; i--) {
        // Fixed base date far from any DST boundary
        const d = new Date(Date.UTC(2026, 0, 15 + (count - 1 - i))); // Jan 15+
        days.push({
          date: d.toISOString().split("T")[0],
          avgScore: scoreFn(i),
          count: 1,
        });
      }
      return days;
    }

    it("detects improving trend with >2% increase", () => {
      const days = makeDays(14, (i) => (i >= 7 ? 50 : 55)); // 10% increase
      const result = analyzeHealthTrend(days);
      expect(result.direction).toBe("improving");
      expect(result.percentChange).toBe(10);
    });

    it("detects declining trend with <-2% decrease", () => {
      const days = makeDays(14, (i) => (i >= 7 ? 80 : 70)); // -12.5% decrease
      const result = analyzeHealthTrend(days);
      expect(result.direction).toBe("declining");
      expect(result.percentChange).toBeLessThan(-2);
    });

    it("reports stable within ±2% threshold", () => {
      const days = makeDays(14, (i) => (i >= 7 ? 75 : 76)); // ~1.3% change
      const result = analyzeHealthTrend(days);
      expect(result.direction).toBe("stable");
      expect(Math.abs(result.percentChange)).toBeLessThanOrEqual(2);
    });

    it("handles exactly 7 days (no prior period)", () => {
      const days = makeDays(7, () => 70);
      const result = analyzeHealthTrend(days);
      expect(result.direction).toBe("stable");
      expect(result.message).toBe("Not enough history for trend comparison");
    });

    it("handles prior period with 0 average (division by zero protection)", () => {
      const days = makeDays(14, (i) => (i >= 7 ? 0 : 50));
      const result = analyzeHealthTrend(days);
      // When priorAvg is 0, should return stable with insufficient baseline
      expect(result.direction).toBe("stable");
      expect(result.message).toBe("Insufficient baseline data");
    });
  });

  describe("aggregateHealthByDay correctness", () => {
    it("correctly averages multiple scores on the same day", () => {
      const snapshots: HealthTrendEntry[] = [
        createEntry({ timestamp: "2026-02-20T01:10:47Z", overall_score: 81 }),
        createEntry({ timestamp: "2026-02-20T01:48:05Z", overall_score: 69 }),
        createEntry({ timestamp: "2026-02-20T02:13:45Z", overall_score: 66 }),
      ];

      const result = aggregateHealthByDay(snapshots);

      expect(result).toHaveLength(1);
      expect(result[0].date).toBe("2026-02-20");
      // (81 + 69 + 66) / 3 = 72
      expect(result[0].avgScore).toBe(72);
      expect(result[0].count).toBe(3);
    });

    it("handles snapshots across multiple days", () => {
      const snapshots: HealthTrendEntry[] = [
        createEntry({ timestamp: "2026-02-17T22:08:25Z", overall_score: 66 }),
        createEntry({ timestamp: "2026-02-17T22:17:28Z", overall_score: 66 }),
        createEntry({ timestamp: "2026-02-18T00:12:31Z", overall_score: 60 }),
        createEntry({ timestamp: "2026-02-19T16:12:37Z", overall_score: 66 }),
      ];

      const result = aggregateHealthByDay(snapshots);

      expect(result).toHaveLength(3);
      expect(result[0].date).toBe("2026-02-17");
      expect(result[0].avgScore).toBe(66);
      expect(result[0].count).toBe(2);
      expect(result[1].date).toBe("2026-02-18");
      expect(result[1].avgScore).toBe(60);
      expect(result[2].date).toBe("2026-02-19");
      expect(result[2].avgScore).toBe(66);
    });

    it("skips entries recorded as null (no dimension had data)", () => {
      const result = aggregateHealthByDay([
        createEntry({ timestamp: "2026-02-20T01:00:00Z", overall_score: 80 }),
        createEntry({
          timestamp: "2026-02-20T02:00:00Z",
          overall_score: null,
          status: "no-data",
          dimensions: {},
        }),
      ]);
      expect(result).toEqual([{ date: "2026-02-20", avgScore: 80, count: 1 }]);
    });
  });
});
