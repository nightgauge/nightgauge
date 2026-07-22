/**
 * Unit tests for outputWindowSettings
 *
 * Tests ConfigBridge integration and fallback behavior.
 *
 * @see Issue #476 - Refactor tree providers to use ConfigBridge
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { getOutputWindowSettings, DEFAULT_OUTPUT_WINDOW_SETTINGS } from "../outputWindowSettings";
import { ConfigBridge } from "../../services/ConfigBridge";
import { DEFAULT_CONFIG } from "../schema";

// Mock ConfigBridge
vi.mock("../../services/ConfigBridge", () => ({
  ConfigBridge: {
    getInstance: vi.fn(),
  },
}));

describe("outputWindowSettings", () => {
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

  describe("getOutputWindowSettings", () => {
    it("returns defaults when ConfigBridge is not initialized", () => {
      mockConfigBridge.isInitialized.mockReturnValue(false);

      const settings = getOutputWindowSettings();

      expect(settings).toEqual(DEFAULT_OUTPUT_WINDOW_SETTINGS);
      expect(mockConfigBridge.isInitialized).toHaveBeenCalled();
      expect(mockConfigBridge.getUI).not.toHaveBeenCalled();
    });

    it("returns values from ConfigBridge when initialized", () => {
      mockConfigBridge.isInitialized.mockReturnValue(true);
      mockConfigBridge.getUI.mockReturnValue({
        output_window: {
          auto_open: false,
          auto_scroll: false,
          verbose_level: "debug",
          show_token_usage: false,
          word_wrap: false,
        },
      });

      const settings = getOutputWindowSettings();

      expect(settings.autoOpen).toBe(false);
      expect(settings.autoScroll).toBe(false);
      expect(settings.verboseLevel).toBe("debug");
      expect(settings.showTokenUsage).toBe(false);
      expect(settings.wordWrap).toBe(false);
    });

    it("falls back to defaults for missing config values", () => {
      mockConfigBridge.isInitialized.mockReturnValue(true);
      mockConfigBridge.getUI.mockReturnValue({
        output_window: {
          auto_open: false,
          // Other fields undefined
        },
      });

      const settings = getOutputWindowSettings();

      expect(settings.autoOpen).toBe(false);
      expect(settings.autoScroll).toBe(DEFAULT_CONFIG.ui!.output_window!.auto_scroll);
      expect(settings.verboseLevel).toBe(DEFAULT_CONFIG.ui!.output_window!.verbose_level);
      expect(settings.showTokenUsage).toBe(DEFAULT_CONFIG.ui!.output_window!.show_token_usage);
      expect(settings.wordWrap).toBe(DEFAULT_CONFIG.ui!.output_window!.word_wrap);
    });

    it("handles undefined ui config gracefully", () => {
      mockConfigBridge.isInitialized.mockReturnValue(true);
      mockConfigBridge.getUI.mockReturnValue(undefined);

      const settings = getOutputWindowSettings();

      expect(settings).toEqual(DEFAULT_OUTPUT_WINDOW_SETTINGS);
    });
  });

  describe("DEFAULT_OUTPUT_WINDOW_SETTINGS", () => {
    it("matches DEFAULT_CONFIG.ui.output_window values", () => {
      const defaults = DEFAULT_CONFIG.ui!.output_window!;

      expect(DEFAULT_OUTPUT_WINDOW_SETTINGS.autoOpen).toBe(defaults.auto_open);
      expect(DEFAULT_OUTPUT_WINDOW_SETTINGS.autoScroll).toBe(defaults.auto_scroll);
      expect(DEFAULT_OUTPUT_WINDOW_SETTINGS.verboseLevel).toBe(defaults.verbose_level);
      expect(DEFAULT_OUTPUT_WINDOW_SETTINGS.showTokenUsage).toBe(defaults.show_token_usage);
      expect(DEFAULT_OUTPUT_WINDOW_SETTINGS.wordWrap).toBe(defaults.word_wrap);
    });
  });
});
