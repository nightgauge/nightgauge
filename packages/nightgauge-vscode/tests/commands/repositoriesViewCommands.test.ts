/**
 * Repositories View Context Menu Command Tests
 *
 * Verifies that sortRepositoriesView, filterRepositoriesView, and
 * searchRepositoriesView commands correctly route to the right repo + status
 * node when invoked from a context menu on an IssueSummaryTreeItem.
 *
 * Cross-repo routing: Each IssueSummaryTreeItem carries its `repoName` so
 * commands update the correct repo's state even in a multi-repo workspace.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import * as vscode from "vscode";
import { registerSortRepositoriesViewCommand } from "../../src/commands/sortRepositoriesView";
import { registerFilterRepositoriesViewCommand } from "../../src/commands/filterRepositoriesView";
import { registerSearchRepositoriesViewCommand } from "../../src/commands/searchRepositoriesView";
import { IssueSummaryTreeItem } from "../../src/views/items/IssueSummaryTreeItem";
import type { RepositoriesTreeProvider } from "../../src/views/RepositoriesTreeProvider";
import type { Logger } from "../../src/utils/logger";

// ─── vscode mock ────────────────────────────────────────────────────────────

const registeredCommands = new Map<string, (...args: unknown[]) => unknown>();

vi.mock("vscode", () => ({
  TreeItemCollapsibleState: { None: 0, Collapsed: 1, Expanded: 2 },
  TreeItem: class {
    label: string;
    collapsibleState: number;
    contextValue?: string;
    constructor(label: string, collapsibleState = 0) {
      this.label = label;
      this.collapsibleState = collapsibleState;
    }
  },
  ThemeIcon: class {
    constructor(public id: string) {}
  },
  ThemeColor: class {
    constructor(public id: string) {}
  },
  MarkdownString: class {
    value = "";
    appendMarkdown(v: string) {
      this.value += v;
      return this;
    }
  },
  commands: {
    registerCommand: vi.fn((id: string, fn: (...args: unknown[]) => unknown) => {
      registeredCommands.set(id, fn);
      return { dispose: vi.fn() };
    }),
  },
  window: {
    showWarningMessage: vi.fn(),
    showInformationMessage: vi.fn(),
    showQuickPick: vi.fn(),
    createQuickPick: vi.fn(() => ({
      placeholder: "",
      value: "",
      title: "",
      items: [],
      onDidChangeValue: vi.fn(() => ({ dispose: vi.fn() })),
      onDidAccept: vi.fn(() => ({ dispose: vi.fn() })),
      onDidChangeSelection: vi.fn(() => ({ dispose: vi.fn() })),
      onDidHide: vi.fn(() => ({ dispose: vi.fn() })),
      show: vi.fn(),
      dispose: vi.fn(),
    })),
  },
  QuickPickItemKind: { Separator: -1 },
}));

// ─── Helpers ─────────────────────────────────────────────────────────────────

function createMockProvider(
  overrides: Partial<RepositoriesTreeProvider> = {}
): RepositoriesTreeProvider {
  return {
    getSortForStatus: vi.fn().mockReturnValue({ sortBy: "board", sortDirection: "asc" }),
    setSortForStatus: vi.fn(),
    getFilterForStatus: vi.fn().mockReturnValue({
      priority: "all",
      size: "all",
      component: "all",
      hideBlocked: false,
      searchText: "",
    }),
    setFilterForStatus: vi.fn(),
    setSearchForStatus: vi.fn(),
    ...overrides,
  } as unknown as RepositoriesTreeProvider;
}

function createMockLogger(): Logger {
  return {
    debug: vi.fn(),
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
  } as unknown as Logger;
}

// ─── Sort command ─────────────────────────────────────────────────────────────

describe("sortRepositoriesView command", () => {
  let provider: RepositoriesTreeProvider;
  let logger: Logger;

  beforeEach(() => {
    registeredCommands.clear();
    vi.clearAllMocks();
    provider = createMockProvider();
    logger = createMockLogger();
  });

  it("registers the command with the correct ID", () => {
    registerSortRepositoriesViewCommand(provider, logger);
    expect(vscode.commands.registerCommand).toHaveBeenCalledWith(
      "nightgauge.sortRepositoriesView",
      expect.any(Function)
    );
  });

  it("shows a warning when invoked without an IssueSummaryTreeItem", async () => {
    registerSortRepositoriesViewCommand(provider, logger);
    const handler = registeredCommands.get("nightgauge.sortRepositoriesView")!;
    await handler(undefined);
    expect(vscode.window.showWarningMessage).toHaveBeenCalledWith(
      expect.stringContaining("Right-click a status group")
    );
  });

  it("reads sort state from the correct repo when invoked with an IssueSummaryTreeItem", async () => {
    registerSortRepositoriesViewCommand(provider, logger);
    const handler = registeredCommands.get("nightgauge.sortRepositoriesView")!;
    const item = new IssueSummaryTreeItem("ready", "repo-a", 5);

    // showQuickPick returns undefined (cancelled)
    vi.mocked(vscode.window.showQuickPick).mockResolvedValue(undefined);

    await handler(item);

    expect(provider.getSortForStatus).toHaveBeenCalledWith("repo-a", "ready");
  });

  it("routes setSortForStatus to the correct repo on selection", async () => {
    registerSortRepositoriesViewCommand(provider, logger);
    const handler = registeredCommands.get("nightgauge.sortRepositoriesView")!;
    const item = new IssueSummaryTreeItem("inProgress", "repo-b", 3);

    vi.mocked(vscode.window.showQuickPick).mockResolvedValue({
      label: "Priority - Descending",
      description: "some description",
      sortBy: "priority",
      sortDirection: "desc",
    } as any);

    await handler(item);

    expect(provider.setSortForStatus).toHaveBeenCalledWith(
      "repo-b",
      "inProgress",
      "priority",
      "desc"
    );
  });

  it("does not call setSortForStatus when selection is cancelled", async () => {
    registerSortRepositoriesViewCommand(provider, logger);
    const handler = registeredCommands.get("nightgauge.sortRepositoriesView")!;
    const item = new IssueSummaryTreeItem("backlog", "repo-c", 2);

    vi.mocked(vscode.window.showQuickPick).mockResolvedValue(undefined);

    await handler(item);

    expect(provider.setSortForStatus).not.toHaveBeenCalled();
  });

  it("routes independently for two different repos", async () => {
    registerSortRepositoriesViewCommand(provider, logger);
    const handler = registeredCommands.get("nightgauge.sortRepositoriesView")!;

    vi.mocked(vscode.window.showQuickPick).mockResolvedValue({
      label: "Number - Ascending",
      sortBy: "number",
      sortDirection: "asc",
    } as any);

    const itemA = new IssueSummaryTreeItem("ready", "repo-alpha", 1);
    const itemB = new IssueSummaryTreeItem("ready", "repo-beta", 1);

    await handler(itemA);
    await handler(itemB);

    expect(provider.setSortForStatus).toHaveBeenNthCalledWith(
      1,
      "repo-alpha",
      "ready",
      "number",
      "asc"
    );
    expect(provider.setSortForStatus).toHaveBeenNthCalledWith(
      2,
      "repo-beta",
      "ready",
      "number",
      "asc"
    );
  });
});

// ─── Filter command ───────────────────────────────────────────────────────────

describe("filterRepositoriesView command", () => {
  let provider: RepositoriesTreeProvider;
  let logger: Logger;

  beforeEach(() => {
    registeredCommands.clear();
    vi.clearAllMocks();
    provider = createMockProvider();
    logger = createMockLogger();
  });

  it("registers the command with the correct ID", () => {
    registerFilterRepositoriesViewCommand(provider, logger);
    expect(vscode.commands.registerCommand).toHaveBeenCalledWith(
      "nightgauge.filterRepositoriesView",
      expect.any(Function)
    );
  });

  it("shows a warning when invoked without an IssueSummaryTreeItem", async () => {
    registerFilterRepositoriesViewCommand(provider, logger);
    const handler = registeredCommands.get("nightgauge.filterRepositoriesView")!;
    await handler(undefined);
    expect(vscode.window.showWarningMessage).toHaveBeenCalledWith(
      expect.stringContaining("Right-click a status group")
    );
  });

  it("reads filter state from the correct repo when invoked with an IssueSummaryTreeItem", async () => {
    registerFilterRepositoriesViewCommand(provider, logger);
    const handler = registeredCommands.get("nightgauge.filterRepositoriesView")!;
    const item = new IssueSummaryTreeItem("ready", "repo-x", 4);

    vi.mocked(vscode.window.showQuickPick).mockResolvedValue(undefined);

    await handler(item);

    expect(provider.getFilterForStatus).toHaveBeenCalledWith("repo-x", "ready");
  });

  it("routes filter update to correct repo on selection", async () => {
    registerFilterRepositoriesViewCommand(provider, logger);
    const handler = registeredCommands.get("nightgauge.filterRepositoriesView")!;
    const item = new IssueSummaryTreeItem("backlog", "repo-y", 7);

    vi.mocked(vscode.window.showQuickPick).mockResolvedValue({
      label: "P0 Critical",
      filterType: "priority",
      filterValue: "p0",
    } as any);

    await handler(item);

    expect(provider.setFilterForStatus).toHaveBeenCalledWith(
      "repo-y",
      "backlog",
      expect.objectContaining({ priority: "p0" })
    );
  });

  it("routes independently for two different repos", async () => {
    registerFilterRepositoriesViewCommand(provider, logger);
    const handler = registeredCommands.get("nightgauge.filterRepositoriesView")!;

    vi.mocked(vscode.window.showQuickPick).mockResolvedValue({
      label: "XS",
      filterType: "size",
      filterValue: "xs",
    } as any);

    const itemA = new IssueSummaryTreeItem("inProgress", "repo-1", 2);
    const itemB = new IssueSummaryTreeItem("inProgress", "repo-2", 3);

    await handler(itemA);
    await handler(itemB);

    expect(provider.setFilterForStatus).toHaveBeenNthCalledWith(
      1,
      "repo-1",
      "inProgress",
      expect.objectContaining({ size: "xs" })
    );
    expect(provider.setFilterForStatus).toHaveBeenNthCalledWith(
      2,
      "repo-2",
      "inProgress",
      expect.objectContaining({ size: "xs" })
    );
  });

  it('clears all filters when "clear" option is selected', async () => {
    registerFilterRepositoriesViewCommand(provider, logger);
    const handler = registeredCommands.get("nightgauge.filterRepositoriesView")!;
    const item = new IssueSummaryTreeItem("ready", "repo-z", 1);

    vi.mocked(vscode.window.showQuickPick).mockResolvedValue({
      label: "$(close) Clear All Filters",
      filterType: "clear",
      filterValue: "",
    } as any);

    await handler(item);

    expect(provider.setFilterForStatus).toHaveBeenCalledWith(
      "repo-z",
      "ready",
      expect.objectContaining({ priority: "all", size: "all" })
    );
  });
});

// ─── Search command ───────────────────────────────────────────────────────────

describe("searchRepositoriesView command", () => {
  let provider: RepositoriesTreeProvider;
  let logger: Logger;
  let mockQuickPick: ReturnType<typeof vi.mocked<typeof vscode.window.createQuickPick>>;

  beforeEach(() => {
    registeredCommands.clear();
    vi.clearAllMocks();
    provider = createMockProvider();
    logger = createMockLogger();
  });

  it("registers the command with the correct ID", () => {
    registerSearchRepositoriesViewCommand(provider, logger);
    expect(vscode.commands.registerCommand).toHaveBeenCalledWith(
      "nightgauge.searchRepositoriesView",
      expect.any(Function)
    );
  });

  it("shows a warning when invoked without an IssueSummaryTreeItem", async () => {
    registerSearchRepositoriesViewCommand(provider, logger);
    const handler = registeredCommands.get("nightgauge.searchRepositoriesView")!;
    await handler(undefined);
    expect(vscode.window.showWarningMessage).toHaveBeenCalledWith(
      expect.stringContaining("Right-click a status group")
    );
  });

  it("reads current search text from the correct repo on open", async () => {
    (provider.getFilterForStatus as ReturnType<typeof vi.fn>).mockReturnValue({
      priority: "all",
      size: "all",
      component: "all",
      hideBlocked: false,
      searchText: "auth",
    });

    registerSearchRepositoriesViewCommand(provider, logger);
    const handler = registeredCommands.get("nightgauge.searchRepositoriesView")!;
    const item = new IssueSummaryTreeItem("ready", "repo-search", 10);

    await handler(item);

    expect(provider.getFilterForStatus).toHaveBeenCalledWith("repo-search", "ready");
  });

  it("routes independently for two different repos (no cross-contamination)", async () => {
    registerSearchRepositoriesViewCommand(provider, logger);

    // Each invocation creates its own QuickPick — mock accepts are independent
    const onAcceptCallbacks: Array<() => void> = [];
    vi.mocked(vscode.window.createQuickPick).mockImplementation(
      () =>
        ({
          placeholder: "",
          value: "",
          title: "",
          items: [],
          onDidChangeValue: vi.fn((_cb: any) => ({ dispose: vi.fn() })),
          onDidAccept: vi.fn((cb: any) => {
            onAcceptCallbacks.push(cb);
            return { dispose: vi.fn() };
          }),
          onDidChangeSelection: vi.fn(() => ({ dispose: vi.fn() })),
          onDidHide: vi.fn(() => ({ dispose: vi.fn() })),
          show: vi.fn(),
          dispose: vi.fn(),
        }) as any
    );

    const handler = registeredCommands.get("nightgauge.searchRepositoriesView")!;

    const itemA = new IssueSummaryTreeItem("ready", "repo-alpha", 5);
    const itemB = new IssueSummaryTreeItem("ready", "repo-beta", 3);

    // Invoke both — neither will trigger accept (no callback fires), just verify routing
    void handler(itemA);
    void handler(itemB);

    expect(provider.getFilterForStatus).toHaveBeenCalledWith("repo-alpha", "ready");
    expect(provider.getFilterForStatus).toHaveBeenCalledWith("repo-beta", "ready");
  });
});
