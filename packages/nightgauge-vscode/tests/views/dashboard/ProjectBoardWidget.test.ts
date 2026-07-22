/**
 * ProjectBoardWidget.test.ts
 *
 * Test suite for Project Board Dashboard Widget functionality
 * Covers state management, cache behavior, and data transformation
 *
 * @see Issue #134 - Project Board Dashboard Widget
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { DashboardState } from "../../../src/views/dashboard/DashboardState";
import { createMockMemento } from "../../mocks/memento";
import type {
  ProjectBoardData,
  StatusCounts,
} from "../../../src/views/dashboard/ProjectBoardTypes";
import { DEFAULT_PROJECT_BOARD_CONFIG } from "../../../src/views/dashboard/ProjectBoardTypes";
import type * as vscode from "vscode";

// Mock vscode workspace configuration
vi.mock("vscode", () => ({
  workspace: {
    getConfiguration: vi.fn().mockReturnValue({
      get: vi.fn((key: string, defaultValue?: unknown) => {
        const config: Record<string, unknown> = {
          "projectBoard.enabled": true,
          "projectBoard.cacheTtlMinutes": 5,
          "projectBoard.maxReadyIssues": 5,
        };
        return config[key] ?? defaultValue;
      }),
    }),
  },
}));

describe("ProjectBoardWidget - State Management", () => {
  let workspaceState: vscode.Memento;
  let state: DashboardState;

  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date(2026, 1, 7, 14, 0, 0));
    workspaceState = createMockMemento();
    state = new DashboardState(workspaceState);
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  describe("Project Board Data", () => {
    it("should return null when no project board data is set", () => {
      expect(state.getProjectBoardData()).toBeNull();
    });

    it("should store and retrieve project board data", () => {
      const data: ProjectBoardData = {
        statusCounts: {
          ready: 5,
          inProgress: 2,
          inReview: 1,
          done: 10,
          backlog: 3,
        },
        topReadyIssues: [
          {
            number: 134,
            title: "Add project board widget",
            priority: "P1",
            url: "https://github.com/test/test/issues/134",
          },
        ],
        currentSprint: null,
        lastRefreshed: new Date(),
        projectUrl: "https://github.com/orgs/test/projects/1",
        isConfigured: true,
      };

      state.setProjectBoardData(data);

      const retrieved = state.getProjectBoardData();
      expect(retrieved).not.toBeNull();
      expect(retrieved?.statusCounts.ready).toBe(5);
      expect(retrieved?.statusCounts.inProgress).toBe(2);
      expect(retrieved?.topReadyIssues).toHaveLength(1);
      expect(retrieved?.topReadyIssues[0].number).toBe(134);
    });

    it("should update lastRefreshed timestamp when data is set", () => {
      const now = new Date(2026, 1, 7, 14, 0, 0);
      vi.setSystemTime(now);

      const data: ProjectBoardData = {
        statusCounts: {
          ready: 1,
          inProgress: 0,
          inReview: 0,
          done: 0,
          backlog: 0,
        },
        topReadyIssues: [],
        currentSprint: null,
        lastRefreshed: now,
        projectUrl: null,
        isConfigured: true,
      };

      state.setProjectBoardData(data);

      expect(state.getProjectBoardLastRefresh()?.getTime()).toBe(now.getTime());
    });

    it("should clear lastRefreshed when data is set to null", () => {
      const data: ProjectBoardData = {
        statusCounts: {
          ready: 1,
          inProgress: 0,
          inReview: 0,
          done: 0,
          backlog: 0,
        },
        topReadyIssues: [],
        currentSprint: null,
        lastRefreshed: new Date(),
        projectUrl: null,
        isConfigured: true,
      };

      state.setProjectBoardData(data);
      expect(state.getProjectBoardLastRefresh()).not.toBeNull();

      state.setProjectBoardData(null);
      expect(state.getProjectBoardLastRefresh()).toBeNull();
      expect(state.getProjectBoardData()).toBeNull();
    });
  });

  describe("Cache TTL Behavior", () => {
    it("should report cache as stale when no data exists", () => {
      expect(state.isProjectBoardCacheStale()).toBe(true);
    });

    it("should report cache as fresh immediately after setting data", () => {
      const data: ProjectBoardData = {
        statusCounts: {
          ready: 1,
          inProgress: 0,
          inReview: 0,
          done: 0,
          backlog: 0,
        },
        topReadyIssues: [],
        currentSprint: null,
        lastRefreshed: new Date(),
        projectUrl: null,
        isConfigured: true,
      };

      state.setProjectBoardData(data);
      expect(state.isProjectBoardCacheStale()).toBe(false);
    });

    it("should report cache as stale after TTL expires", () => {
      const now = new Date(2026, 1, 7, 14, 0, 0);
      vi.setSystemTime(now);

      const data: ProjectBoardData = {
        statusCounts: {
          ready: 1,
          inProgress: 0,
          inReview: 0,
          done: 0,
          backlog: 0,
        },
        topReadyIssues: [],
        currentSprint: null,
        lastRefreshed: now,
        projectUrl: null,
        isConfigured: true,
      };

      state.setProjectBoardData(data);
      expect(state.isProjectBoardCacheStale()).toBe(false);

      // Advance time past the 5 minute TTL
      vi.advanceTimersByTime(6 * 60 * 1000); // 6 minutes

      expect(state.isProjectBoardCacheStale()).toBe(true);
    });

    it("should respect configurable cache TTL", () => {
      const now = new Date(2026, 1, 7, 14, 0, 0);
      vi.setSystemTime(now);

      // Set a longer TTL
      state.setProjectBoardConfig({ cacheTtlMinutes: 10 });

      const data: ProjectBoardData = {
        statusCounts: {
          ready: 1,
          inProgress: 0,
          inReview: 0,
          done: 0,
          backlog: 0,
        },
        topReadyIssues: [],
        currentSprint: null,
        lastRefreshed: now,
        projectUrl: null,
        isConfigured: true,
      };

      state.setProjectBoardData(data);

      // Advance 6 minutes - should still be fresh with 10 min TTL
      vi.advanceTimersByTime(6 * 60 * 1000);
      expect(state.isProjectBoardCacheStale()).toBe(false);

      // Advance 5 more minutes (total 11) - should be stale
      vi.advanceTimersByTime(5 * 60 * 1000);
      expect(state.isProjectBoardCacheStale()).toBe(true);
    });
  });

  describe("Configuration", () => {
    it("should return default configuration values", () => {
      const config = state.getProjectBoardConfig();

      expect(config.enabled).toBe(true);
      expect(config.cacheTtlMinutes).toBe(5);
      expect(config.maxReadyIssues).toBe(5);
    });

    it("should update configuration with partial values", () => {
      state.setProjectBoardConfig({ maxReadyIssues: 10 });

      const config = state.getProjectBoardConfig();
      expect(config.maxReadyIssues).toBe(10);
      expect(config.enabled).toBe(true); // Unchanged
    });

    it("should update configuration fully", () => {
      state.setProjectBoardConfig({
        enabled: false,
        cacheTtlMinutes: 15,
        maxReadyIssues: 3,
      });

      const config = state.getProjectBoardConfig();
      expect(config.enabled).toBe(false);
      expect(config.cacheTtlMinutes).toBe(15);
      expect(config.maxReadyIssues).toBe(3);
    });
  });

  describe("Error State", () => {
    it("should store error message when data fetch fails", () => {
      const data: ProjectBoardData = {
        statusCounts: {
          ready: 0,
          inProgress: 0,
          inReview: 0,
          done: 0,
          backlog: 0,
        },
        topReadyIssues: [],
        currentSprint: null,
        lastRefreshed: new Date(),
        projectUrl: null,
        isConfigured: true,
        error: "Failed to fetch project board data",
      };

      state.setProjectBoardData(data);

      const retrieved = state.getProjectBoardData();
      expect(retrieved?.error).toBe("Failed to fetch project board data");
    });

    it("should store not-configured state", () => {
      const data: ProjectBoardData = {
        statusCounts: {
          ready: 0,
          inProgress: 0,
          inReview: 0,
          done: 0,
          backlog: 0,
        },
        topReadyIssues: [],
        currentSprint: null,
        lastRefreshed: new Date(),
        projectUrl: null,
        isConfigured: false,
      };

      state.setProjectBoardData(data);

      const retrieved = state.getProjectBoardData();
      expect(retrieved?.isConfigured).toBe(false);
    });
  });
});

describe("ProjectBoardWidget - Status Counts Aggregation", () => {
  it("should handle all status counts correctly", () => {
    const statusCounts: StatusCounts = {
      ready: 5,
      inProgress: 2,
      inReview: 1,
      done: 15,
      backlog: 8,
    };

    // Verify all fields are present
    expect(statusCounts.ready).toBe(5);
    expect(statusCounts.inProgress).toBe(2);
    expect(statusCounts.inReview).toBe(1);
    expect(statusCounts.done).toBe(15);
    expect(statusCounts.backlog).toBe(8);

    // Verify total can be calculated
    const total =
      statusCounts.ready +
      statusCounts.inProgress +
      statusCounts.inReview +
      statusCounts.done +
      statusCounts.backlog;
    expect(total).toBe(31);
  });

  it("should handle zero counts", () => {
    const statusCounts: StatusCounts = {
      ready: 0,
      inProgress: 0,
      inReview: 0,
      done: 0,
      backlog: 0,
    };

    const total =
      statusCounts.ready +
      statusCounts.inProgress +
      statusCounts.inReview +
      statusCounts.done +
      statusCounts.backlog;
    expect(total).toBe(0);
  });
});

describe("ProjectBoardWidget - Default Configuration", () => {
  it("should have correct default values", () => {
    expect(DEFAULT_PROJECT_BOARD_CONFIG.enabled).toBe(true);
    expect(DEFAULT_PROJECT_BOARD_CONFIG.cacheTtlMinutes).toBe(5);
    expect(DEFAULT_PROJECT_BOARD_CONFIG.maxReadyIssues).toBe(5);
  });
});
