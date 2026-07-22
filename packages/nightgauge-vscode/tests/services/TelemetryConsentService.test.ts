/**
 * Tests for TelemetryConsentService (#3327).
 *
 * Covers:
 * - Default state and read accessors
 * - VSCode-config-as-source-of-truth via getConfiguration()/update()
 * - inspect()-based "explicitly set" detection
 * - First-run modal: order of buttons (Decline first), branch outcomes,
 *   "Decide later" 7-day reschedule, Esc-dismiss equivalence
 * - Per-stream gating (master off short-circuits)
 * - lastUploadAt round-trip via globalState
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import * as vscode from "vscode";

// ─── Mock vscode ───────────────────────────────────────────────────────────

interface ConfigStore {
  enabled?: boolean;
  streams?: unknown;
  uploadIntervalMinutes?: number;
  // inspect() values
  globalEnabled?: boolean;
  workspaceEnabled?: boolean;
}

const configStore: ConfigStore = {};

vi.mock("vscode", () => {
  const ConfigurationTarget = { Global: 1, Workspace: 2, WorkspaceFolder: 3 };
  return {
    ConfigurationTarget,
    env: { isTelemetryEnabled: true },
    workspace: {
      workspaceFolders: [{ uri: { fsPath: "/workspace" } }] as unknown[],
      getConfiguration: vi.fn((_namespace: string) => ({
        get: vi.fn((key: string) => {
          if (key === "telemetry.enabled") return configStore.enabled;
          if (key === "telemetry.streams") return configStore.streams;
          if (key === "telemetry.uploadIntervalMinutes") return configStore.uploadIntervalMinutes;
          return undefined;
        }),
        inspect: vi.fn((key: string) => {
          if (key === "telemetry.enabled") {
            return {
              key,
              defaultValue: false,
              globalValue: configStore.globalEnabled,
              workspaceValue: configStore.workspaceEnabled,
              workspaceFolderValue: undefined,
            };
          }
          return undefined;
        }),
        update: vi.fn(async (key: string, value: unknown) => {
          if (key === "telemetry.enabled") {
            configStore.enabled = value as boolean;
            configStore.globalEnabled = value as boolean;
          }
          if (key === "telemetry.streams") configStore.streams = value;
          if (key === "telemetry.uploadIntervalMinutes")
            configStore.uploadIntervalMinutes = value as number;
        }),
      })),
      onDidChangeConfiguration: vi.fn(() => ({ dispose: vi.fn() })),
    },
    window: {
      showInformationMessage: vi.fn(),
      showWarningMessage: vi.fn(),
      createWebviewPanel: vi.fn(),
      createOutputChannel: vi.fn(() => ({
        appendLine: vi.fn(),
        show: vi.fn(),
        clear: vi.fn(),
        dispose: vi.fn(),
      })),
    },
    commands: {
      executeCommand: vi.fn(),
      registerCommand: vi.fn(),
    },
    Uri: {
      file: vi.fn((p: string) => ({ fsPath: p })),
      joinPath: vi.fn((base: { fsPath?: string }, ...parts: string[]) => ({
        fsPath: `${base.fsPath ?? ""}/${parts.join("/")}`,
      })),
    },
    ViewColumn: { One: 1 },
    Disposable: { from: vi.fn(() => ({ dispose: vi.fn() })) },
    EventEmitter: vi.fn(function () {
      return { event: vi.fn(), fire: vi.fn(), dispose: vi.fn() };
    }),
  };
});

import { TelemetryConsentService } from "../../src/services/TelemetryConsentService";

// ─── Test helpers ──────────────────────────────────────────────────────────

function makeContext(): {
  ctx: vscode.ExtensionContext;
  workspaceStore: Map<string, unknown>;
  globalStore: Map<string, unknown>;
} {
  const workspaceStore = new Map<string, unknown>();
  const globalStore = new Map<string, unknown>();
  const ctx = {
    workspaceState: {
      get: vi.fn((key: string, def?: unknown) => workspaceStore.get(key) ?? def),
      update: vi.fn(async (key: string, value: unknown) => {
        if (value === undefined) {
          workspaceStore.delete(key);
        } else {
          workspaceStore.set(key, value);
        }
      }),
    },
    globalState: {
      get: vi.fn((key: string, def?: unknown) => globalStore.get(key) ?? def),
      update: vi.fn(async (key: string, value: unknown) => {
        if (value === undefined) {
          globalStore.delete(key);
        } else {
          globalStore.set(key, value);
        }
      }),
    },
    extensionUri: { fsPath: "/ext" } as unknown as vscode.Uri,
    subscriptions: [],
  } as unknown as vscode.ExtensionContext;
  return { ctx, workspaceStore, globalStore };
}

function makeLogger() {
  return { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn() };
}

function resetConfig() {
  configStore.enabled = undefined;
  configStore.streams = undefined;
  configStore.uploadIntervalMinutes = undefined;
  configStore.globalEnabled = undefined;
  configStore.workspaceEnabled = undefined;
}

// ─── Tests ─────────────────────────────────────────────────────────────────

describe("TelemetryConsentService — read accessors", () => {
  beforeEach(() => {
    resetConfig();
    vi.mocked(vscode.env).isTelemetryEnabled = true;
  });

  it("isEnabled() returns false by default", () => {
    const { ctx } = makeContext();
    const svc = new TelemetryConsentService(ctx, makeLogger() as any);
    expect(svc.isEnabled()).toBe(false);
  });

  it("isEnabled() returns true after setEnabled(true)", async () => {
    const { ctx } = makeContext();
    const svc = new TelemetryConsentService(ctx, makeLogger() as any);
    await svc.setEnabled(true);
    expect(svc.isEnabled()).toBe(true);
  });

  it("isEnabled() returns false when VSCode global telemetry is off, even with consent", async () => {
    const { ctx } = makeContext();
    const svc = new TelemetryConsentService(ctx, makeLogger() as any);
    await svc.setEnabled(true);
    vi.mocked(vscode.env).isTelemetryEnabled = false;
    expect(svc.isEnabled()).toBe(false);
  });

  it("getStreams() defaults to every stream", () => {
    const { ctx } = makeContext();
    const svc = new TelemetryConsentService(ctx, makeLogger() as any);
    expect(svc.getStreams()).toEqual(["pipeline-run", "health", "recommendation", "trace"]);
  });

  it("getStreams() filters out invalid values", async () => {
    const { ctx } = makeContext();
    const svc = new TelemetryConsentService(ctx, makeLogger() as any);
    await svc.setStreams(["pipeline-run", "garbage" as any, "health"]);
    expect(svc.getStreams()).toEqual(["pipeline-run", "health"]);
  });

  it("getUploadIntervalMinutes() defaults to 15", () => {
    const { ctx } = makeContext();
    const svc = new TelemetryConsentService(ctx, makeLogger() as any);
    expect(svc.getUploadIntervalMinutes()).toBe(15);
  });

  it("setUploadIntervalMinutes() clamps to [1, 1440]", async () => {
    const { ctx } = makeContext();
    const svc = new TelemetryConsentService(ctx, makeLogger() as any);
    await svc.setUploadIntervalMinutes(0);
    expect(svc.getUploadIntervalMinutes()).toBe(1);
    await svc.setUploadIntervalMinutes(99999);
    expect(svc.getUploadIntervalMinutes()).toBe(1440);
    await svc.setUploadIntervalMinutes(30);
    expect(svc.getUploadIntervalMinutes()).toBe(30);
  });

  it("isStreamEnabled() returns false when master is off, even if stream is in array", async () => {
    const { ctx } = makeContext();
    const svc = new TelemetryConsentService(ctx, makeLogger() as any);
    await svc.setStreams(["pipeline-run"]);
    expect(svc.isStreamEnabled("pipeline-run")).toBe(false);
    await svc.setEnabled(true);
    expect(svc.isStreamEnabled("pipeline-run")).toBe(true);
    expect(svc.isStreamEnabled("health")).toBe(false);
  });

  it("getLastUploadAt() returns null until recordUploadAt() is called", async () => {
    const { ctx } = makeContext();
    const svc = new TelemetryConsentService(ctx, makeLogger() as any);
    expect(svc.getLastUploadAt()).toBeNull();
    await svc.recordUploadAt(1700000000000);
    expect(svc.getLastUploadAt()).toBe(1700000000000);
  });
});

describe("TelemetryConsentService.maybeShowFirstRunPrompt", () => {
  beforeEach(() => {
    resetConfig();
    vi.mocked(vscode.env).isTelemetryEnabled = true;
    vi.mocked(vscode.window.showInformationMessage).mockReset();
  });

  it("does not show modal when VSCode global telemetry is off", async () => {
    vi.mocked(vscode.env).isTelemetryEnabled = false;
    const { ctx } = makeContext();
    const svc = new TelemetryConsentService(ctx, makeLogger() as any);
    await svc.maybeShowFirstRunPrompt();
    expect(vscode.window.showInformationMessage).not.toHaveBeenCalled();
  });

  it("does not show modal when consent is already explicitly set globally", async () => {
    configStore.globalEnabled = true;
    const { ctx, globalStore } = makeContext();
    const svc = new TelemetryConsentService(ctx, makeLogger() as any);
    await svc.maybeShowFirstRunPrompt();
    expect(vscode.window.showInformationMessage).not.toHaveBeenCalled();
    expect(globalStore.get("nightgauge.telemetry.firstRunPromptSeen")).toBe(true);
  });

  it("shows modal once and passes Decline as the first action (default focus)", async () => {
    vi.mocked(vscode.window.showInformationMessage).mockResolvedValue(undefined as any);
    const { ctx } = makeContext();
    const svc = new TelemetryConsentService(ctx, makeLogger() as any);
    await svc.maybeShowFirstRunPrompt();
    expect(vscode.window.showInformationMessage).toHaveBeenCalledTimes(1);
    const args = vi.mocked(vscode.window.showInformationMessage).mock.calls[0];
    // [message, options, ...actions]
    expect(args[0]).toMatch(/anonymous usage data/i);
    expect(args[1]).toMatchObject({ modal: true });
    expect(args.slice(2)).toEqual(["Decline", "Decide later", "Enable"]);
  });

  it("Enable → setEnabled(true)", async () => {
    vi.mocked(vscode.window.showInformationMessage).mockResolvedValueOnce("Enable" as any);
    const { ctx } = makeContext();
    const svc = new TelemetryConsentService(ctx, makeLogger() as any);
    await svc.maybeShowFirstRunPrompt();
    expect(svc.isEnabled()).toBe(true);
  });

  it("Decline → setEnabled(false)", async () => {
    vi.mocked(vscode.window.showInformationMessage).mockResolvedValueOnce("Decline" as any);
    const { ctx } = makeContext();
    const svc = new TelemetryConsentService(ctx, makeLogger() as any);
    await svc.maybeShowFirstRunPrompt();
    expect(svc.isEnabled()).toBe(false);
    expect(configStore.enabled).toBe(false);
  });

  it("Decide later → schedules nextPromptAt 7 days out and does NOT change enabled", async () => {
    vi.mocked(vscode.window.showInformationMessage).mockResolvedValueOnce("Decide later" as any);
    const { ctx, globalStore } = makeContext();
    const before = Date.now();
    const svc = new TelemetryConsentService(ctx, makeLogger() as any);
    await svc.maybeShowFirstRunPrompt();
    const next = globalStore.get("nightgauge.telemetry.nextPromptAtMs") as number | undefined;
    expect(typeof next).toBe("number");
    expect(next!).toBeGreaterThanOrEqual(before + 7 * 24 * 60 * 60 * 1000 - 1000);
    expect(configStore.enabled).toBeUndefined();
  });

  it("Esc-dismissed modal behaves like Decide later (schedules re-prompt)", async () => {
    vi.mocked(vscode.window.showInformationMessage).mockResolvedValueOnce(undefined as any);
    const { ctx, globalStore } = makeContext();
    const svc = new TelemetryConsentService(ctx, makeLogger() as any);
    await svc.maybeShowFirstRunPrompt();
    expect(globalStore.has("nightgauge.telemetry.nextPromptAtMs")).toBe(true);
    expect(configStore.enabled).toBeUndefined();
  });

  it("does not double-show when called twice in the same session", async () => {
    vi.mocked(vscode.window.showInformationMessage).mockResolvedValue("Decline" as any);
    const { ctx } = makeContext();
    const svc = new TelemetryConsentService(ctx, makeLogger() as any);
    await svc.maybeShowFirstRunPrompt();
    await svc.maybeShowFirstRunPrompt();
    expect(vscode.window.showInformationMessage).toHaveBeenCalledTimes(1);
  });

  it("re-shows after nextPromptAtMs has elapsed and consent is still unset", async () => {
    vi.mocked(vscode.window.showInformationMessage).mockResolvedValue("Decline" as any);
    const { ctx, globalStore } = makeContext();
    globalStore.set("nightgauge.telemetry.firstRunPromptSeen", true);
    globalStore.set("nightgauge.telemetry.nextPromptAtMs", Date.now() - 1000);
    const svc = new TelemetryConsentService(ctx, makeLogger() as any);
    await svc.maybeShowFirstRunPrompt();
    expect(vscode.window.showInformationMessage).toHaveBeenCalledTimes(1);
  });

  it("does not re-show when promptSeen=true and no reschedule was set", async () => {
    const { ctx, globalStore } = makeContext();
    globalStore.set("nightgauge.telemetry.firstRunPromptSeen", true);
    const svc = new TelemetryConsentService(ctx, makeLogger() as any);
    await svc.maybeShowFirstRunPrompt();
    expect(vscode.window.showInformationMessage).not.toHaveBeenCalled();
  });

  it("collapses concurrent invocations to a single prompt", async () => {
    let resolvePrompt: (v: string | undefined) => void = () => {};
    const promptPending = new Promise<string | undefined>((r) => {
      resolvePrompt = r;
    });
    vi.mocked(vscode.window.showInformationMessage).mockImplementation(() => promptPending as any);
    const { ctx } = makeContext();
    const svc = new TelemetryConsentService(ctx, makeLogger() as any);
    const a = svc.maybeShowFirstRunPrompt();
    const b = svc.maybeShowFirstRunPrompt();
    // Allow the first invocation to reach the modal call (microtask flush).
    await Promise.resolve();
    await Promise.resolve();
    resolvePrompt("Decline");
    await Promise.all([a, b]);
    expect(vscode.window.showInformationMessage).toHaveBeenCalledTimes(1);
  });
});
