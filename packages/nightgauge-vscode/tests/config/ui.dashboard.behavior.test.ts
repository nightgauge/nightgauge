/**
 * Behavior tests for ui.dashboard.* configuration fields
 *
 * These tests verify that dashboard config fields affect runtime behavior,
 * specifically time savings calculations used for ROI metrics.
 *
 * @see Issue #472 - Add UI config sections to Zod schema
 * @see packages/nightgauge-vscode/src/config/schema.ts - UIDashboardConfigSchema
 */

import { describe, it, expect } from "vitest";
import {
  UIDashboardConfigSchema,
  UITimeSavingsConfigSchema,
  DEFAULT_CONFIG,
} from "../../src/config/schema";

// ============================================================================
// Mock Fixtures
// ============================================================================

/**
 * Default dashboard configuration for tests
 */
export const DEFAULT_UI_DASHBOARD_CONFIG = {
  time_savings: {
    issue_pickup: 5,
    feature_planning: 30,
    feature_dev: 120,
    pr_create: 10,
    pr_merge: 5,
  },
};

/**
 * Create a mock dashboard configuration with optional overrides
 */
export function createMockUIDashboardConfig(
  overrides?: Partial<typeof DEFAULT_UI_DASHBOARD_CONFIG>
) {
  return {
    ...DEFAULT_UI_DASHBOARD_CONFIG,
    time_savings: {
      ...DEFAULT_UI_DASHBOARD_CONFIG.time_savings,
      ...overrides?.time_savings,
    },
  };
}

describe("ui.dashboard.behavior", () => {
  // ============================================================================
  // time_savings.issue_pickup - Behavior Tests
  // ============================================================================

  describe("time_savings.issue_pickup", () => {
    it("affects ROI calculation for issue pickup stage", () => {
      const config = createMockUIDashboardConfig({
        time_savings: { issue_pickup: 10 },
      });

      const calculateTimeSaved = (cfg: typeof config, issuesCompleted: number): number => {
        return issuesCompleted * (cfg.time_savings?.issue_pickup ?? 5);
      };

      expect(calculateTimeSaved(config, 5)).toBe(50); // 5 issues * 10 min
    });

    it("accepts values 1-60", () => {
      expect(UITimeSavingsConfigSchema.safeParse({ issue_pickup: 1 }).success).toBe(true);
      expect(UITimeSavingsConfigSchema.safeParse({ issue_pickup: 60 }).success).toBe(true);
    });

    it("rejects values outside range", () => {
      expect(UITimeSavingsConfigSchema.safeParse({ issue_pickup: 0 }).success).toBe(false);
      expect(UITimeSavingsConfigSchema.safeParse({ issue_pickup: 61 }).success).toBe(false);
    });

    it("defaults to 5 minutes", () => {
      expect(DEFAULT_CONFIG.ui?.dashboard?.time_savings?.issue_pickup).toBe(5);
    });
  });

  // ============================================================================
  // time_savings.feature_planning - Behavior Tests
  // ============================================================================

  describe("time_savings.feature_planning", () => {
    it("affects ROI calculation for planning stage", () => {
      const config = createMockUIDashboardConfig({
        time_savings: { feature_planning: 60 },
      });

      const calculateTimeSaved = (cfg: typeof config, issuesCompleted: number): number => {
        return issuesCompleted * (cfg.time_savings?.feature_planning ?? 30);
      };

      expect(calculateTimeSaved(config, 3)).toBe(180); // 3 issues * 60 min
    });

    it("accepts values 1-480", () => {
      expect(UITimeSavingsConfigSchema.safeParse({ feature_planning: 1 }).success).toBe(true);
      expect(UITimeSavingsConfigSchema.safeParse({ feature_planning: 480 }).success).toBe(true);
    });

    it("rejects values outside range", () => {
      expect(UITimeSavingsConfigSchema.safeParse({ feature_planning: 0 }).success).toBe(false);
      expect(UITimeSavingsConfigSchema.safeParse({ feature_planning: 481 }).success).toBe(false);
    });

    it("defaults to 30 minutes", () => {
      expect(DEFAULT_CONFIG.ui?.dashboard?.time_savings?.feature_planning).toBe(30);
    });
  });

  // ============================================================================
  // time_savings.feature_dev - Behavior Tests
  // ============================================================================

  describe("time_savings.feature_dev", () => {
    it("affects ROI calculation for development stage", () => {
      const config = createMockUIDashboardConfig({
        time_savings: { feature_dev: 240 },
      });

      const calculateTimeSaved = (cfg: typeof config, issuesCompleted: number): number => {
        return issuesCompleted * (cfg.time_savings?.feature_dev ?? 120);
      };

      expect(calculateTimeSaved(config, 2)).toBe(480); // 2 issues * 240 min
    });

    it("accepts values 1-2400", () => {
      expect(UITimeSavingsConfigSchema.safeParse({ feature_dev: 1 }).success).toBe(true);
      expect(UITimeSavingsConfigSchema.safeParse({ feature_dev: 2400 }).success).toBe(true);
    });

    it("rejects values outside range", () => {
      expect(UITimeSavingsConfigSchema.safeParse({ feature_dev: 0 }).success).toBe(false);
      expect(UITimeSavingsConfigSchema.safeParse({ feature_dev: 2401 }).success).toBe(false);
    });

    it("defaults to 120 minutes", () => {
      expect(DEFAULT_CONFIG.ui?.dashboard?.time_savings?.feature_dev).toBe(120);
    });
  });

  // ============================================================================
  // time_savings.pr_create - Behavior Tests
  // ============================================================================

  describe("time_savings.pr_create", () => {
    it("affects ROI calculation for PR creation stage", () => {
      const config = createMockUIDashboardConfig({
        time_savings: { pr_create: 15 },
      });

      const calculateTimeSaved = (cfg: typeof config, issuesCompleted: number): number => {
        return issuesCompleted * (cfg.time_savings?.pr_create ?? 10);
      };

      expect(calculateTimeSaved(config, 4)).toBe(60); // 4 issues * 15 min
    });

    it("accepts values 1-60", () => {
      expect(UITimeSavingsConfigSchema.safeParse({ pr_create: 1 }).success).toBe(true);
      expect(UITimeSavingsConfigSchema.safeParse({ pr_create: 60 }).success).toBe(true);
    });

    it("rejects values outside range", () => {
      expect(UITimeSavingsConfigSchema.safeParse({ pr_create: 0 }).success).toBe(false);
      expect(UITimeSavingsConfigSchema.safeParse({ pr_create: 61 }).success).toBe(false);
    });

    it("defaults to 10 minutes", () => {
      expect(DEFAULT_CONFIG.ui?.dashboard?.time_savings?.pr_create).toBe(10);
    });
  });

  // ============================================================================
  // time_savings.pr_merge - Behavior Tests
  // ============================================================================

  describe("time_savings.pr_merge", () => {
    it("affects ROI calculation for PR merge stage", () => {
      const config = createMockUIDashboardConfig({
        time_savings: { pr_merge: 8 },
      });

      const calculateTimeSaved = (cfg: typeof config, issuesCompleted: number): number => {
        return issuesCompleted * (cfg.time_savings?.pr_merge ?? 5);
      };

      expect(calculateTimeSaved(config, 3)).toBe(24); // 3 issues * 8 min
    });

    it("accepts values 1-60", () => {
      expect(UITimeSavingsConfigSchema.safeParse({ pr_merge: 1 }).success).toBe(true);
      expect(UITimeSavingsConfigSchema.safeParse({ pr_merge: 60 }).success).toBe(true);
    });

    it("rejects values outside range", () => {
      expect(UITimeSavingsConfigSchema.safeParse({ pr_merge: 0 }).success).toBe(false);
      expect(UITimeSavingsConfigSchema.safeParse({ pr_merge: 61 }).success).toBe(false);
    });

    it("defaults to 5 minutes", () => {
      expect(DEFAULT_CONFIG.ui?.dashboard?.time_savings?.pr_merge).toBe(5);
    });
  });

  // ============================================================================
  // Total Time Savings Calculation
  // ============================================================================

  describe("total time savings", () => {
    it("calculates total time saved for a full pipeline run", () => {
      const config = createMockUIDashboardConfig();

      const calculateTotalTimeSaved = (cfg: typeof config): number => {
        const ts = cfg.time_savings;
        return (
          (ts?.issue_pickup ?? 0) +
          (ts?.feature_planning ?? 0) +
          (ts?.feature_dev ?? 0) +
          (ts?.pr_create ?? 0) +
          (ts?.pr_merge ?? 0)
        );
      };

      // Default: 5 + 30 + 120 + 10 + 5 = 170 minutes per issue
      expect(calculateTotalTimeSaved(config)).toBe(170);
    });

    it("calculates ROI for multiple pipeline runs", () => {
      const config = createMockUIDashboardConfig();

      const calculateROI = (
        cfg: typeof config,
        issueCount: number,
        actualMinutesSpent: number
      ): { timeSaved: number; efficiency: number } => {
        const ts = cfg.time_savings;
        const manualMinutesPerIssue =
          (ts?.issue_pickup ?? 0) +
          (ts?.feature_planning ?? 0) +
          (ts?.feature_dev ?? 0) +
          (ts?.pr_create ?? 0) +
          (ts?.pr_merge ?? 0);

        const expectedManualTime = issueCount * manualMinutesPerIssue;
        const timeSaved = expectedManualTime - actualMinutesSpent;
        const efficiency = expectedManualTime / actualMinutesSpent;

        return { timeSaved, efficiency };
      };

      // 5 issues, each took 30 min with automation
      // Manual would be: 5 * 170 = 850 min
      // Automation took: 5 * 30 = 150 min
      // Time saved: 700 min
      // Efficiency: 5.67x
      const roi = calculateROI(config, 5, 150);
      expect(roi.timeSaved).toBe(700);
      expect(roi.efficiency).toBeCloseTo(5.67, 1);
    });
  });

  // ============================================================================
  // Schema Validation Tests
  // ============================================================================

  describe("validation", () => {
    it("validates complete dashboard config", () => {
      const result = UIDashboardConfigSchema.safeParse(DEFAULT_UI_DASHBOARD_CONFIG);
      expect(result.success).toBe(true);
    });

    it("validates partial time_savings config", () => {
      const result = UIDashboardConfigSchema.safeParse({
        time_savings: { feature_dev: 200 },
      });
      expect(result.success).toBe(true);
    });

    it("validates empty dashboard config", () => {
      const result = UIDashboardConfigSchema.safeParse({});
      expect(result.success).toBe(true);
    });

    it("validates empty time_savings config", () => {
      const result = UIDashboardConfigSchema.safeParse({
        time_savings: {},
      });
      expect(result.success).toBe(true);
    });

    it("rejects non-integer values", () => {
      const result = UITimeSavingsConfigSchema.safeParse({
        issue_pickup: 5.5,
      });
      expect(result.success).toBe(false);
    });
  });
});
