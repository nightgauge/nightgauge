/**
 * DashboardState.pagination.test.ts
 *
 * Tests for history pagination (Issue #983).
 * Covers getHistoryPage(), configurable historyLimit, and page size.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { createMockMemento } from "../../mocks/memento";
import type * as vscode from "vscode";

// Mock vscode with configurable history settings
const mockConfigValues: Record<string, unknown> = {};

vi.mock("vscode", () => ({
  workspace: {
    getConfiguration: vi.fn(() => ({
      get: vi.fn((key: string, defaultValue?: unknown) => {
        return mockConfigValues[key] ?? defaultValue;
      }),
    })),
  },
  EventEmitter: class EventEmitter {
    event = vi.fn();
    fire = vi.fn();
    dispose = vi.fn();
  },
}));

import { DashboardState } from "../../../src/views/dashboard/DashboardState";

describe("DashboardState - History Pagination (Issue #983)", () => {
  let workspaceState: vscode.Memento;

  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(new Date(2026, 1, 4, 10, 0, 0));
    workspaceState = createMockMemento();
    // Reset config to defaults
    Object.keys(mockConfigValues).forEach((key) => delete mockConfigValues[key]);
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  /**
   * Helper to create a state with N completed runs in history
   */
  function createStateWithHistory(
    count: number,
    configOverrides?: Record<string, unknown>
  ): DashboardState {
    if (configOverrides) {
      Object.assign(mockConfigValues, configOverrides);
    }
    const state = new DashboardState(workspaceState);

    for (let i = 0; i < count; i++) {
      state.startRun(100 + i, `Test issue ${i}`, `feat/${100 + i}-test`);
      state.completeRun();
    }

    return state;
  }

  describe("getHistoryPage()", () => {
    it("should return correct slice for given offset and limit", () => {
      const state = createStateWithHistory(30);
      const page = state.getHistoryPage(0, 10);

      expect(page.items.length).toBe(10);
      expect(page.total).toBe(30);
      expect(page.hasMore).toBe(true);
    });

    it("should return hasMore=true when more items exist", () => {
      const state = createStateWithHistory(25);
      const page = state.getHistoryPage(0, 20);

      expect(page.items.length).toBe(20);
      expect(page.total).toBe(25);
      expect(page.hasMore).toBe(true);
    });

    it("should return hasMore=false when all items are displayed", () => {
      const state = createStateWithHistory(15);
      const page = state.getHistoryPage(0, 20);

      expect(page.items.length).toBe(15);
      expect(page.total).toBe(15);
      expect(page.hasMore).toBe(false);
    });

    it("should return hasMore=false when offset+limit equals total", () => {
      const state = createStateWithHistory(20);
      const page = state.getHistoryPage(0, 20);

      expect(page.items.length).toBe(20);
      expect(page.total).toBe(20);
      expect(page.hasMore).toBe(false);
    });

    it("should work correctly with empty history", () => {
      const state = new DashboardState(workspaceState);
      const page = state.getHistoryPage(0, 20);

      expect(page.items.length).toBe(0);
      expect(page.total).toBe(0);
      expect(page.hasMore).toBe(false);
    });

    it("should return correct second page", () => {
      const state = createStateWithHistory(35);
      const page = state.getHistoryPage(20, 20);

      expect(page.items.length).toBe(15);
      expect(page.total).toBe(35);
      expect(page.hasMore).toBe(false);
    });

    it("should handle offset beyond total gracefully", () => {
      const state = createStateWithHistory(10);
      const page = state.getHistoryPage(20, 10);

      expect(page.items.length).toBe(0);
      expect(page.total).toBe(10);
      expect(page.hasMore).toBe(false);
    });
  });

  describe("Configurable history limit", () => {
    it("should default to 50 when no config is set", () => {
      const state = new DashboardState(workspaceState);
      expect(state.getHistoryLimit()).toBe(50);
    });

    it("should accept limit of 100", () => {
      const state = new DashboardState(workspaceState);
      // Override config before creating state - need to recreate
      mockConfigValues["history.limit"] = 100;
      const state2 = new DashboardState(workspaceState);
      expect(state2.getHistoryLimit()).toBe(100);
    });

    it("should accept limit of 200", () => {
      mockConfigValues["history.limit"] = 200;
      const state = new DashboardState(workspaceState);
      expect(state.getHistoryLimit()).toBe(200);
    });

    it("should fall back to default for invalid limit values", () => {
      mockConfigValues["history.limit"] = 75;
      const state = new DashboardState(workspaceState);
      expect(state.getHistoryLimit()).toBe(50);
    });

    it("should trim history to configurable limit", () => {
      mockConfigValues["history.limit"] = 50;
      const state = createStateWithHistory(60);

      expect(state.getHistory().length).toBe(50);
    });

    it("should allow more history when limit is increased", () => {
      mockConfigValues["history.limit"] = 100;
      const state = createStateWithHistory(80);

      expect(state.getHistory().length).toBe(80);
    });
  });

  describe("getHistoryPageSize()", () => {
    it("should default to 20", () => {
      const state = new DashboardState(workspaceState);
      expect(state.getHistoryPageSize()).toBe(20);
    });

    it("should return configured page size", () => {
      mockConfigValues["history.page_size"] = 10;
      const state = new DashboardState(workspaceState);
      expect(state.getHistoryPageSize()).toBe(10);
    });
  });
});
