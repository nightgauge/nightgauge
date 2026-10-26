/**
 * PipelineTreeProvider.runOutput.test.ts
 *
 * #586 — a CLI-started run's node says where its output lives.
 *
 * `nightgauge run` writes a session log of stage, phase and scheduler
 * narration under the run's checkout (cmd/nightgauge/run_session_log.go). The
 * CLI-run callbacks resolve it, the run's slot node names it in its tooltip,
 * and `nightgauge.openRunOutput` opens it.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

const { registered, openTextDocument, showTextDocument, showWarningMessage } = vi.hoisted(() => ({
  registered: new Map<string, (...args: unknown[]) => unknown>(),
  openTextDocument: vi.fn(async (uri: { fsPath: string }) => ({ uri })),
  showTextDocument: vi.fn(async () => undefined),
  showWarningMessage: vi.fn(),
}));

vi.mock("vscode", () => ({
  TreeItem: class {
    label: string;
    collapsibleState: number;
    description?: string;
    tooltip?: unknown;
    constructor(label: string, collapsibleState: number) {
      this.label = label;
      this.collapsibleState = collapsibleState;
    }
  },
  TreeItemCollapsibleState: { None: 0, Collapsed: 1, Expanded: 2 },
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
    appendMarkdown(s: string) {
      this.value += s;
      return this;
    }
  },
  Uri: { file: (fsPath: string) => ({ fsPath }) },
  commands: {
    registerCommand: (id: string, fn: (...args: unknown[]) => unknown) => {
      registered.set(id, fn);
      return { dispose: () => registered.delete(id) };
    },
  },
  workspace: { openTextDocument },
  window: { showTextDocument, showWarningMessage, showErrorMessage: vi.fn() },
}));

import { createCliPipelineSlotCallbacks } from "../../src/bootstrap/cliPipelineSlots";
import { ConcurrentSlotTreeItem } from "../../src/views/items/ConcurrentSlotTreeItem";
import { registerOpenRunOutputCommand } from "../../src/commands/openRunOutput";
import type { PipelineStateService } from "../../src/services/PipelineStateService";

const LOG = "/repo/.git/nightgauge/logs/2026-10-26_586_session.log";

function stubStateService(): PipelineStateService {
  return {
    applyRuntimeSnapshot: vi.fn(),
    dispose: vi.fn(),
    onStateChanged: () => ({ dispose: () => {} }),
    onPhaseStart: () => ({ dispose: () => {} }),
    onPhaseComplete: () => ({ dispose: () => {} }),
    onTokenUsageUpdated: () => ({ dispose: () => {} }),
    getState: async () => null,
  } as unknown as PipelineStateService;
}

const run = {
  key: "/repo::586::run-1",
  root: "/repo",
  snapshot: { repo: "nightgauge/nightgauge", issueNumber: 586, runId: "run-1", title: "t" },
};

beforeEach(() => {
  registered.clear();
  vi.clearAllMocks();
});

describe("a CLI-started run's output on its tree node (#586)", () => {
  it("resolves the session log under the run's root, retrying on update until it exists", async () => {
    let slot: ConcurrentSlotTreeItem | undefined;
    const findRunLog = vi.fn().mockResolvedValueOnce(null).mockResolvedValue(LOG);
    const binding = createCliPipelineSlotCallbacks({
      tree: () => ({
        getConcurrentSlot: () => slot,
        addConcurrentSlot: (index, issue, title, svc, _e, _er, repo) => {
          slot = new ConcurrentSlotTreeItem(
            index,
            issue,
            title,
            svc,
            undefined,
            undefined,
            undefined,
            repo
          );
        },
        removeConcurrentSlotIfOwned: vi.fn(),
      }),
      output: () => ({
        registerSlotInfo: vi.fn(),
        setSlotLogRoot: vi.fn(),
        removeSlotInfoIfOwned: vi.fn(),
      }),
      createStateService: () => stubStateService(),
      findRunLog,
      nextSlotIndex: () => 0,
    });

    binding.callbacks.onDiscovered(run);
    await vi.waitFor(() => expect(findRunLog).toHaveBeenCalledTimes(1));
    expect(slot?.runOutputLog).toBeUndefined();

    // The CLI writes its log at the first stage; the next snapshot finds it.
    binding.callbacks.onUpdated(run);
    await vi.waitFor(() => expect(slot?.runOutputLog).toBe(LOG));
    expect(findRunLog).toHaveBeenCalledWith("/repo", 586);
    expect(String(slot?.tooltip)).toContain(`Run output: ${LOG}`);

    // Found once, not looked up again.
    binding.callbacks.onUpdated(run);
    await Promise.resolve();
    expect(findRunLog).toHaveBeenCalledTimes(2);
  });

  it("Open Run Output opens the node's session log", async () => {
    registerOpenRunOutputCommand();
    await registered.get("nightgauge.openRunOutput")?.({ issueNumber: 586, runOutputLog: LOG });

    expect(openTextDocument).toHaveBeenCalledWith({ fsPath: LOG });
    expect(showTextDocument).toHaveBeenCalledTimes(1);
  });

  it("Open Run Output warns rather than opening anything when no log is known", async () => {
    registerOpenRunOutputCommand();
    await registered.get("nightgauge.openRunOutput")?.({ issueNumber: 586 });

    expect(openTextDocument).not.toHaveBeenCalled();
    expect(showWarningMessage).toHaveBeenCalledWith(expect.stringContaining("issue #586"));
  });
});
