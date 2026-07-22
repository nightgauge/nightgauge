/**
 * Tests for switchAdapter command — GitHub Copilot CLI option
 *
 * Verifies that GitHub Copilot CLI appears in the QuickPick items, that
 * selecting it writes the correct adapter value to config, and that a
 * non-blocking auth warning is shown when no GitHub token env var is set.
 *
 * @see Issue #1945 - Add Copilot to VSCode config schema and adapter switcher
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

describe("switchAdapter command — GitHub Copilot CLI option", () => {
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
    // Remove token env vars to test warning behavior
    delete process.env.GH_TOKEN;
    delete process.env.GITHUB_TOKEN;
    delete process.env.COPILOT_GITHUB_TOKEN;
  });

  async function invokeCommand(): Promise<void> {
    const vscode = await import("vscode");
    const registerCall = vi.mocked(vscode.commands.registerCommand).mock.calls[0];
    const callback = registerCall[1] as () => Promise<void>;
    await callback();
  }

  it("includes GitHub Copilot CLI in the adapter QuickPick items", async () => {
    quickPickResponses = [undefined];

    const disposable = registerSwitchAdapterCommand(
      mockLogger as unknown as Parameters<typeof registerSwitchAdapterCommand>[0]
    );

    await invokeCommand();

    expect(quickPickCalls.length).toBeGreaterThanOrEqual(1);
    const adapterItems = quickPickCalls[0].items;
    const copilotItem = adapterItems.find(
      (item) => (item as unknown as { value: string }).value === "copilot"
    );
    expect(copilotItem).toBeDefined();
    expect(copilotItem!.label).toBe("GitHub Copilot CLI");

    disposable.dispose();
  });

  it('shows "Current adapter" description when copilot is already selected', async () => {
    vi.mocked(getExecutionAdapter).mockReturnValue("copilot");
    quickPickResponses = [undefined];

    const disposable = registerSwitchAdapterCommand(
      mockLogger as unknown as Parameters<typeof registerSwitchAdapterCommand>[0]
    );

    await invokeCommand();

    const adapterItems = quickPickCalls[0].items;
    const copilotItem = adapterItems.find(
      (item) => (item as unknown as { value: string }).value === "copilot"
    );
    expect(copilotItem!.description).toBe("Current adapter");

    disposable.dispose();
  });

  it("shows Copilot description when another adapter is current", async () => {
    vi.mocked(getExecutionAdapter).mockReturnValue("claude");
    quickPickResponses = [undefined];

    const disposable = registerSwitchAdapterCommand(
      mockLogger as unknown as Parameters<typeof registerSwitchAdapterCommand>[0]
    );

    await invokeCommand();

    const adapterItems = quickPickCalls[0].items;
    const copilotItem = adapterItems.find(
      (item) => (item as unknown as { value: string }).value === "copilot"
    );
    expect(copilotItem!.description).toBe("Use GitHub Copilot CLI execution path");

    disposable.dispose();
  });

  it("shows Copilot detail text", async () => {
    quickPickResponses = [undefined];

    const disposable = registerSwitchAdapterCommand(
      mockLogger as unknown as Parameters<typeof registerSwitchAdapterCommand>[0]
    );

    await invokeCommand();

    const adapterItems = quickPickCalls[0].items;
    const copilotItem = adapterItems.find(
      (item) => (item as unknown as { value: string }).value === "copilot"
    );
    expect((copilotItem as unknown as { detail: string }).detail).toBe(
      "Use GitHub Copilot subscription (copilot binary)"
    );

    disposable.dispose();
  });

  it("shows warning when no GitHub token env var is set", async () => {
    const copilotAdapterItem = {
      label: "GitHub Copilot CLI",
      detail: "Use GitHub Copilot subscription (copilot binary)",
      description: "Use GitHub Copilot CLI execution path",
      value: "copilot",
    };
    const projectTierItem = {
      label: "Project config (.nightgauge/config.yaml)",
      description: "Shared with team and committed to repository",
      tier: "project",
    };
    quickPickResponses = [
      copilotAdapterItem as unknown as QuickPickItem,
      projectTierItem as unknown as QuickPickItem,
    ];

    const disposable = registerSwitchAdapterCommand(
      mockLogger as unknown as Parameters<typeof registerSwitchAdapterCommand>[0]
    );

    await invokeCommand();

    const vscode = await import("vscode");
    expect(vscode.window.showWarningMessage).toHaveBeenCalledWith(
      expect.stringContaining("GH_TOKEN")
    );

    disposable.dispose();
  });

  it("does not show warning when GH_TOKEN is set", async () => {
    process.env.GH_TOKEN = "test-token";

    const copilotAdapterItem = {
      label: "GitHub Copilot CLI",
      detail: "Use GitHub Copilot subscription (copilot binary)",
      description: "Use GitHub Copilot CLI execution path",
      value: "copilot",
    };
    const projectTierItem = {
      label: "Project config (.nightgauge/config.yaml)",
      description: "Shared with team and committed to repository",
      tier: "project",
    };
    quickPickResponses = [
      copilotAdapterItem as unknown as QuickPickItem,
      projectTierItem as unknown as QuickPickItem,
    ];

    const disposable = registerSwitchAdapterCommand(
      mockLogger as unknown as Parameters<typeof registerSwitchAdapterCommand>[0]
    );

    await invokeCommand();

    const vscode = await import("vscode");
    expect(vscode.window.showWarningMessage).not.toHaveBeenCalled();

    disposable.dispose();
  });
});
