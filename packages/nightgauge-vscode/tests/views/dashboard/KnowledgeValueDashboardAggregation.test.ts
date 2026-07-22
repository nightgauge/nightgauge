import { describe, it, expect } from "vitest";
import {
  computeDelta,
  formatDelta,
  hitRateBand,
} from "../../../src/views/dashboard/KnowledgeValueDashboardTypes";
import type { KnowledgeMetricsResult } from "../../../src/services/IpcClientBase";

function metrics(totals: Partial<KnowledgeMetricsResult["totals"]>): KnowledgeMetricsResult {
  return {
    window_days: 7,
    stale_days: 30,
    status: "enabled",
    generated_at: "2026-05-16T12:00:00Z",
    totals: {
      writes: 0,
      reads: 0,
      recalls: 0,
      recall_hits: 0,
      graduations: 0,
      scaffolds: 0,
      prunes: 0,
      indexes: 0,
      validates: 0,
      stats: 0,
      events_in_range: 0,
      ...totals,
    },
    per_stage: [],
    top_recalled: [],
    stale_entries: [],
    graduation_history: [],
  };
}

describe("KnowledgeValueDashboardTypes — TS aggregation", () => {
  describe("computeDelta", () => {
    it("returns null when current or prior is missing", () => {
      expect(computeDelta(null, null)).toBeNull();
      expect(computeDelta(metrics({ reads: 10 }), null)).toBeNull();
      expect(computeDelta(null, metrics({ reads: 10 }))).toBeNull();
    });

    it("computes positive deltas correctly", () => {
      const cur = metrics({ writes: 10, reads: 20, recalls: 5, recall_hits: 3, graduations: 1 });
      const prior = metrics({ writes: 6, reads: 15, recalls: 2, recall_hits: 1, graduations: 0 });
      const d = computeDelta(cur, prior);
      expect(d).toEqual({
        writes: 4,
        reads: 5,
        recalls: 3,
        recall_hits: 2,
        graduations: 1,
      });
    });

    it("computes negative deltas correctly", () => {
      const cur = metrics({ writes: 2, reads: 5 });
      const prior = metrics({ writes: 8, reads: 10 });
      const d = computeDelta(cur, prior);
      expect(d?.writes).toBe(-6);
      expect(d?.reads).toBe(-5);
    });
  });

  describe("formatDelta", () => {
    it("renders an em-dash for null", () => {
      expect(formatDelta(null)).toBe("—");
      expect(formatDelta(undefined)).toBe("—");
    });

    it("renders positive deltas with ▲", () => {
      expect(formatDelta(5)).toBe("▲ 5");
    });

    it("renders negative deltas with ▼ and abs value", () => {
      expect(formatDelta(-3)).toBe("▼ 3");
    });

    it("renders zero with a neutral marker", () => {
      expect(formatDelta(0)).toBe("· 0");
    });
  });

  describe("hitRateBand", () => {
    it("returns neutral for null", () => {
      expect(hitRateBand(null)).toBe("neutral");
      expect(hitRateBand(undefined)).toBe("neutral");
    });

    it("classifies >50% as green", () => {
      expect(hitRateBand(0.51)).toBe("green");
      expect(hitRateBand(1.0)).toBe("green");
    });

    it("classifies 20-50% as yellow", () => {
      expect(hitRateBand(0.2)).toBe("yellow");
      expect(hitRateBand(0.5)).toBe("yellow");
      expect(hitRateBand(0.35)).toBe("yellow");
    });

    it("classifies <20% as red", () => {
      expect(hitRateBand(0.0)).toBe("red");
      expect(hitRateBand(0.19)).toBe("red");
    });
  });
});
