/**
 * Tests for switchAdapter command — OpenCode option
 *
 * OpenCode is gated behind NIGHTGAUGE_EXPERIMENTAL_OPENCODE=1 (ADR-022): with
 * the switch off, selecting OpenCode must show the enable-switch message and
 * must NOT write `opencode` to config; with it on, it writes normally.
 *
 * @see Issue #1628 - vscode: OpenCode in adapter picker, settings, doctor
 *   view and model picker
 * @see docs/decisions/022-opencode-multi-provider-adapter.md
 * @see packages/nightgauge-vscode/src/commands/switchAdapter.ts
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import type { QuickPickItem } from "vscode";

let quickPickCalls: Array<{ items: QuickPickItem[]; options: unknown }> = [];
let quickPickResponses: Array<QuickPickItem | undefined> = [];
let quickPickCallIndex = 0;

vi.mock("vscode", () => ({
  workspace: {
    workspaceFolders: [{ uri: { fsPath: "/mock/workspace" } }],
  },
  window: {
    showQuickPick: vi.fn((items: QuickPickItem[], options: unknown) => {
      quickPickCalls.push({ items: items as QuickPickItem[], options });
      const response = quickPickResponses[quickPickCallIndex];
      quickPickCallIndex++;
      return Promise.resolve(response);
    }),
    showErrorMessage: vi.fn(),
    showInformationMessage: vi.fn(),
    showWarningMessage: vi.fn(),
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
  },
  commands: {
    registerCommand: vi.fn((_id: string, callback: (...args: unknown[]) => unknown) => {
      return {
        dispose: vi.fn(),
        _callback: callback,
      };
    }),
  },
}));

const { writeLocalMock } = vi.hoisted(() => ({
  writeLocalMock: vi.fn(() => Promise.resolve({ success: true })),
}));

vi.mock("../../src/utils/nightgaugeConfig", () => ({
  getExecutionAdapter: vi.fn(() => "claude"),
}));

vi.mock("../../src/views/settings/NightgaugeYamlService", () => ({
  NightgaugeYamlService: vi.fn(function () {
    return {
      read: vi.fn(() => Promise.resolve({ success: true, config: {} })),
      readLocal: vi.fn(() => Promise.resolve({ success: true, config: {} })),
      write: vi.fn(() => Promise.resolve({ success: true })),
      writeLocal: writeLocalMock,
      dispose: vi.fn(),
    };
  }),
}));

vi.mock("../../src/services/ConfigBridge", () => ({
  ConfigBridge: {
    getInstance: vi.fn(() => ({
      reload: vi.fn(() => Promise.resolve()),
    })),
  },
}));

import { registerSwitchAdapterCommand } from "../../src/commands/switchAdapter";

const OPENCODE_ADAPTER_ITEM = {
  label: "OpenCode",
  value: "opencode",
} as unknown as QuickPickItem;

describe("switchAdapter command — OpenCode option (Issue #1628)", () => {
  const mockLogger = {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  };

  beforeEach(() => {
    vi.clearAllMocks();
    quickPickCalls = [];
    quickPickResponses = [];
    quickPickCallIndex = 0;
    delete process.env.NIGHTGAUGE_EXPERIMENTAL_OPENCODE;
  });

  async function invokeCommand(): Promise<void> {
    const vscode = await import("vscode");
    const registerCall = vi.mocked(vscode.commands.registerCommand).mock.calls[0];
    const callback = registerCall[1] as () => Promise<void>;
    await callback();
  }

  it("includes OpenCode, labeled experimental, in the adapter QuickPick items", async () => {
    quickPickResponses = [undefined];

    const disposable = registerSwitchAdapterCommand(
      mockLogger as unknown as Parameters<typeof registerSwitchAdapterCommand>[0]
    );

    await invokeCommand();

    const adapterItems = quickPickCalls[0].items;
    const openCodeItem = adapterItems.find(
      (item) => (item as unknown as { value: string }).value === "opencode"
    );
    expect(openCodeItem).toBeDefined();
    expect(openCodeItem!.label).toBe("OpenCode");
    expect(openCodeItem!.detail).toEqual(expect.stringContaining("Experimental"));

    disposable.dispose();
  });

  it("gate off: shows the enable-switch message and does not write config", async () => {
    quickPickResponses = [OPENCODE_ADAPTER_ITEM];

    const disposable = registerSwitchAdapterCommand(
      mockLogger as unknown as Parameters<typeof registerSwitchAdapterCommand>[0]
    );

    await invokeCommand();

    const vscode = await import("vscode");
    expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
      expect.stringContaining("NIGHTGAUGE_EXPERIMENTAL_OPENCODE=1")
    );
    expect(writeLocalMock).not.toHaveBeenCalled();

    disposable.dispose();
  });

  it("gate on: writes opencode to config and shows no gate message", async () => {
    process.env.NIGHTGAUGE_EXPERIMENTAL_OPENCODE = "1";
    quickPickResponses = [OPENCODE_ADAPTER_ITEM];

    const disposable = registerSwitchAdapterCommand(
      mockLogger as unknown as Parameters<typeof registerSwitchAdapterCommand>[0]
    );

    await invokeCommand();

    expect(writeLocalMock).toHaveBeenCalledWith({ ui: { core: { adapter: "opencode" } } });

    const vscode = await import("vscode");
    expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
      "Nightgauge adapter set to opencode."
    );

    disposable.dispose();
  });

  it("a non-'1' switch value (e.g. 'true') still gates the adapter off", async () => {
    process.env.NIGHTGAUGE_EXPERIMENTAL_OPENCODE = "true";
    quickPickResponses = [OPENCODE_ADAPTER_ITEM];

    const disposable = registerSwitchAdapterCommand(
      mockLogger as unknown as Parameters<typeof registerSwitchAdapterCommand>[0]
    );

    await invokeCommand();

    expect(writeLocalMock).not.toHaveBeenCalled();

    disposable.dispose();
  });
});
