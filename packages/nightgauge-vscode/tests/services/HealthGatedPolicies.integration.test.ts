/**
 * HealthGatedPolicies.integration.test.ts
 *
 * Integration tests for the health-gated policies lifecycle:
 * 1. Declining health → policies activate at appropriate tiers
 * 2. Health recovery → policies deactivate (tier returns to 'none')
 *
 * Tests verify the full evaluate → policy → orchestrator integration
 * by mocking HealthScoreHistoryReader with declining/recovering scores.
 *
 * @see Issue #1395 - Health-gated pipeline policies
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import * as fs from "node:fs/promises";

vi.mock("node:fs/promises");

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

import { HealthActionService } from "../../src/services/HealthActionService";
import type { PipelinePolicyOverrides } from "../../src/services/PipelinePolicyOverrides";

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

describe("Health-Gated Policies Integration", () => {
  const workspaceRoot = "/test/workspace";
  let logger: ReturnType<typeof createMockLogger>;

  beforeEach(() => {
    vi.clearAllMocks();
    logger = createMockLogger();

    // Default: config file not found — falls back to defaults
    vi.mocked(fs.readFile).mockRejectedValue(
      Object.assign(new Error("ENOENT"), { code: "ENOENT" })
    );
  });

  // ===========================================================================
  // Declining health → policies activate progressively
  // ===========================================================================

  describe("declining health activates policies progressively", () => {
    it("transitions none → warning → critical → emergency as scores decline", async () => {
      const results: PipelinePolicyOverrides[] = [];

      // Run 1: healthy (score 85)
      mockReadAll.mockResolvedValue([createSnapshot(85, "2026-02-20T10:00:00Z", "excellent")]);
      const run1 = await HealthActionService.evaluateWithPolicies(workspaceRoot, logger as any);
      results.push(run1.policies);

      // Run 2: warning (score 65)
      mockReadAll.mockResolvedValue([createSnapshot(65, "2026-02-20T11:00:00Z", "fair")]);
      const run2 = await HealthActionService.evaluateWithPolicies(workspaceRoot, logger as any);
      results.push(run2.policies);

      // Run 3: critical (score 42)
      mockReadAll.mockResolvedValue([createSnapshot(42, "2026-02-20T12:00:00Z", "critical")]);
      const run3 = await HealthActionService.evaluateWithPolicies(workspaceRoot, logger as any);
      results.push(run3.policies);

      // Run 4: emergency (score 15)
      mockReadAll.mockResolvedValue([createSnapshot(15, "2026-02-20T13:00:00Z", "critical")]);
      const run4 = await HealthActionService.evaluateWithPolicies(workspaceRoot, logger as any);
      results.push(run4.policies);

      // Verify progressive tier activation
      expect(results[0].tier).toBe("none");
      expect(results[1].tier).toBe("warning");
      expect(results[2].tier).toBe("critical");
      expect(results[3].tier).toBe("emergency");

      // Verify retry budgets scale with severity
      expect(results[0].retryBudgetIncrease).toBe(0);
      expect(results[1].retryBudgetIncrease).toBe(1);
      expect(results[2].retryBudgetIncrease).toBe(2);
      expect(results[3].retryBudgetIncrease).toBe(2);

      // Verify escalation only at critical and above
      expect(results[0].escalateAllStages).toBe(false);
      expect(results[1].escalateAllStages).toBe(false);
      expect(results[2].escalateAllStages).toBe(true);
      expect(results[3].escalateAllStages).toBe(true);

      // Verify auto-routing pause only at emergency
      expect(results[0].pauseAutoRouting).toBe(false);
      expect(results[1].pauseAutoRouting).toBe(false);
      expect(results[2].pauseAutoRouting).toBe(false);
      expect(results[3].pauseAutoRouting).toBe(true);
    });
  });

  // ===========================================================================
  // Health recovery → policies deactivate
  // ===========================================================================

  describe("health recovery deactivates policies", () => {
    it("transitions emergency → critical → warning → none as scores improve", async () => {
      const results: PipelinePolicyOverrides[] = [];

      // Start at emergency (score 15)
      mockReadAll.mockResolvedValue([createSnapshot(15, "2026-02-20T10:00:00Z", "critical")]);
      const run1 = await HealthActionService.evaluateWithPolicies(workspaceRoot, logger as any);
      results.push(run1.policies);

      // Recovery to critical (score 35)
      mockReadAll.mockResolvedValue([createSnapshot(35, "2026-02-20T11:00:00Z", "poor")]);
      const run2 = await HealthActionService.evaluateWithPolicies(workspaceRoot, logger as any);
      results.push(run2.policies);

      // Recovery to warning (score 55)
      mockReadAll.mockResolvedValue([createSnapshot(55, "2026-02-20T12:00:00Z", "fair")]);
      const run3 = await HealthActionService.evaluateWithPolicies(workspaceRoot, logger as any);
      results.push(run3.policies);

      // Full recovery (score 80)
      mockReadAll.mockResolvedValue([createSnapshot(80, "2026-02-20T13:00:00Z", "good")]);
      const run4 = await HealthActionService.evaluateWithPolicies(workspaceRoot, logger as any);
      results.push(run4.policies);

      // Verify progressive recovery
      expect(results[0].tier).toBe("emergency");
      expect(results[1].tier).toBe("critical");
      expect(results[2].tier).toBe("warning");
      expect(results[3].tier).toBe("none");

      // Verify auto-routing unpaused after emergency
      expect(results[0].pauseAutoRouting).toBe(true);
      expect(results[1].pauseAutoRouting).toBe(false);

      // Verify escalation deactivated after critical
      expect(results[1].escalateAllStages).toBe(true);
      expect(results[2].escalateAllStages).toBe(false);

      // Verify retry budget returns to 0
      expect(results[3].retryBudgetIncrease).toBe(0);
    });
  });

  // ===========================================================================
  // Policies are per-run (stateless)
  // ===========================================================================

  describe("policies are per-run only", () => {
    it("each evaluation is independent — no state carried between calls", async () => {
      // Emergency run
      mockReadAll.mockResolvedValue([createSnapshot(15, "2026-02-20T10:00:00Z", "critical")]);
      const emergency = await HealthActionService.evaluateWithPolicies(
        workspaceRoot,
        logger as any
      );
      expect(emergency.policies.tier).toBe("emergency");

      // Immediately healthy — no memory of previous emergency
      mockReadAll.mockResolvedValue([createSnapshot(90, "2026-02-20T11:00:00Z", "excellent")]);
      const healthy = await HealthActionService.evaluateWithPolicies(workspaceRoot, logger as any);
      expect(healthy.policies.tier).toBe("none");
      expect(healthy.policies.retryBudgetIncrease).toBe(0);
      expect(healthy.policies.escalateAllStages).toBe(false);
      expect(healthy.policies.pauseAutoRouting).toBe(false);
    });
  });
});
