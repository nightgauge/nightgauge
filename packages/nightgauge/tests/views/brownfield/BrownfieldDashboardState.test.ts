/**
 * BrownfieldDashboardState.test.ts
 *
 * Unit tests for BrownfieldDashboardState:
 * - Computes dimension breakdown from health report
 * - Calculates modernization progress percentage
 * - Detects active phase correctly
 * - Handles partial data (only health, no security)
 * - Computes before/after deltas from history
 *
 * @see Issue #1163 - Brownfield Modernization Progress Dashboard
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import { BrownfieldDashboardState } from "../../../src/views/brownfield/BrownfieldDashboardState";
import type { BrownfieldDashboardData } from "../../../src/views/brownfield/BrownfieldTypes";

vi.mock("vscode", () => ({
  EventEmitter: class {
    event = vi.fn();
    fire = vi.fn();
    dispose = vi.fn();
  },
  RelativePattern: class {
    constructor(
      public base: string,
      public pattern: string
    ) {}
  },
  workspace: {
    createFileSystemWatcher: vi.fn(() => ({
      onDidCreate: vi.fn(),
      onDidChange: vi.fn(),
      onDidDelete: vi.fn(),
      dispose: vi.fn(),
    })),
  },
}));

// Create a mock data service
function createMockDataService(data: BrownfieldDashboardData) {
  return {
    loadAll: vi.fn(async () => data),
    loadHealth: vi.fn(),
    loadSecurity: vi.fn(),
    loadPlan: vi.fn(),
    loadDeps: vi.fn(),
    loadHistory: vi.fn(),
    onDataChanged: vi.fn(),
    dispose: vi.fn(),
  } as any;
}

describe("BrownfieldDashboardState", () => {
  describe("getDimensionBreakdown", () => {
    it("returns empty array when no health data", async () => {
      const data: BrownfieldDashboardData = {
        health: null,
        security: null,
        plan: null,
        deps: null,
        history: [],
        hasAnyData: false,
      };
      const service = createMockDataService(data);
      const state = new BrownfieldDashboardState(service);
      await state.loadData();

      const result = state.getDimensionBreakdown();
      expect(result).toEqual([]);
    });

    it("returns dimensions sorted by score ascending", async () => {
      const data: BrownfieldDashboardData = {
        health: {
          schema_version: "1.0",
          assessment_date: "2026-02-21",
          summary: {
            overall_health_score: 65,
            status: "fair",
            dimensions_assessed: 3,
            dimensions_skipped: 0,
          },
          dimensions: {
            test_coverage: {
              score: 80,
              status: "good",
              weight: 0.2,
              findings: [],
              metrics: {},
            },
            code_quality: {
              score: 40,
              status: "poor",
              weight: 0.15,
              findings: [],
              metrics: {},
            },
            documentation: {
              score: 60,
              status: "fair",
              weight: 0.15,
              findings: [],
              metrics: {},
            },
          },
          top_recommendations: [],
          created_at: "2026-02-21T00:00:00Z",
        },
        security: null,
        plan: null,
        deps: null,
        history: [],
        hasAnyData: true,
      };

      const service = createMockDataService(data);
      const state = new BrownfieldDashboardState(service);
      await state.loadData();

      const result = state.getDimensionBreakdown();
      expect(result).toHaveLength(3);
      // Sorted ascending: code_quality(40), documentation(60), test_coverage(80)
      expect(result[0].name).toBe("code_quality");
      expect(result[0].score).toBe(40);
      expect(result[1].name).toBe("documentation");
      expect(result[2].name).toBe("test_coverage");
    });
  });

  describe("getModernizationProgress", () => {
    it("returns zero progress when no plan data", async () => {
      const data: BrownfieldDashboardData = {
        health: null,
        security: null,
        plan: null,
        deps: null,
        history: [],
        hasAnyData: false,
      };
      const service = createMockDataService(data);
      const state = new BrownfieldDashboardState(service);
      await state.loadData();

      const result = state.getModernizationProgress();
      expect(result.totalTasks).toBe(0);
      expect(result.percent).toBe(0);
      expect(result.activePhase).toBeNull();
    });

    it("returns correct total tasks and active phase", async () => {
      const data: BrownfieldDashboardData = {
        health: null,
        security: null,
        plan: {
          schema_version: "1.0",
          generated_at: "2026-02-21",
          summary: {
            total_tasks: 15,
            tasks_by_phase: { "0": 3, "1": 5, "2": 7 },
            tasks_by_effort: { XS: 3, S: 5, M: 4, L: 2, XL: 1 },
            tasks_by_risk: { low: 8, medium: 5, high: 2, critical: 0 },
            total_story_points: 45,
            quick_wins_count: 3,
            estimated_sprints: 5,
            estimated_weeks: 10,
          },
          quick_wins: [],
          phases: [
            {
              phase_number: 0,
              name: "Quick Wins",
              description: "Easy fixes",
              tasks: [
                {
                  id: "t1",
                  title: "Fix lint",
                  description: "",
                  rationale: "",
                  effort: "XS",
                  risk: "low",
                  dependencies: [],
                  execution_method: "automated",
                  source: "health-check",
                  source_dimension: "code_quality",
                },
              ],
              total_story_points: 5,
              estimated_sprints: 1,
            },
          ],
          created_at: "2026-02-21T00:00:00Z",
        },
        deps: null,
        history: [],
        hasAnyData: true,
      };

      const service = createMockDataService(data);
      const state = new BrownfieldDashboardState(service);
      await state.loadData();

      const result = state.getModernizationProgress();
      expect(result.totalTasks).toBe(15);
      expect(result.activePhase).not.toBeNull();
      expect(result.activePhase!.name).toBe("Quick Wins");
      expect(result.activePhaseIndex).toBe(0);
    });
  });

  describe("getSecuritySeverityCounts", () => {
    it("returns zeros when no security data", async () => {
      const data: BrownfieldDashboardData = {
        health: null,
        security: null,
        plan: null,
        deps: null,
        history: [],
        hasAnyData: false,
      };
      const service = createMockDataService(data);
      const state = new BrownfieldDashboardState(service);
      await state.loadData();

      const result = state.getSecuritySeverityCounts();
      expect(result.critical).toBe(0);
      expect(result.high).toBe(0);
    });

    it("returns correct counts from security data", async () => {
      const data: BrownfieldDashboardData = {
        health: null,
        security: {
          schema_version: "1.0",
          assessment_date: "2026-02-21",
          summary: {
            overall_security_score: 70,
            status: "good",
            dimensions_assessed: 7,
            dimensions_skipped: 0,
            total_findings: 6,
            findings_by_severity: {
              critical: 1,
              high: 2,
              medium: 3,
              low: 0,
              info: 0,
            },
          },
          dimensions: {},
          top_recommendations: [],
          created_at: "2026-02-21T00:00:00Z",
        },
        plan: null,
        deps: null,
        history: [],
        hasAnyData: true,
      };

      const service = createMockDataService(data);
      const state = new BrownfieldDashboardState(service);
      await state.loadData();

      const result = state.getSecuritySeverityCounts();
      expect(result.critical).toBe(1);
      expect(result.high).toBe(2);
      expect(result.medium).toBe(3);
    });
  });

  describe("getBeforeAfterDelta", () => {
    it("returns null when history has fewer than 2 entries", async () => {
      const data: BrownfieldDashboardData = {
        health: null,
        security: null,
        plan: null,
        deps: null,
        history: [
          {
            timestamp: "2026-02-20T00:00:00Z",
            health_score: 50,
            security_score: 60,
            tasks_completed: 0,
            tasks_total: 10,
          },
        ],
        hasAnyData: false,
      };
      const service = createMockDataService(data);
      const state = new BrownfieldDashboardState(service);
      await state.loadData();

      expect(state.getBeforeAfterDelta()).toBeNull();
    });

    it("returns correct delta from history", async () => {
      const data: BrownfieldDashboardData = {
        health: null,
        security: null,
        plan: null,
        deps: null,
        history: [
          {
            timestamp: "2026-02-18T00:00:00Z",
            health_score: 50,
            security_score: 60,
            tasks_completed: 0,
            tasks_total: 10,
          },
          {
            timestamp: "2026-02-20T00:00:00Z",
            health_score: 65,
            security_score: 75,
            tasks_completed: 3,
            tasks_total: 10,
          },
        ],
        hasAnyData: false,
      };
      const service = createMockDataService(data);
      const state = new BrownfieldDashboardState(service);
      await state.loadData();

      const delta = state.getBeforeAfterDelta();
      expect(delta).not.toBeNull();
      expect(delta!.initialHealthScore).toBe(50);
      expect(delta!.currentHealthScore).toBe(65);
      expect(delta!.initialSecurityScore).toBe(60);
      expect(delta!.currentSecurityScore).toBe(75);
    });
  });

  describe("getDependencyHealth", () => {
    it("returns zeros when no dep data", async () => {
      const data: BrownfieldDashboardData = {
        health: null,
        security: null,
        plan: null,
        deps: null,
        history: [],
        hasAnyData: false,
      };
      const service = createMockDataService(data);
      const state = new BrownfieldDashboardState(service);
      await state.loadData();

      const result = state.getDependencyHealth();
      expect(result.total).toBe(0);
      expect(result.upToDatePercent).toBe(0);
    });

    it("returns correct ratios from dep data", async () => {
      const data: BrownfieldDashboardData = {
        health: null,
        security: null,
        plan: null,
        deps: {
          schema_version: "1.0",
          generated_at: "2026-02-21",
          summary: {
            total_dependencies: 100,
            outdated_count: 20,
            vulnerable_count: 5,
            deprecated_count: 3,
            unmaintained_count: 2,
            categories: {},
            auto_fixable: 10,
            needs_manual: 10,
          },
          dependencies: [],
          update_groups: [],
        },
        history: [],
        hasAnyData: true,
      };

      const service = createMockDataService(data);
      const state = new BrownfieldDashboardState(service);
      await state.loadData();

      const result = state.getDependencyHealth();
      expect(result.total).toBe(100);
      expect(result.outdated).toBe(20);
      expect(result.vulnerable).toBe(5);
      expect(result.upToDatePercent).toBe(80);
    });
  });

  describe("getQuickWins", () => {
    it("returns top 5 quick wins", async () => {
      const wins = Array.from({ length: 8 }, (_, i) => ({
        task_id: `t${i}`,
        title: `Win ${i}`,
        effort: "XS" as const,
        impact: "High",
        phase: 0,
      }));

      const data: BrownfieldDashboardData = {
        health: null,
        security: null,
        plan: {
          schema_version: "1.0",
          generated_at: "2026-02-21",
          summary: {
            total_tasks: 8,
            tasks_by_phase: {},
            tasks_by_effort: { XS: 8, S: 0, M: 0, L: 0, XL: 0 },
            tasks_by_risk: { low: 8, medium: 0, high: 0, critical: 0 },
            total_story_points: 8,
            quick_wins_count: 8,
            estimated_sprints: 1,
            estimated_weeks: 2,
          },
          quick_wins: wins,
          phases: [],
          created_at: "2026-02-21T00:00:00Z",
        },
        deps: null,
        history: [],
        hasAnyData: true,
      };

      const service = createMockDataService(data);
      const state = new BrownfieldDashboardState(service);
      await state.loadData();

      const result = state.getQuickWins();
      expect(result).toHaveLength(5);
    });
  });

  describe("getStatus", () => {
    it("maps scores to correct statuses", () => {
      expect(BrownfieldDashboardState.getStatus(95)).toBe("excellent");
      expect(BrownfieldDashboardState.getStatus(75)).toBe("good");
      expect(BrownfieldDashboardState.getStatus(55)).toBe("fair");
      expect(BrownfieldDashboardState.getStatus(35)).toBe("poor");
      expect(BrownfieldDashboardState.getStatus(15)).toBe("critical");
    });
  });
});
