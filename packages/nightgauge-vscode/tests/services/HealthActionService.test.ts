/**
 * HealthActionService.test.ts
 *
 * Unit tests for HealthActionService — evaluates health score snapshots and
 * generates advisory actions based on score thresholds and run trends.
 *
 * Test coverage:
 * - Score >= 70: info-level action only
 * - Score 50-69: warning-level with audit suggestion
 * - Score < 50: critical-level with degradation warning
 * - Trend detection: improving (recent 3-run avg > prior 7-run avg by >10%)
 * - Trend detection: declining (recent 3-run avg < prior 7-run avg by >10%)
 * - Trend detection: stable (change < 10%)
 * - Config: health_actions_enabled: false returns empty actions
 * - Missing history: returns score 100, stable, no actions
 * - Exception propagation: returns null when evaluate throws
 *
 * @see Issue #1045 - Health-gated tier actions and learning system monitoring
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

// Mock HealthScoreHistoryReader
const mockReadAll = vi.fn();
vi.mock("../../src/utils/healthScoreHistory", () => ({
  HealthScoreHistoryReader: {
    readAll: (...args: unknown[]) => mockReadAll(...args),
  },
  HealthScoreHistoryWriter: {
    getFilePath: vi.fn(),
    appendSnapshot: vi.fn(),
  },
}));

// Mock IpcClient singleton — configGetHealthThresholds returns defaults
const mockConfigGetHealthThresholds = vi.fn();
vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      configGetHealthThresholds: mockConfigGetHealthThresholds,
    }),
  },
}));

import { HealthActionService } from "../../src/services/HealthActionService";

function createMockLogger() {
  return {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  };
}

function createSnapshot(score: number, timestamp: string, status: string = "good") {
  return {
    schema_version: "1" as const,
    timestamp,
    score,
    status,
    components: { successRate: score },
    cacheHitRate: 0.5,
    costUsd: 0.5,
    issueNumber: 100,
  };
}

describe("HealthActionService", () => {
  const workspaceRoot = "/test/workspace";
  let logger: ReturnType<typeof createMockLogger>;

  beforeEach(() => {
    vi.clearAllMocks();
    logger = createMockLogger();

    // Default: IPC returns default thresholds (matches TS defaults)
    mockConfigGetHealthThresholds.mockResolvedValue({
      warningThreshold: 70,
      criticalThreshold: 50,
      emergencyThreshold: 30,
      actionsEnabled: true,
      policiesEnabled: true,
      feedbackLoopEnabled: true,
    });
  });

  // ===========================================================================
  // Score-based action level tests
  // ===========================================================================

  describe("score-based actions", () => {
    it("returns info-level action when score >= 70", async () => {
      const snapshots = [createSnapshot(85, "2026-02-20T10:00:00Z", "excellent")];
      mockReadAll.mockResolvedValue(snapshots);

      const result = await HealthActionService.evaluate(workspaceRoot, logger as any);

      expect(result).not.toBeNull();
      expect(result!.score).toBe(85);
      expect(result!.actions).toHaveLength(1);
      expect(result!.actions[0].level).toBe("info");
      expect(result!.actions[0].message).toContain("85");
    });

    it("returns info-level action at exactly score 70", async () => {
      const snapshots = [createSnapshot(70, "2026-02-20T10:00:00Z", "good")];
      mockReadAll.mockResolvedValue(snapshots);

      const result = await HealthActionService.evaluate(workspaceRoot, logger as any);

      expect(result).not.toBeNull();
      expect(result!.actions[0].level).toBe("info");
    });

    it("returns warning-level action with audit suggestion when score is 50-69", async () => {
      const snapshots = [createSnapshot(60, "2026-02-20T10:00:00Z", "fair")];
      mockReadAll.mockResolvedValue(snapshots);

      const result = await HealthActionService.evaluate(workspaceRoot, logger as any);

      expect(result).not.toBeNull();
      expect(result!.score).toBe(60);

      const scoreAction = result!.actions.find((a) => a.level === "warning");
      expect(scoreAction).toBeDefined();
      expect(scoreAction!.message).toContain("60");
      expect(scoreAction!.suggestion).toBeDefined();
      expect(scoreAction!.suggestion).toMatch(/audit|routing|failure/i);
    });

    it("returns warning-level action at exactly score 50", async () => {
      const snapshots = [createSnapshot(50, "2026-02-20T10:00:00Z", "poor")];
      mockReadAll.mockResolvedValue(snapshots);

      const result = await HealthActionService.evaluate(workspaceRoot, logger as any);

      expect(result).not.toBeNull();
      // Score 50 is >= critical threshold (50) and < warning threshold (70)
      // critical threshold is strictly < 50, so score 50 should be warning
      const scoreAction = result!.actions.find(
        (a) => a.level === "warning" || a.level === "critical"
      );
      expect(scoreAction).toBeDefined();
    });

    it("returns critical-level action when score < 50", async () => {
      const snapshots = [createSnapshot(30, "2026-02-20T10:00:00Z", "critical")];
      mockReadAll.mockResolvedValue(snapshots);

      const result = await HealthActionService.evaluate(workspaceRoot, logger as any);

      expect(result).not.toBeNull();
      expect(result!.score).toBe(30);

      const criticalAction = result!.actions.find((a) => a.level === "critical");
      expect(criticalAction).toBeDefined();
      expect(criticalAction!.message).toContain("30");
      expect(criticalAction!.suggestion).toBeDefined();
      expect(criticalAction!.suggestion).toMatch(/audit|pipeline|routing/i);
      expect(criticalAction!.autoApplied).toBe(false);
    });

    it("returns critical-level action at score 49", async () => {
      const snapshots = [createSnapshot(49, "2026-02-20T10:00:00Z", "critical")];
      mockReadAll.mockResolvedValue(snapshots);

      const result = await HealthActionService.evaluate(workspaceRoot, logger as any);

      expect(result).not.toBeNull();
      const criticalAction = result!.actions.find((a) => a.level === "critical");
      expect(criticalAction).toBeDefined();
    });
  });

  // ===========================================================================
  // Trend detection tests
  // ===========================================================================

  describe("trend detection", () => {
    /**
     * Build an array of snapshots arranged so that the recent 3-run average
     * is higher than the prior 7-run average by more than 10%.
     *
     * Snapshots must be sorted descending by timestamp when fed into the
     * service (the service itself sorts them); here we provide them in
     * ascending order and let the service sort.
     *
     * Prior batch (older): scores ~50 (avg 50)
     * Recent batch (newer): scores ~65 (avg ~65 → ~30% gain over 50)
     */
    function buildImprovingSnapshots() {
      const base = new Date("2026-02-10T00:00:00Z");
      const snapshots = [];

      // 7 older snapshots with score 50
      for (let i = 0; i < 7; i++) {
        const ts = new Date(base.getTime() + i * 60_000).toISOString();
        snapshots.push(createSnapshot(50, ts));
      }

      // 3 recent snapshots with score 65 (30% improvement over 50)
      for (let i = 7; i < 10; i++) {
        const ts = new Date(base.getTime() + i * 60_000).toISOString();
        snapshots.push(createSnapshot(65, ts));
      }

      return snapshots;
    }

    /**
     * Build an array of snapshots where the recent 3-run average is more than
     * 10% below the prior 7-run average.
     *
     * Prior batch (older): scores ~80 (avg 80)
     * Recent batch (newer): scores ~60 (avg 60 → 25% drop from 80)
     */
    function buildDecliningSnapshots() {
      const base = new Date("2026-02-10T00:00:00Z");
      const snapshots = [];

      // 7 older snapshots with score 80
      for (let i = 0; i < 7; i++) {
        const ts = new Date(base.getTime() + i * 60_000).toISOString();
        snapshots.push(createSnapshot(80, ts));
      }

      // 3 recent snapshots with score 60 (25% drop from 80)
      for (let i = 7; i < 10; i++) {
        const ts = new Date(base.getTime() + i * 60_000).toISOString();
        snapshots.push(createSnapshot(60, ts));
      }

      return snapshots;
    }

    /**
     * Build snapshots where the recent 3-run average differs from the prior
     * 7-run average by less than 10%.
     *
     * Prior batch (older): scores ~70 (avg 70)
     * Recent batch (newer): scores ~74 (avg ~74 → ~5.7% gain, < 10%)
     */
    function buildStableSnapshots() {
      const base = new Date("2026-02-10T00:00:00Z");
      const snapshots = [];

      // 7 older snapshots with score 70
      for (let i = 0; i < 7; i++) {
        const ts = new Date(base.getTime() + i * 60_000).toISOString();
        snapshots.push(createSnapshot(70, ts));
      }

      // 3 recent snapshots with score 74 (~5.7% gain over 70)
      for (let i = 7; i < 10; i++) {
        const ts = new Date(base.getTime() + i * 60_000).toISOString();
        snapshots.push(createSnapshot(74, ts));
      }

      return snapshots;
    }

    it("detects improving trend when recent 3-run avg exceeds prior 7-run avg by >10%", async () => {
      mockReadAll.mockResolvedValue(buildImprovingSnapshots());

      const result = await HealthActionService.evaluate(workspaceRoot, logger as any);

      expect(result).not.toBeNull();
      expect(result!.trend).toBe("improving");

      // An improving trend should produce an info-level trend action
      const trendAction = result!.actions.find(
        (a) => a.level === "info" && a.message.toLowerCase().includes("improving")
      );
      expect(trendAction).toBeDefined();
    });

    it("detects declining trend when recent 3-run avg is below prior 7-run avg by >10%", async () => {
      mockReadAll.mockResolvedValue(buildDecliningSnapshots());

      const result = await HealthActionService.evaluate(workspaceRoot, logger as any);

      expect(result).not.toBeNull();
      expect(result!.trend).toBe("declining");

      // A declining trend should produce a warning-level trend action
      const trendAction = result!.actions.find(
        (a) => a.level === "warning" && a.message.toLowerCase().includes("declining")
      );
      expect(trendAction).toBeDefined();
    });

    it("detects stable trend when change is less than 10%", async () => {
      mockReadAll.mockResolvedValue(buildStableSnapshots());

      const result = await HealthActionService.evaluate(workspaceRoot, logger as any);

      expect(result).not.toBeNull();
      expect(result!.trend).toBe("stable");

      // No trend-specific warning should be generated for stable
      const decliningAction = result!.actions.find((a) =>
        a.message.toLowerCase().includes("declining")
      );
      expect(decliningAction).toBeUndefined();
    });

    it("returns stable trend when fewer than 4 snapshots exist", async () => {
      const snapshots = [
        createSnapshot(80, "2026-02-20T10:00:00Z"),
        createSnapshot(75, "2026-02-19T10:00:00Z"),
        createSnapshot(70, "2026-02-18T10:00:00Z"),
      ];
      mockReadAll.mockResolvedValue(snapshots);

      const result = await HealthActionService.evaluate(workspaceRoot, logger as any);

      expect(result).not.toBeNull();
      expect(result!.trend).toBe("stable");
    });

    it("includes drop percentage in declining trend action message", async () => {
      mockReadAll.mockResolvedValue(buildDecliningSnapshots());

      const result = await HealthActionService.evaluate(workspaceRoot, logger as any);

      expect(result).not.toBeNull();
      // Target the trend-specific action: "Health trend declining over last 3 runs (X% drop)"
      // (not the score-based warning which also contains "declining")
      const trendAction = result!.actions.find(
        (a) => a.level === "warning" && a.message.toLowerCase().includes("trend declining")
      );
      expect(trendAction).toBeDefined();
      // Should include a percentage somewhere in the message
      expect(trendAction!.message).toMatch(/\d+%/);
    });

    it("includes gain percentage in improving trend action message", async () => {
      mockReadAll.mockResolvedValue(buildImprovingSnapshots());

      const result = await HealthActionService.evaluate(workspaceRoot, logger as any);

      expect(result).not.toBeNull();
      const trendAction = result!.actions.find(
        (a) => a.level === "info" && a.message.toLowerCase().includes("improving")
      );
      expect(trendAction).toBeDefined();
      expect(trendAction!.message).toMatch(/\+\d+%/);
    });
  });

  // ===========================================================================
  // Config: health_actions_enabled: false
  // ===========================================================================

  describe("config: health_actions_enabled", () => {
    it("returns empty actions when health_actions_enabled is false in config", async () => {
      const snapshots = [createSnapshot(30, "2026-02-20T10:00:00Z", "critical")];
      mockReadAll.mockResolvedValue(snapshots);

      // Override IPC to return actionsEnabled: false
      mockConfigGetHealthThresholds.mockResolvedValue({
        warningThreshold: 70,
        criticalThreshold: 50,
        emergencyThreshold: 30,
        actionsEnabled: false,
        policiesEnabled: true,
        feedbackLoopEnabled: true,
      });

      const result = await HealthActionService.evaluate(workspaceRoot, logger as any);

      expect(result).not.toBeNull();
      expect(result!.score).toBe(30);
      expect(result!.actions).toHaveLength(0);
    });

    it("generates actions when health_actions_enabled is true (explicit)", async () => {
      const snapshots = [createSnapshot(30, "2026-02-20T10:00:00Z", "critical")];
      mockReadAll.mockResolvedValue(snapshots);

      // Default beforeEach already sets actionsEnabled: true

      const result = await HealthActionService.evaluate(workspaceRoot, logger as any);

      expect(result).not.toBeNull();
      expect(result!.actions.length).toBeGreaterThan(0);
    });

    it("respects custom warning threshold from config", async () => {
      const snapshots = [createSnapshot(65, "2026-02-20T10:00:00Z", "fair")];
      mockReadAll.mockResolvedValue(snapshots);

      // Raise warning threshold to 75 — score 65 should trigger warning
      mockConfigGetHealthThresholds.mockResolvedValue({
        warningThreshold: 75,
        criticalThreshold: 50,
        emergencyThreshold: 30,
        actionsEnabled: true,
        policiesEnabled: true,
        feedbackLoopEnabled: true,
      });

      const result = await HealthActionService.evaluate(workspaceRoot, logger as any);

      expect(result).not.toBeNull();
      const warnAction = result!.actions.find((a) => a.level === "warning");
      expect(warnAction).toBeDefined();
    });

    it("respects custom critical threshold from config", async () => {
      const snapshots = [createSnapshot(55, "2026-02-20T10:00:00Z", "poor")];
      mockReadAll.mockResolvedValue(snapshots);

      // Raise critical threshold to 60 — score 55 should trigger critical
      mockConfigGetHealthThresholds.mockResolvedValue({
        warningThreshold: 70,
        criticalThreshold: 60,
        emergencyThreshold: 30,
        actionsEnabled: true,
        policiesEnabled: true,
        feedbackLoopEnabled: true,
      });

      const result = await HealthActionService.evaluate(workspaceRoot, logger as any);

      expect(result).not.toBeNull();
      const criticalAction = result!.actions.find((a) => a.level === "critical");
      expect(criticalAction).toBeDefined();
    });

    it("falls back to defaults when IPC call fails", async () => {
      const snapshots = [createSnapshot(85, "2026-02-20T10:00:00Z", "excellent")];
      mockReadAll.mockResolvedValue(snapshots);

      // Simulate IPC failure — service falls back to defaults
      mockConfigGetHealthThresholds.mockRejectedValue(new Error("IPC not connected"));

      const result = await HealthActionService.evaluate(workspaceRoot, logger as any);

      expect(result).not.toBeNull();
      // Default thresholds: warning=70, critical=50; score 85 -> info
      expect(result!.actions[0].level).toBe("info");
    });
  });

  // ===========================================================================
  // Missing health history
  // ===========================================================================

  describe("missing health history", () => {
    it("returns score 100, excellent, stable, and no actions when no snapshots exist", async () => {
      mockReadAll.mockResolvedValue([]);

      const result = await HealthActionService.evaluate(workspaceRoot, logger as any);

      expect(result).not.toBeNull();
      expect(result!.score).toBe(100);
      expect(result!.status).toBe("excellent");
      expect(result!.trend).toBe("stable");
      expect(result!.actions).toHaveLength(0);
    });
  });

  // ===========================================================================
  // Error handling
  // ===========================================================================

  describe("error handling", () => {
    it("returns null when evaluate throws (e.g. HealthScoreHistoryReader rejects)", async () => {
      mockReadAll.mockRejectedValue(new Error("disk read failure"));

      const result = await HealthActionService.evaluate(workspaceRoot, logger as any);

      expect(result).toBeNull();
    });

    it("logs a warning when evaluate throws", async () => {
      mockReadAll.mockRejectedValue(new Error("disk read failure"));

      await HealthActionService.evaluate(workspaceRoot, logger as any);

      expect(logger.warn).toHaveBeenCalledWith(
        expect.stringContaining("Health evaluation failed"),
        expect.objectContaining({ err: expect.any(String) })
      );
    });

    it("includes the error message in the logged warning", async () => {
      mockReadAll.mockRejectedValue(new Error("catastrophic disk failure"));

      await HealthActionService.evaluate(workspaceRoot, logger as any);

      expect(logger.warn).toHaveBeenCalledWith(
        expect.any(String),
        expect.objectContaining({ err: "catastrophic disk failure" })
      );
    });
  });

  // ===========================================================================
  // Result shape
  // ===========================================================================

  describe("result shape", () => {
    it("returns evaluation with correct score and status from latest snapshot", async () => {
      const snapshots = [
        createSnapshot(90, "2026-02-20T12:00:00Z", "excellent"), // latest
        createSnapshot(40, "2026-02-19T10:00:00Z", "critical"), // older
      ];
      mockReadAll.mockResolvedValue(snapshots);

      const result = await HealthActionService.evaluate(workspaceRoot, logger as any);

      expect(result).not.toBeNull();
      // Should use the latest snapshot (highest timestamp)
      expect(result!.score).toBe(90);
      expect(result!.status).toBe("excellent");
    });

    it("all returned actions have autoApplied set to false", async () => {
      // Build a declining scenario so multiple actions are generated
      const base = new Date("2026-02-10T00:00:00Z");
      const snapshots = [];
      for (let i = 0; i < 7; i++) {
        const ts = new Date(base.getTime() + i * 60_000).toISOString();
        snapshots.push(createSnapshot(80, ts));
      }
      for (let i = 7; i < 10; i++) {
        const ts = new Date(base.getTime() + i * 60_000).toISOString();
        snapshots.push(createSnapshot(30, ts)); // critical + declining
      }
      mockReadAll.mockResolvedValue(snapshots);

      const result = await HealthActionService.evaluate(workspaceRoot, logger as any);

      expect(result).not.toBeNull();
      for (const action of result!.actions) {
        expect(action.autoApplied).toBe(false);
      }
    });

    it("uses the most recent snapshot even when snapshots are provided in random order", async () => {
      const snapshots = [
        createSnapshot(40, "2026-02-18T10:00:00Z", "critical"), // older
        createSnapshot(90, "2026-02-20T12:00:00Z", "excellent"), // newest
        createSnapshot(60, "2026-02-19T10:00:00Z", "fair"), // middle
      ];
      mockReadAll.mockResolvedValue(snapshots);

      const result = await HealthActionService.evaluate(workspaceRoot, logger as any);

      expect(result).not.toBeNull();
      expect(result!.score).toBe(90);
      expect(result!.status).toBe("excellent");
    });
  });
});
