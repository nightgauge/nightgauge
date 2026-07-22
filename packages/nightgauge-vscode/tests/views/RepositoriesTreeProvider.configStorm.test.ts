/**
 * RepositoriesTreeProvider config-reload storm guard (#360).
 *
 * Root cause of the ~3s `board.list × 3` GitHub refresh storm: the
 * `ConfigBridge.onConfigChanged` handler's Path 3 unconditionally cleared every
 * per-repo board cache and refreshed on EVERY reload event — including no-op
 * reloads produced by a watched config file being touched without any content
 * change. That turned each such event into a cold-cache refetch of all status
 * tabs. These tests assert Path 3 now no-ops when the merged config is
 * unchanged, and still fires when it actually changes.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { RepositoriesTreeProvider } from "../../src/views/RepositoriesTreeProvider";
import type { WorkspaceManager } from "../../src/services/WorkspaceManager";
import type { IWorkItemProvider } from "../../src/services/types/WorkItemProvider";

// Capture the onConfigChanged listener and control getEffectiveConfig so we can
// simulate no-op vs real config reloads.
const configChangedListeners: Array<(result: unknown) => void> = [];
let effectiveConfig: unknown = { a: 1 };
function fireConfigChanged(): void {
  for (const l of [...configChangedListeners]) l({});
}

vi.mock("../../src/services/ConfigBridge", () => ({
  ConfigBridge: {
    getInstance: () => ({
      onConfigChanged: (listener: (result: unknown) => void) => {
        configChangedListeners.push(listener);
        return { dispose: () => {} };
      },
      getEffectiveConfig: () => ({ config: effectiveConfig }),
    }),
  },
}));

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: { getInstance: () => ({ on: vi.fn(() => ({ dispose: vi.fn() })) }) },
  IpcClientBase: {
    activeCallSource: undefined,
    withCallSource: async (_s: string, fn: () => Promise<unknown>) => fn(),
  },
}));

vi.mock("../../src/config/projectBoardSettings", () => ({
  getProjectBoardSettings: () => ({ groupByEpic: false, defaultEpicCollapsed: false }),
}));

vi.mock("../../src/views/items/EpicGroupTreeItem", () => ({
  EpicGroupTreeItem: class {
    getChildren() {
      return [];
    }
  },
  groupIssuesByEpic: vi.fn().mockReturnValue({ groups: [] }),
}));

vi.mock("vscode", () => ({
  EventEmitter: class EventEmitter<T> {
    private listeners: Array<(e: T) => void> = [];
    event = (listener: (e: T) => void) => {
      this.listeners.push(listener);
      return { dispose: () => {} };
    };
    fire = (e?: T) => this.listeners.forEach((l) => l(e as T));
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
      public color?: unknown
    ) {}
  },
  ThemeColor: class {
    constructor(public id: string) {}
  },
  window: {
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
    onDidChangeActiveTextEditor: vi.fn(() => ({ dispose: vi.fn() })),
  },
  workspace: { workspaceFolders: [] },
}));

class MockEventEmitter<T> {
  private listeners: Array<(e: T) => void> = [];
  event = (listener: (e: T) => void) => {
    this.listeners.push(listener);
    return { dispose: () => {} };
  };
  fire = (e?: T) => this.listeners.forEach((l) => l(e as T));
  dispose = vi.fn();
}

function makeWorkspaceManager(): WorkspaceManager {
  const emitter = new MockEventEmitter<void>();
  return {
    isInitialized: vi.fn().mockReturnValue(true),
    isMultiWorkspace: vi.fn().mockReturnValue(true),
    getAllRepositories: vi.fn().mockReturnValue([]),
    getRepository: vi.fn().mockReturnValue(undefined),
    getRepositoryCount: vi.fn().mockReturnValue(1),
    findRepositoryByGitHub: vi.fn().mockReturnValue(undefined),
    getSharedProjectNumber: vi.fn().mockReturnValue(6),
    areReposDerivedFromProject: vi.fn().mockReturnValue(false),
    onWorkspaceChanged: emitter.event,
  } as unknown as WorkspaceManager;
}

function makeService(): { service: IWorkItemProvider; clearCache: ReturnType<typeof vi.fn> } {
  const items = new MockEventEmitter<void>();
  const clearCache = vi.fn();
  const service = {
    clearCache,
    getIssuesByStatus: vi.fn(async () => []),
    getReadyIssues: vi.fn(async () => []),
    getAllItems: vi.fn(async () => []),
    getAggregatedStatusCounts: vi.fn(async () => ({})),
    prefetchAllItems: vi.fn(async () => undefined),
    getEpicMetadataFromCache: vi.fn(() => new Map()),
    invalidateAndRefresh: vi.fn(),
    onItemsUpdated: items.event,
  } as unknown as IWorkItemProvider;
  return { service, clearCache };
}

describe("RepositoriesTreeProvider config-reload storm guard (#360)", () => {
  let provider: RepositoriesTreeProvider;

  beforeEach(() => {
    configChangedListeners.length = 0;
    effectiveConfig = { a: 1 };
  });
  afterEach(() => provider?.dispose());

  it("does NOT clear board caches on a no-op config reload (unchanged merged config)", () => {
    provider = new RepositoriesTreeProvider(makeWorkspaceManager());
    const { service, clearCache } = makeService();
    provider.setProjectBoardServices(new Map([["repo1", service]]));

    // A no-op reload — the watched config file was touched but nothing changed.
    // This is the ~3s storm trigger; Path 3 must not clear caches / refetch.
    fireConfigChanged();
    fireConfigChanged();
    fireConfigChanged();

    expect(clearCache).not.toHaveBeenCalled();
  });

  it("DOES clear board caches when the merged config actually changes", () => {
    provider = new RepositoriesTreeProvider(makeWorkspaceManager());
    const { service, clearCache } = makeService();
    provider.setProjectBoardServices(new Map([["repo1", service]]));

    // Baseline no-op is suppressed…
    fireConfigChanged();
    expect(clearCache).not.toHaveBeenCalled();

    // …then a real change (adapter switch, edited key) flows through Path 3.
    effectiveConfig = { a: 2 };
    fireConfigChanged();
    expect(clearCache).toHaveBeenCalled();
  });
});
