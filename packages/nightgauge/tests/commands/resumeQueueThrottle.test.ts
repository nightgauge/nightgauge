/**
 * resumeQueueThrottle.test.ts
 *
 * Resume Queue with nothing running and no free slot (#2337 review): when the
 * platform's workspace throttle holds dispatch, the command says so instead
 * of reporting a missing pipeline manager, which a reload would not fix.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

vi.mock("vscode", () => ({
  commands: {
    registerCommand: vi.fn((command: string, callback: (...args: unknown[]) => unknown) => ({
      dispose: vi.fn(),
      command,
      callback,
    })),
    executeCommand: vi.fn(),
  },
  window: {
    showInformationMessage: vi.fn(),
    showWarningMessage: vi.fn(),
    showErrorMessage: vi.fn(),
  },
  TreeItemCollapsibleState: { None: 0, Collapsed: 1, Expanded: 2 },
  TreeItemCheckboxState: { Unchecked: 0, Checked: 1 },
  ThemeIcon: class {
    constructor(
      public id: string,
      public color?: unknown
    ) {}
  },
  ThemeColor: class {
    constructor(public id: string) {}
  },
  MarkdownString: class {
    value = "";
    appendMarkdown(text: string) {
      this.value += text;
    }
  },
  TreeItem: class {
    constructor(
      public label: string,
      public collapsibleState = 0
    ) {}
  },
}));

import * as vscode from "vscode";
import { registerQueueCommands } from "../../src/commands/startPipelineForIssue";
import type { WorkspaceThrottle } from "../../src/services/WorkspaceThrottle";

const logger = { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn() };

function resumeQueue(manager: object): () => Promise<void> {
  const queueService = {
    getStatus: vi.fn().mockResolvedValue("waiting"),
    getQueueLength: vi.fn().mockResolvedValue(2),
    resume: vi.fn(),
  };
  const disposables = registerQueueCommands(
    logger as never,
    queueService as never,
    null,
    manager as never
  ) as unknown as Array<{ command: string; callback: () => Promise<void> }>;
  const registration = disposables.find((d) => d.command === "nightgauge.resumeQueue");
  if (!registration) throw new Error("nightgauge.resumeQueue was not registered");
  return registration.callback;
}

function manager(throttle: WorkspaceThrottle | null) {
  return {
    activeSlotCount: 0,
    availableSlotCount: 0,
    getWorkspaceThrottle: vi.fn(() => throttle),
    fillSlots: vi.fn().mockResolvedValue(0),
  };
}

describe("Resume Queue while no slot is free", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("names the workspace throttle that holds dispatch", async () => {
    const held = manager({ maxConcurrent: 0, resumeAt: null });
    await resumeQueue(held)();

    expect(vscode.window.showErrorMessage).not.toHaveBeenCalled();
    expect(held.fillSlots).not.toHaveBeenCalled();
    expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
      "The workspace's concurrency cap allows 0 runs at once until it is cleared. " +
        "Queued issues start when it is raised, cleared or ends."
    );
  });

  it("says the queue is active while slots are still being prepared", async () => {
    await resumeQueue(manager(null))();

    expect(vscode.window.showErrorMessage).not.toHaveBeenCalled();
    expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
      "Queue is active. Next issue will start when a slot is free."
    );
  });

  it("still reports a missing pipeline manager", async () => {
    await resumeQueue(null as never)();
    expect(vscode.window.showErrorMessage).toHaveBeenCalledWith(
      "Cannot resume queue — pipeline manager not available. Try reloading the window."
    );
  });
});
