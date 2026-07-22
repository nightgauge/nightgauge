/**
 * Unit tests for sidebarSettings
 *
 * Tests ConfigBridge integration and fallback behavior.
 *
 * @see Issue #476 - Refactor tree providers to use ConfigBridge
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { getSidebarSettings, DEFAULT_SIDEBAR_SETTINGS } from "../sidebarSettings";
import { ConfigBridge } from "../../services/ConfigBridge";
import { DEFAULT_CONFIG } from "../schema";

// Mock ConfigBridge
vi.mock("../../services/ConfigBridge", () => ({
  ConfigBridge: {
    getInstance: vi.fn(),
  },
}));

describe("sidebarSettings", () => {
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

  describe("getSidebarSettings", () => {
    it("returns defaults when ConfigBridge is not initialized", () => {
      mockConfigBridge.isInitialized.mockReturnValue(false);

      const settings = getSidebarSettings();

      expect(settings).toEqual(DEFAULT_SIDEBAR_SETTINGS);
      expect(mockConfigBridge.isInitialized).toHaveBeenCalled();
      expect(mockConfigBridge.getUI).not.toHaveBeenCalled();
    });

    it("returns values from ConfigBridge when initialized", () => {
      mockConfigBridge.isInitialized.mockReturnValue(true);
      mockConfigBridge.getUI.mockReturnValue({
        sidebar: {
          hide_empty_sections: true,
        },
      });

      const settings = getSidebarSettings();

      expect(settings.hideEmptySections).toBe(true);
    });

    it("falls back to defaults for missing config values", () => {
      mockConfigBridge.isInitialized.mockReturnValue(true);
      mockConfigBridge.getUI.mockReturnValue({
        sidebar: {},
      });

      const settings = getSidebarSettings();

      expect(settings.hideEmptySections).toBe(DEFAULT_CONFIG.ui!.sidebar!.hide_empty_sections);
    });

    it("handles undefined ui config gracefully", () => {
      mockConfigBridge.isInitialized.mockReturnValue(true);
      mockConfigBridge.getUI.mockReturnValue(undefined);

      const settings = getSidebarSettings();

      expect(settings).toEqual(DEFAULT_SIDEBAR_SETTINGS);
    });
  });

  describe("DEFAULT_SIDEBAR_SETTINGS", () => {
    it("matches DEFAULT_CONFIG.ui.sidebar values", () => {
      const defaults = DEFAULT_CONFIG.ui!.sidebar!;

      expect(DEFAULT_SIDEBAR_SETTINGS.hideEmptySections).toBe(defaults.hide_empty_sections);
    });
  });
});
