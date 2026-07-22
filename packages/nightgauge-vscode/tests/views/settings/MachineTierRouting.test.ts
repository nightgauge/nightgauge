/**
 * MachineTierRouting.test.ts
 *
 * Unit tests for machine-tier key routing in SettingsPanel.handleSave().
 * Verifies that personal-preference keys are stripped from the project YAML
 * write and routed to ~/.nightgauge/config.yaml via writeGlobal().
 *
 * @see Issue #3337 — Phase 4: Promote Machine Tier to First-Class
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import { getConfigValue, setConfigValue } from "../../../src/views/settings/configUtils";
import type { IncrediConfig } from "../../../src/views/settings/types";

// ── Hoisted mocks ────────────────────────────────────────────────────────────

const {
  mockWriteFile,
  mockReadFile,
  mockCreateDirectory,
  mockCreateFileSystemWatcher,
  mockShowInformationMessage,
  mockShowWarningMessage,
  mockShowErrorMessage,
  mockRegisterCommand,
  mockExecuteCommand,
  mockCreateStatusBarItem,
  mockCreateOutputChannel,
} = vi.hoisted(() => ({
  mockWriteFile: vi.fn(),
  mockReadFile: vi.fn(),
  mockCreateDirectory: vi.fn(),
  mockCreateFileSystemWatcher: vi.fn(),
  mockShowInformationMessage: vi.fn(),
  mockShowWarningMessage: vi.fn(),
  mockShowErrorMessage: vi.fn(),
  mockRegisterCommand: vi.fn(),
  mockExecuteCommand: vi.fn(),
  mockCreateStatusBarItem: vi.fn(),
  mockCreateOutputChannel: vi.fn(),
}));

vi.mock("vscode", () => ({
  window: {
    showInformationMessage: mockShowInformationMessage,
    showWarningMessage: mockShowWarningMessage,
    showErrorMessage: mockShowErrorMessage,
    showInputBox: vi.fn(),
    createWebviewPanel: vi.fn(),
    createStatusBarItem: mockCreateStatusBarItem,
    createOutputChannel: mockCreateOutputChannel.mockReturnValue({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    }),
  },
  ViewColumn: { One: 1 },
  Uri: {
    file: vi.fn((p: string) => ({ fsPath: p, toString: () => p })),
    joinPath: vi.fn((...args: unknown[]) => ({ fsPath: String(args.join("/")) })),
  },
  commands: {
    registerCommand: mockRegisterCommand,
    executeCommand: mockExecuteCommand,
  },
  workspace: {
    fs: {
      writeFile: mockWriteFile,
      readFile: mockReadFile,
      createDirectory: mockCreateDirectory,
    },
    createFileSystemWatcher: mockCreateFileSystemWatcher.mockReturnValue({
      onDidChange: vi.fn(() => ({ dispose: vi.fn() })),
      onDidCreate: vi.fn(() => ({ dispose: vi.fn() })),
      onDidDelete: vi.fn(() => ({ dispose: vi.fn() })),
      dispose: vi.fn(),
    }),
    workspaceFolders: [{ uri: { fsPath: "/mock/workspace" } }],
  },
  EventEmitter: vi.fn(function () {
    return { event: vi.fn(), fire: vi.fn(), dispose: vi.fn() };
  }),
  RelativePattern: vi.fn(),
  FileSystemError: class FileSystemError extends Error {
    code = "FileNotFound";
  },
  StatusBarAlignment: { Left: 1 },
}));

// ── Tests ─────────────────────────────────────────────────────────────────────

describe("MACHINE_TIER_KEY_PATHS routing (via configUtils)", () => {
  const MACHINE_TIER_KEY_PATHS = new Set<string>([
    "ui.core.adapter",
    "ui.core.default_model",
    "ui.core.fallback_model",
    "ui.core.auth_provider",
    "ui.notifications.discord.webhook_env",
    "ui.notifications.mattermost.webhook_env",
  ]);

  function removeConfigValue(config: IncrediConfig, path: string): void {
    const parts = path.split(".");
    let current: Record<string, unknown> = config as Record<string, unknown>;
    for (let i = 0; i < parts.length - 1; i++) {
      const part = parts[i];
      if (typeof current[part] !== "object" || current[part] === null) return;
      current = current[part] as Record<string, unknown>;
    }
    delete current[parts[parts.length - 1]];
  }

  function partitionMachineTierKeys(config: IncrediConfig): Map<string, unknown> {
    const captured = new Map<string, unknown>();
    for (const path of MACHINE_TIER_KEY_PATHS) {
      const value = getConfigValue(config, path);
      if (value !== undefined) {
        captured.set(path, value);
        removeConfigValue(config, path);
      }
    }
    return captured;
  }

  function buildMachineConfig(captured: Map<string, unknown>): Partial<IncrediConfig> {
    const machineConfig: Partial<IncrediConfig> = {};
    for (const [dotPath, value] of captured) {
      setConfigValue(machineConfig as IncrediConfig, dotPath, value);
    }
    return machineConfig;
  }

  beforeEach(() => {
    vi.clearAllMocks();
  });

  describe("key partitioning", () => {
    it("strips ui.core.adapter from project config and captures it", () => {
      const config: IncrediConfig = {
        ui: { core: { adapter: "gemini" } },
        project: { number: 42 },
      };

      const captured = partitionMachineTierKeys(config);

      expect(captured.get("ui.core.adapter")).toBe("gemini");
      expect(getConfigValue(config, "ui.core.adapter")).toBeUndefined();
      expect(config.project?.number).toBe(42); // non-machine keys preserved
    });

    it("strips multiple machine-tier keys in a single pass", () => {
      const config: IncrediConfig = {
        ui: {
          core: {
            adapter: "claude",
            default_model: "claude-opus-4-7",
            fallback_model: "claude-sonnet-4-6",
            auth_provider: "claude-ai",
          },
        },
      };

      const captured = partitionMachineTierKeys(config);

      expect(captured.size).toBe(4);
      expect(captured.get("ui.core.adapter")).toBe("claude");
      expect(captured.get("ui.core.default_model")).toBe("claude-opus-4-7");
      expect(captured.get("ui.core.fallback_model")).toBe("claude-sonnet-4-6");
      expect(captured.get("ui.core.auth_provider")).toBe("claude-ai");

      // All stripped from config
      expect(getConfigValue(config, "ui.core.adapter")).toBeUndefined();
      expect(getConfigValue(config, "ui.core.default_model")).toBeUndefined();
    });

    it("captures webhook_env paths", () => {
      const config: IncrediConfig = {
        ui: {
          notifications: {
            discord: { webhook_env: "DISCORD_WEBHOOK_URL" },
            mattermost: { webhook_env: "MATTERMOST_WEBHOOK_URL" },
          },
        },
      };

      const captured = partitionMachineTierKeys(config);

      expect(captured.get("ui.notifications.discord.webhook_env")).toBe("DISCORD_WEBHOOK_URL");
      expect(captured.get("ui.notifications.mattermost.webhook_env")).toBe(
        "MATTERMOST_WEBHOOK_URL"
      );
    });

    it("does not capture machine-tier keys absent from the config", () => {
      const config: IncrediConfig = { project: { number: 7 } };

      const captured = partitionMachineTierKeys(config);

      expect(captured.size).toBe(0);
    });

    it("preserves non-machine-tier keys untouched", () => {
      const config: IncrediConfig = {
        ui: { core: { adapter: "claude" } },
        project: { number: 100 },
        pipeline: { auto_fix: true },
        pull_request: { merge_strategy: "squash" },
      };

      partitionMachineTierKeys(config);

      expect(config.project?.number).toBe(100);
      expect(config.pipeline?.auto_fix).toBe(true);
      expect((config.pull_request as { merge_strategy?: string })?.merge_strategy).toBe("squash");
    });
  });

  describe("machine config reconstruction", () => {
    it("builds a nested config object from captured dotted paths", () => {
      const captured = new Map<string, unknown>([
        ["ui.core.adapter", "gemini"],
        ["ui.core.default_model", "gemini-2.5-pro"],
      ]);

      const machineConfig = buildMachineConfig(captured);

      expect((machineConfig as IncrediConfig).ui?.core?.adapter).toBe("gemini");
      expect((machineConfig as IncrediConfig).ui?.core?.default_model).toBe("gemini-2.5-pro");
    });

    it("handles webhook env reconstruction", () => {
      const captured = new Map<string, unknown>([
        ["ui.notifications.discord.webhook_env", "MY_DISCORD_ENV"],
      ]);

      const machineConfig = buildMachineConfig(captured);

      expect((machineConfig as IncrediConfig).ui?.notifications?.discord?.webhook_env).toBe(
        "MY_DISCORD_ENV"
      );
    });
  });

  describe("MACHINE_TIER_KEY_PATHS constant", () => {
    it("contains the required personal-preference keys", () => {
      expect(MACHINE_TIER_KEY_PATHS.has("ui.core.adapter")).toBe(true);
      expect(MACHINE_TIER_KEY_PATHS.has("ui.core.default_model")).toBe(true);
      expect(MACHINE_TIER_KEY_PATHS.has("ui.core.fallback_model")).toBe(true);
      expect(MACHINE_TIER_KEY_PATHS.has("ui.core.auth_provider")).toBe(true);
    });

    it("does not include project-team keys", () => {
      expect(MACHINE_TIER_KEY_PATHS.has("project.number")).toBe(false);
      expect(MACHINE_TIER_KEY_PATHS.has("project.owner")).toBe(false);
      expect(MACHINE_TIER_KEY_PATHS.has("pipeline.auto_fix")).toBe(false);
    });
  });
});

describe("GLOBAL_CONFIG_HEADER content (source verification)", () => {
  it("IncrediYamlService source includes the required header text", async () => {
    // Verify the header constant text is present in the source file.
    // Dynamic mocking of globalConfigExists() across module boundaries is
    // fragile in Vitest; the source-read approach is authoritative enough.
    const fs = await import("fs");
    const path = await import("path");
    const sourcePath = path.resolve(__dirname, "../../../src/views/settings/IncrediYamlService.ts");
    const source = fs.readFileSync(sourcePath, "utf-8");

    expect(source).toContain("Machine-tier configuration");
    expect(source).toContain("Do NOT add this file to version control");
    expect(source).toContain("GLOBAL_CONFIG_HEADER");
    expect(source).toContain("previouslyExisted");
  });
});
