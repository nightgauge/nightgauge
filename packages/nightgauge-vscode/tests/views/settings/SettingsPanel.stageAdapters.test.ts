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

const { writeMock, writeLocalMock, validateAdapterAuthMock } = vi.hoisted(() => ({
  writeMock: vi.fn().mockResolvedValue({ success: true }),
  writeLocalMock: vi.fn().mockResolvedValue({ success: true }),
  validateAdapterAuthMock: vi.fn(),
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
    write = writeMock;
    writeLocal = writeLocalMock;
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

vi.mock("@nightgauge/sdk", () => ({
  validateAdapterAuth: validateAdapterAuthMock,
}));

vi.mock("../../../src/utils/resolvers/adapterResolver", () => ({
  resolveStageAdapter: vi.fn((stage: string) => ({
    adapter: stage === "feature-dev" ? "codex" : "claude",
    source: stage === "feature-dev" ? "stage-config" : "default",
  })),
}));

vi.mock("../../../src/utils/modeProfiles", () => ({
  getModeStageAdapterModel: vi.fn(() => undefined),
}));

vi.mock("../../../src/utils/resolvers/monitoringResolver", () => ({
  getPerformanceMode: vi.fn(() => "elevated"),
}));

vi.mock("../../../src/services/HeadlessOrchestrator", () => ({
  toIncrediAdapter: vi.fn((adapter: string) =>
    adapter === "claude" ? "claude-headless" : adapter
  ),
}));

import { SettingsPanel } from "../../../src/views/settings/SettingsPanel";

function makePanel(): any {
  const panel = new SettingsPanel({ fsPath: "/ext" } as never, "/workspace") as any;
  // Inject a fake panel.webview so postMessage can be observed.
  panel.panel = { webview: { postMessage: vi.fn() } };
  panel.tierState = {
    currentTier: "project",
    defaultEditTier: "project",
    hasGlobalConfig: false,
    hasLocalConfig: false,
    hasProjectConfig: true,
    activeEnvVars: [],
  };
  // Stub updatePanel — re-rendering HTML requires mergeWithDefaults from the
  // real config module, but our test mocks the YAML service. Bypass.
  panel.updatePanel = vi.fn();
  return panel;
}

describe("SettingsPanel per-stage adapter handling (Issue #3225)", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    writeMock.mockResolvedValue({ success: true });
    writeLocalMock.mockResolvedValue({ success: true });
  });

  it("persists pipeline.stage_adapters.<stage> on change", () => {
    const panel = makePanel();

    panel.handleChange("pipeline.stage_adapters.feature-dev", "codex");

    expect(panel.projectConfig.pipeline.stage_adapters["feature-dev"]).toBe("codex");
    expect(panel.currentConfig.pipeline.stage_adapters["feature-dev"]).toBe("codex");
  });

  it("deletes the stage_adapters leaf when the user picks the empty (use-global) option", () => {
    const panel = makePanel();
    panel.projectConfig.pipeline = {
      stage_adapters: { "feature-dev": "codex", "pr-create": "gemini" },
    };
    panel.currentConfig.pipeline = {
      stage_adapters: { "feature-dev": "codex", "pr-create": "gemini" },
    };

    panel.handleChange("pipeline.stage_adapters.feature-dev", "");

    expect(panel.projectConfig.pipeline.stage_adapters["feature-dev"]).toBeUndefined();
    expect(panel.projectConfig.pipeline.stage_adapters["pr-create"]).toBe("gemini");
    expect(panel.currentConfig.pipeline.stage_adapters["feature-dev"]).toBeUndefined();
  });

  it("posts stage-adapter-auth-result with status='ok' when validateAdapterAuth resolves ok", async () => {
    const panel = makePanel();
    validateAdapterAuthMock.mockResolvedValueOnce({ ok: true });

    await panel.handleAction("validate-stage-adapter", { stage: "feature-dev", adapter: "codex" });

    expect(validateAdapterAuthMock).toHaveBeenCalledWith("codex");
    expect(panel.panel.webview.postMessage).toHaveBeenCalledWith(
      expect.objectContaining({
        type: "stage-adapter-auth-result",
        stage: "feature-dev",
        adapter: "codex",
        status: "ok",
      })
    );
  });

  it("posts stage-adapter-auth-result with status='warn' when adapter reports AUTH_MISSING", async () => {
    const panel = makePanel();
    validateAdapterAuthMock.mockResolvedValueOnce({
      ok: false,
      reason: "no codex login",
      category: "AUTH_MISSING",
    });

    await panel.handleAction("validate-stage-adapter", { stage: "feature-dev", adapter: "codex" });

    expect(panel.panel.webview.postMessage).toHaveBeenCalledWith(
      expect.objectContaining({
        type: "stage-adapter-auth-result",
        stage: "feature-dev",
        adapter: "codex",
        status: "warn",
        reason: "no codex login",
      })
    );
  });

  it("posts stage-adapter-auth-result with status='error' when adapter reports a non-AUTH_MISSING failure", async () => {
    const panel = makePanel();
    validateAdapterAuthMock.mockResolvedValueOnce({
      ok: false,
      reason: "config invalid",
      category: "CONFIG_INVALID",
    });

    await panel.handleAction("validate-stage-adapter", { stage: "feature-dev", adapter: "codex" });

    expect(panel.panel.webview.postMessage).toHaveBeenCalledWith(
      expect.objectContaining({
        type: "stage-adapter-auth-result",
        stage: "feature-dev",
        status: "error",
        reason: "config invalid",
      })
    );
  });

  it("ignores validate-stage-adapter without stage or adapter payload", async () => {
    const panel = makePanel();

    await panel.handleAction("validate-stage-adapter", { stage: "feature-dev" });
    await panel.handleAction("validate-stage-adapter", { adapter: "codex" });

    expect(validateAdapterAuthMock).not.toHaveBeenCalled();
    expect(panel.panel.webview.postMessage).not.toHaveBeenCalled();
  });

  it("preview-stage-resolution recomputes the panel HTML", async () => {
    const panel = makePanel();
    const updatePanelSpy = vi.spyOn(panel, "updatePanel").mockImplementation(() => {});

    await panel.handleAction("preview-stage-resolution");

    expect(updatePanelSpy).toHaveBeenCalled();
  });

  it("reset-setting on a stage_adapters path removes the override and saves", async () => {
    const panel = makePanel();
    panel.projectConfig.pipeline = {
      stage_adapters: { "feature-dev": "codex", "pr-create": "gemini" },
    };
    panel.localConfig = {};

    await panel.handleResetSetting("pipeline.stage_adapters.feature-dev", "default");

    expect(writeMock).toHaveBeenCalled();
    const writtenConfig = writeMock.mock.calls[0][0];
    expect(writtenConfig.pipeline.stage_adapters["feature-dev"]).toBeUndefined();
    expect(writtenConfig.pipeline.stage_adapters["pr-create"]).toBe("gemini");
  });

  it("save persists stage_adapters into the project config write path", async () => {
    const panel = makePanel();
    panel.handleChange("pipeline.stage_adapters.feature-dev", "codex");
    panel.handleChange("pipeline.stage_adapters.pr-create", "gemini");

    await panel.handleSave("project");

    expect(writeMock).toHaveBeenCalledTimes(1);
    const writtenConfig = writeMock.mock.calls[0][0];
    expect(writtenConfig.pipeline.stage_adapters["feature-dev"]).toBe("codex");
    expect(writtenConfig.pipeline.stage_adapters["pr-create"]).toBe("gemini");
  });

  it("save targeting local routes through writeLocal with stage_adapters intact", async () => {
    const panel = makePanel();
    panel.handleChange("pipeline.stage_adapters.feature-dev", "copilot", "local");

    await panel.handleSave("local");

    expect(writeLocalMock).toHaveBeenCalledTimes(1);
    const writtenConfig = writeLocalMock.mock.calls[0][0];
    expect(writtenConfig.pipeline.stage_adapters["feature-dev"]).toBe("copilot");
  });
});
