/**
 * OpenCodeModelMachineTier.test.ts
 *
 * Issue #2138: the settings panel persists the chosen OpenCode model to the
 * machine-tier `opencode.model` key (ADR-022 § 7), never the committed
 * project config, and renders it back as a bound select.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

// ── Hoisted spies for the YAML service + SecretStorage ───────────────────────

const {
  mockWrite,
  mockWriteLocal,
  mockWriteGlobal,
  mockReadProject,
  mockReadLocal,
  mockReadGlobal,
  mockReadEffective,
  mockSetSecret,
  mockGetSecret,
  mockDeleteSecret,
} = vi.hoisted(() => ({
  mockWrite: vi.fn().mockResolvedValue({ success: true }),
  mockWriteLocal: vi.fn().mockResolvedValue({ success: true }),
  mockWriteGlobal: vi.fn().mockResolvedValue({ success: true }),
  mockReadProject: vi.fn().mockResolvedValue({ config: {} }),
  mockReadLocal: vi.fn().mockResolvedValue({ config: {} }),
  mockReadGlobal: vi.fn().mockResolvedValue({ config: {} }),
  mockReadEffective: vi.fn().mockResolvedValue({
    config: {},
    sources: {},
    tiers: { hasGlobal: true, hasLocal: false, hasProject: true },
    envVarsApplied: [],
  }),
  mockSetSecret: vi.fn().mockResolvedValue(undefined),
  mockGetSecret: vi.fn().mockResolvedValue(undefined),
  mockDeleteSecret: vi.fn().mockResolvedValue(undefined),
}));

vi.mock("vscode", () => ({
  window: {
    showWarningMessage: vi.fn(),
    showInformationMessage: vi.fn(),
    showErrorMessage: vi.fn(),
    showInputBox: vi.fn(),
    createWebviewPanel: vi.fn(),
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
  },
  ViewColumn: { One: 1 },
  Uri: {
    joinPath: vi.fn((...args: unknown[]) => ({ fsPath: args.join("/") })),
    file: vi.fn((p: string) => ({ fsPath: p })),
  },
  commands: { executeCommand: vi.fn() },
  workspace: {
    createFileSystemWatcher: vi.fn(() => ({
      onDidChange: vi.fn(() => ({ dispose: vi.fn() })),
      onDidCreate: vi.fn(() => ({ dispose: vi.fn() })),
      onDidDelete: vi.fn(() => ({ dispose: vi.fn() })),
      dispose: vi.fn(),
    })),
    workspaceFolders: [{ uri: { fsPath: "/test" } }],
  },
  EventEmitter: vi.fn(function () {
    return { event: vi.fn(), fire: vi.fn(), dispose: vi.fn() };
  }),
  RelativePattern: vi.fn(),
}));

// Use the real configUtils helpers so nested path get/set behave correctly.
vi.mock("../../../src/views/settings/NightgaugeYamlService", async () => {
  const actual = await vi.importActual<typeof import("../../../src/views/settings/configUtils")>(
    "../../../src/views/settings/configUtils"
  );
  return {
    NightgaugeYamlService: class NightgaugeYamlServiceMock {
      onDidChange = vi.fn(() => ({ dispose: vi.fn() }));
      dispose = vi.fn();
      readEffective = mockReadEffective;
      read = mockReadProject;
      readLocal = mockReadLocal;
      readGlobal = mockReadGlobal;
      write = mockWrite;
      writeLocal = mockWriteLocal;
      writeGlobal = mockWriteGlobal;
      getConfigPath = vi.fn(() => "/test/.nightgauge/config.yaml");
      getLocalConfigPath = vi.fn(() => "/test/.nightgauge/config.local.yaml");
      getGlobalConfigPath = vi.fn(() => "/home/.nightgauge/config.yaml");
    },
    setConfigValue: actual.setConfigValue,
    getConfigValue: actual.getConfigValue,
    mergeWithDefaults: (
      await vi.importActual<typeof import("../../../src/views/settings/NightgaugeYamlService")>(
        "../../../src/views/settings/NightgaugeYamlService"
      )
    ).mergeWithDefaults,
  };
});

vi.mock("../../../src/services/SecretStorageService", () => ({
  SECRET_KEYS: { platformLicenseKey: "nightgauge.platform.licenseKey" },
  SecretStorageService: {
    getInstance: () => ({
      getSecret: mockGetSecret,
      setSecret: mockSetSecret,
      deleteSecret: mockDeleteSecret,
    }),
  },
}));

vi.mock("../../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      forgeList: vi.fn().mockResolvedValue({ forges: [] }),
      configTierAudit: vi.fn().mockResolvedValue({ entries: [] }),
    }),
  },
}));

vi.mock("../../../src/services/CodexModelCatalogService", () => ({
  CodexModelCatalogService: class {
    listModels = vi.fn(() => []);
  },
}));

vi.mock("../../../src/utils/logger", () => ({
  Logger: class {
    info = vi.fn();
    warn = vi.fn();
    error = vi.fn();
    debug = vi.fn();
    dispose = vi.fn();
  },
}));

import { SettingsPanel } from "../../../src/views/settings/SettingsPanel";
import { MACHINE_TIER_KEY_PATHS } from "../../../src/views/settings/SettingsPanel";
import { TIER_TABS } from "../../../src/views/settings/types";

interface PanelInternals {
  projectConfig: Record<string, unknown>;
  globalConfig: Record<string, unknown>;
  tierState: { currentTier: string; defaultEditTier: string };
  handleChange: (path: string, value: unknown) => void;
  handleSave: (tier?: string) => Promise<void>;
  loadAllTiers: () => Promise<void>;
}

function newPanel(): PanelInternals {
  return new SettingsPanel({ fsPath: "/ext" } as never, "/workspace") as unknown as PanelInternals;
}
import { getSettingsHtml } from "../../../src/views/settings/SettingsHtml";
import { NightgaugeConfigSchema, getDefaultConfig } from "../../../src/config/schema";
import { isMachineTierPath } from "../../../src/views/settings/tierRouting";
import type { NightgaugeConfig } from "../../../src/views/settings/types";

describe("#2138 — opencode.model settings round trip", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockReadProject.mockResolvedValue({ config: {} });
    mockReadLocal.mockResolvedValue({ config: {} });
    mockReadGlobal.mockResolvedValue({ config: {} });
  });

  it("routes the opencode block to the machine tier", () => {
    expect(MACHINE_TIER_KEY_PATHS.has("opencode")).toBe(true);
    expect(isMachineTierPath("opencode.model")).toBe(true);
  });

  it("keeps opencode.model and passes the block's other keys through the schema", () => {
    const parsed = NightgaugeConfigSchema.parse({
      opencode: {
        model: "lmstudio/qwen",
        base_url: "http://127.0.0.1:1234/v1",
        endpoints: [{ id: "box", kind: "self_hosted" }],
      },
    });
    expect(parsed.opencode?.model).toBe("lmstudio/qwen");
    expect((parsed.opencode as Record<string, unknown>).endpoints).toEqual([
      { id: "box", kind: "self_hosted" },
    ]);
  });

  it("writes a chosen model to the machine tier and never the project file", async () => {
    const panel = newPanel();
    panel.tierState = { currentTier: "merged", defaultEditTier: "project" };

    panel.handleChange("opencode.model", "lmstudio/qwen");
    await panel.handleSave();

    const projectArg = mockWrite.mock.calls[0]?.[0] as { opencode?: unknown };
    expect(projectArg?.opencode).toBeUndefined();
    expect(mockWriteGlobal).toHaveBeenCalledTimes(1);
    expect(mockWriteGlobal.mock.calls[0][0]).toEqual({ opencode: { model: "lmstudio/qwen" } });
  });

  it("writes it directly on the Global tab", async () => {
    const panel = newPanel();
    panel.tierState = { currentTier: "global", defaultEditTier: "project" };

    panel.handleChange("opencode.model", "ollama/llama3");
    await panel.handleSave();

    expect(mockWrite).not.toHaveBeenCalled();
    const arg = mockWriteGlobal.mock.calls[0][0] as { opencode?: { model?: string } };
    expect(arg.opencode?.model).toBe("ollama/llama3");
  });

  it("reads the saved model back and renders it as the selected option", async () => {
    mockReadGlobal.mockResolvedValue({ config: { opencode: { model: "lmstudio/qwen" } } });
    const panel = newPanel();
    await panel.loadAllTiers();
    expect(panel.globalConfig.opencode).toMatchObject({ model: "lmstudio/qwen" });

    const config = {
      ...getDefaultConfig(),
      ...panel.globalConfig,
    } as NightgaugeConfig;
    config.ui = { ...config.ui, core: { ...config.ui?.core, adapter: "opencode" } } as never;
    const html = getSettingsHtml(
      { cspSource: "test-csp" } as never,
      config,
      new Set(),
      {},
      undefined,
      {
        openCodeModels: [
          { id: "ollama/llama3", label: "ollama/llama3", selectable: true },
          { id: "", label: "could not list models", selectable: false },
        ],
      }
    );
    expect(html).toContain('id="core-opencode-settings"');
    expect(html).toMatch(/data-path="opencode\.model"|id="opencode\.model"/);
    expect(html).toMatch(/<option value="lmstudio\/qwen"[^>]*selected/);
    expect(html).toContain('<option value="ollama/llama3"');
  });
});
