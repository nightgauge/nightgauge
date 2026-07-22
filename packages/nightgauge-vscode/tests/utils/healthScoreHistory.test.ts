/**
 * healthScoreHistory.test.ts
 *
 * Unit tests for HealthScoreHistoryWriter and HealthScoreHistoryReader.
 *
 * @see Issue #789 - Persist Health Scores to Disk with 30-Day Trend
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as fs from "node:fs/promises";
import {
  HealthScoreHistoryWriter,
  HealthScoreHistoryReader,
} from "../../src/utils/healthScoreHistory";
import type { HealthScoreSnapshot } from "../../src/schemas/healthScoreHistory";

vi.mock("node:fs/promises");

function createValidSnapshot(overrides: Partial<HealthScoreSnapshot> = {}): HealthScoreSnapshot {
  return {
    schema_version: "1",
    timestamp: "2026-02-15T10:00:00Z",
    score: 75,
    status: "good",
    components: { successRate: 80, costTrend: 70 },
    cacheHitRate: 0.45,
    costUsd: 0.12,
    issueNumber: 42,
    ...overrides,
  };
}

describe("HealthScoreHistoryWriter", () => {
  const workspaceRoot = "/test/workspace";

  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(fs.mkdir).mockResolvedValue(undefined);
    vi.mocked(fs.appendFile).mockResolvedValue();
    vi.mocked(fs.readFile).mockRejectedValue(new Error("ENOENT"));
    vi.mocked(fs.writeFile).mockResolvedValue();
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  describe("getFilePath()", () => {
    it("should return the correct path", () => {
      expect(HealthScoreHistoryWriter.getFilePath(workspaceRoot)).toBe(
        "/test/workspace/.nightgauge/pipeline/health-history.jsonl"
      );
    });
  });

  describe("appendSnapshot()", () => {
    it("should validate and write a valid snapshot as JSONL", async () => {
      const snapshot = createValidSnapshot();

      await HealthScoreHistoryWriter.appendSnapshot(workspaceRoot, snapshot);

      expect(fs.mkdir).toHaveBeenCalledWith(expect.stringContaining(".nightgauge/pipeline"), {
        recursive: true,
      });
      expect(fs.appendFile).toHaveBeenCalledWith(
        expect.stringContaining("health-history.jsonl"),
        expect.stringContaining('"schema_version":"1"'),
        "utf-8"
      );
    });

    it("should skip writing an invalid snapshot", async () => {
      const warnSpy = vi.spyOn(console, "warn").mockImplementation(() => {});
      const invalid = { ...createValidSnapshot(), score: -10 };

      await HealthScoreHistoryWriter.appendSnapshot(workspaceRoot, invalid as HealthScoreSnapshot);

      expect(fs.appendFile).not.toHaveBeenCalled();
      warnSpy.mockRestore();
    });

    it("should not throw on fs errors", async () => {
      vi.mocked(fs.appendFile).mockRejectedValue(new Error("disk full"));
      const warnSpy = vi.spyOn(console, "warn").mockImplementation(() => {});

      await expect(
        HealthScoreHistoryWriter.appendSnapshot(workspaceRoot, createValidSnapshot())
      ).resolves.toBeUndefined();

      warnSpy.mockRestore();
    });
  });

  describe("pruneOldEntries()", () => {
    it("should remove entries older than retention period", async () => {
      const oldDate = new Date();
      oldDate.setDate(oldDate.getDate() - 45);
      const recentDate = new Date();
      recentDate.setDate(recentDate.getDate() - 5);

      const oldLine = JSON.stringify(createValidSnapshot({ timestamp: oldDate.toISOString() }));
      const recentLine = JSON.stringify(
        createValidSnapshot({ timestamp: recentDate.toISOString() })
      );

      vi.mocked(fs.readFile).mockResolvedValue(`${oldLine}\n${recentLine}\n`);

      await HealthScoreHistoryWriter.pruneOldEntries(workspaceRoot, 30);

      expect(fs.writeFile).toHaveBeenCalledWith(
        expect.stringContaining("health-history.jsonl"),
        expect.not.stringContaining(oldDate.toISOString()),
        "utf-8"
      );
      // Recent entry should be kept
      const writtenContent = vi.mocked(fs.writeFile).mock.calls[0][1] as string;
      expect(writtenContent).toContain(recentDate.toISOString());
    });

    it("should handle missing file gracefully", async () => {
      vi.mocked(fs.readFile).mockRejectedValue(new Error("ENOENT"));

      await expect(
        HealthScoreHistoryWriter.pruneOldEntries(workspaceRoot)
      ).resolves.toBeUndefined();

      expect(fs.writeFile).not.toHaveBeenCalled();
    });

    it("should skip malformed lines during pruning", async () => {
      const recentSnapshot = createValidSnapshot({
        timestamp: new Date().toISOString(),
      });
      const validLine = JSON.stringify(recentSnapshot);
      vi.mocked(fs.readFile).mockResolvedValue(`${validLine}\n{bad json\n`);

      await HealthScoreHistoryWriter.pruneOldEntries(workspaceRoot, 30);

      const writtenContent = vi.mocked(fs.writeFile).mock.calls[0][1] as string;
      expect(writtenContent).toContain('"schema_version"');
      expect(writtenContent).not.toContain("{bad json");
    });
  });
});

describe("HealthScoreHistoryReader", () => {
  const workspaceRoot = "/test/workspace";

  beforeEach(() => {
    vi.clearAllMocks();
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  describe("readAll()", () => {
    it("should parse valid JSONL lines", async () => {
      const s1 = createValidSnapshot({ score: 75 });
      const s2 = createValidSnapshot({
        score: 85,
        timestamp: "2026-02-16T10:00:00Z",
      });

      vi.mocked(fs.readFile).mockResolvedValue(`${JSON.stringify(s1)}\n${JSON.stringify(s2)}\n`);

      const results = await HealthScoreHistoryReader.readAll(workspaceRoot);
      expect(results).toHaveLength(2);
      expect(results[0].score).toBe(75);
      expect(results[1].score).toBe(85);
    });

    it("should return empty array when file does not exist", async () => {
      vi.mocked(fs.readFile).mockRejectedValue(new Error("ENOENT"));

      const results = await HealthScoreHistoryReader.readAll(workspaceRoot);
      expect(results).toEqual([]);
    });

    it("should skip malformed lines", async () => {
      const valid = createValidSnapshot();
      const warnSpy = vi.spyOn(console, "warn").mockImplementation(() => {});

      vi.mocked(fs.readFile).mockResolvedValue(
        `${JSON.stringify(valid)}\n{bad json\n{"score": "not a number"}\n`
      );

      const results = await HealthScoreHistoryReader.readAll(workspaceRoot);
      expect(results).toHaveLength(1);
      warnSpy.mockRestore();
    });

    it("should handle empty file", async () => {
      vi.mocked(fs.readFile).mockResolvedValue("");

      const results = await HealthScoreHistoryReader.readAll(workspaceRoot);
      expect(results).toEqual([]);
    });
  });

  describe("readDateRange()", () => {
    it("should filter snapshots by date range", async () => {
      const s1 = createValidSnapshot({ timestamp: "2026-02-10T10:00:00Z" });
      const s2 = createValidSnapshot({ timestamp: "2026-02-15T10:00:00Z" });
      const s3 = createValidSnapshot({ timestamp: "2026-02-20T10:00:00Z" });

      vi.mocked(fs.readFile).mockResolvedValue(
        [s1, s2, s3].map((s) => JSON.stringify(s)).join("\n") + "\n"
      );

      const results = await HealthScoreHistoryReader.readDateRange(
        workspaceRoot,
        new Date("2026-02-12"),
        new Date("2026-02-18")
      );

      expect(results).toHaveLength(1);
      expect(results[0].timestamp).toBe("2026-02-15T10:00:00Z");
    });
  });

  describe("aggregateByDay()", () => {
    it("should group snapshots by date and compute daily averages", () => {
      const snapshots: HealthScoreSnapshot[] = [
        createValidSnapshot({ timestamp: "2026-02-15T10:00:00Z", score: 70 }),
        createValidSnapshot({ timestamp: "2026-02-15T14:00:00Z", score: 80 }),
        createValidSnapshot({ timestamp: "2026-02-16T10:00:00Z", score: 90 }),
      ];

      const result = HealthScoreHistoryReader.aggregateByDay(snapshots);

      expect(result).toHaveLength(2);
      expect(result[0]).toEqual({ date: "2026-02-15", avgScore: 75, count: 2 });
      expect(result[1]).toEqual({ date: "2026-02-16", avgScore: 90, count: 1 });
    });

    it("should return sorted results by date", () => {
      const snapshots: HealthScoreSnapshot[] = [
        createValidSnapshot({ timestamp: "2026-02-17T10:00:00Z", score: 60 }),
        createValidSnapshot({ timestamp: "2026-02-15T10:00:00Z", score: 80 }),
      ];

      const result = HealthScoreHistoryReader.aggregateByDay(snapshots);
      expect(result[0].date).toBe("2026-02-15");
      expect(result[1].date).toBe("2026-02-17");
    });

    it("should handle empty input", () => {
      const result = HealthScoreHistoryReader.aggregateByDay([]);
      expect(result).toEqual([]);
    });

    it("should handle single entry", () => {
      const result = HealthScoreHistoryReader.aggregateByDay([createValidSnapshot({ score: 50 })]);
      expect(result).toHaveLength(1);
      expect(result[0].avgScore).toBe(50);
      expect(result[0].count).toBe(1);
    });
  });

  describe("analyzeTrend()", () => {
    it("should detect improving trend when recent avg > prior avg", () => {
      // Build 14 days of data: prior 7 days at 60, recent 7 days at 80
      const days = [];
      for (let i = 13; i >= 0; i--) {
        const d = new Date();
        d.setDate(d.getDate() - i);
        days.push({
          date: d.toISOString().split("T")[0],
          avgScore: i >= 7 ? 60 : 80,
          count: 1,
        });
      }

      const result = HealthScoreHistoryReader.analyzeTrend(days);
      expect(result.direction).toBe("improving");
      expect(result.percentChange).toBeGreaterThan(0);
      expect(result.message).toContain("improved");
    });

    it("should detect declining trend when recent avg < prior avg", () => {
      const days = [];
      for (let i = 13; i >= 0; i--) {
        const d = new Date();
        d.setDate(d.getDate() - i);
        days.push({
          date: d.toISOString().split("T")[0],
          avgScore: i >= 7 ? 80 : 60,
          count: 1,
        });
      }

      const result = HealthScoreHistoryReader.analyzeTrend(days);
      expect(result.direction).toBe("declining");
      expect(result.percentChange).toBeLessThan(0);
      expect(result.message).toContain("declined");
    });

    it("should report stable when within threshold", () => {
      const days = [];
      for (let i = 13; i >= 0; i--) {
        const d = new Date();
        d.setDate(d.getDate() - i);
        days.push({
          date: d.toISOString().split("T")[0],
          avgScore: 75,
          count: 1,
        });
      }

      const result = HealthScoreHistoryReader.analyzeTrend(days);
      expect(result.direction).toBe("stable");
    });

    it("should handle empty data", () => {
      const result = HealthScoreHistoryReader.analyzeTrend([]);
      expect(result.direction).toBe("stable");
      expect(result.message).toBe("Not enough data for trend analysis");
    });

    it("should handle single data point", () => {
      const result = HealthScoreHistoryReader.analyzeTrend([
        { date: "2026-02-15", avgScore: 75, count: 1 },
      ]);
      expect(result.direction).toBe("stable");
      expect(result.message).toBe("Tracking started");
    });

    it("should handle insufficient prior data", () => {
      // Only 3 days of data — not enough for prior 7-day comparison
      const days = [];
      for (let i = 2; i >= 0; i--) {
        const d = new Date();
        d.setDate(d.getDate() - i);
        days.push({
          date: d.toISOString().split("T")[0],
          avgScore: 75,
          count: 1,
        });
      }

      const result = HealthScoreHistoryReader.analyzeTrend(days);
      expect(result.direction).toBe("stable");
      expect(result.message).toBe("Not enough history for trend comparison");
    });
  });
});

// ── Recalibration tests (Issue #1262) ─────────────────────────────

describe("HealthScoreHistoryWriter.appendRecalibrationMarker()", () => {
  const workspaceRoot = "/test/workspace";

  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(fs.mkdir).mockResolvedValue(undefined);
    vi.mocked(fs.appendFile).mockResolvedValue();
    vi.mocked(fs.readFile).mockRejectedValue(new Error("ENOENT"));
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("should write a recalibration marker to the file", async () => {
    await HealthScoreHistoryWriter.appendRecalibrationMarker(
      workspaceRoot,
      "Systemic fixes completed"
    );

    expect(fs.mkdir).toHaveBeenCalledWith(expect.stringContaining(".nightgauge/pipeline"), {
      recursive: true,
    });

    const written = vi.mocked(fs.appendFile).mock.calls[0][1] as string;
    const marker = JSON.parse(written.trim());
    expect(marker.type).toBe("recalibration");
    expect(marker.schema_version).toBe("1");
    expect(marker.reason).toBe("Systemic fixes completed");
    expect(typeof marker.timestamp).toBe("string");
  });

  it("should write a recalibration marker without a reason", async () => {
    await HealthScoreHistoryWriter.appendRecalibrationMarker(workspaceRoot);

    const written = vi.mocked(fs.appendFile).mock.calls[0][1] as string;
    const marker = JSON.parse(written.trim());
    expect(marker.type).toBe("recalibration");
    expect(marker.reason).toBeUndefined();
  });

  it("should not throw on fs errors", async () => {
    vi.mocked(fs.appendFile).mockRejectedValue(new Error("disk full"));
    const warnSpy = vi.spyOn(console, "warn").mockImplementation(() => {});

    await expect(
      HealthScoreHistoryWriter.appendRecalibrationMarker(workspaceRoot, "test")
    ).resolves.toBeUndefined();

    warnSpy.mockRestore();
  });
});

describe("HealthScoreHistoryReader.getMostRecentRecalibration()", () => {
  const workspaceRoot = "/test/workspace";

  beforeEach(() => {
    vi.clearAllMocks();
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("should return null when no file exists", async () => {
    vi.mocked(fs.readFile).mockRejectedValue(new Error("ENOENT"));

    const result = await HealthScoreHistoryReader.getMostRecentRecalibration(workspaceRoot);
    expect(result).toBeNull();
  });

  it("should return null when no recalibration marker exists", async () => {
    const snapshot = JSON.stringify({
      schema_version: "1",
      timestamp: "2026-02-15T10:00:00Z",
      score: 75,
      status: "good",
      components: {},
      cacheHitRate: 0.5,
      costUsd: 0.1,
      issueNumber: 1,
    });

    vi.mocked(fs.readFile).mockResolvedValue(snapshot + "\n");

    const result = await HealthScoreHistoryReader.getMostRecentRecalibration(workspaceRoot);
    expect(result).toBeNull();
  });

  it("should return the most recent recalibration marker", async () => {
    const marker1 = JSON.stringify({
      schema_version: "1",
      type: "recalibration",
      timestamp: "2026-02-10T10:00:00Z",
      reason: "First reset",
    });
    const marker2 = JSON.stringify({
      schema_version: "1",
      type: "recalibration",
      timestamp: "2026-02-20T10:00:00Z",
      reason: "Second reset",
    });

    vi.mocked(fs.readFile).mockResolvedValue(`${marker1}\n${marker2}\n`);

    const result = await HealthScoreHistoryReader.getMostRecentRecalibration(workspaceRoot);
    expect(result).not.toBeNull();
    expect(result!.timestamp).toBe("2026-02-20T10:00:00Z");
    expect(result!.reason).toBe("Second reset");
  });
});

describe("HealthScoreHistoryReader.readDateRange() with recalibration", () => {
  const workspaceRoot = "/test/workspace";

  beforeEach(() => {
    vi.clearAllMocks();
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("should use recalibration date as effective start when it is more recent", async () => {
    const s1 = JSON.stringify({
      schema_version: "1",
      timestamp: "2026-02-01T10:00:00Z",
      score: 50,
      status: "fair",
      components: {},
      cacheHitRate: 0.5,
      costUsd: 0.1,
      issueNumber: 1,
    });
    const recalibration = JSON.stringify({
      schema_version: "1",
      type: "recalibration",
      timestamp: "2026-02-14T00:00:00Z",
      reason: "Systemic fixes",
    });
    const s2 = JSON.stringify({
      schema_version: "1",
      timestamp: "2026-02-20T10:00:00Z",
      score: 80,
      status: "good",
      components: {},
      cacheHitRate: 0.6,
      costUsd: 0.15,
      issueNumber: 2,
    });

    vi.mocked(fs.readFile).mockResolvedValue(`${s1}\n${recalibration}\n${s2}\n`);

    // Request the full 30-day window — but only post-recalibration data should return
    const results = await HealthScoreHistoryReader.readDateRange(
      workspaceRoot,
      new Date("2026-01-25"),
      new Date("2026-02-25")
    );

    expect(results).toHaveLength(1);
    expect(results[0].timestamp).toBe("2026-02-20T10:00:00Z");
  });

  it("should use original startDate when recalibration is older", async () => {
    const recalibration = JSON.stringify({
      schema_version: "1",
      type: "recalibration",
      timestamp: "2026-01-01T00:00:00Z",
      reason: "Old reset",
    });
    const s1 = JSON.stringify({
      schema_version: "1",
      timestamp: "2026-02-15T10:00:00Z",
      score: 75,
      status: "good",
      components: {},
      cacheHitRate: 0.5,
      costUsd: 0.1,
      issueNumber: 1,
    });

    vi.mocked(fs.readFile).mockResolvedValue(`${recalibration}\n${s1}\n`);

    const results = await HealthScoreHistoryReader.readDateRange(
      workspaceRoot,
      new Date("2026-02-12"),
      new Date("2026-02-18")
    );

    expect(results).toHaveLength(1);
    expect(results[0].timestamp).toBe("2026-02-15T10:00:00Z");
  });
});

// ── aggregateByHour tests ────────────────────────────────────────

describe("HealthScoreHistoryReader.aggregateByHour()", () => {
  it("should group snapshots by hour", () => {
    const snapshots = [
      createValidSnapshot({ timestamp: "2026-03-07T14:10:00Z", score: 70 }),
      createValidSnapshot({ timestamp: "2026-03-07T14:45:00Z", score: 80 }),
      createValidSnapshot({ timestamp: "2026-03-07T15:20:00Z", score: 90 }),
    ];

    const result = HealthScoreHistoryReader.aggregateByHour(snapshots);
    expect(result).toHaveLength(2);
    expect(result[0].date).toBe("2026-03-07T14");
    expect(result[0].avgScore).toBe(75); // avg of 70 and 80
    expect(result[0].count).toBe(2);
    expect(result[1].date).toBe("2026-03-07T15");
    expect(result[1].avgScore).toBe(90);
    expect(result[1].count).toBe(1);
  });

  it("should return empty array for no data", () => {
    expect(HealthScoreHistoryReader.aggregateByHour([])).toEqual([]);
  });

  it("should sort by hour ascending", () => {
    const snapshots = [
      createValidSnapshot({ timestamp: "2026-03-07T16:00:00Z", score: 80 }),
      createValidSnapshot({ timestamp: "2026-03-07T14:00:00Z", score: 70 }),
    ];

    const result = HealthScoreHistoryReader.aggregateByHour(snapshots);
    expect(result[0].date).toBe("2026-03-07T14");
    expect(result[1].date).toBe("2026-03-07T16");
  });
});

// ── analyzeTrend with custom comparisonBuckets ───────────────────

describe("HealthScoreHistoryReader.analyzeTrend() with custom buckets", () => {
  it("should use custom comparison bucket size", () => {
    // 6 buckets: prior 3 at 60, recent 3 at 80
    const days = [];
    for (let i = 5; i >= 0; i--) {
      const d = new Date();
      d.setDate(d.getDate() - i);
      days.push({
        date: d.toISOString().split("T")[0],
        avgScore: i >= 3 ? 60 : 80,
        count: 1,
      });
    }

    const result = HealthScoreHistoryReader.analyzeTrend(days, 3);
    expect(result.direction).toBe("improving");
    expect(result.periodDays).toBe(3);
  });

  it("should report insufficient data when fewer than 2x buckets", () => {
    const days = [
      { date: "2026-03-06", avgScore: 75, count: 1 },
      { date: "2026-03-07", avgScore: 80, count: 1 },
    ];

    const result = HealthScoreHistoryReader.analyzeTrend(days, 3);
    expect(result.direction).toBe("stable");
    expect(result.message).toBe("Not enough history for trend comparison");
  });
});
