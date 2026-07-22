/**
 * RepositoriesTreeProvider.queueChanged.test.ts
 *
 * Regression tests for the queue.changed scoped-refresh fix (Issue #2984).
 *
 * Bug: Every queue.changed IPC event triggered refreshAll(), lighting up
 * spinners on every repository in the workspace — even repos with no queue
 * involvement — on every autonomous dispatch.
 *
 * Fix: handleQueueChanged() diffs the old and new sets of queue-owning repo
 * slugs and fires fireTargetedRefresh(repoName, statusKey) only for repos in
 * (previous ∪ new). Repos with no queue involvement (past or present) stay
 * untouched.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { RepositoriesTreeProvider } from "../../src/views/RepositoriesTreeProvider";
import type { WorkspaceManager } from "../../src/services/WorkspaceManager";
import type { IpcQueueState } from "../../src/services/IpcClientBase";

vi.mock("../../src/views/items/EpicGroupTreeItem", () => ({
  EpicGroupTreeItem: class {
    constructor(
      public epic: any,
      _issues: any[],
      _opts?: any
    ) {}
    getChildren() {
      return [];
    }
  },
  groupIssuesByEpic: vi.fn().mockReturnValue({ groups: [] }),
}));

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      on: vi.fn(() => ({ dispose: vi.fn() })),
    }),
  },
}));

vi.mock("../../src/config/projectBoardSettings", () => ({
  getProjectBoardSettings: () => ({ groupByEpic: false, defaultEpicCollapsed: false }),
}));

vi.mock("vscode", () => ({
  EventEmitter: class EventEmitter<T> {
    private _listeners: Array<(e: T) => void> = [];
    event = (listener: (e: T) => void) => {
      this._listeners.push(listener);
      return { dispose: () => {} };
    };
    fire = (event?: T) => {
      this._listeners.forEach((l) => l(event as T));
    };
    dispose = vi.fn();
  },
  TreeItemCheckboxState: { Checked: 1, Unchecked: 0 },
  TreeItemCollapsibleState: { None: 0, Collapsed: 1, Expanded: 2 },
  TreeItem: class {
    constructor(
      public label: string,
      public collapsibleState = 0
    ) {}
  },
  ThemeIcon: class {
    constructor(
      public id: string,
      public color?: any
    ) {}
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
  window: {
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
  },
  workspace: { workspaceFolders: [] },
  TreeCheckboxChangeEvent: class {},
}));

class MockEventEmitter<T> {
  private _listeners: Array<(e: T) => void> = [];
  event = (listener: (e: T) => void) => {
    this._listeners.push(listener);
    return { dispose: () => {} };
  };
  fire = (event?: T) => {
    this._listeners.forEach((l) => l(event as T));
  };
  dispose = vi.fn();
}

function makeWorkspaceManager(repoSlugToName: Record<string, string> = {}): WorkspaceManager {
  const emitter = new MockEventEmitter<void>();
  return {
    isInitialized: vi.fn().mockReturnValue(true),
    isMultiWorkspace: vi.fn().mockReturnValue(true),
    getAllRepositories: vi.fn().mockReturnValue([]),
    getRepository: vi.fn().mockReturnValue(undefined),
    getRepositoryCount: vi.fn().mockReturnValue(0),
    findRepositoryByGitHub: vi.fn((slug: string) => {
      const name = repoSlugToName[slug];
      return name ? ({ name } as any) : undefined;
    }),
    getSharedProjectNumber: vi.fn().mockReturnValue(undefined),
    areReposDerivedFromProject: vi.fn().mockReturnValue(false),
    onWorkspaceChanged: emitter.event,
  } as unknown as WorkspaceManager;
}

function makeState(repos: string[]): IpcQueueState {
  return {
    schema_version: "2.0",
    status: "waiting",
    updated_at: "2026-04-24T22:00:00Z",
    items: repos.map((repo, idx) => ({
      repo,
      issueNumber: 100 + idx,
      title: `Issue ${100 + idx}`,
      priority: 0,
      status: "ready",
      addedAt: "2026-04-24T22:00:00Z",
      position: idx + 1,
    })),
  };
}

describe("RepositoriesTreeProvider.handleQueueChanged (Issue #2984)", () => {
  let provider: RepositoriesTreeProvider;

  beforeEach(() => {
    vi.useFakeTimers();
  });

  afterEach(() => {
    provider?.dispose();
    vi.useRealTimers();
  });

  function invokeHandleQueueChanged(p: RepositoriesTreeProvider, state: IpcQueueState | undefined) {
    // handleQueueChanged is private — access via cast for testability.
    (p as any).handleQueueChanged(state);
  }

  it("fires targeted refresh for a newly-queued repo and does not touch others", () => {
    const wm = makeWorkspaceManager({
      "owner/repo1": "repo1",
      "owner/repo2": "repo2",
      "owner/repo3": "repo3",
    });
    provider = new RepositoriesTreeProvider(wm);

    const spy = vi.spyOn(provider, "fireTargetedRefresh");

    // First queue.changed — one item for repo1
    invokeHandleQueueChanged(provider, makeState(["owner/repo1"]));

    expect(spy).toHaveBeenCalledWith("repo1", "ready");
    expect(spy).toHaveBeenCalledWith("repo1", "inProgress");
    expect(spy).toHaveBeenCalledTimes(2);

    // repo2 and repo3 never saw a targeted refresh
    expect(spy).not.toHaveBeenCalledWith("repo2", expect.anything());
    expect(spy).not.toHaveBeenCalledWith("repo3", expect.anything());
  });

  it("fires targeted refresh for a repo whose last item was dequeued", () => {
    const wm = makeWorkspaceManager({
      "owner/repo1": "repo1",
      "owner/repo2": "repo2",
    });
    provider = new RepositoriesTreeProvider(wm);
    const spy = vi.spyOn(provider, "fireTargetedRefresh");

    // Prime: repo1 had a queue item
    invokeHandleQueueChanged(provider, makeState(["owner/repo1"]));
    spy.mockClear();

    // Now: repo1's item is dequeued — payload is empty
    invokeHandleQueueChanged(provider, makeState([]));

    // repo1 still gets a targeted refresh (was in lastQueueRepoSlugs)
    expect(spy).toHaveBeenCalledWith("repo1", "ready");
    expect(spy).toHaveBeenCalledWith("repo1", "inProgress");
    expect(spy).toHaveBeenCalledTimes(2);

    // repo2 untouched
    expect(spy).not.toHaveBeenCalledWith("repo2", expect.anything());
  });

  it("refreshes only the intersection + symmetric-diff when repo set shifts", () => {
    const wm = makeWorkspaceManager({
      "owner/repo1": "repo1",
      "owner/repo2": "repo2",
      "owner/repo3": "repo3",
    });
    provider = new RepositoriesTreeProvider(wm);
    const spy = vi.spyOn(provider, "fireTargetedRefresh");

    // Prime: repo1 + repo2 have items
    invokeHandleQueueChanged(provider, makeState(["owner/repo1", "owner/repo2"]));
    spy.mockClear();

    // New: repo2 + repo3 have items (repo1 dequeued, repo3 newly enqueued)
    invokeHandleQueueChanged(provider, makeState(["owner/repo2", "owner/repo3"]));

    // All three should be refreshed: repo1 (lost), repo2 (still), repo3 (gained)
    expect(spy).toHaveBeenCalledWith("repo1", "ready");
    expect(spy).toHaveBeenCalledWith("repo2", "ready");
    expect(spy).toHaveBeenCalledWith("repo3", "ready");
    // Each repo refreshed twice (ready + inProgress)
    expect(spy).toHaveBeenCalledTimes(6);
  });

  it("no-ops when both old and new queue state are empty", () => {
    const wm = makeWorkspaceManager();
    provider = new RepositoriesTreeProvider(wm);
    const spy = vi.spyOn(provider, "fireTargetedRefresh");
    const refreshAllSpy = vi.spyOn(provider, "refreshAll");

    invokeHandleQueueChanged(provider, makeState([]));

    expect(spy).not.toHaveBeenCalled();
    expect(refreshAllSpy).not.toHaveBeenCalled();
  });

  it("falls back to refreshAll() when payload is undefined", () => {
    const wm = makeWorkspaceManager();
    provider = new RepositoriesTreeProvider(wm);
    const refreshAllSpy = vi.spyOn(provider, "refreshAll");

    invokeHandleQueueChanged(provider, undefined);

    expect(refreshAllSpy).toHaveBeenCalledTimes(1);
  });

  it("falls back to refreshAll() when items is not an array", () => {
    const wm = makeWorkspaceManager();
    provider = new RepositoriesTreeProvider(wm);
    const refreshAllSpy = vi.spyOn(provider, "refreshAll");

    invokeHandleQueueChanged(provider, {
      schema_version: "2.0",
      status: "waiting",
      updated_at: "",
      items: null as any,
    });

    expect(refreshAllSpy).toHaveBeenCalledTimes(1);
  });

  it("uses the raw slug as repo name when findRepositoryByGitHub returns undefined", () => {
    // Slug is in the queue but not yet registered in the workspace manager
    const wm = makeWorkspaceManager({});
    provider = new RepositoriesTreeProvider(wm);
    const spy = vi.spyOn(provider, "fireTargetedRefresh");

    invokeHandleQueueChanged(provider, makeState(["owner/unknown"]));

    expect(spy).toHaveBeenCalledWith("owner/unknown", "ready");
    expect(spy).toHaveBeenCalledWith("owner/unknown", "inProgress");
  });

  it("ignores items with missing or empty repo field", () => {
    const wm = makeWorkspaceManager({ "owner/repo1": "repo1" });
    provider = new RepositoriesTreeProvider(wm);
    const spy = vi.spyOn(provider, "fireTargetedRefresh");

    invokeHandleQueueChanged(provider, {
      schema_version: "2.0",
      status: "waiting",
      updated_at: "",
      items: [
        {
          repo: "owner/repo1",
          issueNumber: 1,
          title: "ok",
          priority: 0,
          status: "ready",
          addedAt: "",
          position: 1,
        },
        {
          repo: "",
          issueNumber: 2,
          title: "empty-repo",
          priority: 0,
          status: "ready",
          addedAt: "",
          position: 2,
        },
        {
          // @ts-expect-error - testing defensive handling of malformed payloads
          repo: undefined,
          issueNumber: 3,
          title: "missing-repo",
          priority: 0,
          status: "ready",
          addedAt: "",
          position: 3,
        },
      ],
    });

    // Only repo1 refreshed; empty/missing repo items are skipped
    expect(spy).toHaveBeenCalledWith("repo1", "ready");
    expect(spy).toHaveBeenCalledWith("repo1", "inProgress");
    expect(spy).toHaveBeenCalledTimes(2);
  });
});
