/**
 * Unit tests for coreSettings
 *
 * Tests ConfigBridge integration and fallback behavior.
 *
 * @see Issue #476 - Refactor tree providers to use ConfigBridge
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { getCoreSettings, DEFAULT_CORE_SETTINGS } from "../coreSettings";
import { ConfigBridge } from "../../services/ConfigBridge";
import { DEFAULT_CONFIG } from "../schema";

// Mock ConfigBridge
vi.mock("../../services/ConfigBridge", () => ({
  ConfigBridge: {
    getInstance: vi.fn(),
  },
}));

describe("coreSettings", () => {
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

  describe("getCoreSettings", () => {
    it("returns defaults when ConfigBridge is not initialized", () => {
      mockConfigBridge.isInitialized.mockReturnValue(false);

      const settings = getCoreSettings();

      expect(settings).toEqual(DEFAULT_CORE_SETTINGS);
      expect(mockConfigBridge.isInitialized).toHaveBeenCalled();
      expect(mockConfigBridge.getUI).not.toHaveBeenCalled();
    });

    it("returns values from ConfigBridge when initialized", () => {
      mockConfigBridge.isInitialized.mockReturnValue(true);
      mockConfigBridge.getUI.mockReturnValue({
        core: {
          auth_provider: "bedrock",
          default_model: "opus",
          context_path: ".custom/pipeline",
          plans_path: ".custom/plans",
        },
      });

      const settings = getCoreSettings();

      expect(settings.authProvider).toBe("bedrock");
      expect(settings.defaultModel).toBe("opus");
      expect(settings.contextPath).toBe(".custom/pipeline");
      expect(settings.plansPath).toBe(".custom/plans");
    });

    it("falls back to defaults for missing config values", () => {
      mockConfigBridge.isInitialized.mockReturnValue(true);
      mockConfigBridge.getUI.mockReturnValue({
        core: {
          auth_provider: "vertex",
          // Other fields undefined
        },
      });

      const settings = getCoreSettings();

      expect(settings.authProvider).toBe("vertex");
      expect(settings.defaultModel).toBe(DEFAULT_CONFIG.ui!.core!.default_model);
      expect(settings.contextPath).toBe(DEFAULT_CONFIG.ui!.core!.context_path);
      expect(settings.plansPath).toBe(DEFAULT_CONFIG.ui!.core!.plans_path);
    });

    it("handles undefined ui config gracefully", () => {
      mockConfigBridge.isInitialized.mockReturnValue(true);
      mockConfigBridge.getUI.mockReturnValue(undefined);

      const settings = getCoreSettings();

      expect(settings).toEqual(DEFAULT_CORE_SETTINGS);
    });
  });

  describe("DEFAULT_CORE_SETTINGS", () => {
    it("matches DEFAULT_CONFIG.ui.core values", () => {
      const defaults = DEFAULT_CONFIG.ui!.core!;

      expect(DEFAULT_CORE_SETTINGS.authProvider).toBe(defaults.auth_provider);
      expect(DEFAULT_CORE_SETTINGS.defaultModel).toBe(defaults.default_model);
      expect(DEFAULT_CORE_SETTINGS.contextPath).toBe(defaults.context_path);
      expect(DEFAULT_CORE_SETTINGS.plansPath).toBe(defaults.plans_path);
    });
  });
});
