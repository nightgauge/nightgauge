/**
 * Tests for TelemetryConsentService (#3327, revised by #738).
 *
 * Covers:
 * - Opt-out default state and read accessors
 * - VSCode-config-as-source-of-truth via getConfiguration()/update()
 * - inspect()-based "explicitly set" detection
 * - First-run **disclosure notice**: button order (Turn off first), branch
 *   outcomes, Esc-dismiss leaving the default standing
 * - The old prompt-seen flag not suppressing the new notice
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
              defaultValue: true,
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

  // Telemetry is opt-out (#738). `configStore.enabled` is undefined here,
  // standing in for a fresh install where nothing has been configured.
  it("isEnabled() returns true by default", () => {
    const { ctx } = makeContext();
    const svc = new TelemetryConsentService(ctx, makeLogger() as any);
    expect(svc.isEnabled()).toBe(true);
  });

  // The guarantee the flip must not break.
  it("isEnabled() returns false after an explicit setEnabled(false)", async () => {
    const { ctx } = makeContext();
    const svc = new TelemetryConsentService(ctx, makeLogger() as any);
    await svc.setEnabled(false);
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

  it("isStreamEnabled() honours the stream list while the master is on", async () => {
    const { ctx } = makeContext();
    const svc = new TelemetryConsentService(ctx, makeLogger() as any);
    await svc.setStreams(["pipeline-run"]);
    expect(svc.isStreamEnabled("pipeline-run")).toBe(true);
    expect(svc.isStreamEnabled("health")).toBe(false);
  });

  it("isStreamEnabled() returns false when master is off, even if stream is in array", async () => {
    const { ctx } = makeContext();
    const svc = new TelemetryConsentService(ctx, makeLogger() as any);
    await svc.setStreams(["pipeline-run"]);
    await svc.setEnabled(false);
    expect(svc.isStreamEnabled("pipeline-run")).toBe(false);
  });

  it("getLastUploadAt() returns null until recordUploadAt() is called", async () => {
    const { ctx } = makeContext();
    const svc = new TelemetryConsentService(ctx, makeLogger() as any);
    expect(svc.getLastUploadAt()).toBeNull();
    await svc.recordUploadAt(1700000000000);
    expect(svc.getLastUploadAt()).toBe(1700000000000);
  });
});

describe("TelemetryConsentService.maybeShowFirstRunPrompt — disclosure notice (#738)", () => {
  const NOTICE_KEY = "nightgauge.telemetry.optOutNoticeSeen";

  /**
   * Count only modal notices. `showInformationMessage` also carries the
   * non-modal confirmation toast after "Turn off", and counting raw calls would
   * make a working confirmation look like a duplicate notice.
   */
  function modalCallCount(): number {
    return vi
      .mocked(vscode.window.showInformationMessage)
      .mock.calls.filter((c) => (c[1] as { modal?: boolean } | undefined)?.modal === true).length;
  }

  beforeEach(() => {
    resetConfig();
    vi.mocked(vscode.env).isTelemetryEnabled = true;
    vi.mocked(vscode.window.showInformationMessage).mockReset();
  });

  it("does not show the notice when VSCode global telemetry is off", async () => {
    vi.mocked(vscode.env).isTelemetryEnabled = false;
    const { ctx } = makeContext();
    const svc = new TelemetryConsentService(ctx, makeLogger() as any);
    await svc.maybeShowFirstRunPrompt();
    expect(vscode.window.showInformationMessage).not.toHaveBeenCalled();
  });

  it("does not show the notice when consent is already explicitly set", async () => {
    configStore.globalEnabled = true;
    const { ctx, globalStore } = makeContext();
    const svc = new TelemetryConsentService(ctx, makeLogger() as any);
    await svc.maybeShowFirstRunPrompt();
    expect(vscode.window.showInformationMessage).not.toHaveBeenCalled();
    expect(globalStore.get(NOTICE_KEY)).toBe(true);
  });

  // The notice states a fact and offers the off switch. It must not read as a
  // request — "Turn off" / "Keep on", never "Enable".
  it("states rather than asks, and puts Turn off in default focus", async () => {
    vi.mocked(vscode.window.showInformationMessage).mockResolvedValue(undefined as any);
    const { ctx } = makeContext();
    const svc = new TelemetryConsentService(ctx, makeLogger() as any);
    await svc.maybeShowFirstRunPrompt();
    expect(vscode.window.showInformationMessage).toHaveBeenCalledTimes(1);
    const args = vi.mocked(vscode.window.showInformationMessage).mock.calls[0];
    expect(args[0]).toMatch(/anonymous usage data/i);
    expect(args[0]).not.toMatch(/\?$/); // a statement, not a question
    expect(args[1]).toMatchObject({ modal: true });
    expect(args.slice(2)).toEqual(["Turn off", "Keep on"]);
    expect(String((args[1] as { detail: string }).detail)).toMatch(/on by default/i);
  });

  it("Turn off → setEnabled(false)", async () => {
    vi.mocked(vscode.window.showInformationMessage).mockResolvedValueOnce("Turn off" as any);
    const { ctx } = makeContext();
    const svc = new TelemetryConsentService(ctx, makeLogger() as any);
    await svc.maybeShowFirstRunPrompt();
    expect(configStore.enabled).toBe(false);
    expect(svc.isEnabled()).toBe(false);
  });

  // Recording the choice explicitly keeps this operator out of any future
  // disclosure aimed at people who never decided.
  it("Keep on → writes an explicit true rather than leaning on the default", async () => {
    vi.mocked(vscode.window.showInformationMessage).mockResolvedValueOnce("Keep on" as any);
    const { ctx } = makeContext();
    const svc = new TelemetryConsentService(ctx, makeLogger() as any);
    await svc.maybeShowFirstRunPrompt();
    expect(configStore.enabled).toBe(true);
  });

  // There is no question outstanding, so dismissal defers nothing: the notice
  // was displayed, the default stands, and nothing is written.
  it("Esc-dismissed leaves the default standing and writes nothing", async () => {
    vi.mocked(vscode.window.showInformationMessage).mockResolvedValueOnce(undefined as any);
    const { ctx, globalStore } = makeContext();
    const svc = new TelemetryConsentService(ctx, makeLogger() as any);
    await svc.maybeShowFirstRunPrompt();
    expect(configStore.enabled).toBeUndefined();
    expect(svc.isEnabled()).toBe(true);
    expect(globalStore.get(NOTICE_KEY)).toBe(true);
  });

  it("does not double-show when called twice in the same session", async () => {
    vi.mocked(vscode.window.showInformationMessage).mockResolvedValue("Turn off" as any);
    const { ctx } = makeContext();
    const svc = new TelemetryConsentService(ctx, makeLogger() as any);
    await svc.maybeShowFirstRunPrompt();
    await svc.maybeShowFirstRunPrompt();
    expect(modalCallCount()).toBe(1);
  });

  it("does not re-show once the notice has been seen on this machine", async () => {
    const { ctx, globalStore } = makeContext();
    globalStore.set(NOTICE_KEY, true);
    const svc = new TelemetryConsentService(ctx, makeLogger() as any);
    await svc.maybeShowFirstRunPrompt();
    expect(vscode.window.showInformationMessage).not.toHaveBeenCalled();
  });

  // The whole reason the notice uses a new globalState key. Someone who saw the
  // old permission dialog and never answered is exactly the population the
  // default just moved, and is the last group that should be switched silently.
  it("still shows to an operator who saw the OLD prompt but never answered", async () => {
    vi.mocked(vscode.window.showInformationMessage).mockResolvedValue(undefined as any);
    const { ctx, globalStore } = makeContext();
    globalStore.set("nightgauge.telemetry.firstRunPromptSeen", true);
    globalStore.set("nightgauge.telemetry.nextPromptAtMs", Date.now() + 86_400_000);
    const svc = new TelemetryConsentService(ctx, makeLogger() as any);
    await svc.maybeShowFirstRunPrompt();
    expect(vscode.window.showInformationMessage).toHaveBeenCalledTimes(1);
  });

  // An operator who actively declined under the old prompt has `false` written
  // in config. They must never see the notice, and never be re-enabled.
  it("never re-enables someone who declined under the old prompt", async () => {
    configStore.enabled = false;
    configStore.globalEnabled = false;
    const { ctx } = makeContext();
    const svc = new TelemetryConsentService(ctx, makeLogger() as any);
    await svc.maybeShowFirstRunPrompt();
    expect(vscode.window.showInformationMessage).not.toHaveBeenCalled();
    expect(svc.isEnabled()).toBe(false);
  });

  // Marked seen before the modal is awaited, so a window closed mid-notice does
  // not re-show it on every subsequent activation.
  it("records the notice as seen before awaiting the operator's answer", async () => {
    const { ctx, globalStore } = makeContext();
    let seenDuringModal: unknown;
    vi.mocked(vscode.window.showInformationMessage).mockImplementation((() => {
      seenDuringModal = globalStore.get(NOTICE_KEY);
      return Promise.resolve(undefined);
    }) as any);
    const svc = new TelemetryConsentService(ctx, makeLogger() as any);
    await svc.maybeShowFirstRunPrompt();
    expect(seenDuringModal).toBe(true);
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
    resolvePrompt("Turn off");
    await Promise.all([a, b]);
    expect(modalCallCount()).toBe(1);
  });
});
