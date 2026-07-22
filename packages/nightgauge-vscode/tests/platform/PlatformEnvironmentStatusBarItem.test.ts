/**
 * PlatformEnvironmentStatusBarItem unit tests.
 *
 * Verifies environment-to-style mapping, reactive updates via ConfigBridge,
 * custom URL tooltip, default fallback, and dispose cleanup.
 *
 * @see Issue #3721 — feat: status-bar indicator for active platform environment
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

// ---------------------------------------------------------------------------
// VSCode mock
// ---------------------------------------------------------------------------

vi.mock("vscode", () => {
  class InternalEventEmitter<T> {
    listeners: Array<(e: T) => void> = [];
    event = (listener: (e: T) => void) => {
      this.listeners.push(listener);
      return {
        dispose: () => {
          const idx = this.listeners.indexOf(listener);
          if (idx >= 0) this.listeners.splice(idx, 1);
        },
      };
    };
    fire = (data: T) => {
      for (const l of this.listeners) l(data);
    };
    dispose = () => {};
  }

  return {
    EventEmitter: InternalEventEmitter,
    StatusBarAlignment: { Left: 1, Right: 2 },
    ThemeColor: class {
      constructor(public readonly id: string) {}
    },
    window: {
      createStatusBarItem: vi.fn(() => ({
        text: "",
        tooltip: "",
        backgroundColor: undefined as unknown,
        command: undefined as unknown,
        show: vi.fn(),
        hide: vi.fn(),
        dispose: vi.fn(),
      })),
    },
  };
});

// ---------------------------------------------------------------------------
// ConfigBridge mock
// ---------------------------------------------------------------------------

import * as vscode from "vscode";

type ConfigChangedListener = () => void;

const mockConfigChangedListeners: ConfigChangedListener[] = [];
let mockPlatformConfig: { environment?: string; api_url?: string } | undefined = {
  environment: "production",
};

const mockConfigBridgeInstance = {
  getPlatform: vi.fn(() => mockPlatformConfig),
  onConfigChanged: vi.fn((listener: ConfigChangedListener) => {
    mockConfigChangedListeners.push(listener);
    return {
      dispose: () => {
        const idx = mockConfigChangedListeners.indexOf(listener);
        if (idx >= 0) mockConfigChangedListeners.splice(idx, 1);
      },
    };
  }),
};

vi.mock("../../src/services/ConfigBridge", () => ({
  ConfigBridge: {
    getInstance: vi.fn(() => mockConfigBridgeInstance),
  },
}));

import { PlatformEnvironmentStatusBarItem } from "../../src/platform/PlatformEnvironmentStatusBarItem";

function fireConfigChanged(): void {
  for (const l of [...mockConfigChangedListeners]) l();
}

function getItem(): ReturnType<typeof vscode.window.createStatusBarItem> {
  return vi.mocked(vscode.window.createStatusBarItem).mock.results.at(-1)!.value as ReturnType<
    typeof vscode.window.createStatusBarItem
  >;
}

describe("PlatformEnvironmentStatusBarItem", () => {
  beforeEach(() => {
    vi.mocked(vscode.window.createStatusBarItem).mockClear();
    mockConfigChangedListeners.length = 0;
    mockPlatformConfig = { environment: "production" };
    mockConfigBridgeInstance.getPlatform.mockClear();
    mockConfigBridgeInstance.onConfigChanged.mockClear();
  });

  describe("environment-to-style mapping", () => {
    it("production → neutral background, globe icon, prod label", () => {
      mockPlatformConfig = { environment: "production" };
      const sbi = new PlatformEnvironmentStatusBarItem();
      const item = getItem();

      expect(item.text).toBe("$(globe) Platform: prod");
      expect(item.backgroundColor).toBeUndefined();
      expect(item.tooltip).toBe("Platform environment: Production");

      sbi.dispose();
    });

    it("canary → warningBackground, beaker icon, canary label", () => {
      mockPlatformConfig = { environment: "canary" };
      const sbi = new PlatformEnvironmentStatusBarItem();
      const item = getItem();

      expect(item.text).toBe("$(beaker) Platform: canary");
      expect((item.backgroundColor as { id: string }).id).toBe("statusBarItem.warningBackground");
      expect(item.tooltip).toBe("Platform environment: Canary — pre-release API");

      sbi.dispose();
    });

    it("local → prominentBackground, home icon, local label", () => {
      mockPlatformConfig = { environment: "local" };
      const sbi = new PlatformEnvironmentStatusBarItem();
      const item = getItem();

      expect(item.text).toBe("$(home) Platform: local");
      expect((item.backgroundColor as { id: string }).id).toBe("statusBarItem.prominentBackground");
      expect(item.tooltip).toBe("Platform environment: Local (http://localhost:8787)");

      sbi.dispose();
    });

    it("custom → prominentBackground, settings-gear icon, custom label", () => {
      mockPlatformConfig = { environment: "custom", api_url: "https://my.api.dev" };
      const sbi = new PlatformEnvironmentStatusBarItem();
      const item = getItem();

      expect(item.text).toBe("$(settings-gear) Platform: custom");
      expect((item.backgroundColor as { id: string }).id).toBe("statusBarItem.prominentBackground");
      expect(item.tooltip).toBe("Platform environment: Custom (https://my.api.dev)");

      sbi.dispose();
    });
  });

  describe("custom URL tooltip", () => {
    it("shows actual api_url in tooltip for custom environment", () => {
      mockPlatformConfig = { environment: "custom", api_url: "http://localhost:9000" };
      const sbi = new PlatformEnvironmentStatusBarItem();
      const item = getItem();

      expect(item.tooltip).toBe("Platform environment: Custom (http://localhost:9000)");

      sbi.dispose();
    });

    it("falls back to 'unknown URL' when api_url is missing for custom", () => {
      mockPlatformConfig = { environment: "custom" };
      const sbi = new PlatformEnvironmentStatusBarItem();
      const item = getItem();

      expect(item.tooltip).toBe("Platform environment: Custom (unknown URL)");

      sbi.dispose();
    });
  });

  describe("reactive update via ConfigBridge.onConfigChanged", () => {
    it("updates text and background when environment changes to canary", () => {
      mockPlatformConfig = { environment: "production" };
      const sbi = new PlatformEnvironmentStatusBarItem();
      const item = getItem();

      expect(item.text).toBe("$(globe) Platform: prod");

      mockPlatformConfig = { environment: "canary" };
      fireConfigChanged();

      expect(item.text).toBe("$(beaker) Platform: canary");
      expect((item.backgroundColor as { id: string }).id).toBe("statusBarItem.warningBackground");

      sbi.dispose();
    });

    it("clears background color when switching back to production", () => {
      mockPlatformConfig = { environment: "canary" };
      const sbi = new PlatformEnvironmentStatusBarItem();
      const item = getItem();

      mockPlatformConfig = { environment: "production" };
      fireConfigChanged();

      expect(item.text).toBe("$(globe) Platform: prod");
      expect(item.backgroundColor).toBeUndefined();

      sbi.dispose();
    });
  });

  describe("default fallback", () => {
    it("renders as production when getPlatform() returns undefined", () => {
      mockPlatformConfig = undefined;
      const sbi = new PlatformEnvironmentStatusBarItem();
      const item = getItem();

      expect(item.text).toBe("$(globe) Platform: prod");
      expect(item.backgroundColor).toBeUndefined();

      sbi.dispose();
    });

    it("renders as production when environment field is missing from config", () => {
      mockPlatformConfig = { api_url: "https://api.nightgauge.dev" };
      const sbi = new PlatformEnvironmentStatusBarItem();
      const item = getItem();

      expect(item.text).toBe("$(globe) Platform: prod");

      sbi.dispose();
    });
  });

  describe("command wiring", () => {
    it("sets command to switchEnvironment by default", () => {
      const sbi = new PlatformEnvironmentStatusBarItem();
      const item = getItem();

      expect(item.command).toBe("nightgauge.platform.switchEnvironment");

      sbi.dispose();
    });

    it("accepts a custom commandId override", () => {
      const sbi = new PlatformEnvironmentStatusBarItem("custom.command");
      const item = getItem();

      expect(item.command).toBe("custom.command");

      sbi.dispose();
    });
  });

  describe("dispose", () => {
    it("disposes the status bar item and unregisters config listener", () => {
      const sbi = new PlatformEnvironmentStatusBarItem();
      const item = getItem();

      expect(mockConfigChangedListeners).toHaveLength(1);

      sbi.dispose();

      expect(item.dispose).toHaveBeenCalled();
      expect(mockConfigChangedListeners).toHaveLength(0);
    });
  });
});
