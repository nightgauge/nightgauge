import { describe, it, expect, vi, beforeEach } from "vitest";
import type { QuickPickItem } from "vscode";

let quickPickCalls: Array<{ items: QuickPickItem[]; options: unknown }> = [];
let quickPickResponse: QuickPickItem | undefined;
const executeCommandSpy = vi.hoisted(() => vi.fn(() => Promise.resolve()));
const listModelsMock = vi.hoisted(() =>
  vi.fn(() => ["gpt-5.6-sol", "gpt-5.6-terra", "gpt-5.6-luna", "gpt-5.5", "gpt-5.4"])
);
const listOpenCodeModelsMock = vi.hoisted(() =>
  vi.fn(async (_configuredModel?: string) => [
    {
      id: "lmstudio/qwen/qwen3.8-27b",
      label: "lmstudio/qwen/qwen3.8-27b (Configured)",
      selectable: true,
    },
    { id: "anthropic/claude-sonnet-5", label: "anthropic/claude-sonnet-5", selectable: true },
  ])
);

vi.mock("vscode", () => {
  return {
    TreeItem: class TreeItem {
      constructor(
        public label?: string,
        public collapsibleState?: number
      ) {}
    },
    TreeItemCollapsibleState: {
      None: 0,
      Collapsed: 1,
      Expanded: 2,
    },
    ThemeIcon: class ThemeIcon {
      constructor(
        public id: string,
        public color?: unknown
      ) {}
    },
    ThemeColor: class ThemeColor {
      constructor(public id: string) {}
    },
    workspace: {
      workspaceFolders: [{ uri: { fsPath: "/mock/workspace" } }],
    },
    window: {
      showQuickPick: vi.fn((items: QuickPickItem[], options: unknown) => {
        quickPickCalls.push({ items, options });
        return Promise.resolve(quickPickResponse);
      }),
      showErrorMessage: vi.fn(),
      showWarningMessage: vi.fn(),
    },
    commands: {
      registerCommand: vi.fn((_id: string, callback: (...args: unknown[]) => unknown) => ({
        dispose: vi.fn(),
        _callback: callback,
      })),
      executeCommand: executeCommandSpy,
    },
  };
});

vi.mock("../../src/utils/nightgaugeConfig", () => ({
  getExecutionAdapter: vi.fn(() => "claude"),
  getCodexModel: vi.fn(() => "gpt-5.4"),
  getOpenCodeModel: vi.fn(() => "lmstudio/qwen/qwen3.8-27b"),
}));

vi.mock("../../src/services/CodexModelCatalogService", () => ({
  CodexModelCatalogService: class CodexModelCatalogService {
    listModels() {
      return listModelsMock();
    }
  },
}));

vi.mock("../../src/services/OpenCodeModelCatalogService", () => ({
  OpenCodeModelCatalogService: class OpenCodeModelCatalogService {
    listModels(configuredModel?: string) {
      return listOpenCodeModelsMock(configuredModel);
    }
  },
}));

import { registerRunPipelineWithModelCommand } from "../../src/commands/runPipelineWithModel";
import {
  getExecutionAdapter,
  getCodexModel,
  getOpenCodeModel,
} from "../../src/utils/nightgaugeConfig";

describe("runPipelineWithModel command", () => {
  const logger = {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  };

  const orchestrator = {
    setNextRunModelOverride: vi.fn(),
  };

  const statusBar = {
    setModelOverrideLabel: vi.fn(),
  };

  beforeEach(() => {
    vi.clearAllMocks();
    quickPickCalls = [];
    quickPickResponse = undefined;
    listModelsMock.mockReturnValue([
      "gpt-5.6-sol",
      "gpt-5.6-terra",
      "gpt-5.6-luna",
      "gpt-5.5",
      "gpt-5.4",
    ]);
  });

  async function invokeCommand(): Promise<void> {
    const vscode = await import("vscode");
    const registerCall = vi.mocked(vscode.commands.registerCommand).mock.calls[0];
    const callback = registerCall[1] as () => Promise<void>;
    await callback();
  }

  it("shows Claude model options when adapter is claude", async () => {
    vi.mocked(getExecutionAdapter).mockReturnValue("claude");
    quickPickResponse = undefined;

    const disposable = registerRunPipelineWithModelCommand(
      logger as never,
      orchestrator as never,
      statusBar as never
    );

    await invokeCommand();

    const labels = quickPickCalls[0].items.map((item) => item.label);
    expect(labels).toContain("$(rocket) Opus");
    expect(labels).toContain("$(zap) Sonnet");
    expect(labels).toContain("$(dashboard) Haiku");

    disposable.dispose();
  });

  it("shows Codex model options when adapter is codex", async () => {
    vi.mocked(getExecutionAdapter).mockReturnValue("codex");
    vi.mocked(getCodexModel).mockReturnValue("gpt-5.4");
    quickPickResponse = undefined;

    const disposable = registerRunPipelineWithModelCommand(
      logger as never,
      orchestrator as never,
      statusBar as never
    );

    await invokeCommand();

    const labels = quickPickCalls[0].items.map((item) => item.label);
    expect(labels).toContain("gpt-5.4 (Configured)");
    expect(labels).toContain("gpt-5.6-sol");
    expect(labels).toContain("gpt-5.6-luna");

    disposable.dispose();
  });

  it("labels the current frontier model as the recommended default", async () => {
    vi.mocked(getExecutionAdapter).mockReturnValue("codex");
    vi.mocked(getCodexModel).mockReturnValue("gpt-5.4");
    quickPickResponse = undefined;

    const disposable = registerRunPipelineWithModelCommand(
      logger as never,
      orchestrator as never,
      statusBar as never
    );

    await invokeCommand();

    const items = quickPickCalls[0].items;
    const recommended = items.find((item) => item.description === "Recommended default");
    expect(recommended?.label).toBe("gpt-5.6-sol");
    // The previous base default (gpt-5.4) must NOT carry the recommended tag.
    const base = items.find((item) => item.label === "gpt-5.4 (Configured)");
    expect(base?.description).toBeUndefined();

    disposable.dispose();
  });

  it("stores the selected Codex override and starts the pipeline", async () => {
    vi.mocked(getExecutionAdapter).mockReturnValue("codex");
    quickPickResponse = {
      label: "gpt-5.4",
      model: "gpt-5.4",
      displayLabel: "gpt-5.4",
    } as QuickPickItem;

    const disposable = registerRunPipelineWithModelCommand(
      logger as never,
      orchestrator as never,
      statusBar as never
    );

    await invokeCommand();

    expect(orchestrator.setNextRunModelOverride).toHaveBeenCalledWith("gpt-5.4");
    expect(statusBar.setModelOverrideLabel).toHaveBeenCalledWith("gpt-5.4");
    expect(executeCommandSpy).toHaveBeenCalledWith("nightgauge.pickupIssue", undefined);

    disposable.dispose();
  });

  it("shows OpenCode catalog options, configured model first, when adapter is opencode (#1628)", async () => {
    vi.mocked(getExecutionAdapter).mockReturnValue("opencode");
    vi.mocked(getOpenCodeModel).mockReturnValue("lmstudio/qwen/qwen3.8-27b");
    quickPickResponse = undefined;

    const disposable = registerRunPipelineWithModelCommand(
      logger as never,
      orchestrator as never,
      statusBar as never
    );

    await invokeCommand();

    expect(listOpenCodeModelsMock).toHaveBeenCalledWith("lmstudio/qwen/qwen3.8-27b");
    const items = quickPickCalls[0].items;
    expect(items[0].label).toBe("lmstudio/qwen/qwen3.8-27b (Configured)");
    expect(items.map((item) => item.label)).toContain("anthropic/claude-sonnet-5");

    disposable.dispose();
  });

  it("never offers a non-selectable OpenCode notice entry, and warns instead of opening an empty QuickPick (#1628)", async () => {
    vi.mocked(getExecutionAdapter).mockReturnValue("opencode");
    listOpenCodeModelsMock.mockResolvedValueOnce([
      { id: "", label: "Could not list OpenCode models", selectable: false },
    ]);
    quickPickResponse = undefined;

    const disposable = registerRunPipelineWithModelCommand(
      logger as never,
      orchestrator as never,
      statusBar as never
    );

    const vscode = await import("vscode");
    await invokeCommand();

    // No QuickPick is ever opened — an empty picker gives the user nothing
    // to act on.
    expect(quickPickCalls).toEqual([]);
    // The catalog's own "could not list models" notice explains why instead.
    expect(vscode.window.showWarningMessage).toHaveBeenCalledWith("Could not list OpenCode models");

    disposable.dispose();
  });

  it("stores the selected OpenCode override and starts the pipeline (#1628)", async () => {
    vi.mocked(getExecutionAdapter).mockReturnValue("opencode");
    quickPickResponse = {
      label: "anthropic/claude-sonnet-5",
      model: "anthropic/claude-sonnet-5",
      displayLabel: "anthropic/claude-sonnet-5",
    } as QuickPickItem;

    const disposable = registerRunPipelineWithModelCommand(
      logger as never,
      orchestrator as never,
      statusBar as never
    );

    await invokeCommand();

    expect(orchestrator.setNextRunModelOverride).toHaveBeenCalledWith("anthropic/claude-sonnet-5");
    expect(statusBar.setModelOverrideLabel).toHaveBeenCalledWith("anthropic/claude-sonnet-5");

    disposable.dispose();
  });
});
