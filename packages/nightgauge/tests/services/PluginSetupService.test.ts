/**
 * Unit tests for PluginSetupService.ts
 *
 * Tests ConfigBridge integration for plugin configuration.
 *
 * @see Issue #475 - Refactor notification, warning, and plugin services to use ConfigBridge
 */

import { describe, it, expect, vi, beforeEach, afterEach, type Mock } from "vitest";
import * as fs from "fs";
import * as os from "os";
import * as path from "path";
import { ConfigBridge } from "../../src/services/ConfigBridge";
import { DEFAULT_CONFIG } from "../../src/config/schema";
import { resetMainChannelForTests } from "../../src/utils/logger";

const mockExecFile = vi.hoisted(() => vi.fn());

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
  promisify: vi.fn(() => mockExecFile),
}));

const PLUGIN_ID = "nightgauge@nightgauge-plugins";
const CACHE_MISS = "Marketplace nightgauge-plugins failed to load: cache-miss";

// `claude plugin list --json`, as captured from Claude Code 2.1.316.
const HEALTHY = [{ id: PLUGIN_ID, version: "1.25.0", scope: "user", enabled: true }];
const DISABLED = [{ ...HEALTHY[0], enabled: false }];
const OUTAGE = [{ ...HEALTHY[0], errors: [CACHE_MISS] }];

interface FakeClaude {
  plugins: unknown;
  marketplacePath?: string;
  afterAdd?: unknown;
  addFails?: string;
  listFails?: string;
  marketplaceEntry?: unknown;
}

/** A scripted `claude` CLI. Every invocation lands in `mockExecFile.mock.calls`. */
function scriptClaude(fake: FakeClaude): void {
  mockExecFile.mockImplementation(async (file: string, args: string[]) => {
    expect(file).toBe("claude");
    const command = args.join(" ");
    if (command === "plugin list --json") {
      if (fake.listFails) {
        throw new Error(fake.listFails);
      }
      return {
        stdout: typeof fake.plugins === "string" ? fake.plugins : JSON.stringify(fake.plugins),
      };
    }
    if (command === "plugin marketplace list --json") {
      // Field shape captured from Claude Code 2.1.316.
      const entries =
        fake.marketplaceEntry !== undefined
          ? [fake.marketplaceEntry]
          : fake.marketplacePath === undefined
            ? []
            : [
                {
                  name: "nightgauge-plugins",
                  source: "directory",
                  path: fake.marketplacePath,
                  installLocation: fake.marketplacePath,
                },
              ];
      return { stdout: JSON.stringify(entries) };
    }
    if (command.startsWith("plugin marketplace add ")) {
      if (fake.addFails) {
        throw new Error(fake.addFails);
      }
      fake.marketplacePath = args[3];
      if (fake.afterAdd !== undefined) {
        fake.plugins = fake.afterAdd;
      }
    }
    return { stdout: "" };
  });
}

const cliCalls = (prefix: string): string[][] =>
  mockExecFile.mock.calls
    .map((call) => call[1] as string[])
    .filter((args) => args.join(" ").startsWith(prefix));

describe("PluginSetupService", () => {
  let mockConfigBridge: {
    isInitialized: Mock;
    getUI: Mock;
  };

  beforeEach(async () => {
    const vscode = await import("vscode");
    vi.mocked(vscode.window.showWarningMessage).mockResolvedValue(undefined as never);
    mockExecFile.mockReset();
    mockExecFile.mockResolvedValue({ stdout: "" });
    mockConfigBridge = {
      isInitialized: vi.fn(),
      getUI: vi.fn(),
    };
    vi.mocked(ConfigBridge.getInstance).mockReturnValue(
      mockConfigBridge as unknown as ConfigBridge
    );
    vi.spyOn(console, "warn").mockImplementation(() => {});
    vi.spyOn(console, "debug").mockImplementation(() => {});
    // PluginSetupService now shares the memoized main "Nightgauge" channel
    // (#749) — drop it between tests so each one sees its own mocked
    // createOutputChannel() rather than a channel cached by an earlier test.
    resetMainChannelForTests();
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
      expect(defaults.marketplace_url).toBe("https://github.com/nightgauge/nightgauge.git#stable");
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

  describe("failure detection and repair (#2719)", () => {
    let tmp: string;
    let extensionPath: string;
    let stablePath: string;
    let dismissed: boolean;

    const makeService = async () => {
      const { PluginSetupService } = await import("../../src/services/PluginSetupService");
      return new PluginSetupService({
        extensionPath,
        globalStorageUri: { fsPath: path.join(tmp, "globalStorage") },
        extension: { packageJSON: { version: "2.0.0" } },
        globalState: {
          get: vi.fn((_key: string, fallback: boolean) => dismissed || fallback),
          update: vi.fn(),
        },
      } as never);
    };

    beforeEach(() => {
      tmp = fs.mkdtempSync(path.join(os.tmpdir(), "ng-plugin-setup-"));
      extensionPath = path.join(tmp, "extensions", "nightgauge-2.0.0");
      stablePath = path.join(tmp, "globalStorage", "claude-marketplace");
      dismissed = false;
      const dist = path.join(extensionPath, "dist");
      fs.mkdirSync(path.join(dist, ".claude-plugin"), { recursive: true });
      fs.mkdirSync(path.join(dist, "claude-plugins", "nightgauge"), { recursive: true });
      fs.writeFileSync(path.join(dist, ".claude-plugin", "marketplace.json"), "{}");
      mockConfigBridge.isInitialized.mockReturnValue(false);
    });

    afterEach(() => {
      fs.rmSync(tmp, { recursive: true, force: true });
    });

    describe("getPluginStatus", () => {
      it("reads an enabled plugin", async () => {
        scriptClaude({ plugins: HEALTHY, marketplacePath: "/src/nightgauge" });
        const status = await (await makeService()).getPluginStatus();
        expect(status).toMatchObject({
          plugin: "enabled",
          pluginErrors: [],
          marketplaceAdded: true,
          marketplacePath: "/src/nightgauge",
        });
      });

      it("reads failed from errors, although the entry says enabled: true", async () => {
        scriptClaude({ plugins: OUTAGE, marketplacePath: "/gone" });
        const status = await (await makeService()).getPluginStatus();
        expect(status.plugin).toBe("failed");
        expect(status.pluginErrors).toEqual([CACHE_MISS]);
      });

      it("reads absent from a list without the exact id", async () => {
        scriptClaude({ plugins: [{ id: "nightgauge-extras@elsewhere", enabled: true }] });
        expect((await (await makeService()).getPluginStatus()).plugin).toBe("absent");
      });

      it("reads disabled", async () => {
        scriptClaude({ plugins: DISABLED });
        expect((await (await makeService()).getPluginStatus()).plugin).toBe("disabled");
      });

      it("reads unknown from unparsable output, never enabled", async () => {
        scriptClaude({ plugins: "Error: not json" });
        expect((await (await makeService()).getPluginStatus()).plugin).toBe("unknown");
      });

      it("reads a git source as added with no path", async () => {
        mockExecFile.mockImplementation(async (_file: string, args: string[]) => ({
          stdout:
            args[1] === "marketplace"
              ? JSON.stringify([{ name: "nightgauge-plugins", source: "git", url: "u" }])
              : "[]",
        }));
        const status = await (await makeService()).getPluginStatus();
        expect(status.marketplaceAdded).toBe(true);
        expect(status.marketplacePath).toBeUndefined();
      });
    });

    it("repoints a failed plugin to the stable copy, in place", async () => {
      const vscode = await import("vscode");
      scriptClaude({ plugins: OUTAGE, marketplacePath: "/gone", afterAdd: HEALTHY });

      await (await makeService()).checkAndPromptSetup();

      expect(cliCalls("plugin marketplace add")).toEqual([
        ["plugin", "marketplace", "add", stablePath],
      ]);
      expect(cliCalls("plugin marketplace remove")).toEqual([]);
      expect(vscode.window.showErrorMessage).not.toHaveBeenCalled();
    });

    it("surfaces Claude's error verbatim when the repair does not take", async () => {
      const vscode = await import("vscode");
      vi.mocked(vscode.window.showErrorMessage).mockResolvedValue(undefined as never);
      scriptClaude({ plugins: OUTAGE, marketplacePath: "/gone" });

      await (await makeService()).checkAndPromptSetup();

      expect(vscode.window.showErrorMessage).toHaveBeenCalledWith(
        expect.stringContaining(CACHE_MISS),
        "View Output"
      );
      expect(cliCalls("plugin marketplace add")).toHaveLength(1);
    });

    it("surfaces a refused add alongside the plugin's own error", async () => {
      const vscode = await import("vscode");
      vi.mocked(vscode.window.showErrorMessage).mockResolvedValue(undefined as never);
      scriptClaude({ plugins: OUTAGE, marketplacePath: "/gone", addFails: "add was refused" });

      await (await makeService()).checkAndPromptSetup();

      const [message] = vi.mocked(vscode.window.showErrorMessage).mock.calls[0];
      expect(message).toContain(CACHE_MISS);
      expect(message).toContain("add was refused");
    });

    it("repairs and surfaces a failure while the install prompt is dismissed", async () => {
      const vscode = await import("vscode");
      vi.mocked(vscode.window.showErrorMessage).mockResolvedValue(undefined as never);
      dismissed = true;
      scriptClaude({ plugins: OUTAGE, marketplacePath: "/gone" });

      await (await makeService()).checkAndPromptSetup();

      expect(cliCalls("plugin marketplace add")).toHaveLength(1);
      expect(vscode.window.showErrorMessage).toHaveBeenCalledTimes(1);
    });

    it("repairs and surfaces a failure with autoPrompt off", async () => {
      const vscode = await import("vscode");
      vi.mocked(vscode.window.showErrorMessage).mockResolvedValue(undefined as never);
      mockConfigBridge.isInitialized.mockReturnValue(true);
      mockConfigBridge.getUI.mockReturnValue({ plugins: { auto_prompt: false } });
      scriptClaude({ plugins: OUTAGE, marketplacePath: "/gone" });

      await (await makeService()).checkAndPromptSetup();

      expect(vscode.window.showErrorMessage).toHaveBeenCalledTimes(1);
    });

    it("moves a healthy plugin off a version-keyed source before an upgrade deletes it", async () => {
      scriptClaude({ plugins: HEALTHY, marketplacePath: path.join(extensionPath, "dist") });

      await (await makeService()).checkAndPromptSetup();

      expect(cliCalls("plugin marketplace add")).toEqual([
        ["plugin", "marketplace", "add", stablePath],
      ]);
    });

    it("leaves a healthy foreign marketplace alone", async () => {
      const vscode = await import("vscode");
      scriptClaude({ plugins: HEALTHY, marketplacePath: "/src/nightgauge" });

      await (await makeService()).checkAndPromptSetup();

      expect(cliCalls("plugin marketplace add")).toEqual([]);
      expect(cliCalls("plugin marketplace remove")).toEqual([]);
      expect(cliCalls("plugin marketplace update")).toEqual([]);
      expect(vscode.window.showInformationMessage).not.toHaveBeenCalled();
    });

    it("neither prompts for nor repairs a disabled plugin", async () => {
      const vscode = await import("vscode");
      scriptClaude({ plugins: DISABLED, marketplacePath: path.join(extensionPath, "dist") });

      await (await makeService()).checkAndPromptSetup();

      expect(cliCalls("plugin marketplace add")).toEqual([]);
      expect(vscode.window.showInformationMessage).not.toHaveBeenCalled();
    });

    it("warns, and neither prompts nor repairs, on unparsable output", async () => {
      const vscode = await import("vscode");
      scriptClaude({ plugins: "Error: not json", marketplacePath: "/src/nightgauge" });

      await (await makeService()).checkAndPromptSetup();

      expect(cliCalls("plugin marketplace add")).toEqual([]);
      expect(vscode.window.showInformationMessage).not.toHaveBeenCalled();
      expect(vscode.window.showWarningMessage).toHaveBeenCalledWith(
        expect.stringContaining("unreadable output"),
        "View Output"
      );
    });

    it("names the failure when plugin list cannot run, even with the prompt dismissed", async () => {
      const vscode = await import("vscode");
      dismissed = true;
      scriptClaude({ plugins: [], listFails: "unknown option --json" });

      await (await makeService()).checkAndPromptSetup();

      expect(vscode.window.showWarningMessage).toHaveBeenCalledWith(
        expect.stringContaining("unknown option --json"),
        "View Output"
      );
    });

    it("does not call a repair it could not verify a success", async () => {
      const vscode = await import("vscode");
      scriptClaude({ plugins: OUTAGE, marketplacePath: "/gone", afterAdd: "Error: not json" });

      await (await makeService()).checkAndPromptSetup();

      expect(cliCalls("plugin marketplace add")).toHaveLength(1);
      expect(vscode.window.showWarningMessage).toHaveBeenCalledTimes(1);
      expect(vscode.window.showWarningMessage).toHaveBeenCalledWith(
        expect.stringContaining("unreadable output"),
        "View Output"
      );
      const output = vi.mocked(vscode.window.createOutputChannel).mock.results[0]?.value;
      const lines = vi.mocked(output.appendLine).mock.calls.map((call) => String(call[0]));
      expect(lines.join("\n")).not.toContain("✓ Marketplace repaired");
    });

    it("logs a marketplace entry whose source cannot be read", async () => {
      const vscode = await import("vscode");
      scriptClaude({
        plugins: HEALTHY,
        marketplaceEntry: { name: "nightgauge-plugins", source: "directory" },
      });

      const status = await (await makeService()).getPluginStatus();

      expect(status.marketplaceAdded).toBe(true);
      expect(status.marketplacePath).toBeUndefined();
      const output = vi.mocked(vscode.window.createOutputChannel).mock.results[0]?.value;
      const lines = vi.mocked(output.appendLine).mock.calls.map((call) => String(call[0]));
      expect(lines.join("\n")).toContain("has no usable source");
    });

    it("offers an install for an absent plugin", async () => {
      const vscode = await import("vscode");
      vi.mocked(vscode.window.showInformationMessage).mockResolvedValue(undefined as never);
      scriptClaude({ plugins: [] });

      await (await makeService()).checkAndPromptSetup();

      expect(vscode.window.showInformationMessage).toHaveBeenCalledTimes(1);
    });

    it("installs an absent plugin from the stable copy, and only the real plugin", async () => {
      const vscode = await import("vscode");
      vi.mocked(vscode.window.showInformationMessage).mockResolvedValue("Install Plugins" as never);
      vi.mocked(vscode.window.withProgress).mockImplementation((_options, task) =>
        task({ report: vi.fn() }, {} as never)
      );
      scriptClaude({ plugins: [] });

      await (await makeService()).checkAndPromptSetup();

      expect(cliCalls("plugin marketplace add")).toEqual([
        ["plugin", "marketplace", "add", stablePath],
      ]);
      expect(cliCalls("plugin install")).toEqual([["plugin", "install", PLUGIN_ID]]);
    });

    it("refreshes the registered stable copy after a recopy", async () => {
      scriptClaude({ plugins: HEALTHY, marketplacePath: stablePath });

      await (await makeService()).checkAndPromptSetup();

      expect(cliCalls("plugin marketplace update")).toHaveLength(1);
      expect(cliCalls("plugin update")).toEqual([["plugin", "update", PLUGIN_ID]]);
    });

    it("does not refresh when the stable copy did not change", async () => {
      scriptClaude({ plugins: HEALTHY, marketplacePath: stablePath });
      await (await makeService()).checkAndPromptSetup();
      mockExecFile.mockClear();

      await (await makeService()).checkAndPromptSetup();

      expect(cliCalls("plugin marketplace update")).toEqual([]);
      expect(cliCalls("plugin update")).toEqual([]);
    });
  });

  describe("dispose", () => {
    it("does not tear down the shared Nightgauge channel it no longer owns (#749)", async () => {
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

      // Plugin Setup is folded into the shared main channel behind a
      // "plugin-setup" prefix — disposing this service must not kill the
      // channel every other subsystem still writes to.
      expect(mockDispose).not.toHaveBeenCalled();
    });
  });
});
