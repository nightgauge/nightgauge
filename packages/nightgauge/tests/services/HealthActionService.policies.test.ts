/**
 * HealthActionService.policies.test.ts
 *
 * Unit tests for health-gated pipeline policies (Issue #1395).
 *
 * Tests evaluateWithPolicies() which extends evaluate() with per-run
 * policy overrides based on health score thresholds:
 * - Healthy (score >= 70): tier 'none', no overrides
 * - Warning (score < 70): tier 'warning', retryBudgetIncrease: 1
 * - Critical (score < 50): tier 'critical', retryBudgetIncrease: 2, escalateAllStages
 * - Emergency (score < 30): tier 'emergency', all overrides + pauseAutoRouting
 * - Disabled via config: always returns tier 'none'
 * - No health data: tier 'none' (defaults to score 100)
 *
 * @see Issue #1395 - Health-gated pipeline policies
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

describe("HealthActionService.evaluateWithPolicies", () => {
  const workspaceRoot = "/test/workspace";
  let logger: ReturnType<typeof createMockLogger>;

  beforeEach(() => {
    vi.clearAllMocks();
    logger = createMockLogger();

    // Default: IPC returns default thresholds with policies enabled
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
  // Healthy — no policies
  // ===========================================================================

  describe("healthy (score >= 70)", () => {
    it('returns tier "none" with no overrides when score is 85', async () => {
      const snapshots = [createSnapshot(85, "2026-02-20T10:00:00Z", "excellent")];
      mockReadAll.mockResolvedValue(snapshots);

      const { policies } = await HealthActionService.evaluateWithPolicies(
        workspaceRoot,
        logger as any
      );

      expect(policies.tier).toBe("none");
      expect(policies.retryBudgetIncrease).toBe(0);
      expect(policies.escalateAllStages).toBe(false);
      expect(policies.pauseAutoRouting).toBe(false);
      expect(policies.reasons).toHaveLength(0);
      expect(policies.score).toBe(85);
    });

    it('returns tier "none" at exactly score 70', async () => {
      const snapshots = [createSnapshot(70, "2026-02-20T10:00:00Z", "good")];
      mockReadAll.mockResolvedValue(snapshots);

      const { policies } = await HealthActionService.evaluateWithPolicies(
        workspaceRoot,
        logger as any
      );

      expect(policies.tier).toBe("none");
      expect(policies.retryBudgetIncrease).toBe(0);
    });
  });

  // ===========================================================================
  // Warning — retry budget +1
  // ===========================================================================

  describe("warning (score 50-69)", () => {
    it('returns tier "warning" with retryBudgetIncrease: 1 when score is 60', async () => {
      const snapshots = [createSnapshot(60, "2026-02-20T10:00:00Z", "fair")];
      mockReadAll.mockResolvedValue(snapshots);

      const { evaluation, policies } = await HealthActionService.evaluateWithPolicies(
        workspaceRoot,
        logger as any
      );

      expect(policies.tier).toBe("warning");
      expect(policies.retryBudgetIncrease).toBe(1);
      expect(policies.escalateAllStages).toBe(false);
      expect(policies.pauseAutoRouting).toBe(false);
      expect(policies.reasons).toHaveLength(1);
      expect(policies.reasons[0]).toContain("60");
      expect(policies.score).toBe(60);

      // Should include autoApplied policy actions
      const autoAppliedActions = evaluation.actions.filter((a) => a.autoApplied);
      expect(autoAppliedActions.length).toBeGreaterThanOrEqual(1);
    });

    it('returns tier "warning" at exactly score 50', async () => {
      const snapshots = [createSnapshot(50, "2026-02-20T10:00:00Z", "fair")];
      mockReadAll.mockResolvedValue(snapshots);

      const { policies } = await HealthActionService.evaluateWithPolicies(
        workspaceRoot,
        logger as any
      );

      // Score 50 is >= critical (50) and < warning (70) → warning tier
      expect(policies.tier).toBe("warning");
      expect(policies.retryBudgetIncrease).toBe(1);
    });
  });

  // ===========================================================================
  // Critical — retry budget +2, escalate all stages
  // ===========================================================================

  describe("critical (score 30-49)", () => {
    it('returns tier "critical" with retryBudgetIncrease: 2 and escalation when score is 42', async () => {
      const snapshots = [createSnapshot(42, "2026-02-20T10:00:00Z", "critical")];
      mockReadAll.mockResolvedValue(snapshots);

      const { evaluation, policies } = await HealthActionService.evaluateWithPolicies(
        workspaceRoot,
        logger as any
      );

      expect(policies.tier).toBe("critical");
      expect(policies.retryBudgetIncrease).toBe(2);
      expect(policies.escalateAllStages).toBe(true);
      expect(policies.pauseAutoRouting).toBe(false);
      expect(policies.reasons).toHaveLength(1);
      expect(policies.reasons[0]).toContain("42");
      expect(policies.score).toBe(42);

      // Should include autoApplied policy actions for retry + escalation
      const autoAppliedActions = evaluation.actions.filter((a) => a.autoApplied);
      expect(autoAppliedActions.length).toBe(2);
    });

    it('returns tier "critical" at exactly score 30', async () => {
      const snapshots = [createSnapshot(30, "2026-02-20T10:00:00Z", "critical")];
      mockReadAll.mockResolvedValue(snapshots);

      const { policies } = await HealthActionService.evaluateWithPolicies(
        workspaceRoot,
        logger as any
      );

      // Score 30 is >= emergency (30) and < critical (50) → critical tier
      expect(policies.tier).toBe("critical");
      expect(policies.retryBudgetIncrease).toBe(2);
      expect(policies.escalateAllStages).toBe(true);
    });
  });

  // ===========================================================================
  // Emergency — all overrides + pause auto-routing
  // ===========================================================================

  describe("emergency (score < 30)", () => {
    it('returns tier "emergency" with all overrides when score is 20', async () => {
      const snapshots = [createSnapshot(20, "2026-02-20T10:00:00Z", "critical")];
      mockReadAll.mockResolvedValue(snapshots);

      const { evaluation, policies } = await HealthActionService.evaluateWithPolicies(
        workspaceRoot,
        logger as any
      );

      expect(policies.tier).toBe("emergency");
      expect(policies.retryBudgetIncrease).toBe(2);
      expect(policies.escalateAllStages).toBe(true);
      expect(policies.pauseAutoRouting).toBe(true);
      expect(policies.reasons).toHaveLength(1);
      expect(policies.reasons[0]).toContain("20");
      expect(policies.score).toBe(20);

      // Should include autoApplied policy actions for retry + escalation + pause
      const autoAppliedActions = evaluation.actions.filter((a) => a.autoApplied);
      expect(autoAppliedActions.length).toBe(3);
    });

    it('returns tier "emergency" at score 0', async () => {
      const snapshots = [createSnapshot(0, "2026-02-20T10:00:00Z", "critical")];
      mockReadAll.mockResolvedValue(snapshots);

      const { policies } = await HealthActionService.evaluateWithPolicies(
        workspaceRoot,
        logger as any
      );

      expect(policies.tier).toBe("emergency");
      expect(policies.pauseAutoRouting).toBe(true);
    });
  });

  // ===========================================================================
  // Policies disabled via config
  // ===========================================================================

  describe("config: health_policies_enabled", () => {
    it('returns tier "none" when health_policies_enabled is false even with critical score', async () => {
      const snapshots = [createSnapshot(20, "2026-02-20T10:00:00Z", "critical")];
      mockReadAll.mockResolvedValue(snapshots);

      // Override IPC to return policiesEnabled: false
      mockConfigGetHealthThresholds.mockResolvedValue({
        warningThreshold: 70,
        criticalThreshold: 50,
        emergencyThreshold: 30,
        actionsEnabled: true,
        policiesEnabled: false,
        feedbackLoopEnabled: true,
      });

      const { policies } = await HealthActionService.evaluateWithPolicies(
        workspaceRoot,
        logger as any
      );

      expect(policies.tier).toBe("none");
      expect(policies.retryBudgetIncrease).toBe(0);
      expect(policies.escalateAllStages).toBe(false);
      expect(policies.pauseAutoRouting).toBe(false);
      expect(policies.score).toBe(20);
    });
  });

  // ===========================================================================
  // Custom thresholds
  // ===========================================================================

  describe("custom thresholds", () => {
    it("uses custom emergency threshold from config", async () => {
      const snapshots = [createSnapshot(35, "2026-02-20T10:00:00Z", "poor")];
      mockReadAll.mockResolvedValue(snapshots);

      // Override IPC to return emergencyThreshold: 40 — score 35 should be emergency
      mockConfigGetHealthThresholds.mockResolvedValue({
        warningThreshold: 70,
        criticalThreshold: 50,
        emergencyThreshold: 40,
        actionsEnabled: true,
        policiesEnabled: true,
        feedbackLoopEnabled: true,
      });

      const { policies } = await HealthActionService.evaluateWithPolicies(
        workspaceRoot,
        logger as any
      );

      expect(policies.tier).toBe("emergency");
      expect(policies.pauseAutoRouting).toBe(true);
    });
  });

  // ===========================================================================
  // No health data
  // ===========================================================================

  describe("no health data", () => {
    it('returns tier "none" when no snapshots exist (score defaults to 100)', async () => {
      mockReadAll.mockResolvedValue([]);

      const { evaluation, policies } = await HealthActionService.evaluateWithPolicies(
        workspaceRoot,
        logger as any
      );

      expect(policies.tier).toBe("none");
      expect(policies.score).toBe(100);
      expect(evaluation.score).toBe(100);
    });

    it('returns tier "none" when evaluate returns null (read error)', async () => {
      mockReadAll.mockRejectedValue(new Error("disk read failure"));

      const { evaluation, policies } = await HealthActionService.evaluateWithPolicies(
        workspaceRoot,
        logger as any
      );

      expect(policies.tier).toBe("none");
      expect(policies.score).toBe(100);
      expect(evaluation.score).toBe(100);
    });
  });

  // ===========================================================================
  // Evaluation shape
  // ===========================================================================

  describe("evaluation shape", () => {
    it("returns evaluation and policies together", async () => {
      const snapshots = [createSnapshot(60, "2026-02-20T10:00:00Z", "fair")];
      mockReadAll.mockResolvedValue(snapshots);

      const result = await HealthActionService.evaluateWithPolicies(workspaceRoot, logger as any);

      expect(result).toHaveProperty("evaluation");
      expect(result).toHaveProperty("policies");
      expect(result.evaluation.score).toBe(60);
      expect(result.policies.timestamp).toBeDefined();
    });

    it("preserves original evaluation actions alongside policy actions", async () => {
      const snapshots = [createSnapshot(40, "2026-02-20T10:00:00Z", "critical")];
      mockReadAll.mockResolvedValue(snapshots);

      const { evaluation } = await HealthActionService.evaluateWithPolicies(
        workspaceRoot,
        logger as any
      );

      // Should have both advisory (autoApplied: false) and policy (autoApplied: true) actions
      const advisoryActions = evaluation.actions.filter((a) => !a.autoApplied);
      const policyActions = evaluation.actions.filter((a) => a.autoApplied);

      expect(advisoryActions.length).toBeGreaterThanOrEqual(1);
      expect(policyActions.length).toBeGreaterThanOrEqual(1);
    });
  });
});
