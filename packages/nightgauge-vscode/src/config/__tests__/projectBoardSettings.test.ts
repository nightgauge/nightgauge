/**
 * Unit tests for projectBoardSettings
 *
 * Tests ConfigBridge integration and fallback behavior.
 *
 * @see Issue #476 - Refactor tree providers to use ConfigBridge
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { getProjectBoardSettings, DEFAULT_PROJECT_BOARD_SETTINGS } from "../projectBoardSettings";
import { ConfigBridge } from "../../services/ConfigBridge";
import { DEFAULT_CONFIG } from "../schema";

// Mock ConfigBridge
vi.mock("../../services/ConfigBridge", () => ({
  ConfigBridge: {
    getInstance: vi.fn(),
  },
}));

describe("projectBoardSettings", () => {
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

  describe("getProjectBoardSettings", () => {
    it("returns defaults when ConfigBridge is not initialized", () => {
      mockConfigBridge.isInitialized.mockReturnValue(false);

      const settings = getProjectBoardSettings();

      expect(settings).toEqual(DEFAULT_PROJECT_BOARD_SETTINGS);
      expect(mockConfigBridge.isInitialized).toHaveBeenCalled();
      expect(mockConfigBridge.getUI).not.toHaveBeenCalled();
    });

    it("returns values from ConfigBridge when initialized", () => {
      mockConfigBridge.isInitialized.mockReturnValue(true);
      mockConfigBridge.getUI.mockReturnValue({
        project_board: {
          group_by_epic: false,
          default_epic_collapsed: true,
        },
      });

      const settings = getProjectBoardSettings();

      expect(settings.groupByEpic).toBe(false);
      expect(settings.defaultEpicCollapsed).toBe(true);
    });

    it("falls back to defaults for missing config values", () => {
      mockConfigBridge.isInitialized.mockReturnValue(true);
      mockConfigBridge.getUI.mockReturnValue({
        project_board: {
          group_by_epic: false,
          // default_epic_collapsed undefined
        },
      });

      const settings = getProjectBoardSettings();

      expect(settings.groupByEpic).toBe(false);
      expect(settings.defaultEpicCollapsed).toBe(
        DEFAULT_CONFIG.ui!.project_board!.default_epic_collapsed
      );
    });

    it("handles undefined ui config gracefully", () => {
      mockConfigBridge.isInitialized.mockReturnValue(true);
      mockConfigBridge.getUI.mockReturnValue(undefined);

      const settings = getProjectBoardSettings();

      expect(settings).toEqual(DEFAULT_PROJECT_BOARD_SETTINGS);
    });
  });

  describe("DEFAULT_PROJECT_BOARD_SETTINGS", () => {
    it("matches DEFAULT_CONFIG.ui.project_board values", () => {
      const defaults = DEFAULT_CONFIG.ui!.project_board!;

      expect(DEFAULT_PROJECT_BOARD_SETTINGS.groupByEpic).toBe(defaults.group_by_epic);
      expect(DEFAULT_PROJECT_BOARD_SETTINGS.defaultEpicCollapsed).toBe(
        defaults.default_epic_collapsed
      );
    });
  });
});
