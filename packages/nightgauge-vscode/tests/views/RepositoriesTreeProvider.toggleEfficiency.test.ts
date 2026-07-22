/**
 * RepositoriesTreeProvider.toggleEfficiency.test.ts
 *
 * Regression tests for Issue #3432 — toggling a single repo's autonomous
 * checkbox must not cascade refreshes onto unrelated repos.
 *
 * The user-visible bug (post-#3429): unchecking ONE repo lit up loading
 * spinners across every other repo in the Repositories tree. Root cause:
 * the `ConfigBridge.onConfigChanged` echo from our own `enabled_repos`
 * write missed the suppression guard whenever the post-merge value
 * differed from the raw value we wrote (e.g. writing `[]` to delete the
 * runtime overlay falls through to a non-empty lower tier). The cascade
 * then cleared every per-repo `ProjectBoardService` cache and fired
 * `refreshAll()`.
 *
 * Hard requirements asserted here (the standard described on the issue):
 *
 *   (a) No `refreshAll()` cascade — no `_onDidChangeTreeData.fire(undefined)`.
 *   (b) No per-repo `ProjectBoardService.clearCache()` calls.
 *   (c) Zero GitHub API calls (verified by asserting `getReadyIssues` /
 *       `getAllItems` / `getAggregatedStatusCounts` / `prefetchAllItems`
 *       were not called on any mock service after the toggle).
 *   (d) Exactly one targeted `_onDidChangeTreeData.fire(item)` for the
 *       toggled row (and only that row).
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

// ─────────────────────────────────────────────────────────────────────────────
// Test fixtures — ConfigBridge / IPC / vscode mocks. Mirrors the layout used
// in RepositoriesTreeProvider.checkbox.test.ts so the two suites can be read
// side-by-side.

const mockWorkspaceFolders: Array<{ uri: { fsPath: string } }> = [];
const configChangedListeners: Array<(result: unknown) => void> = [];

function fireConfigChanged(result: unknown = {}): void {
  for (const l of [...configChangedListeners]) l(result);
}

// Tracks the debounced echoes that production fires ~100ms after every
// runtime-store write (chain: runtimeStore.onDidChange →
// IncrediYamlService.handleFileChange → 100ms debounced ConfigBridge
// .reload() → _onConfigChanged.fire). Each `reload()` schedules a second
// echo so the test simulates the real two-event-per-write pattern. Pre-
// #3435 the test only fired ONE echo, the counter absorbed it, and the
// missing second echo's cascade never showed up — production saw the
// cascade and the test saw green. This mock now matches reality.
const pendingDebouncedEchoes: Array<NodeJS.Timeout> = [];
function flushDebouncedEchoes(): void {
  while (pendingDebouncedEchoes.length > 0) {
    const t = pendingDebouncedEchoes.shift()!;
    // Eagerly fire any scheduled echo by invoking the listeners directly.
    clearTimeout(t);
  }
  // Fire one consolidated debounced echo (matches debounce coalescing).
  fireConfigChanged({});
}

vi.mock("../../src/services/ConfigBridge", () => ({
  ConfigBridge: {
    getInstance: () => ({
      onConfigChanged: (listener: (result: unknown) => void) => {
        configChangedListeners.push(listener);
        return {
          dispose: () => {
            const i = configChangedListeners.indexOf(listener);
            if (i >= 0) configChangedListeners.splice(i, 1);
          },
        };
      },
      // Fire `onConfigChanged` from inside `reload()` to match the real
      // implementation's synchronous echo. Then schedule a debounced echo
      // matching production's `runtimeStore.onDidChange` →
      // `IncrediYamlService.handleFileChange` → 100ms timer chain. Tests
      // call `flushDebouncedEchoes()` to fast-forward past the debounce.
      reload: vi.fn(async () => {
        fireConfigChanged({});
        const t = setTimeout(() => {
          fireConfigChanged({});
        }, 100);
        pendingDebouncedEchoes.push(t);
      }),
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

const ipcMock = {
  on: vi.fn(() => ({ dispose: vi.fn() })),
  autonomousStatus: vi.fn().mockResolvedValue({ status: "stopped" }),
  autonomousUpdateAllowlist: vi.fn().mockResolvedValue({ status: "running" }),
};

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ipcMock,
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
    showInformationMessage: vi.fn().mockResolvedValue(undefined),
    showErrorMessage: vi.fn(),
    setStatusBarMessage: vi.fn(() => ({ dispose: vi.fn() })),
  },
  workspace: {
    get workspaceFolders() {
      return mockWorkspaceFolders;
    },
  },
  commands: {
    executeCommand: vi.fn(),
  },
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
    loadConfig: vi.fn().mockResolvedValue(undefined),
    github: { owner: "nightgauge", repo: name },
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

/**
 * Build a `ProjectBoardService`-shaped mock that records every call to the
 * data-fetching methods. The toggle-efficiency assertion checks all of
 * these call counts are zero post-toggle.
 */
function makeService(): IWorkItemProvider & {
  __dataCalls: () => number;
} {
  const fetches = {
    getReadyIssues: vi.fn().mockResolvedValue([]),
    getIssuesByStatus: vi.fn().mockResolvedValue([]),
    getAllItems: vi.fn().mockResolvedValue([]),
    getItemsByStatusFromCache: vi.fn().mockReturnValue([]),
    getEpicMetadataFromCache: vi.fn().mockReturnValue(new Map()),
    getAggregatedStatusCounts: vi.fn().mockResolvedValue({ ready: 0, inProgress: 0, backlog: 0 }),
    prefetchAllItems: vi.fn().mockResolvedValue(undefined),
    clearCache: vi.fn(),
    invalidateAndRefresh: vi.fn(),
  };
  const svc = {
    ...fetches,
    __dataCalls: () =>
      fetches.getReadyIssues.mock.calls.length +
      fetches.getIssuesByStatus.mock.calls.length +
      fetches.getAllItems.mock.calls.length +
      fetches.getAggregatedStatusCounts.mock.calls.length +
      fetches.prefetchAllItems.mock.calls.length,
  } as unknown as IWorkItemProvider & { __dataCalls: () => number };
  return svc;
}

function makeMockEnabledReposService(initial: string[] = []): {
  service: import("../../src/utils/enabledReposConfig").EnabledReposConfigService;
  state: { value: string[] };
  writes: Array<string[]>;
} {
  const state = { value: [...initial] };
  const writes: Array<string[]> = [];
  const service = {
    readEnabledRepos: () => [...state.value],
    writeEnabledRepos: async (selected: string[]) => {
      // Mirror the runtime-store semantics: writing `[]` deletes the key,
      // and the merged read falls through to whatever lower tiers hold.
      // The test fixture simulates "no lower tier" by storing the raw
      // value; an explicit team-tier scenario is exercised separately.
      state.value = [...selected];
      writes.push([...selected]);
    },
  };
  return { service, state, writes };
}

describe("RepositoriesTreeProvider — toggle efficiency (#3432)", () => {
  let tmpRoot: string;
  let provider: RepositoriesTreeProvider;

  beforeEach(() => {
    tmpRoot = fs.mkdtempSync(path.join(os.tmpdir(), "toggle-eff-3431-"));
    mockWorkspaceFolders.length = 0;
    mockWorkspaceFolders.push({ uri: { fsPath: tmpRoot } });
    configChangedListeners.length = 0;
    ipcMock.autonomousStatus.mockReset().mockResolvedValue({ status: "stopped" });
    ipcMock.autonomousUpdateAllowlist.mockReset().mockResolvedValue({ status: "running" });
  });

  afterEach(() => {
    provider?.dispose();
    mockWorkspaceFolders.length = 0;
    configChangedListeners.length = 0;
    fs.rmSync(tmpRoot, { recursive: true, force: true });
  });

  it("toggling one checkbox triggers zero GitHub calls, zero cache clears, zero global refreshes — only one scoped row fire", async () => {
    const repos = [makeRepo("alpha"), makeRepo("beta"), makeRepo("gamma")];
    const enabled = makeMockEnabledReposService();
    provider = new RepositoriesTreeProvider(makeWorkspaceManager(repos), undefined, {
      enabledReposConfigService: enabled.service,
    });

    const services = new Map<string, IWorkItemProvider & { __dataCalls: () => number }>();
    for (const r of repos) services.set(r.name, makeService());
    provider.setProjectBoardServices(services as unknown as Map<string, IWorkItemProvider>);

    // Prime the children tree so the cached repo items exist (they're the
    // target of `fireRepoRowRefresh`). After this, `setProjectBoardServices`
    // already fired its initial `refreshAll`, so we reset all spies to a
    // clean slate before performing the toggle we want to measure.
    await provider.getChildren();

    for (const svc of services.values()) {
      (svc.clearCache as any).mockClear();
      (svc.getReadyIssues as any).mockClear();
      (svc.getIssuesByStatus as any).mockClear();
      (svc.getAllItems as any).mockClear();
      (svc.getAggregatedStatusCounts as any).mockClear();
      (svc.prefetchAllItems as any).mockClear();
    }

    const firedPayloads: Array<unknown> = [];
    provider.onDidChangeTreeData((p) => firedPayloads.push(p));

    // Toggle "beta" off — the user's complaint: this single click was
    // refreshing every other repo too.
    const betaItem = (provider as any).cachedRepositories.get("beta") as RepositoryTreeItem;
    expect(betaItem).toBeDefined();
    await provider.handleCheckboxChange({
      items: [[betaItem, 0]] as any,
    } as any);

    // Drain microtasks so any deferred listener callbacks (the mocked
    // reload fires onConfigChanged synchronously, but we still want to
    // catch promise-tail work like applyLiveAllowlistUpdate's IPC call).
    await new Promise((r) => setImmediate(r));
    // CRITICAL (#3435): flush the debounced echo too — every real
    // runtime-store write produces a second `onConfigChanged` ~100ms
    // later. The pre-#3435 counter scheme absorbed only the synchronous
    // echo and the debounced one always fell into Path 3 (cascade). The
    // value-window suppression must absorb both — flushing the debounced
    // echo here is what proves it.
    flushDebouncedEchoes();
    await new Promise((r) => setImmediate(r));

    // (a) No global tree-wide refresh — `fire(undefined)` is the cascade
    //     symptom from the user complaint.
    const globalRefreshes = firedPayloads.filter((p) => p === undefined);
    expect(globalRefreshes, "global refresh fired during single-repo toggle").toHaveLength(0);

    // (b) No per-repo `ProjectBoardService.clearCache()` calls. The board-
    //     service caches are status-keyed, not allowlist-keyed — clearing
    //     them on an `enabled_repos` change is pure waste.
    for (const [name, svc] of services) {
      expect(svc.clearCache, `clearCache called on ${name}`).not.toHaveBeenCalled();
    }

    // (c) Zero GitHub calls. The autonomous IPC ping (autonomousStatus +
    //     autonomousUpdateAllowlist) is local IPC, not GitHub, so we
    //     specifically check the GitHub-bound data-fetch methods.
    for (const [name, svc] of services) {
      expect(svc.__dataCalls(), `GitHub-bound fetches issued for ${name}`).toBe(0);
    }

    // (d) Exactly one tree fire — for the row the user toggled.
    expect(firedPayloads).toHaveLength(1);
    expect(firedPayloads[0]).toBeInstanceOf(RepositoryTreeItem);
    expect((firedPayloads[0] as RepositoryTreeItem).repository.name).toBe("beta");

    // Sanity: persisted state matches the toggle.
    expect(enabled.state.value).toEqual(["alpha", "gamma"]);
  });

  it("uncheck-all (writes []) triggers no cascade even when reload's echo carries the unchanged value", async () => {
    // Regression for the specific scenario from the user complaint:
    // unchecking the last enabled repo writes `[]` to delete the runtime
    // overlay. The post-merge `enabled_repos` falls through to whatever
    // lower tiers hold — for this test, the fixture's lower tier is empty
    // so the reload echo carries `[]`. The previous snapshot guard
    // happened to match here, but the new counter approach is value-
    // independent and guaranteed to suppress regardless of whether the
    // post-merge value equals what we wrote.
    const repos = [makeRepo("alpha"), makeRepo("beta")];
    const enabled = makeMockEnabledReposService(["alpha"]);
    provider = new RepositoriesTreeProvider(makeWorkspaceManager(repos), undefined, {
      enabledReposConfigService: enabled.service,
    });

    const services = new Map<string, IWorkItemProvider & { __dataCalls: () => number }>();
    for (const r of repos) services.set(r.name, makeService());
    provider.setProjectBoardServices(services as unknown as Map<string, IWorkItemProvider>);
    await provider.getChildren();

    for (const svc of services.values()) {
      (svc.clearCache as any).mockClear();
    }
    const firedPayloads: Array<unknown> = [];
    provider.onDidChangeTreeData((p) => firedPayloads.push(p));

    // Uncheck the only enabled repo — provider's "uncheck-all = reset to
    // scan-all" branch writes [] and shows an info toast.
    const alphaItem = (provider as any).cachedRepositories.get("alpha") as RepositoryTreeItem;
    await provider.handleCheckboxChange({
      items: [[alphaItem, 0]] as any,
    } as any);
    await new Promise((r) => setImmediate(r));
    // Flush the debounced echo (#3435).
    flushDebouncedEchoes();
    await new Promise((r) => setImmediate(r));

    // No global cascade, no cache clears.
    const globalRefreshes = firedPayloads.filter((p) => p === undefined);
    expect(globalRefreshes).toHaveLength(0);
    for (const [, svc] of services) {
      expect(svc.clearCache).not.toHaveBeenCalled();
    }
  });

  it("debounced echo (#3435) — second onConfigChanged after the toggle does NOT cascade", async () => {
    // The user-facing bug behind this regression: every checkbox toggle
    // produced TWO onConfigChanged events (synchronous reload echo +
    // debounced runtimeStore-onDidChange echo ~100ms later). The pre-
    // #3435 counter incremented by 1, absorbed the first, and let the
    // second fall into Path 3 → clearCache + refreshAll → "all spinners
    // spin and checkboxes appear to come back checked" UX bug. This test
    // explicitly fires the debounced echo and asserts ZERO cascade
    // artifacts — no global refresh, no per-repo clearCache, no GitHub
    // re-fetch.
    const repos = [makeRepo("alpha"), makeRepo("beta"), makeRepo("gamma"), makeRepo("delta")];
    const enabled = makeMockEnabledReposService(["alpha", "beta", "gamma", "delta"]);
    provider = new RepositoriesTreeProvider(makeWorkspaceManager(repos), undefined, {
      enabledReposConfigService: enabled.service,
    });

    const services = new Map<string, IWorkItemProvider & { __dataCalls: () => number }>();
    for (const r of repos) services.set(r.name, makeService());
    provider.setProjectBoardServices(services as unknown as Map<string, IWorkItemProvider>);
    await provider.getChildren();

    for (const svc of services.values()) {
      (svc.clearCache as any).mockClear();
      (svc.getReadyIssues as any).mockClear();
      (svc.getIssuesByStatus as any).mockClear();
      (svc.getAllItems as any).mockClear();
      (svc.getAggregatedStatusCounts as any).mockClear();
      (svc.prefetchAllItems as any).mockClear();
    }

    const firedPayloads: Array<unknown> = [];
    provider.onDidChangeTreeData((p) => firedPayloads.push(p));

    // User unchecks beta, gamma, delta — wants only alpha enabled.
    // Three sequential single-toggle events (the way VS Code delivers
    // separate clicks) — each produces its own sync + debounced echo.
    const betaItem = (provider as any).cachedRepositories.get("beta") as RepositoryTreeItem;
    const gammaItem = (provider as any).cachedRepositories.get("gamma") as RepositoryTreeItem;
    const deltaItem = (provider as any).cachedRepositories.get("delta") as RepositoryTreeItem;

    await provider.handleCheckboxChange({ items: [[betaItem, 0]] as any } as any);
    await provider.handleCheckboxChange({ items: [[gammaItem, 0]] as any } as any);
    await provider.handleCheckboxChange({ items: [[deltaItem, 0]] as any } as any);
    await new Promise((r) => setImmediate(r));

    // Now fire the debounced echoes. In production these arrive ~100ms
    // after each runtime-store write. Pre-#3435 these would each cascade.
    flushDebouncedEchoes();
    flushDebouncedEchoes();
    flushDebouncedEchoes();
    await new Promise((r) => setImmediate(r));

    // Hard requirement (a): no global tree-wide refresh.
    const globalRefreshes = firedPayloads.filter((p) => p === undefined);
    expect(
      globalRefreshes,
      "debounced echo cascaded into refreshAll() — bug regressed (#3435)"
    ).toHaveLength(0);

    // Hard requirement (b): no per-repo clearCache.
    for (const [name, svc] of services) {
      expect(
        svc.clearCache,
        `clearCache fired for ${name} after debounced echo — cascade leaked`
      ).not.toHaveBeenCalled();
    }

    // Hard requirement (c): no GitHub data fetches triggered.
    for (const [name, svc] of services) {
      expect(svc.__dataCalls(), `GitHub fetches issued for ${name}`).toBe(0);
    }

    // Hard requirement (d): exactly three scoped row fires (one per
    // toggle), no extras from the debounced echoes.
    const itemFires = firedPayloads.filter((p) => p instanceof RepositoryTreeItem);
    expect(
      itemFires,
      "expected exactly 3 scoped row fires (one per toggle); extras = unsuppressed echo"
    ).toHaveLength(3);
    const firedRepoNames = itemFires.map((p) => (p as RepositoryTreeItem).repository.name).sort();
    expect(firedRepoNames).toEqual(["beta", "delta", "gamma"]);

    // Sanity: persisted state matches the toggles.
    expect(enabled.state.value).toEqual(["alpha"]);

    // Sanity: in-memory enabledRepos reflects the final state — checkbox
    // visuals computed from this list will show alpha checked, others
    // unchecked. (The "checkboxes come back enabled" symptom from the
    // user complaint would manifest as enabledRepos containing the OLD
    // pre-toggle list here.)
    expect((provider as any).enabledRepos).toEqual(["alpha"]);
  });

  it("expired self-write entries (>1s old) do NOT suppress genuine external changes", async () => {
    // Safety check on the value-window suppression: an external manual
    // edit that happens to set the same value as a recent self-write is
    // suppressed only briefly. Once the TTL expires, normal cascade
    // behavior resumes. This test directly mutates the entries' expiry
    // timestamps to simulate the passage of time without fake timers
    // (which conflict with the provider's async setup paths).
    const repos = [makeRepo("alpha"), makeRepo("beta")];
    const enabled = makeMockEnabledReposService(["alpha", "beta"]);
    provider = new RepositoriesTreeProvider(makeWorkspaceManager(repos), undefined, {
      enabledReposConfigService: enabled.service,
    });

    const services = new Map<string, IWorkItemProvider & { __dataCalls: () => number }>();
    for (const r of repos) services.set(r.name, makeService());
    provider.setProjectBoardServices(services as unknown as Map<string, IWorkItemProvider>);
    await provider.getChildren();
    for (const svc of services.values()) (svc.clearCache as any).mockClear();

    // Record a self-write, then expire it by rewriting the timestamp.
    (provider as any).markRecentSelfWrite(["alpha", "beta"]);
    const entries = (provider as any).recentSelfWrites as Array<{ expiresAt: number }>;
    for (const e of entries) e.expiresAt = Date.now() - 1; // already expired

    // External echo arrives carrying the same value. After TTL expiry,
    // it should NOT be suppressed; Path 3 should cascade (correct
    // behavior for an external adapter-switch where enabled_repos
    // happens to match). Path 3's immediate side effect is per-repo
    // `clearCache()` — `refreshAll()` is debounced so we can't easily
    // observe it inline. Cache clears prove Path 3 was reached.
    fireConfigChanged({});
    await new Promise((r) => setImmediate(r));

    let cacheClearCount = 0;
    for (const [, svc] of services) {
      cacheClearCount += (svc.clearCache as any).mock.calls.length;
    }
    expect(
      cacheClearCount,
      "expired self-write entries should not suppress Path 3 cascade"
    ).toBeGreaterThan(0);
  });
});
