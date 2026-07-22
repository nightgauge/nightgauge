/**
 * Unit tests for readyItemsSettings
 *
 * Tests ConfigBridge integration and fallback behavior.
 *
 * @see Issue #476 - Refactor tree providers to use ConfigBridge
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { getReadyItemsSettings, DEFAULT_READY_ITEMS_SETTINGS } from "../readyItemsSettings";
import { ConfigBridge } from "../../services/ConfigBridge";
import { DEFAULT_CONFIG } from "../schema";

// Mock ConfigBridge
vi.mock("../../services/ConfigBridge", () => ({
  ConfigBridge: {
    getInstance: vi.fn(),
  },
}));

describe("readyItemsSettings", () => {
  let mockConfigBridge: {
    isInitialized: ReturnType<typeof vi.fn>;
    getUI: ReturnType<typeof vi.fn>;
  };

  beforeEach(() => {
    mockConfigBridge = {
      isInitialized: vi.fn(),
      getUI: vi.fn(),
    };
    vi.mocked(ConfigBridge.getInstance).mockReturnValue(
      mockConfigBridge as unknown as ConfigBridge
    );
  });

  afterEach(() => {
    vi.clearAllMocks();
  });

  describe("getReadyItemsSettings", () => {
    it("returns defaults when ConfigBridge is not initialized", () => {
      mockConfigBridge.isInitialized.mockReturnValue(false);

      const settings = getReadyItemsSettings();

      expect(settings).toEqual(DEFAULT_READY_ITEMS_SETTINGS);
      expect(mockConfigBridge.isInitialized).toHaveBeenCalled();
      expect(mockConfigBridge.getUI).not.toHaveBeenCalled();
    });

    it("returns values from ConfigBridge when initialized", () => {
      mockConfigBridge.isInitialized.mockReturnValue(true);
      mockConfigBridge.getUI.mockReturnValue({
        ready_items: {
          auto_refresh: true,
          refresh_interval: 600,
          sort_by: "priority",
          sort_direction: "desc",
          filters: {
            priority: "high",
            size: "S",
            component: "frontend",
          },
          search_text: "test search",
          show_dependencies: false,
        },
      });

      const settings = getReadyItemsSettings();

      expect(settings.autoRefresh).toBe(true);
      expect(settings.refreshInterval).toBe(600);
      expect(settings.sortBy).toBe("priority");
      expect(settings.sortDirection).toBe("desc");
      expect(settings.filters.priority).toBe("high");
      expect(settings.filters.size).toBe("S");
      expect(settings.filters.component).toBe("frontend");
      expect(settings.searchText).toBe("test search");
      expect(settings.showDependencies).toBe(false);
    });

    it("falls back to defaults for missing nested config values", () => {
      mockConfigBridge.isInitialized.mockReturnValue(true);
      mockConfigBridge.getUI.mockReturnValue({
        ready_items: {
          auto_refresh: true,
          // Other fields undefined
        },
      });

      const settings = getReadyItemsSettings();

      expect(settings.autoRefresh).toBe(true);
      // Should fall back to defaults for missing fields
      expect(settings.refreshInterval).toBe(DEFAULT_CONFIG.ui!.ready_items!.refresh_interval);
      expect(settings.sortBy).toBe(DEFAULT_CONFIG.ui!.ready_items!.sort_by);
      expect(settings.sortDirection).toBe(DEFAULT_CONFIG.ui!.ready_items!.sort_direction);
    });

    it("handles undefined ui config gracefully", () => {
      mockConfigBridge.isInitialized.mockReturnValue(true);
      mockConfigBridge.getUI.mockReturnValue(undefined);

      const settings = getReadyItemsSettings();

      expect(settings).toEqual(DEFAULT_READY_ITEMS_SETTINGS);
    });
  });

  describe("DEFAULT_READY_ITEMS_SETTINGS", () => {
    it("matches DEFAULT_CONFIG.ui.ready_items values", () => {
      const defaults = DEFAULT_CONFIG.ui!.ready_items!;

      expect(DEFAULT_READY_ITEMS_SETTINGS.autoRefresh).toBe(defaults.auto_refresh);
      expect(DEFAULT_READY_ITEMS_SETTINGS.refreshInterval).toBe(defaults.refresh_interval);
      expect(DEFAULT_READY_ITEMS_SETTINGS.sortBy).toBe(defaults.sort_by);
      expect(DEFAULT_READY_ITEMS_SETTINGS.sortDirection).toBe(defaults.sort_direction);
      expect(DEFAULT_READY_ITEMS_SETTINGS.showDependencies).toBe(defaults.show_dependencies);
    });
  });
});
