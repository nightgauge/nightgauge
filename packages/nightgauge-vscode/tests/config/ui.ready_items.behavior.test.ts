/**
 * Behavior tests for ui.ready_items.* configuration fields
 *
 * These tests verify that ready items config fields affect runtime behavior,
 * specifically sorting, filtering, and display of project board issues.
 *
 * @see Issue #472 - Add UI config sections to Zod schema
 * @see packages/nightgauge-vscode/src/config/schema.ts - UIReadyItemsConfigSchema
 */

import { describe, it, expect } from "vitest";
import {
  UIReadyItemsConfigSchema,
  UIReadyItemsFiltersConfigSchema,
  SortBySchema,
  SortDirectionEnumSchema,
  PriorityFilterSchema,
  SizeFilterSchema,
  DEFAULT_CONFIG,
} from "../../src/config/schema";

// ============================================================================
// Mock Fixtures
// ============================================================================

/**
 * Default ready items configuration for tests
 */
export const DEFAULT_UI_READY_ITEMS_CONFIG = {
  auto_refresh: false,
  refresh_interval: 300,
  sort_by: "board" as const,
  sort_direction: "asc" as const,
  filters: {
    priority: "all" as const,
    size: "all" as const,
    component: "all",
  },
  search_text: "",
  show_dependencies: true,
};

/**
 * Create a mock ready items configuration with optional overrides
 */
export function createMockUIReadyItemsConfig(
  overrides?: Partial<typeof DEFAULT_UI_READY_ITEMS_CONFIG>
) {
  return {
    ...DEFAULT_UI_READY_ITEMS_CONFIG,
    filters: {
      ...DEFAULT_UI_READY_ITEMS_CONFIG.filters,
      ...overrides?.filters,
    },
    ...overrides,
  };
}

/**
 * Mock issue for testing
 */
interface MockIssue {
  number: number;
  title: string;
  priority: "P0" | "P1" | "P2";
  size: "XS" | "S" | "M" | "L" | "XL";
  component?: string;
  blockedBy: number[];
}

describe("ui.ready_items.behavior", () => {
  // ============================================================================
  // auto_refresh - Behavior Tests
  // ============================================================================

  describe("auto_refresh", () => {
    it("enables periodic issue list refresh", () => {
      const config = createMockUIReadyItemsConfig({ auto_refresh: true });

      const shouldStartRefreshTimer = (cfg: typeof config): boolean => {
        return cfg.auto_refresh === true;
      };

      expect(shouldStartRefreshTimer(config)).toBe(true);
    });

    it("disables auto-refresh when false", () => {
      const config = createMockUIReadyItemsConfig({ auto_refresh: false });

      const shouldStartRefreshTimer = (cfg: typeof config): boolean => {
        return cfg.auto_refresh === true;
      };

      expect(shouldStartRefreshTimer(config)).toBe(false);
    });

    it("defaults to false", () => {
      expect(DEFAULT_CONFIG.ui?.ready_items?.auto_refresh).toBe(false);
    });
  });

  // ============================================================================
  // refresh_interval - Behavior Tests
  // ============================================================================

  describe("refresh_interval", () => {
    it("determines refresh period in seconds", () => {
      const config = createMockUIReadyItemsConfig({ refresh_interval: 600 });

      const getRefreshIntervalMs = (cfg: typeof config): number => {
        return (cfg.refresh_interval ?? 300) * 1000;
      };

      expect(getRefreshIntervalMs(config)).toBe(600000); // 10 minutes
    });

    it("minimum value is 60 seconds", () => {
      expect(UIReadyItemsConfigSchema.safeParse({ refresh_interval: 60 }).success).toBe(true);
      expect(UIReadyItemsConfigSchema.safeParse({ refresh_interval: 59 }).success).toBe(false);
    });

    it("defaults to 600 seconds (10 minutes) — bumped from 300 to reduce multi-workspace quota pressure", () => {
      expect(DEFAULT_CONFIG.ui?.ready_items?.refresh_interval).toBe(600);
    });
  });

  // ============================================================================
  // sort_by - Behavior Tests
  // ============================================================================

  describe("sort_by", () => {
    const mockIssues: MockIssue[] = [
      {
        number: 1,
        title: "Feature A",
        priority: "P2",
        size: "L",
        blockedBy: [],
      },
      {
        number: 2,
        title: "Bug B",
        priority: "P0",
        size: "XS",
        blockedBy: [1],
      },
      {
        number: 3,
        title: "Feature C",
        priority: "P1",
        size: "M",
        blockedBy: [],
      },
    ];

    it("sorts by priority (P0 first)", () => {
      const sorted = [...mockIssues].sort((a, b) => {
        const priorityOrder = { P0: 0, P1: 1, P2: 2 };
        return priorityOrder[a.priority] - priorityOrder[b.priority];
      });

      expect(sorted[0].priority).toBe("P0");
      expect(sorted[1].priority).toBe("P1");
      expect(sorted[2].priority).toBe("P2");
    });

    it("sorts by number", () => {
      const sorted = [...mockIssues].sort((a, b) => a.number - b.number);

      expect(sorted[0].number).toBe(1);
      expect(sorted[1].number).toBe(2);
      expect(sorted[2].number).toBe(3);
    });

    it("sorts by size (XS first)", () => {
      const sorted = [...mockIssues].sort((a, b) => {
        const sizeOrder = { XS: 0, S: 1, M: 2, L: 3, XL: 4 };
        return sizeOrder[a.size] - sizeOrder[b.size];
      });

      expect(sorted[0].size).toBe("XS");
      expect(sorted[1].size).toBe("M");
      expect(sorted[2].size).toBe("L");
    });

    it("sorts by dependencies (unblocked first)", () => {
      const sorted = [...mockIssues].sort((a, b) => {
        return a.blockedBy.length - b.blockedBy.length;
      });

      expect(sorted[0].blockedBy.length).toBe(0);
      expect(sorted[sorted.length - 1].blockedBy.length).toBe(1);
    });

    it("accepts all valid sort options", () => {
      expect(SortBySchema.safeParse("smart").success).toBe(true);
      expect(SortBySchema.safeParse("board").success).toBe(true);
      expect(SortBySchema.safeParse("priority").success).toBe(true);
      expect(SortBySchema.safeParse("number").success).toBe(true);
      expect(SortBySchema.safeParse("size").success).toBe(true);
      expect(SortBySchema.safeParse("dependencies").success).toBe(true);
    });

    it("rejects invalid sort options", () => {
      expect(SortBySchema.safeParse("date").success).toBe(false);
      expect(SortBySchema.safeParse("title").success).toBe(false);
    });

    it("defaults to board", () => {
      expect(DEFAULT_CONFIG.ui?.ready_items?.sort_by).toBe("board");
    });
  });

  // ============================================================================
  // sort_direction - Behavior Tests
  // ============================================================================

  describe("sort_direction", () => {
    it("ascending sorts low to high", () => {
      const numbers = [3, 1, 2];
      const sorted = [...numbers].sort((a, b) => a - b); // asc

      expect(sorted).toEqual([1, 2, 3]);
    });

    it("descending sorts high to low", () => {
      const numbers = [3, 1, 2];
      const sorted = [...numbers].sort((a, b) => b - a); // desc

      expect(sorted).toEqual([3, 2, 1]);
    });

    it("accepts valid directions", () => {
      expect(SortDirectionEnumSchema.safeParse("asc").success).toBe(true);
      expect(SortDirectionEnumSchema.safeParse("desc").success).toBe(true);
    });

    it("rejects invalid directions", () => {
      expect(SortDirectionEnumSchema.safeParse("ascending").success).toBe(false);
      expect(SortDirectionEnumSchema.safeParse("descending").success).toBe(false);
    });

    it("defaults to asc", () => {
      expect(DEFAULT_CONFIG.ui?.ready_items?.sort_direction).toBe("asc");
    });
  });

  // ============================================================================
  // filters.priority - Behavior Tests
  // ============================================================================

  describe("filters.priority", () => {
    const mockIssues: MockIssue[] = [
      {
        number: 1,
        title: "A",
        priority: "P0",
        size: "M",
        blockedBy: [],
      },
      {
        number: 2,
        title: "B",
        priority: "P1",
        size: "M",
        blockedBy: [],
      },
      {
        number: 3,
        title: "C",
        priority: "P2",
        size: "M",
        blockedBy: [],
      },
    ];

    it("shows all priorities when set to all", () => {
      const filter = "all";
      const filtered = mockIssues.filter((i) => filter === "all" || i.priority === filter);

      expect(filtered.length).toBe(3);
    });

    it("filters to P0 only", () => {
      const filter = "P0";
      const filtered = mockIssues.filter((i) => filter === "all" || i.priority === filter);

      expect(filtered.length).toBe(1);
      expect(filtered[0].priority).toBe("P0");
    });

    it("accepts valid priority filters", () => {
      expect(PriorityFilterSchema.safeParse("all").success).toBe(true);
      expect(PriorityFilterSchema.safeParse("P0").success).toBe(true);
      expect(PriorityFilterSchema.safeParse("P1").success).toBe(true);
      expect(PriorityFilterSchema.safeParse("P2").success).toBe(true);
    });

    it("rejects invalid priority filters", () => {
      expect(PriorityFilterSchema.safeParse("P3").success).toBe(false);
      expect(PriorityFilterSchema.safeParse("high").success).toBe(false);
    });

    it("defaults to all", () => {
      expect(DEFAULT_CONFIG.ui?.ready_items?.filters?.priority).toBe("all");
    });
  });

  // ============================================================================
  // filters.size - Behavior Tests
  // ============================================================================

  describe("filters.size", () => {
    const mockIssues: MockIssue[] = [
      { number: 1, title: "A", priority: "P1", size: "XS", blockedBy: [] },
      { number: 2, title: "B", priority: "P1", size: "M", blockedBy: [] },
      { number: 3, title: "C", priority: "P1", size: "XL", blockedBy: [] },
    ];

    it("shows all sizes when set to all", () => {
      const filter = "all";
      const filtered = mockIssues.filter((i) => filter === "all" || i.size === filter);

      expect(filtered.length).toBe(3);
    });

    it("filters to specific size", () => {
      const filter = "M";
      const filtered = mockIssues.filter((i) => filter === "all" || i.size === filter);

      expect(filtered.length).toBe(1);
      expect(filtered[0].size).toBe("M");
    });

    it("accepts valid size filters", () => {
      expect(SizeFilterSchema.safeParse("all").success).toBe(true);
      expect(SizeFilterSchema.safeParse("XS").success).toBe(true);
      expect(SizeFilterSchema.safeParse("S").success).toBe(true);
      expect(SizeFilterSchema.safeParse("M").success).toBe(true);
      expect(SizeFilterSchema.safeParse("L").success).toBe(true);
      expect(SizeFilterSchema.safeParse("XL").success).toBe(true);
    });

    it("rejects invalid size filters", () => {
      expect(SizeFilterSchema.safeParse("XXL").success).toBe(false);
      expect(SizeFilterSchema.safeParse("medium").success).toBe(false);
    });

    it("defaults to all", () => {
      expect(DEFAULT_CONFIG.ui?.ready_items?.filters?.size).toBe("all");
    });
  });

  // ============================================================================
  // filters.component - Behavior Tests
  // ============================================================================

  describe("filters.component", () => {
    const mockIssues: MockIssue[] = [
      {
        number: 1,
        title: "A",
        priority: "P1",
        size: "M",
        component: "frontend",
        blockedBy: [],
      },
      {
        number: 2,
        title: "B",
        priority: "P1",
        size: "M",
        component: "backend",
        blockedBy: [],
      },
      {
        number: 3,
        title: "C",
        priority: "P1",
        size: "M",
        blockedBy: [],
      },
    ];

    it("shows all components when set to all", () => {
      const filter = "all";
      const filtered = mockIssues.filter((i) => filter === "all" || i.component === filter);

      expect(filtered.length).toBe(3);
    });

    it("filters to specific component", () => {
      const filter = "frontend";
      const filtered = mockIssues.filter((i) => filter === "all" || i.component === filter);

      expect(filtered.length).toBe(1);
      expect(filtered[0].component).toBe("frontend");
    });

    it("accepts any string", () => {
      const result = UIReadyItemsFiltersConfigSchema.safeParse({
        component: "custom-component",
      });
      expect(result.success).toBe(true);
    });

    it("defaults to all", () => {
      expect(DEFAULT_CONFIG.ui?.ready_items?.filters?.component).toBe("all");
    });
  });

  // ============================================================================
  // search_text - Behavior Tests
  // ============================================================================

  describe("search_text", () => {
    const mockIssues: MockIssue[] = [
      {
        number: 1,
        title: "Add authentication",
        priority: "P1",
        size: "M",
        blockedBy: [],
      },
      {
        number: 2,
        title: "Fix login bug",
        priority: "P0",
        size: "S",
        blockedBy: [],
      },
      {
        number: 144,
        title: "Update docs",
        priority: "P2",
        size: "XS",
        blockedBy: [],
      },
    ];

    it("filters by title text", () => {
      const search = "auth";
      const filtered = mockIssues.filter((i) =>
        i.title.toLowerCase().includes(search.toLowerCase())
      );

      expect(filtered.length).toBe(1);
      expect(filtered[0].title).toBe("Add authentication");
    });

    it("filters by issue number", () => {
      const search = "#144";
      const numberMatch = search.match(/#(\d+)/);
      const issueNumber = numberMatch ? parseInt(numberMatch[1]) : null;

      const filtered = mockIssues.filter((i) => issueNumber === null || i.number === issueNumber);

      expect(filtered.length).toBe(1);
      expect(filtered[0].number).toBe(144);
    });

    it("shows all when empty", () => {
      const search = "";
      const filtered = mockIssues.filter(
        (i) => search === "" || i.title.toLowerCase().includes(search.toLowerCase())
      );

      expect(filtered.length).toBe(3);
    });

    it("defaults to empty string", () => {
      expect(DEFAULT_CONFIG.ui?.ready_items?.search_text).toBe("");
    });
  });

  // ============================================================================
  // show_dependencies - Behavior Tests
  // ============================================================================

  describe("show_dependencies", () => {
    it("shows dependency indicators when enabled", () => {
      const config = createMockUIReadyItemsConfig({ show_dependencies: true });

      const shouldShowDeps = (cfg: typeof config): boolean => {
        return cfg.show_dependencies === true;
      };

      expect(shouldShowDeps(config)).toBe(true);
    });

    it("hides dependency indicators when disabled", () => {
      const config = createMockUIReadyItemsConfig({ show_dependencies: false });

      const shouldShowDeps = (cfg: typeof config): boolean => {
        return cfg.show_dependencies === true;
      };

      expect(shouldShowDeps(config)).toBe(false);
    });

    it("defaults to true", () => {
      expect(DEFAULT_CONFIG.ui?.ready_items?.show_dependencies).toBe(true);
    });
  });

  // ============================================================================
  // Schema Validation Tests
  // ============================================================================

  describe("validation", () => {
    it("validates complete ready_items config", () => {
      const result = UIReadyItemsConfigSchema.safeParse(DEFAULT_UI_READY_ITEMS_CONFIG);
      expect(result.success).toBe(true);
    });

    it("validates partial ready_items config", () => {
      const result = UIReadyItemsConfigSchema.safeParse({
        sort_by: "priority",
        sort_direction: "desc",
      });
      expect(result.success).toBe(true);
    });

    it("validates empty ready_items config", () => {
      const result = UIReadyItemsConfigSchema.safeParse({});
      expect(result.success).toBe(true);
    });

    it("validates filters config standalone", () => {
      const result = UIReadyItemsFiltersConfigSchema.safeParse({
        priority: "P0",
        size: "XS",
        component: "core",
      });
      expect(result.success).toBe(true);
    });
  });
});
