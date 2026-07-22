import { describe, it, expect } from "vitest";
import {
  matchesPriorityFilter,
  matchesSizeFilter,
  matchesComponentFilter,
  matchesSearchText,
  hasActiveFilters,
  getFilterSummary,
  type FilterState,
  DEFAULT_FILTER_STATE,
  PRIORITY_OPTIONS,
  SIZE_OPTIONS,
  COMPONENT_OPTIONS,
} from "../../src/types/FilterConfig";

describe("FilterConfig", () => {
  describe("matchesPriorityFilter()", () => {
    it('should return true for any priority when filter is "all"', () => {
      expect(matchesPriorityFilter("P0", "all")).toBe(true);
      expect(matchesPriorityFilter("P1", "all")).toBe(true);
      expect(matchesPriorityFilter("P2", "all")).toBe(true);
      expect(matchesPriorityFilter(null, "all")).toBe(true);
    });

    it("should return true when priority matches filter", () => {
      expect(matchesPriorityFilter("P0", "P0")).toBe(true);
      expect(matchesPriorityFilter("P1", "P1")).toBe(true);
      expect(matchesPriorityFilter("P2", "P2")).toBe(true);
    });

    it("should return false when priority does not match filter", () => {
      expect(matchesPriorityFilter("P0", "P1")).toBe(false);
      expect(matchesPriorityFilter("P1", "P0")).toBe(false);
      expect(matchesPriorityFilter(null, "P0")).toBe(false);
    });
  });

  describe("matchesSizeFilter()", () => {
    it('should return true for any size when filter is "all"', () => {
      expect(matchesSizeFilter("XS", "all")).toBe(true);
      expect(matchesSizeFilter("S", "all")).toBe(true);
      expect(matchesSizeFilter("M", "all")).toBe(true);
      expect(matchesSizeFilter("L", "all")).toBe(true);
      expect(matchesSizeFilter("XL", "all")).toBe(true);
      expect(matchesSizeFilter(null, "all")).toBe(true);
    });

    it("should return true when size matches filter", () => {
      expect(matchesSizeFilter("XS", "XS")).toBe(true);
      expect(matchesSizeFilter("M", "M")).toBe(true);
      expect(matchesSizeFilter("XL", "XL")).toBe(true);
    });

    it("should return false when size does not match filter", () => {
      expect(matchesSizeFilter("S", "M")).toBe(false);
      expect(matchesSizeFilter("L", "S")).toBe(false);
      expect(matchesSizeFilter(null, "M")).toBe(false);
    });
  });

  describe("matchesComponentFilter()", () => {
    it('should return true for any labels when filter is "all"', () => {
      expect(matchesComponentFilter(["component:nightgauge"], "all")).toBe(true);
      expect(matchesComponentFilter(["component:smart-setup"], "all")).toBe(true);
      expect(matchesComponentFilter([], "all")).toBe(true);
    });

    it("should return true when labels contain filtered component", () => {
      expect(matchesComponentFilter(["component:nightgauge", "type:feature"], "nightgauge")).toBe(
        true
      );
      expect(matchesComponentFilter(["component:smart-setup"], "smart-setup")).toBe(true);
    });

    it("should return false when labels do not contain filtered component", () => {
      expect(matchesComponentFilter(["component:nightgauge"], "smart-setup")).toBe(false);
      expect(matchesComponentFilter(["type:feature"], "nightgauge")).toBe(false);
      expect(matchesComponentFilter([], "nightgauge")).toBe(false);
    });
  });

  describe("matchesSearchText()", () => {
    it("should return true when search text is empty", () => {
      expect(matchesSearchText("Any title", 123, "")).toBe(true);
      expect(matchesSearchText("Any title", 123, "   ")).toBe(true);
    });

    it("should return true when search text is null or undefined", () => {
      expect(matchesSearchText("Any title", 123, null as unknown as string)).toBe(true);
      expect(matchesSearchText("Any title", 123, undefined as unknown as string)).toBe(true);
    });

    it("should match issue title case-insensitively", () => {
      expect(matchesSearchText("Add user authentication", 100, "auth")).toBe(true);
      expect(matchesSearchText("Add user authentication", 100, "AUTH")).toBe(true);
      expect(matchesSearchText("Add user authentication", 100, "User")).toBe(true);
      expect(matchesSearchText("Add user authentication", 100, "add user")).toBe(true);
    });

    it("should match issue number with # prefix", () => {
      expect(matchesSearchText("Some title", 144, "#144")).toBe(true);
      expect(matchesSearchText("Some title", 42, "#42")).toBe(true);
    });

    it("should match issue number without # prefix", () => {
      expect(matchesSearchText("Some title", 144, "144")).toBe(true);
      expect(matchesSearchText("Some title", 42, "42")).toBe(true);
    });

    it("should return false when search text does not match", () => {
      expect(matchesSearchText("Add user authentication", 100, "database")).toBe(false);
      expect(matchesSearchText("Add user authentication", 100, "#200")).toBe(false);
      expect(matchesSearchText("Add user authentication", 100, "200")).toBe(false);
    });

    it("should handle partial number matches correctly", () => {
      // "14" should NOT match issue #144 (exact number match only)
      expect(matchesSearchText("Some title", 144, "14")).toBe(false);
      // But "14" in title should match
      expect(matchesSearchText("Issue 14 fix", 144, "14")).toBe(true);
    });

    it("should trim whitespace from search text", () => {
      expect(matchesSearchText("Add user authentication", 100, "  auth  ")).toBe(true);
      expect(matchesSearchText("Some title", 144, "  #144  ")).toBe(true);
    });
  });

  describe("hasActiveFilters()", () => {
    it('should return false when all filters are "all"', () => {
      const state: FilterState = {
        priority: "all",
        size: "all",
        component: "all",
      };
      expect(hasActiveFilters(state)).toBe(false);
    });

    it("should return true when priority filter is active", () => {
      const state: FilterState = {
        priority: "P0",
        size: "all",
        component: "all",
      };
      expect(hasActiveFilters(state)).toBe(true);
    });

    it("should return true when size filter is active", () => {
      const state: FilterState = {
        priority: "all",
        size: "M",
        component: "all",
      };
      expect(hasActiveFilters(state)).toBe(true);
    });

    it("should return true when component filter is active", () => {
      const state: FilterState = {
        priority: "all",
        size: "all",
        component: "nightgauge",
      };
      expect(hasActiveFilters(state)).toBe(true);
    });

    it("should return true when multiple filters are active", () => {
      const state: FilterState = {
        priority: "P1",
        size: "L",
        component: "nightgauge",
      };
      expect(hasActiveFilters(state)).toBe(true);
    });
  });

  describe("getFilterSummary()", () => {
    it('should return "No filters" when no filters active', () => {
      const state: FilterState = {
        priority: "all",
        size: "all",
        component: "all",
      };
      expect(getFilterSummary(state)).toBe("No filters");
    });

    it("should include priority when priority filter is active", () => {
      const state: FilterState = {
        priority: "P0",
        size: "all",
        component: "all",
      };
      expect(getFilterSummary(state)).toBe("Priority: P0");
    });

    it("should include size when size filter is active", () => {
      const state: FilterState = {
        priority: "all",
        size: "M",
        component: "all",
      };
      expect(getFilterSummary(state)).toBe("Size: M");
    });

    it("should include component when component filter is active", () => {
      const state: FilterState = {
        priority: "all",
        size: "all",
        component: "nightgauge",
      };
      expect(getFilterSummary(state)).toBe("Component: nightgauge");
    });

    it("should include all active filters", () => {
      const state: FilterState = {
        priority: "P1",
        size: "L",
        component: "smart-setup",
      };
      expect(getFilterSummary(state)).toBe("Priority: P1, Size: L, Component: smart-setup");
    });
  });

  describe("DEFAULT_FILTER_STATE", () => {
    it('should have all filters set to "all"', () => {
      expect(DEFAULT_FILTER_STATE.priority).toBe("all");
      expect(DEFAULT_FILTER_STATE.size).toBe("all");
      expect(DEFAULT_FILTER_STATE.component).toBe("all");
    });
  });

  describe("constants", () => {
    it("PRIORITY_OPTIONS should have correct values", () => {
      expect(PRIORITY_OPTIONS).toHaveLength(4);
      expect(PRIORITY_OPTIONS.map((o) => o.value)).toEqual(["all", "P0", "P1", "P2"]);
    });

    it("SIZE_OPTIONS should have correct values", () => {
      expect(SIZE_OPTIONS).toHaveLength(6);
      expect(SIZE_OPTIONS.map((o) => o.value)).toEqual(["all", "XS", "S", "M", "L", "XL"]);
    });

    it("COMPONENT_OPTIONS should contain expected components", () => {
      expect(COMPONENT_OPTIONS).toContain("pattern-mining");
      expect(COMPONENT_OPTIONS).toContain("configs");
      expect(COMPONENT_OPTIONS).toContain("platform");
      expect(COMPONENT_OPTIONS).toContain("smart-setup");
      expect(COMPONENT_OPTIONS).toContain("standards");
      // 'nightgauge' is not a real component label in the repo
      expect(COMPONENT_OPTIONS).not.toContain("nightgauge");
    });
  });
});
