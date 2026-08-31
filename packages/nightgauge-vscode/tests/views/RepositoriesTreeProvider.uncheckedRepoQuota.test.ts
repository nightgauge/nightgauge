/**
 * RepositoriesTreeProvider.uncheckedRepoQuota.test.ts
 *
 * The row checkbox is `autonomous.enabled_repos` — "include this repo in
 * autonomous board scans" — but an unchecked repo was still being polled by
 * this view. Every repository row rendered EXPANDED, so every global refresh
 * asked every row for children, and each ask is a `board.counts` GraphQL call
 * once the service's 5-minute cache lapses. The operator's read of the
 * checkbox ("I turned this repo off") and the traffic it produced disagreed,
 * and the only visible symptom was every row's spinner lighting up.
 *
 * These tests pin both halves of the gate: excluded rows render collapsed so
 * their children are never requested, and a fetch for an excluded repo
 * happens only on first expand or on an explicit refresh command.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import * as fs from "fs";
import * as os from "os";
import * as path from "path";
import { RepositoriesTreeProvider } from "../../src/views/RepositoriesTreeProvider";
import { RepositoryTreeItem } from "../../src/views/items/RepositoryTreeItem";
import type { WorkspaceManager } from "../../src/services/WorkspaceManager";
import type { Repository } from "../../src/models/Repository";
import type { IWorkItemProvider } from "../../src/services/types/WorkItemProvider";

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

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      on: vi.fn(() => ({ dispose: vi.fn() })),
      autonomousStatus: vi.fn().mockResolvedValue({ status: "stopped" }),
      autonomousUpdateAllowlist: vi.fn().mockResolvedValue({ status: "running" }),
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

function makeRepo(name: string): Repository {
  return {
    name,
    path: `/path/to/${name}`,
    role: "primary",
    isConfigLoaded: true,
    github: { owner: "acme", repo: name },
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
    findRepositoryByGitHub: vi.fn((slug: string) => repos.find((r) => `acme/${r.name}` === slug)),
    getSharedProjectNumber: vi.fn().mockReturnValue(undefined),
    areReposDerivedFromProject: vi.fn().mockReturnValue(false),
    onWorkspaceChanged: emitter.event,
  } as unknown as WorkspaceManager;
}

/** A board provider that counts every counts-fetch it is asked to perform. */
function makeCountingService(): { service: IWorkItemProvider; calls: () => number } {
  let calls = 0;
  const noop = makeEmitterEvent();
  const service = {
    getAggregatedStatusCounts: vi.fn(async () => {
      calls += 1;
      return { ready: 1, inProgress: 0, backlog: 0 };
    }),
    getIssuesByStatus: vi.fn(async () => []),
    getReadyIssues: vi.fn(async () => []),
    getAllItems: vi.fn(async () => []),
    clearCache: vi.fn(),
    onItemsUpdated: noop,
    onStatusChanged: noop,
    onRateLimitStateChanged: noop,
  } as unknown as IWorkItemProvider;
  return { service, calls: () => calls };
}

function makeEmitterEvent() {
  return (_listener: unknown) => ({ dispose: () => {} });
}

describe("RepositoriesTreeProvider — unchecked repos make no background GitHub calls", () => {
  let tmpRoot: string;
  let provider: RepositoriesTreeProvider;

  beforeEach(() => {
    tmpRoot = fs.mkdtempSync(path.join(os.tmpdir(), "unchecked-quota-"));
    mockWorkspaceFolders.length = 0;
    mockWorkspaceFolders.push({ uri: { fsPath: tmpRoot } });
  });

  afterEach(() => {
    provider?.dispose();
    mockWorkspaceFolders.length = 0;
    fs.rmSync(tmpRoot, { recursive: true, force: true });
  });

  /** Build a provider whose enabled_repos allowlist contains only `enabled`. */
  function build(
    repos: Repository[],
    enabled: string[]
  ): Map<string, { service: IWorkItemProvider; calls: () => number }> {
    const services = new Map<string, { service: IWorkItemProvider; calls: () => number }>();
    const wired = new Map<string, IWorkItemProvider>();
    for (const r of repos) {
      const s = makeCountingService();
      services.set(r.name, s);
      wired.set(r.name, s.service);
    }
    provider = new RepositoriesTreeProvider(makeWorkspaceManager(repos), undefined, {
      enabledReposConfigService: {
        readEnabledRepos: () => [...enabled],
        writeEnabledRepos: async () => {},
      } as any,
    });
    provider.setProjectBoardServices(wired);
    return services;
  }

  it("renders an excluded repo collapsed and an included repo expanded", async () => {
    build([makeRepo("dashboard"), makeRepo("api")], ["dashboard"]);

    const rows = (await provider.getChildren()).filter(
      (i): i is RepositoryTreeItem => i instanceof RepositoryTreeItem
    );
    const byName = new Map(rows.map((r) => [r.repository.name, r]));

    // 2 = Expanded, 1 = Collapsed. VSCode only asks a row for children when
    // it is expanded, so this IS the quota gate for a never-touched row.
    expect(byName.get("dashboard")?.collapsibleState).toBe(2);
    expect(byName.get("api")?.collapsibleState).toBe(1);
  });

  it("keeps every row expanded when no allowlist is set (scan-all default)", async () => {
    build([makeRepo("dashboard"), makeRepo("api")], []);

    const rows = (await provider.getChildren()).filter(
      (i): i is RepositoryTreeItem => i instanceof RepositoryTreeItem
    );
    for (const r of rows) expect(r.collapsibleState).toBe(2);
  });

  it("fetches an excluded repo's counts once on first expand, then never again", async () => {
    const services = build([makeRepo("dashboard"), makeRepo("api")], ["dashboard"]);
    const rows = (await provider.getChildren()).filter(
      (i): i is RepositoryTreeItem => i instanceof RepositoryTreeItem
    );
    const api = rows.find((r) => r.repository.name === "api")!;

    // First expand: the row must show real numbers rather than a permanent
    // zero, so one fetch is allowed.
    await provider.getChildren(api);
    expect(services.get("api")!.calls()).toBe(1);

    // Every subsequent background repaint serves the cached counts.
    await provider.getChildren(api);
    await provider.getChildren(api);
    expect(services.get("api")!.calls()).toBe(1);
  });

  it("keeps refetching an included repo on every render", async () => {
    const services = build([makeRepo("dashboard")], ["dashboard"]);
    const rows = (await provider.getChildren()).filter(
      (i): i is RepositoryTreeItem => i instanceof RepositoryTreeItem
    );
    const dashboard = rows[0];

    await provider.getChildren(dashboard);
    await provider.getChildren(dashboard);
    // The service's own 5-minute cache is what throttles a scanned repo —
    // the view must not add a second, invisible throttle on top of it.
    expect(services.get("dashboard")!.calls()).toBe(2);
  });

  it("lets an explicit refresh command re-fetch an excluded repo", async () => {
    const services = build([makeRepo("dashboard"), makeRepo("api")], ["dashboard"]);
    const rows = (await provider.getChildren()).filter(
      (i): i is RepositoryTreeItem => i instanceof RepositoryTreeItem
    );
    const api = rows.find((r) => r.repository.name === "api")!;

    await provider.getChildren(api);
    await provider.getChildren(api);
    expect(services.get("api")!.calls()).toBe(1);

    // The operator clicking Refresh on the row is not background traffic.
    provider.invalidateAndRefreshRepo("acme/api");
    await provider.getChildren(api);
    expect(services.get("api")!.calls()).toBe(2);

    // …and the pass is one-shot, not a permanent opt-back-in.
    await provider.getChildren(api);
    expect(services.get("api")!.calls()).toBe(2);
  });

  it("serves the last known counts to an excluded repo rather than blanking to zero", async () => {
    const services = build([makeRepo("dashboard"), makeRepo("api")], ["dashboard"]);
    const rows = (await provider.getChildren()).filter(
      (i): i is RepositoryTreeItem => i instanceof RepositoryTreeItem
    );
    const api = rows.find((r) => r.repository.name === "api")!;

    await provider.getChildren(api); // one real fetch → ready: 1
    const children = await provider.getChildren(api); // cached path
    const ready = children.find((c) => (c as any).statusType === "ready") as any;

    expect(services.get("api")!.calls()).toBe(1);
    expect(ready.count).toBe(1);
  });
});
