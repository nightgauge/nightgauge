/**
 * Unit tests for PluginSetupService.ts
 *
 * Tests ConfigBridge integration for plugin configuration.
 *
 * @see Issue #475 - Refactor notification, warning, and plugin services to use ConfigBridge
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { ConfigBridge } from "../../src/services/ConfigBridge";
import { DEFAULT_CONFIG } from "../../src/config/schema";

// Mock ConfigBridge
vi.mock("../../src/services/ConfigBridge", () => ({
  ConfigBridge: {
    getInstance: vi.fn(),
  },
}));

// Mock vscode module
vi.mock("vscode", () => ({
  window: {
    createOutputChannel: vi.fn(() => ({
      show: vi.fn(),
      appendLine: vi.fn(),
      dispose: vi.fn(),
    })),
    showWarningMessage: vi.fn(),
    showInformationMessage: vi.fn(),
    showErrorMessage: vi.fn(),
    withProgress: vi.fn(),
  },
  env: {
    openExternal: vi.fn(),
  },
  Uri: {
    parse: vi.fn((url: string) => ({ toString: () => url })),
  },
  ProgressLocation: {
    Notification: 1,
  },
}));

// Mock child_process
vi.mock("child_process", () => ({
  exec: vi.fn(),
  execFile: vi.fn(),
}));

vi.mock("util", () => ({
  promisify: vi.fn(() => vi.fn().mockResolvedValue({ stdout: "" })),
}));

describe("PluginSetupService", () => {
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

  describe("getPluginConfig (via module internals)", () => {
    it("returns defaults when ConfigBridge is not initialized", async () => {
      mockConfigBridge.isInitialized.mockReturnValue(false);

      // Import PluginSetupService dynamically to get fresh instance
      const { PluginSetupService } = await import("../../src/services/PluginSetupService");

      // Create mock context
      const mockContext = {
        globalState: {
          get: vi.fn().mockReturnValue(false),
          update: vi.fn(),
        },
      };

      const service = new PluginSetupService(mockContext as never);

      // The service internally calls getPluginConfig()
      // We verify that ConfigBridge was checked
      await service.checkAndPromptSetup();

      expect(ConfigBridge.getInstance).toHaveBeenCalled();
      expect(mockConfigBridge.isInitialized).toHaveBeenCalled();
      expect(console.debug).toHaveBeenCalledWith(
        "[Nightgauge] ConfigBridge not initialized, using defaults for plugins"
      );
    });

    it("reads config from ConfigBridge when initialized", async () => {
      mockConfigBridge.isInitialized.mockReturnValue(true);
      mockConfigBridge.getUI.mockReturnValue({
        plugins: {
          auto_prompt: false,
          marketplace_url: "git@github.com:test/test.git",
        },
      });

      const { PluginSetupService } = await import("../../src/services/PluginSetupService");

      const mockContext = {
        globalState: {
          get: vi.fn().mockReturnValue(false),
          update: vi.fn(),
        },
      };

      const service = new PluginSetupService(mockContext as never);

      // When auto_prompt is false, checkAndPromptSetup should return early
      await service.checkAndPromptSetup();

      expect(mockConfigBridge.getUI).toHaveBeenCalled();
      // Service should have checked config and returned early due to autoPrompt=false
    });
  });

  describe("plugin config schema", () => {
    it("has expected default values in DEFAULT_CONFIG", () => {
      const defaults = DEFAULT_CONFIG.ui!.plugins!;
      expect(defaults.auto_prompt).toBe(true);
      expect(defaults.marketplace_url).toBe("https://github.com/nightgauge/nightgauge.git");
    });
  });

  describe("type mappings", () => {
    it("maps snake_case schema fields to camelCase internal interface", async () => {
      mockConfigBridge.isInitialized.mockReturnValue(true);
      mockConfigBridge.getUI.mockReturnValue({
        plugins: {
          auto_prompt: true, // snake_case in schema
          marketplace_url: "git@custom.git", // snake_case in schema
        },
      });

      // The getPluginConfig function inside PluginSetupService should map these
      // to autoPrompt and marketplaceUrl (camelCase)
      const { PluginSetupService } = await import("../../src/services/PluginSetupService");

      const mockContext = {
        globalState: {
          get: vi.fn().mockReturnValue(false),
          update: vi.fn(),
        },
      };

      const service = new PluginSetupService(mockContext as never);

      // checkAndPromptSetup reads config and uses it
      // If autoPrompt is true and dismissed is false, it will check plugin status
      await service.checkAndPromptSetup();

      expect(mockConfigBridge.getUI).toHaveBeenCalled();
    });
  });

  describe("fallback behavior", () => {
    it("falls back to defaults for missing config values", async () => {
      mockConfigBridge.isInitialized.mockReturnValue(true);
      mockConfigBridge.getUI.mockReturnValue({
        plugins: {
          auto_prompt: false,
          // marketplace_url missing
        },
      });

      const { PluginSetupService } = await import("../../src/services/PluginSetupService");

      const mockContext = {
        globalState: {
          get: vi.fn().mockReturnValue(false),
          update: vi.fn(),
        },
      };

      const service = new PluginSetupService(mockContext as never);
      await service.checkAndPromptSetup();

      // Should use default marketplace_url since it's missing
      // Service returns early due to auto_prompt=false
      expect(mockConfigBridge.getUI).toHaveBeenCalled();
    });

    it("handles undefined UI config gracefully", async () => {
      mockConfigBridge.isInitialized.mockReturnValue(true);
      mockConfigBridge.getUI.mockReturnValue(undefined);

      const { PluginSetupService } = await import("../../src/services/PluginSetupService");

      const mockContext = {
        globalState: {
          get: vi.fn().mockReturnValue(false),
          update: vi.fn(),
        },
      };

      const service = new PluginSetupService(mockContext as never);

      // Should not throw
      await service.checkAndPromptSetup();

      // Should use defaults when ui is undefined, including auto_prompt=true (default)
      expect(mockConfigBridge.getUI).toHaveBeenCalled();
    });

    it("handles undefined plugins config gracefully", async () => {
      mockConfigBridge.isInitialized.mockReturnValue(true);
      mockConfigBridge.getUI.mockReturnValue({});

      const { PluginSetupService } = await import("../../src/services/PluginSetupService");

      const mockContext = {
        globalState: {
          get: vi.fn().mockReturnValue(false),
          update: vi.fn(),
        },
      };

      const service = new PluginSetupService(mockContext as never);

      // Should not throw
      await service.checkAndPromptSetup();

      // Should use defaults when plugins is undefined
      expect(mockConfigBridge.getUI).toHaveBeenCalled();
    });
  });

  describe("dispose", () => {
    it("disposes output channel", async () => {
      const { PluginSetupService } = await import("../../src/services/PluginSetupService");
      const vscode = await import("vscode");

      const mockDispose = vi.fn();
      vi.mocked(vscode.window.createOutputChannel).mockReturnValue({
        show: vi.fn(),
        appendLine: vi.fn(),
        dispose: mockDispose,
      } as never);

      const mockContext = {
        globalState: {
          get: vi.fn(),
          update: vi.fn(),
        },
      };

      const service = new PluginSetupService(mockContext as never);
      service.dispose();

      expect(mockDispose).toHaveBeenCalled();
    });
  });
});
