/**
 * RepositoriesTreeProvider.repoHalt.test.ts
 *
 * Per-row visibility for repo-scoped autonomous halts (#1148).
 *
 * The halt narrows the blast radius of a terminal stage failure to one repo
 * and leaves the fleet status "running" — which means nothing in the status
 * bar, and nothing in the global Resume button's gating, says that a given
 * repository has stopped. On the Repositories view the only symptom was a
 * Ready issue that never got picked up. These tests pin the provider half of
 * the fix: reading `autonomous.status`, matching halt keys onto workspace
 * rows, and repainting scoped rows on a live halt/resume.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as fs from "fs";
import * as os from "os";
import * as path from "path";
import { RepositoriesTreeProvider } from "../../src/views/RepositoriesTreeProvider";
import { RepositoryTreeItem } from "../../src/views/items/RepositoryTreeItem";
import type { WorkspaceManager } from "../../src/services/WorkspaceManager";
import type { Repository } from "../../src/models/Repository";

const mockWorkspaceFolders: Array<{ uri: { fsPath: string } }> = [];

vi.mock("../../src/services/ConfigBridge", () => ({
  ConfigBridge: {
    getInstance: () => ({
      onConfigChanged: () => ({ dispose: () => {} }),
      reload: vi.fn(async () => {}),
    }),
  },
}));

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

/** IPC event handlers registered by the provider, so tests can fire them. */
const ipcHandlers = new Map<string, Array<(data: unknown) => void>>();

const ipcMock = {
  on: vi.fn((event: string, handler: (data: unknown) => void) => {
    const list = ipcHandlers.get(event) ?? [];
    list.push(handler);
    ipcHandlers.set(event, list);
    return { dispose: vi.fn() };
  }),
  autonomousStatus: vi.fn().mockResolvedValue({ status: "running" }),
  autonomousUpdateAllowlist: vi.fn().mockResolvedValue({ status: "running" }),
};

async function fireIpc(event: string): Promise<void> {
  for (const h of ipcHandlers.get(event) ?? []) h(undefined);
  // Handlers are `void`-ed async calls; let their microtasks drain.
  await new Promise((r) => setTimeout(r, 0));
}

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: { getInstance: () => ipcMock },
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
    constructor(public value: string = "") {}
  },
  window: {
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
    showInformationMessage: vi.fn().mockResolvedValue(undefined),
    showErrorMessage: vi.fn(),
    setStatusBarMessage: vi.fn(() => ({ dispose: vi.fn() })),
  },
  workspace: {
    get workspaceFolders() {
      return mockWorkspaceFolders;
    },
  },
  commands: { executeCommand: vi.fn() },
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

function makeRepo(name: string, owner = "acme"): Repository {
  return {
    name,
    path: `/path/to/${name}`,
    role: "primary",
    isConfigLoaded: true,
    github: { owner, repo: name },
    loadConfig: vi.fn().mockResolvedValue(undefined),
  } as unknown as Repository;
}

/**
 * A workspace folder with no `github:` block and no flat owner/repo keys —
 * common enough that the halt lookup cannot depend on a fully-qualified key
 * being derivable from config.
 */
function makeRepoWithoutGitHub(name: string): Repository {
  return {
    name,
    path: `/path/to/${name}`,
    role: "primary",
    isConfigLoaded: true,
    github: undefined,
    loadConfig: vi.fn().mockResolvedValue(undefined),
  } as unknown as Repository;
}

function makeWorkspaceManager(repos: Repository[]): WorkspaceManager {
  const emitter = new MockEventEmitter<void>();
  return {
    isInitialized: vi.fn().mockReturnValue(true),
    isMultiWorkspace: vi.fn().mockReturnValue(true),
    getAllRepositories: vi.fn().mockReturnValue(repos),
    getRepository: vi.fn((name: string) => repos.find((r) => r.name === name)),
    getRepositoryCount: vi.fn().mockReturnValue(repos.length),
    findRepositoryByGitHub: vi.fn(),
    getSharedProjectNumber: vi.fn().mockReturnValue(undefined),
    areReposDerivedFromProject: vi.fn().mockReturnValue(false),
    onWorkspaceChanged: emitter.event,
  } as unknown as WorkspaceManager;
}

function haltRecord(repo: string, issue = 42) {
  return {
    repo,
    reason: `haltQueueOnSlotFailure: issue #${issue} failed at feature-validate`,
    triggeredBy: "haltQueueOnSlotFailure",
    pausedAt: "2026-08-31T12:00:00Z",
    issue,
    stage: "feature-validate",
  };
}

function rowsOf(items: unknown[]): RepositoryTreeItem[] {
  return items.filter((i): i is RepositoryTreeItem => i instanceof RepositoryTreeItem);
}

describe("RepositoriesTreeProvider — repo-scoped halt badge (#1148)", () => {
  let tmpRoot: string;
  let provider: RepositoriesTreeProvider;

  beforeEach(() => {
    tmpRoot = fs.mkdtempSync(path.join(os.tmpdir(), "repo-halt-1148-"));
    mockWorkspaceFolders.length = 0;
    mockWorkspaceFolders.push({ uri: { fsPath: tmpRoot } });
    ipcHandlers.clear();
    ipcMock.autonomousStatus.mockResolvedValue({ status: "running" });
  });

  afterEach(() => {
    provider?.dispose();
    mockWorkspaceFolders.length = 0;
    fs.rmSync(tmpRoot, { recursive: true, force: true });
  });

  it("badges only the halted repository — the fleet keeps reading 'running'", async () => {
    ipcMock.autonomousStatus.mockResolvedValue({
      status: "running",
      pausedRepos: { "acme/dashboard": haltRecord("acme/dashboard") },
    });
    provider = new RepositoriesTreeProvider(
      makeWorkspaceManager([makeRepo("dashboard"), makeRepo("api")])
    );

    const rows = rowsOf(await provider.getChildren());
    const byName = new Map(rows.map((r) => [r.repository.name, r]));

    expect(byName.get("dashboard")?.isHalted).toBe(true);
    expect(byName.get("dashboard")?.haltedRepoKey).toBe("acme/dashboard");
    expect(byName.get("dashboard")?.contextValue).toMatch(/-halted$/);
    // The repo that never failed must look exactly as it did before.
    expect(byName.get("api")?.isHalted).toBe(false);
    expect(byName.get("api")?.contextValue).not.toContain("-halted");
  });

  it("matches a halt key against a repo with no github: block by short name", async () => {
    ipcMock.autonomousStatus.mockResolvedValue({
      status: "running",
      pausedRepos: { "acme/dashboard": haltRecord("acme/dashboard") },
    });
    provider = new RepositoriesTreeProvider(
      makeWorkspaceManager([makeRepoWithoutGitHub("dashboard")])
    );

    const rows = rowsOf(await provider.getChildren());
    expect(rows[0].isHalted).toBe(true);
  });

  it("badges neither row when two halted repos share a short name", async () => {
    // Guessing between acme/web and globex/web would badge the wrong row,
    // which is worse than badging none — the fully-qualified path still works
    // for any repo whose config carries owner/repo.
    ipcMock.autonomousStatus.mockResolvedValue({
      status: "running",
      pausedRepos: {
        "acme/web": haltRecord("acme/web"),
        "globex/web": haltRecord("globex/web", 7),
      },
    });
    provider = new RepositoriesTreeProvider(makeWorkspaceManager([makeRepoWithoutGitHub("web")]));

    const rows = rowsOf(await provider.getChildren());
    expect(rows[0].isHalted).toBe(false);
  });

  it("paints the badge on a live halt and clears it on resume, scoped to the affected row", async () => {
    provider = new RepositoriesTreeProvider(
      makeWorkspaceManager([makeRepo("dashboard"), makeRepo("api")])
    );
    await provider.getChildren(); // prime the row cache, nothing halted

    const fired: unknown[] = [];
    provider.onDidChangeTreeData((p) => fired.push(p));

    // Go raises the halt and emits autonomous.repoHaltChanged.
    ipcMock.autonomousStatus.mockResolvedValue({
      status: "running",
      pausedRepos: { "acme/dashboard": haltRecord("acme/dashboard") },
    });
    await fireIpc("autonomous.repoHaltChanged");

    const cached = (provider as any).cachedRepositories as Map<string, RepositoryTreeItem>;
    expect(cached.get("dashboard")?.isHalted).toBe(true);
    expect(cached.get("api")?.isHalted).toBe(false);

    // Scoped repaint: only the halted row, never a global (undefined) fire
    // that would re-fetch every repo's board counts.
    expect(fired.length).toBe(1);
    expect((fired[0] as RepositoryTreeItem).repository.name).toBe("dashboard");

    // Resume clears it.
    fired.length = 0;
    ipcMock.autonomousStatus.mockResolvedValue({ status: "running", pausedRepos: {} });
    await fireIpc("autonomous.repoHaltChanged");

    expect(cached.get("dashboard")?.isHalted).toBe(false);
    expect(cached.get("dashboard")?.contextValue).not.toContain("-halted");
    expect(fired.length).toBe(1);
  });

  it("repaints when an already-halted repo is re-halted by a newer failure", async () => {
    ipcMock.autonomousStatus.mockResolvedValue({
      status: "running",
      pausedRepos: { "acme/dashboard": haltRecord("acme/dashboard", 42) },
    });
    provider = new RepositoriesTreeProvider(makeWorkspaceManager([makeRepo("dashboard")]));
    await provider.getChildren();

    const fired: unknown[] = [];
    provider.onDidChangeTreeData((p) => fired.push(p));

    // Re-pausing REFRESHES the record: the second failure is the current
    // reason the repo is stopped, so a tooltip naming the first is stale.
    ipcMock.autonomousStatus.mockResolvedValue({
      status: "running",
      pausedRepos: { "acme/dashboard": haltRecord("acme/dashboard", 99) },
    });
    await fireIpc("autonomous.repoHaltChanged");

    const cached = (provider as any).cachedRepositories as Map<string, RepositoryTreeItem>;
    expect(cached.get("dashboard")?.halt?.issue).toBe(99);
    expect(fired.length).toBe(1);
  });

  it("keeps the existing badge when the status read fails rather than flickering it off", async () => {
    ipcMock.autonomousStatus.mockResolvedValue({
      status: "running",
      pausedRepos: { "acme/dashboard": haltRecord("acme/dashboard") },
    });
    provider = new RepositoriesTreeProvider(makeWorkspaceManager([makeRepo("dashboard")]));
    await provider.getChildren();

    // A transient IPC error is not evidence the halt was released.
    ipcMock.autonomousStatus.mockRejectedValue(new Error("daemon not connected"));
    await fireIpc("autonomous.repoHaltChanged");

    const cached = (provider as any).cachedRepositories as Map<string, RepositoryTreeItem>;
    expect(cached.get("dashboard")?.isHalted).toBe(true);
  });

  it("does not badge anything when no repo is halted", async () => {
    provider = new RepositoriesTreeProvider(makeWorkspaceManager([makeRepo("dashboard")]));
    const rows = rowsOf(await provider.getChildren());
    expect(rows[0].isHalted).toBe(false);
    expect(rows[0].contextValue).not.toContain("-halted");
  });
});
