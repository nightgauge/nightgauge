/**
 * Tests for pipeline adapter choices — Ollama
 *
 * Verifies that the chat-completion-only Ollama adapter is not offered for
 * pipeline execution.
 *
 * @see Issue #2593 - Add Ollama and Gemini to VSCode adapter switcher
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

describe("switchAdapter command — Ollama option", () => {
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
    // Remove Ollama env var to test warning behavior
    delete process.env.NIGHTGAUGE_OLLAMA_MODEL;
  });

  async function invokeCommand(): Promise<void> {
    const vscode = await import("vscode");
    const registerCall = vi.mocked(vscode.commands.registerCommand).mock.calls[0];
    const callback = registerCall[1] as () => Promise<void>;
    await callback();
  }

  it("excludes chat-only Ollama from pipeline adapter choices", async () => {
    quickPickResponses = [undefined];

    const disposable = registerSwitchAdapterCommand(
      mockLogger as unknown as Parameters<typeof registerSwitchAdapterCommand>[0]
    );

    await invokeCommand();

    expect(quickPickCalls.length).toBeGreaterThanOrEqual(1);
    const adapterItems = quickPickCalls[0].items;
    const ollamaItem = adapterItems.find(
      (item) => (item as unknown as { value: string }).value === "ollama"
    );
    expect(ollamaItem).toBeUndefined();

    disposable.dispose();
  });
});
