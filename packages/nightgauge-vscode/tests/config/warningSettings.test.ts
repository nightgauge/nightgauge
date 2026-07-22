/**
 * Unit tests for warningSettings.ts
 *
 * Tests ConfigBridge integration, fallback behavior, and type mappings.
 *
 * @see Issue #475 - Refactor notification, warning, and plugin services to use ConfigBridge
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { getWarningSettings, DEFAULT_WARNING_SETTINGS } from "../../src/config/warningSettings";
import { ConfigBridge } from "../../src/services/ConfigBridge";
import { DEFAULT_CONFIG } from "../../src/config/schema";

// Mock ConfigBridge
vi.mock("../../src/services/ConfigBridge", () => ({
  ConfigBridge: {
    getInstance: vi.fn(),
  },
}));

describe("warningSettings", () => {
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
    vi.spyOn(console, "warn").mockImplementation(() => {});
    vi.spyOn(console, "debug").mockImplementation(() => {});
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  describe("DEFAULT_WARNING_SETTINGS", () => {
    it("has expected default values from schema", () => {
      expect(DEFAULT_WARNING_SETTINGS.enabled).toBe(true);
      expect(DEFAULT_WARNING_SETTINGS.warnOnInProgress).toBe(true);
      expect(DEFAULT_WARNING_SETTINGS.warnOnInReview).toBe(true);
    });

    it("matches DEFAULT_CONFIG.ui.warnings", () => {
      const schemaDefaults = DEFAULT_CONFIG.ui!.warnings!;
      expect(DEFAULT_WARNING_SETTINGS.enabled).toBe(schemaDefaults.enabled);
      expect(DEFAULT_WARNING_SETTINGS.warnOnInProgress).toBe(schemaDefaults.warn_on_in_progress);
      expect(DEFAULT_WARNING_SETTINGS.warnOnInReview).toBe(schemaDefaults.warn_on_in_review);
    });
  });

  describe("getWarningSettings", () => {
    it("returns defaults when ConfigBridge is not initialized", () => {
      mockConfigBridge.isInitialized.mockReturnValue(false);

      const settings = getWarningSettings();

      expect(settings).toEqual(DEFAULT_WARNING_SETTINGS);
      expect(console.debug).toHaveBeenCalledWith(
        "[Nightgauge] ConfigBridge not initialized, using defaults for warnings"
      );
    });

    it("returns values from ConfigBridge when initialized", () => {
      mockConfigBridge.isInitialized.mockReturnValue(true);
      mockConfigBridge.getUI.mockReturnValue({
        warnings: {
          enabled: false,
          warn_on_in_progress: false,
          warn_on_in_review: false,
        },
      });

      const settings = getWarningSettings();

      expect(settings.enabled).toBe(false);
      expect(settings.warnOnInProgress).toBe(false);
      expect(settings.warnOnInReview).toBe(false);
    });

    it("falls back to defaults for missing config values", () => {
      mockConfigBridge.isInitialized.mockReturnValue(true);
      mockConfigBridge.getUI.mockReturnValue({
        warnings: {
          enabled: false,
          // Other fields missing
        },
      });

      const settings = getWarningSettings();

      expect(settings.enabled).toBe(false);
      // Missing values should fall back to defaults
      expect(settings.warnOnInProgress).toBe(DEFAULT_CONFIG.ui!.warnings!.warn_on_in_progress);
      expect(settings.warnOnInReview).toBe(DEFAULT_CONFIG.ui!.warnings!.warn_on_in_review);
    });

    it("handles undefined UI config gracefully", () => {
      mockConfigBridge.isInitialized.mockReturnValue(true);
      mockConfigBridge.getUI.mockReturnValue(undefined);

      const settings = getWarningSettings();

      expect(settings).toEqual(DEFAULT_WARNING_SETTINGS);
    });

    it("handles undefined warnings config gracefully", () => {
      mockConfigBridge.isInitialized.mockReturnValue(true);
      mockConfigBridge.getUI.mockReturnValue({});

      const settings = getWarningSettings();

      expect(settings).toEqual(DEFAULT_WARNING_SETTINGS);
    });

    it("handles partial config with only some fields", () => {
      mockConfigBridge.isInitialized.mockReturnValue(true);
      mockConfigBridge.getUI.mockReturnValue({
        warnings: {
          warn_on_in_progress: false,
          // enabled and warn_on_in_review missing
        },
      });

      const settings = getWarningSettings();

      expect(settings.enabled).toBe(DEFAULT_CONFIG.ui!.warnings!.enabled);
      expect(settings.warnOnInProgress).toBe(false);
      expect(settings.warnOnInReview).toBe(DEFAULT_CONFIG.ui!.warnings!.warn_on_in_review);
    });
  });

  describe("type mappings", () => {
    it("maps snake_case schema fields to camelCase interface", () => {
      mockConfigBridge.isInitialized.mockReturnValue(true);
      mockConfigBridge.getUI.mockReturnValue({
        warnings: {
          enabled: true,
          warn_on_in_progress: false, // snake_case in schema
          warn_on_in_review: false, // snake_case in schema
        },
      });

      const settings = getWarningSettings();

      // Verify mapping to camelCase
      expect(settings.warnOnInProgress).toBe(false);
      expect(settings.warnOnInReview).toBe(false);
    });
  });
});
