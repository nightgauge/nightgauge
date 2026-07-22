/**
 * Tests for switchAdapter command — Gemini CLI and Gemini SDK options
 *
 * Verifies that Gemini CLI and Gemini SDK appear in the QuickPick items, that
 * selecting them writes the correct adapter value to config, and that a
 * non-blocking auth warning is shown when no Gemini API key env var is set.
 *
 * @see Issue #2590 - Add Gemini CLI and Gemini SDK to VSCode adapter switcher
 * @see packages/nightgauge-vscode/src/commands/switchAdapter.ts
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import type { QuickPickItem } from "vscode";

// Track calls to showQuickPick to inspect the items list
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

vi.mock("../../src/utils/incrediConfig", () => ({
  getExecutionAdapter: vi.fn(() => "claude"),
}));

vi.mock("../../src/views/settings/IncrediYamlService", () => ({
  IncrediYamlService: vi.fn(function () {
    return {
      read: vi.fn(() => Promise.resolve({ success: true, config: {} })),
      readLocal: vi.fn(() => Promise.resolve({ success: true, config: {} })),
      write: vi.fn(() => Promise.resolve({ success: true })),
      writeLocal: vi.fn(() => Promise.resolve({ success: true })),
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
import { getExecutionAdapter } from "../../src/utils/incrediConfig";

describe("switchAdapter command — Gemini CLI and Gemini SDK options", () => {
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
    // Remove Gemini API key env vars to test warning behavior
    delete process.env.GEMINI_API_KEY;
    delete process.env.GOOGLE_API_KEY;
  });

  async function invokeCommand(): Promise<void> {
    const vscode = await import("vscode");
    const registerCall = vi.mocked(vscode.commands.registerCommand).mock.calls[0];
    const callback = registerCall[1] as () => Promise<void>;
    await callback();
  }

  it("includes Gemini CLI in the adapter QuickPick items", async () => {
    quickPickResponses = [undefined];

    const disposable = registerSwitchAdapterCommand(
      mockLogger as unknown as Parameters<typeof registerSwitchAdapterCommand>[0]
    );

    await invokeCommand();

    expect(quickPickCalls.length).toBeGreaterThanOrEqual(1);
    const adapterItems = quickPickCalls[0].items;
    const geminiItem = adapterItems.find(
      (item) => (item as unknown as { value: string }).value === "gemini"
    );
    expect(geminiItem).toBeDefined();
    expect(geminiItem!.label).toBe("Gemini CLI");

    disposable.dispose();
  });

  it("includes Gemini SDK in the adapter QuickPick items", async () => {
    quickPickResponses = [undefined];

    const disposable = registerSwitchAdapterCommand(
      mockLogger as unknown as Parameters<typeof registerSwitchAdapterCommand>[0]
    );

    await invokeCommand();

    const adapterItems = quickPickCalls[0].items;
    const geminiSdkItem = adapterItems.find(
      (item) => (item as unknown as { value: string }).value === "gemini-sdk"
    );
    expect(geminiSdkItem).toBeDefined();
    expect(geminiSdkItem!.label).toBe("Gemini SDK (Direct API)");

    disposable.dispose();
  });

  it('shows "Current adapter" description when gemini is already selected', async () => {
    vi.mocked(getExecutionAdapter).mockReturnValue("gemini");
    quickPickResponses = [undefined];

    const disposable = registerSwitchAdapterCommand(
      mockLogger as unknown as Parameters<typeof registerSwitchAdapterCommand>[0]
    );

    await invokeCommand();

    const adapterItems = quickPickCalls[0].items;
    const geminiItem = adapterItems.find(
      (item) => (item as unknown as { value: string }).value === "gemini"
    );
    expect(geminiItem!.description).toBe("Current adapter");

    disposable.dispose();
  });

  it('shows "Current adapter" description when gemini-sdk is already selected', async () => {
    vi.mocked(getExecutionAdapter).mockReturnValue("gemini-sdk");
    quickPickResponses = [undefined];

    const disposable = registerSwitchAdapterCommand(
      mockLogger as unknown as Parameters<typeof registerSwitchAdapterCommand>[0]
    );

    await invokeCommand();

    const adapterItems = quickPickCalls[0].items;
    const geminiSdkItem = adapterItems.find(
      (item) => (item as unknown as { value: string }).value === "gemini-sdk"
    );
    expect(geminiSdkItem!.description).toBe("Current adapter");

    disposable.dispose();
  });

  it("shows Gemini CLI description when another adapter is current", async () => {
    vi.mocked(getExecutionAdapter).mockReturnValue("claude");
    quickPickResponses = [undefined];

    const disposable = registerSwitchAdapterCommand(
      mockLogger as unknown as Parameters<typeof registerSwitchAdapterCommand>[0]
    );

    await invokeCommand();

    const adapterItems = quickPickCalls[0].items;
    const geminiItem = adapterItems.find(
      (item) => (item as unknown as { value: string }).value === "gemini"
    );
    expect(geminiItem!.description).toBe("Use Google Gemini CLI binary execution path");

    disposable.dispose();
  });

  it("shows Gemini SDK description when another adapter is current", async () => {
    vi.mocked(getExecutionAdapter).mockReturnValue("claude");
    quickPickResponses = [undefined];

    const disposable = registerSwitchAdapterCommand(
      mockLogger as unknown as Parameters<typeof registerSwitchAdapterCommand>[0]
    );

    await invokeCommand();

    const adapterItems = quickPickCalls[0].items;
    const geminiSdkItem = adapterItems.find(
      (item) => (item as unknown as { value: string }).value === "gemini-sdk"
    );
    expect(geminiSdkItem!.description).toBe("Use Google Gemini SDK with direct API access");

    disposable.dispose();
  });

  it("shows Gemini SDK detail text about API key requirement", async () => {
    quickPickResponses = [undefined];

    const disposable = registerSwitchAdapterCommand(
      mockLogger as unknown as Parameters<typeof registerSwitchAdapterCommand>[0]
    );

    await invokeCommand();

    const adapterItems = quickPickCalls[0].items;
    const geminiSdkItem = adapterItems.find(
      (item) => (item as unknown as { value: string }).value === "gemini-sdk"
    );
    expect((geminiSdkItem as unknown as { detail: string }).detail).toBe(
      "Requires GEMINI_API_KEY or GOOGLE_API_KEY environment variable"
    );

    disposable.dispose();
  });

  it("shows auth warning when selecting gemini and no API key env vars are set", async () => {
    const geminiAdapterItem = {
      label: "Gemini CLI",
      description: "Use Google Gemini CLI binary execution path",
      value: "gemini",
    };
    quickPickResponses = [geminiAdapterItem as unknown as QuickPickItem];

    const disposable = registerSwitchAdapterCommand(
      mockLogger as unknown as Parameters<typeof registerSwitchAdapterCommand>[0]
    );

    await invokeCommand();

    const vscode = await import("vscode");
    expect(vscode.window.showWarningMessage).toHaveBeenCalledWith(
      expect.stringContaining("GEMINI_API_KEY")
    );

    disposable.dispose();
  });

  it("shows auth warning when selecting gemini-sdk and no API key env vars are set", async () => {
    const geminiSdkAdapterItem = {
      label: "Gemini SDK (Direct API)",
      detail: "Requires GEMINI_API_KEY or GOOGLE_API_KEY environment variable",
      description: "Use Google Gemini SDK with direct API access",
      value: "gemini-sdk",
    };
    quickPickResponses = [geminiSdkAdapterItem as unknown as QuickPickItem];

    const disposable = registerSwitchAdapterCommand(
      mockLogger as unknown as Parameters<typeof registerSwitchAdapterCommand>[0]
    );

    await invokeCommand();

    const vscode = await import("vscode");
    expect(vscode.window.showWarningMessage).toHaveBeenCalledWith(
      expect.stringContaining("GEMINI_API_KEY")
    );

    disposable.dispose();
  });

  it("does not show warning when GEMINI_API_KEY is set", async () => {
    process.env.GEMINI_API_KEY = "test-key";

    const geminiAdapterItem = {
      label: "Gemini CLI",
      description: "Use Google Gemini CLI binary execution path",
      value: "gemini",
    };
    quickPickResponses = [geminiAdapterItem as unknown as QuickPickItem];

    const disposable = registerSwitchAdapterCommand(
      mockLogger as unknown as Parameters<typeof registerSwitchAdapterCommand>[0]
    );

    await invokeCommand();

    const vscode = await import("vscode");
    expect(vscode.window.showWarningMessage).not.toHaveBeenCalled();

    disposable.dispose();
  });

  it("does not show warning when GOOGLE_API_KEY is set", async () => {
    process.env.GOOGLE_API_KEY = "test-key";

    const geminiAdapterItem = {
      label: "Gemini CLI",
      description: "Use Google Gemini CLI binary execution path",
      value: "gemini",
    };
    quickPickResponses = [geminiAdapterItem as unknown as QuickPickItem];

    const disposable = registerSwitchAdapterCommand(
      mockLogger as unknown as Parameters<typeof registerSwitchAdapterCommand>[0]
    );

    await invokeCommand();

    const vscode = await import("vscode");
    expect(vscode.window.showWarningMessage).not.toHaveBeenCalled();

    disposable.dispose();
  });

  it("mentions gcloud auth fallback in the warning message", async () => {
    const geminiAdapterItem = {
      label: "Gemini CLI",
      description: "Use Google Gemini CLI binary execution path",
      value: "gemini",
    };
    quickPickResponses = [geminiAdapterItem as unknown as QuickPickItem];

    const disposable = registerSwitchAdapterCommand(
      mockLogger as unknown as Parameters<typeof registerSwitchAdapterCommand>[0]
    );

    await invokeCommand();

    const vscode = await import("vscode");
    expect(vscode.window.showWarningMessage).toHaveBeenCalledWith(
      expect.stringContaining("gcloud auth")
    );

    disposable.dispose();
  });
});
