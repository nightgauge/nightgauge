import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("vscode", () => ({
  window: {
    showWarningMessage: vi.fn(),
    showInformationMessage: vi.fn(),
    showInputBox: vi.fn(),
    showErrorMessage: vi.fn(),
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

vi.mock("../../../src/views/settings/IncrediYamlService", () => ({
  IncrediYamlService: class IncrediYamlServiceMock {
    onDidChange = vi.fn(() => ({ dispose: vi.fn() }));
    dispose = vi.fn();
    readEffective = vi.fn().mockResolvedValue({
      config: {},
      sources: {},
      tiers: { hasGlobal: false, hasLocal: false, hasProject: true },
      envVarsApplied: [],
    });
    read = vi.fn().mockResolvedValue({ config: {} });
    readLocal = vi.fn().mockResolvedValue({ config: {} });
    readGlobal = vi.fn().mockResolvedValue({ config: {} });
    write = vi.fn().mockResolvedValue({ success: true });
    writeLocal = vi.fn().mockResolvedValue({ success: true });
    writeGlobal = vi.fn().mockResolvedValue({ success: true });
  },
  setConfigValue: vi.fn((config: Record<string, unknown>, path: string, value: unknown) => {
    const parts = path.split(".");
    let current: Record<string, unknown> = config;
    for (const part of parts.slice(0, -1)) {
      const next = current[part];
      if (!next || typeof next !== "object") {
        current[part] = {};
      }
      current = current[part] as Record<string, unknown>;
    }
    current[parts[parts.length - 1]] = value;
  }),
  getConfigValue: vi.fn(),
}));

vi.mock("../../../src/services/LmStudioService", () => ({
  LmStudioService: class LmStudioServiceMock {
    listModels = vi.fn().mockResolvedValue([]);
    startServer = vi.fn().mockResolvedValue(undefined);
    loadModel = vi.fn().mockResolvedValue(undefined);
  },
}));

vi.mock("../../../src/services/CodexModelCatalogService", () => ({
  CodexModelCatalogService: class CodexModelCatalogServiceMock {
    listModels = vi.fn(() => ["gpt-5.4"]);
  },
}));

vi.mock("../../../src/utils/logger", () => ({
  Logger: class LoggerMock {
    info = vi.fn();
    warn = vi.fn();
    error = vi.fn();
    debug = vi.fn();
    dispose = vi.fn();
  },
}));

import { SettingsPanel } from "../../../src/views/settings/SettingsPanel";
import * as vscode from "vscode";

describe("SettingsPanel adapter change handling", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("does not auto-refresh Codex models when adapter changes", () => {
    const panel = new SettingsPanel({ fsPath: "/ext" } as never, "/workspace") as any;
    const refreshCodexModels = vi.spyOn(panel, "refreshCodexModels");

    panel.handleChange("ui.core.adapter", "codex");

    expect(refreshCodexModels).not.toHaveBeenCalled();
  });

  it("does not auto-refresh LM Studio models when adapter changes", () => {
    const panel = new SettingsPanel({ fsPath: "/ext" } as never, "/workspace") as any;
    const refreshLmStudioModels = vi.spyOn(panel, "refreshLmStudioModels");

    panel.handleChange("ui.core.adapter", "lm-studio");

    expect(refreshLmStudioModels).not.toHaveBeenCalled();
  });

  it("keeps merged-view saves in config.local.yaml after tiers load", async () => {
    const panel = new SettingsPanel({ fsPath: "/ext" } as never, "/workspace") as any;
    await panel.loadAllTiers();
    panel.handleChange("project.number", 8);

    await panel.handleSave();

    expect(panel.yamlService.writeLocal).toHaveBeenCalledOnce();
    expect(panel.yamlService.write).not.toHaveBeenCalled();
    expect(panel.tierState.defaultEditTier).toBe("local");
  });

  it("preserves dirty edits when an external config change is kept", async () => {
    vi.mocked(vscode.window.showWarningMessage).mockResolvedValueOnce("Keep My Edits" as never);
    const panel = new SettingsPanel({ fsPath: "/ext" } as never, "/workspace") as any;
    panel.handleChange("project.number", 8);
    const load = vi.spyOn(panel, "loadAllTiers");

    await panel.handleExternalConfigChange();

    expect(load).not.toHaveBeenCalled();
    expect(panel.hasUnsavedChanges).toBe(true);
  });

  it("reloads only after explicit confirmation and clears dirty state", async () => {
    vi.mocked(vscode.window.showWarningMessage).mockResolvedValueOnce("Reload from Disk" as never);
    const panel = new SettingsPanel({ fsPath: "/ext" } as never, "/workspace") as any;
    panel.handleChange("project.number", 8);
    const load = vi.spyOn(panel, "loadAllTiers");

    await panel.handleExternalConfigChange();

    expect(load).toHaveBeenCalledOnce();
    expect(panel.hasUnsavedChanges).toBe(false);
  });
});
