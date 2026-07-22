/**
 * Tests for switchAdapter command — Ollama option
 *
 * Verifies that Ollama appears in the QuickPick items, that selecting it writes
 * the correct adapter value to config, and that a non-blocking warning is shown
 * when no NIGHTGAUGE_OLLAMA_MODEL env var is set.
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
import { getExecutionAdapter } from "../../src/utils/incrediConfig";

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

  it("includes Ollama in the adapter QuickPick items", async () => {
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
    expect(ollamaItem).toBeDefined();
    expect(ollamaItem!.label).toBe("Ollama");

    disposable.dispose();
  });

  it('shows "Current adapter" description when ollama is already selected', async () => {
    vi.mocked(getExecutionAdapter).mockReturnValue("ollama");
    quickPickResponses = [undefined];

    const disposable = registerSwitchAdapterCommand(
      mockLogger as unknown as Parameters<typeof registerSwitchAdapterCommand>[0]
    );

    await invokeCommand();

    const adapterItems = quickPickCalls[0].items;
    const ollamaItem = adapterItems.find(
      (item) => (item as unknown as { value: string }).value === "ollama"
    );
    expect(ollamaItem!.description).toBe("Current adapter");

    disposable.dispose();
  });

  it("shows Ollama description when another adapter is current", async () => {
    vi.mocked(getExecutionAdapter).mockReturnValue("claude");
    quickPickResponses = [undefined];

    const disposable = registerSwitchAdapterCommand(
      mockLogger as unknown as Parameters<typeof registerSwitchAdapterCommand>[0]
    );

    await invokeCommand();

    const adapterItems = quickPickCalls[0].items;
    const ollamaItem = adapterItems.find(
      (item) => (item as unknown as { value: string }).value === "ollama"
    );
    expect(ollamaItem!.description).toBe("Use Ollama local inference (HTTP to localhost:11434)");

    disposable.dispose();
  });

  it("shows warning when selecting ollama and no model env var is set", async () => {
    const ollamaAdapterItem = {
      label: "Ollama",
      description: "Use Ollama local inference (HTTP to localhost:11434)",
      value: "ollama",
    };
    quickPickResponses = [ollamaAdapterItem as unknown as QuickPickItem];

    const disposable = registerSwitchAdapterCommand(
      mockLogger as unknown as Parameters<typeof registerSwitchAdapterCommand>[0]
    );

    await invokeCommand();

    const vscode = await import("vscode");
    expect(vscode.window.showWarningMessage).toHaveBeenCalledWith(
      expect.stringContaining("NIGHTGAUGE_OLLAMA_MODEL")
    );

    disposable.dispose();
  });

  it("suppresses warning when NIGHTGAUGE_OLLAMA_MODEL is set", async () => {
    process.env.NIGHTGAUGE_OLLAMA_MODEL = "llama3.1";

    const ollamaAdapterItem = {
      label: "Ollama",
      description: "Use Ollama local inference (HTTP to localhost:11434)",
      value: "ollama",
    };
    quickPickResponses = [ollamaAdapterItem as unknown as QuickPickItem];

    const disposable = registerSwitchAdapterCommand(
      mockLogger as unknown as Parameters<typeof registerSwitchAdapterCommand>[0]
    );

    await invokeCommand();

    const vscode = await import("vscode");
    expect(vscode.window.showWarningMessage).not.toHaveBeenCalled();

    disposable.dispose();
  });

  it("mentions ollama pull in the warning message", async () => {
    const ollamaAdapterItem = {
      label: "Ollama",
      description: "Use Ollama local inference (HTTP to localhost:11434)",
      value: "ollama",
    };
    quickPickResponses = [ollamaAdapterItem as unknown as QuickPickItem];

    const disposable = registerSwitchAdapterCommand(
      mockLogger as unknown as Parameters<typeof registerSwitchAdapterCommand>[0]
    );

    await invokeCommand();

    const vscode = await import("vscode");
    expect(vscode.window.showWarningMessage).toHaveBeenCalledWith(
      expect.stringContaining("ollama pull")
    );

    disposable.dispose();
  });

  it("writes ollama adapter to config on selection", async () => {
    const ollamaAdapterItem = {
      label: "Ollama",
      description: "Use Ollama local inference (HTTP to localhost:11434)",
      value: "ollama",
    };
    quickPickResponses = [ollamaAdapterItem as unknown as QuickPickItem];
    process.env.NIGHTGAUGE_OLLAMA_MODEL = "codellama";

    const disposable = registerSwitchAdapterCommand(
      mockLogger as unknown as Parameters<typeof registerSwitchAdapterCommand>[0]
    );

    await invokeCommand();

    const { IncrediYamlService } = await import("../../src/views/settings/IncrediYamlService");
    const mockInstance = vi.mocked(IncrediYamlService).mock.results[0].value as {
      writeLocal: ReturnType<typeof vi.fn>;
    };
    expect(mockInstance.writeLocal).toHaveBeenCalledWith(
      expect.objectContaining({
        ui: expect.objectContaining({
          core: expect.objectContaining({ adapter: "ollama" }),
        }),
      })
    );

    disposable.dispose();
  });

  it("calls ConfigBridge.reload() after selection", async () => {
    const ollamaAdapterItem = {
      label: "Ollama",
      description: "Use Ollama local inference (HTTP to localhost:11434)",
      value: "ollama",
    };
    quickPickResponses = [ollamaAdapterItem as unknown as QuickPickItem];
    process.env.NIGHTGAUGE_OLLAMA_MODEL = "llama3.1";

    const { ConfigBridge } = await import("../../src/services/ConfigBridge");
    const reloadMock = vi.fn(() => Promise.resolve());
    vi.mocked(ConfigBridge.getInstance).mockReturnValue({ reload: reloadMock } as ReturnType<
      typeof ConfigBridge.getInstance
    >);

    const disposable = registerSwitchAdapterCommand(
      mockLogger as unknown as Parameters<typeof registerSwitchAdapterCommand>[0]
    );

    await invokeCommand();

    expect(reloadMock).toHaveBeenCalled();

    disposable.dispose();
  });
});
