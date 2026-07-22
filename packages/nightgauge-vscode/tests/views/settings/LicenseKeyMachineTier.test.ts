/**
 * LicenseKeyMachineTier.test.ts
 *
 * Regression tests for #3997:
 *  1. The typed license key persists to the machine tier
 *     (~/.nightgauge/config.yaml via writeGlobal) and is mirrored to
 *     SecretStorage — it is no longer silently dropped from every YAML write.
 *  2. The Global tier tab is editable, so a save targeting "global" writes to
 *     the machine YAML and machine-tier keys are not partitioned away.
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
vi.mock("../../../src/views/settings/IncrediYamlService", async () => {
  const actual = await vi.importActual<typeof import("../../../src/views/settings/configUtils")>(
    "../../../src/views/settings/configUtils"
  );
  return {
    IncrediYamlService: class IncrediYamlServiceMock {
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

vi.mock("../../../src/services/LmStudioService", () => ({
  LmStudioService: class {
    listModels = vi.fn().mockResolvedValue([]);
    startServer = vi.fn().mockResolvedValue(undefined);
    loadModel = vi.fn().mockResolvedValue(undefined);
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

describe("#3997 — license key machine-tier persistence", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockReadProject.mockResolvedValue({ config: {} });
    mockReadLocal.mockResolvedValue({ config: {} });
    mockReadGlobal.mockResolvedValue({ config: {} });
  });

  it("declares platform.license_key as a machine-tier key", () => {
    expect(MACHINE_TIER_KEY_PATHS.has("platform.license_key")).toBe(true);
  });

  it("exposes the Global tier as editable", () => {
    const globalTab = TIER_TABS.find((t) => t.id === "global");
    expect(globalTab?.editable).toBe(true);
  });

  it("persists a typed license key to the machine YAML (writeGlobal) from project view", async () => {
    const panel = newPanel();
    panel.tierState = { currentTier: "merged", defaultEditTier: "project" };

    // User types a new key — flows into the project working config.
    panel.handleChange("platform.license_key", "ib_live_NEWKEY");
    await panel.handleSave();

    // The new value must reach the machine YAML, not be dropped.
    expect(mockWriteGlobal).toHaveBeenCalledTimes(1);
    const machineArg = mockWriteGlobal.mock.calls[0][0] as {
      platform?: { license_key?: string };
    };
    expect(machineArg.platform?.license_key).toBe("ib_live_NEWKEY");

    // It is also mirrored to SecretStorage so runtime readers stay in sync.
    expect(mockSetSecret).toHaveBeenCalledWith("nightgauge.platform.licenseKey", "ib_live_NEWKEY");

    // It must NOT be written to the project (committed) config.
    const projectArg = mockWrite.mock.calls[0]?.[0] as {
      platform?: { license_key?: string };
    };
    expect(projectArg?.platform?.license_key).toBeUndefined();
  });

  it("writes to the machine YAML when saving on the Global tab", async () => {
    const panel = newPanel();
    panel.tierState = { currentTier: "global", defaultEditTier: "project" };

    panel.handleChange("platform.license_key", "ib_live_GLOBALEDIT");
    await panel.handleSave();

    // Global save goes straight to writeGlobal with the typed value present.
    expect(mockWriteGlobal).toHaveBeenCalled();
    const arg = mockWriteGlobal.mock.calls[0][0] as {
      platform?: { license_key?: string };
    };
    expect(arg.platform?.license_key).toBe("ib_live_GLOBALEDIT");

    // The project/local writers are not used for a global save.
    expect(mockWrite).not.toHaveBeenCalled();
    expect(mockWriteLocal).not.toHaveBeenCalled();
  });

  it("clears the keychain entry when the license key is emptied", async () => {
    const panel = newPanel();
    panel.tierState = { currentTier: "merged", defaultEditTier: "project" };

    panel.handleChange("platform.license_key", "");
    await panel.handleSave();

    expect(mockDeleteSecret).toHaveBeenCalledWith("nightgauge.platform.licenseKey");
    expect(mockSetSecret).not.toHaveBeenCalled();
  });

  it("displays the license key from the machine YAML on load", async () => {
    mockReadGlobal.mockResolvedValue({
      config: { platform: { license_key: "ib_live_FROMYAML" } },
    });
    const panel = newPanel();
    await panel.loadAllTiers();

    expect(panel.globalConfig.platform).toMatchObject({ license_key: "ib_live_FROMYAML" });
  });

  it("falls back to SecretStorage for display when the machine YAML has no key", async () => {
    mockReadGlobal.mockResolvedValue({ config: {} });
    mockGetSecret.mockResolvedValue("ib_live_FROMSECRET");
    const panel = newPanel();
    await panel.loadAllTiers();

    expect(panel.globalConfig.platform).toMatchObject({ license_key: "ib_live_FROMSECRET" });
  });
});
