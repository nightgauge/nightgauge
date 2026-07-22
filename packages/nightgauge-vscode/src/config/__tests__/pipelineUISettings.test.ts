/**
 * Unit tests for pipelineUISettings
 *
 * Tests ConfigBridge integration and fallback behavior.
 *
 * @see Issue #476 - Refactor tree providers to use ConfigBridge
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { getPipelineUISettings, DEFAULT_PIPELINE_UI_SETTINGS } from "../pipelineUISettings";
import { ConfigBridge } from "../../services/ConfigBridge";
import { DEFAULT_CONFIG } from "../schema";

// Mock ConfigBridge
vi.mock("../../services/ConfigBridge", () => ({
  ConfigBridge: {
    getInstance: vi.fn(),
  },
}));

describe("pipelineUISettings", () => {
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

  describe("getPipelineUISettings", () => {
    it("returns defaults when ConfigBridge is not initialized", () => {
      mockConfigBridge.isInitialized.mockReturnValue(false);

      const settings = getPipelineUISettings();

      expect(settings).toEqual(DEFAULT_PIPELINE_UI_SETTINGS);
      expect(mockConfigBridge.isInitialized).toHaveBeenCalled();
      expect(mockConfigBridge.getUI).not.toHaveBeenCalled();
    });

    it("returns values from ConfigBridge when initialized", () => {
      mockConfigBridge.isInitialized.mockReturnValue(true);
      mockConfigBridge.getUI.mockReturnValue({
        pipeline: {
          auto_continue: false,
          auto_continue_delay: 2000,
        },
      });

      const settings = getPipelineUISettings();

      expect(settings.autoContinue).toBe(false);
      expect(settings.autoContinueDelay).toBe(2000);
    });

    it("falls back to defaults for missing config values", () => {
      mockConfigBridge.isInitialized.mockReturnValue(true);
      mockConfigBridge.getUI.mockReturnValue({
        pipeline: {
          auto_continue: false,
          // auto_continue_delay undefined
        },
      });

      const settings = getPipelineUISettings();

      expect(settings.autoContinue).toBe(false);
      expect(settings.autoContinueDelay).toBe(DEFAULT_CONFIG.ui!.pipeline!.auto_continue_delay);
    });

    it("handles undefined ui config gracefully", () => {
      mockConfigBridge.isInitialized.mockReturnValue(true);
      mockConfigBridge.getUI.mockReturnValue(undefined);

      const settings = getPipelineUISettings();

      expect(settings).toEqual(DEFAULT_PIPELINE_UI_SETTINGS);
    });
  });

  describe("DEFAULT_PIPELINE_UI_SETTINGS", () => {
    it("matches DEFAULT_CONFIG.ui.pipeline values", () => {
      const defaults = DEFAULT_CONFIG.ui!.pipeline!;

      expect(DEFAULT_PIPELINE_UI_SETTINGS.autoContinue).toBe(defaults.auto_continue);
      expect(DEFAULT_PIPELINE_UI_SETTINGS.autoContinueDelay).toBe(defaults.auto_continue_delay);
    });
  });
});
