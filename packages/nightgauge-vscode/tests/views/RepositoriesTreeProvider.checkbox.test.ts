/**
 * RepositoriesTreeProvider.checkbox.test.ts
 *
 * Regression tests for the checkbox scoped-refresh fix (Issue #2988).
 *
 * Bugs fixed:
 *   1. Checkbox state appeared not to persist across reload — actually a
 *      symptom of bug 2 (the global refresh re-rendered with cached state
 *      mid-toggle making the checkbox flicker back).
 *   2. Toggling one checkbox triggered a global tree refresh, lighting up
 *      loading spinners on every repository.
 *   3. No bulk include-all / exclude-all view-title buttons.
 *
 * Fix: `handleCheckboxChange` and `setAllReposEnabledForAutonomous` now
 * emit per-affected-repo `_onDidChangeTreeData.fire(item)` events instead
 * of the global no-arg fire. The cached RepositoryTreeItem's checkboxState
 * is mutated in place so the visual matches the on-disk state.
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

// Mutable workspaceFolders so tests can swap the config root per-test.
const mockWorkspaceFolders: Array<{ uri: { fsPath: string } }> = [];

// Synthetic ConfigBridge so suppression tests can fire `onConfigChanged`
// events directly without driving the real file watcher. Used by the
// Issue #3051 self-write suppression tests below.
const configChangedListeners: Array<(result: unknown) => void> = [];
function fireConfigChanged(result: unknown = {}): void {
  for (const l of [...configChangedListeners]) l(result);
}

// Track ConfigBridge.reload() invocations so the bounce-fix test (#3429) can
// assert reload was awaited before the row visual was recomputed.
const configReloadCalls: { count: number; resolveNext?: () => void } = { count: 0 };

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
      // #3429 — RepositoriesTreeProvider.handleCheckboxChange now awaits
      // reload() between writeEnabledReposSelf and reloadEnabledRepos to
      // synchronously refresh the merged-config cache and prevent the
      // "checkbox bounce" UX bug. Test mock returns a resolved promise so
      // unrelated tests stay synchronous-feeling.
      reload: vi.fn(async () => {
        configReloadCalls.count += 1;
        if (configReloadCalls.resolveNext) {
          configReloadCalls.resolveNext();
          configReloadCalls.resolveNext = undefined;
        }
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

// Mutable IPC mock so per-test setup can flip autonomous status to "running"
// to exercise the new live-allowlist path (#3429).
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

// vscode mock — minimal but sufficient for checkbox-toggle paths.
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
    // #3429 — setStatusBarMessage is the new non-blocking notification used
    // when allowlist updates are live-applied to a running scheduler.
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
 * Build an in-memory mock of the runtime-tier `EnabledReposConfigService`
 * used in Phase 3 of #3313 (#3336). Round-tripping through it lets the
 * tests assert behavior that previously relied on YAML round-trips on disk.
 */
function makeMockEnabledReposService(): {
  service: import("../../src/utils/enabledReposConfig").EnabledReposConfigService;
  state: { value: string[] };
  writes: Array<string[]>;
} {
  const state = { value: [] as string[] };
  const writes: Array<string[]> = [];
  const service = {
    readEnabledRepos: () => [...state.value],
    writeEnabledRepos: async (selected: string[]) => {
      state.value = [...selected];
      writes.push([...selected]);
    },
  };
  return { service, state, writes };
}

describe("RepositoriesTreeProvider — checkbox scoped refresh (Issue #2988)", () => {
  let tmpRoot: string;
  let provider: RepositoriesTreeProvider;

  beforeEach(() => {
    tmpRoot = fs.mkdtempSync(path.join(os.tmpdir(), "checkbox-2988-"));
    mockWorkspaceFolders.length = 0;
    mockWorkspaceFolders.push({ uri: { fsPath: tmpRoot } });
  });

  afterEach(() => {
    provider?.dispose();
    mockWorkspaceFolders.length = 0;
    fs.rmSync(tmpRoot, { recursive: true, force: true });
  });

  it("toggling a single checkbox fires a scoped refresh — never a global tree-wide refresh", async () => {
    const repos = [makeRepo("alpha"), makeRepo("beta"), makeRepo("gamma")];
    provider = new RepositoriesTreeProvider(makeWorkspaceManager(repos));

    // Prime the cache so fireRepoRowRefresh has items to mutate.
    await provider.getChildren();

    // Capture every payload that fires onDidChangeTreeData.
    const firedPayloads: Array<unknown> = [];
    provider.onDidChangeTreeData((p) => firedPayloads.push(p));

    // Uncheck "beta" only.
    const betaItem = (provider as any).cachedRepositories.get("beta") as RepositoryTreeItem;
    expect(betaItem).toBeDefined();
    await provider.handleCheckboxChange({
      items: [[betaItem, 0]] as any, // 0 = Unchecked
    } as any);

    // Every fired payload must be a RepositoryTreeItem (scoped) — never
    // `undefined` (global tree refresh). That's the regression for bug 2.
    expect(firedPayloads.length).toBeGreaterThan(0);
    for (const p of firedPayloads) {
      expect(p).toBeInstanceOf(RepositoryTreeItem);
      expect((p as RepositoryTreeItem).repository.name).toBe("beta");
    }
    // Specifically: alpha and gamma rows were never refreshed.
    const refreshedNames = firedPayloads.map((p) => (p as RepositoryTreeItem).repository.name);
    expect(refreshedNames).not.toContain("alpha");
    expect(refreshedNames).not.toContain("gamma");
  });

  it("in-memory enabledRepos reflects the new state before the refresh event fires (bug 1 root cause)", async () => {
    const repos = [makeRepo("alpha"), makeRepo("beta")];
    const enabled = makeMockEnabledReposService();
    provider = new RepositoriesTreeProvider(makeWorkspaceManager(repos), undefined, {
      enabledReposConfigService: enabled.service,
    });
    await provider.getChildren();

    // When the refresh event fires, the cached item's checkboxState must
    // already match the new state — otherwise VSCode renders the pre-toggle
    // state and the user sees their click "revert".
    let observedCheckboxStateAtFire: number | undefined;
    provider.onDidChangeTreeData((p) => {
      if (p instanceof RepositoryTreeItem && p.repository.name === "beta") {
        const cs = (p as any).checkboxState;
        observedCheckboxStateAtFire =
          typeof cs === "object" && cs !== null ? cs.state : (cs as number);
      }
    });

    const betaItem = (provider as any).cachedRepositories.get("beta") as RepositoryTreeItem;
    await provider.handleCheckboxChange({
      items: [[betaItem, 0]] as any, // 0 = Unchecked
    } as any);

    // After unchecking beta, the cached item's checkbox MUST already reflect
    // the new state at the moment the refresh event fires.
    expect(observedCheckboxStateAtFire).toBe(0);

    // And the runtime-tier service persisted "alpha" only (Phase 3 of #3313).
    expect(enabled.state.value).toEqual(["alpha"]);
  });

  it("setAllReposEnabledForAutonomous(true) writes [] and fires per-row refreshes", async () => {
    const repos = [makeRepo("one"), makeRepo("two"), makeRepo("three")];
    const enabled = makeMockEnabledReposService();
    provider = new RepositoriesTreeProvider(makeWorkspaceManager(repos), undefined, {
      enabledReposConfigService: enabled.service,
    });
    await provider.getChildren();

    const firedNames: string[] = [];
    provider.onDidChangeTreeData((p) => {
      if (p instanceof RepositoryTreeItem) firedNames.push(p.repository.name);
    });

    const count = await provider.setAllReposEnabledForAutonomous(true);

    expect(count).toBe(3);
    // One scoped fire per repo — no global fire.
    expect(firedNames.sort()).toEqual(["one", "three", "two"]);

    // Runtime tier: scan-all default → empty list (Phase 3 of #3313).
    expect(enabled.state.value).toEqual([]);
  });

  it("setAllReposEnabledForAutonomous(false) writes a sentinel that excludes every real repo", async () => {
    const repos = [makeRepo("alpha"), makeRepo("beta")];
    const enabled = makeMockEnabledReposService();
    provider = new RepositoriesTreeProvider(makeWorkspaceManager(repos), undefined, {
      enabledReposConfigService: enabled.service,
    });
    await provider.getChildren();

    const firedNames: string[] = [];
    provider.onDidChangeTreeData((p) => {
      if (p instanceof RepositoryTreeItem) firedNames.push(p.repository.name);
    });

    const count = await provider.setAllReposEnabledForAutonomous(false);

    expect(count).toBe(2);
    expect(firedNames.sort()).toEqual(["alpha", "beta"]);

    // Runtime tier holds only the sentinel "__none__" (Phase 3 of #3313).
    expect(enabled.state.value).toEqual(["__none__"]);

    // Both repos render as unchecked because the sentinel doesn't match anything.
    for (const name of ["alpha", "beta"]) {
      const item = (provider as any).cachedRepositories.get(name) as RepositoryTreeItem;
      const cs = (item as any).checkboxState;
      const state = typeof cs === "object" && cs !== null ? cs.state : cs;
      expect(state).toBe(0); // Unchecked
    }
  });
});

/**
 * Self-write suppression for `enabled_repos` (Issue #3051).
 *
 * When `handleCheckboxChange` writes config.yaml, the `ConfigBridge` file
 * watcher fires `onConfigChanged`. The provider's previous behavior was to
 * `clearCache()` every per-repo service and call `refreshAll()` on every
 * such event — which cascaded loading spinners across every row in the
 * sidebar on every checkbox toggle. The fix records the value we just
 * wrote and suppresses the cascade when the incoming value matches.
 *
 * External edits and adapter switches (incoming value differs from the
 * snapshot, or no snapshot is present) keep the existing full-refresh
 * behavior so they continue to clear caches and re-fetch.
 */
describe("RepositoriesTreeProvider — self-write suppression (Issue #3051)", () => {
  let tmpRoot: string;
  let provider: RepositoriesTreeProvider;
  let configPath: string;

  beforeEach(() => {
    tmpRoot = fs.mkdtempSync(path.join(os.tmpdir(), "self-write-3051-"));
    mockWorkspaceFolders.length = 0;
    mockWorkspaceFolders.push({ uri: { fsPath: tmpRoot } });
    configPath = path.join(tmpRoot, ".nightgauge", "config.yaml");
    configChangedListeners.length = 0;
  });

  afterEach(() => {
    provider?.dispose();
    mockWorkspaceFolders.length = 0;
    configChangedListeners.length = 0;
    fs.rmSync(tmpRoot, { recursive: true, force: true });
  });

  function makeService(): IWorkItemProvider {
    return {
      getIssuesByStatus: vi.fn().mockResolvedValue([]),
      getReadyIssues: vi.fn().mockResolvedValue([]),
      getAllItems: vi.fn().mockResolvedValue([]),
      getItemsByStatusFromCache: vi.fn().mockReturnValue([]),
      getEpicMetadataFromCache: vi.fn().mockReturnValue(new Map()),
      getAggregatedStatusCounts: vi.fn().mockResolvedValue({ ready: 0, inProgress: 0, backlog: 0 }),
      prefetchAllItems: vi.fn().mockResolvedValue(undefined),
      clearCache: vi.fn(),
      invalidateAndRefresh: vi.fn(),
    } as unknown as IWorkItemProvider;
  }

  it("suppresses clearCache + refreshAll when the incoming enabled_repos echoes our last self-write", async () => {
    const repos = [makeRepo("alpha"), makeRepo("beta"), makeRepo("gamma")];
    const enabled = makeMockEnabledReposService();
    provider = new RepositoriesTreeProvider(makeWorkspaceManager(repos), undefined, {
      enabledReposConfigService: enabled.service,
    });

    // Wire per-repo services so we can assert clearCache() was/was not called.
    const services = new Map<string, IWorkItemProvider>();
    services.set("alpha", makeService());
    services.set("beta", makeService());
    services.set("gamma", makeService());
    provider.setProjectBoardServices(services);

    await provider.getChildren();

    // setProjectBoardServices triggers a refreshAll() which fires a global
    // (undefined) event. Drop everything captured before our toggle so we
    // can isolate the post-toggle event payloads.
    const firedPayloads: Array<unknown> = [];
    provider.onDidChangeTreeData((p) => firedPayloads.push(p));

    // Reset clearCache spies so we measure only what fires after the toggle.
    for (const svc of services.values()) {
      (svc.clearCache as any).mockClear();
    }

    // Toggle "beta" — this calls writeEnabledReposSelf(["alpha", "gamma"]).
    const betaItem = (provider as any).cachedRepositories.get("beta") as RepositoryTreeItem;
    await provider.handleCheckboxChange({
      items: [[betaItem, 0]] as any,
    } as any);

    // Synthesize the ConfigBridge echo with the same on-disk value the
    // provider just wrote. The watcher would normally fire this — we drive
    // it directly so the test is deterministic.
    fireConfigChanged({});

    // No service should have had its cache cleared.
    for (const [, svc] of services) {
      expect(svc.clearCache).not.toHaveBeenCalled();
    }

    // No global tree-wide refresh — only the targeted "beta" row event.
    const globalRefreshes = firedPayloads.filter((p) => p === undefined);
    expect(globalRefreshes).toHaveLength(0);
    for (const p of firedPayloads) {
      expect(p).toBeInstanceOf(RepositoryTreeItem);
      expect((p as RepositoryTreeItem).repository.name).toBe("beta");
    }
  });

  it("external edit to enabled_repos updates in-memory state and fires scoped row refreshes — never clears per-repo caches (#3432)", async () => {
    const repos = [makeRepo("alpha"), makeRepo("beta"), makeRepo("gamma")];
    const enabled = makeMockEnabledReposService();
    provider = new RepositoriesTreeProvider(makeWorkspaceManager(repos), undefined, {
      enabledReposConfigService: enabled.service,
    });

    const services = new Map<string, IWorkItemProvider>();
    services.set("alpha", makeService());
    services.set("beta", makeService());
    services.set("gamma", makeService());
    provider.setProjectBoardServices(services);

    await provider.getChildren();

    // Self-write to seed: toggle beta off, alpha + gamma stay on.
    const betaItem = (provider as any).cachedRepositories.get("beta") as RepositoryTreeItem;
    await provider.handleCheckboxChange({
      items: [[betaItem, 0]] as any,
    } as any);

    // The toggle's self-write recorded the post-merge value ["alpha","gamma"]
    // in `recentSelfWrites` (#3435). The simulated external edit below
    // changes the value to ["alpha"], which does NOT match any recent
    // self-write entry, so Path 2 (external-edit handling) fires
    // correctly. No explicit drain needed — the value-window suppression
    // is keyed on the merged value, not on a counter.
    fireConfigChanged({});

    // External edit: a hand-edit changes the allowlist to ["alpha"] only —
    // beta is already off, so its row checkbox is unchanged; gamma flips
    // from on→off; alpha stays on.
    enabled.state.value = ["alpha"];

    // Reset spies so we only measure post-edit behavior.
    for (const svc of services.values()) {
      (svc.clearCache as any).mockClear();
    }
    const firedPayloads: Array<unknown> = [];
    provider.onDidChangeTreeData((p) => firedPayloads.push(p));

    fireConfigChanged({});

    // Per-repo `ProjectBoardService` caches are status-keyed, not allowlist-
    // keyed. An `enabled_repos`-only change must NEVER clear them — that
    // was the user-visible "spinner cascade" bug (#3432).
    for (const [, svc] of services) {
      expect(svc.clearCache).not.toHaveBeenCalled();
    }

    // In-memory enabledRepos was updated to the on-disk value.
    const enabledRepos = (provider as any).enabledRepos as string[];
    expect(enabledRepos.sort()).toEqual(["alpha"]);

    // Only rows whose checkbox state actually flipped (gamma: on→off)
    // were re-fired. No global tree-wide refresh.
    const repoNames = firedPayloads
      .filter((p): p is RepositoryTreeItem => p instanceof RepositoryTreeItem)
      .map((p) => p.repository.name);
    expect(repoNames).toEqual(["gamma"]);
    const globalRefreshes = firedPayloads.filter((p) => p === undefined);
    expect(globalRefreshes).toHaveLength(0);
  });
});

/**
 * Bounce-fix + live allowlist regression tests (Issue #3429).
 *
 * Two regressions PR #3428 didn't address:
 *
 *   1. Checkbox bounce — handleCheckboxChange wrote via runtimeStore.set(...)
 *      then immediately read enabledRepos through ConfigBridge.getEffectiveConfig().
 *      The cache was only refreshed by a debounced 100ms reload, so the
 *      synchronous read returned stale data and the checkbox repainted to the
 *      pre-toggle state.
 *
 *   2. Restart-autonomous modal — toggling a checkbox while autonomous was
 *      running showed a blocking modal "Restart Autonomous Now to Apply?"
 *      because no IPC method existed to live-apply allowlist changes to a
 *      running scheduler.
 *
 * The fixes:
 *   - `handleCheckboxChange` and `setAllReposEnabledForAutonomous` now
 *     `await ConfigBridge.getInstance().reload()` between the write and the
 *     read, so the cache is fresh when the row visual is recomputed.
 *   - A new IPC method `autonomous.updateAllowlist` lets the TS side push
 *     a new allowlist to the running scheduler with no restart. The modal
 *     is replaced with a non-blocking `setStatusBarMessage` toast.
 */
describe("RepositoriesTreeProvider — bounce fix + live allowlist (Issue #3429)", () => {
  let tmpRoot: string;
  let provider: RepositoriesTreeProvider;

  beforeEach(() => {
    tmpRoot = fs.mkdtempSync(path.join(os.tmpdir(), "bounce-3429-"));
    mockWorkspaceFolders.length = 0;
    mockWorkspaceFolders.push({ uri: { fsPath: tmpRoot } });
    configReloadCalls.count = 0;
    ipcMock.autonomousStatus.mockReset().mockResolvedValue({ status: "stopped" });
    ipcMock.autonomousUpdateAllowlist.mockReset().mockResolvedValue({ status: "running" });
  });

  afterEach(() => {
    provider?.dispose();
    mockWorkspaceFolders.length = 0;
    fs.rmSync(tmpRoot, { recursive: true, force: true });
  });

  it("awaits ConfigBridge.reload() between writeEnabledReposSelf and reloadEnabledRepos so the row visual reflects the post-write state synchronously (no bounce)", async () => {
    const repos = [makeRepo("alpha"), makeRepo("beta"), makeRepo("gamma")];
    const enabled = makeMockEnabledReposService();
    provider = new RepositoriesTreeProvider(makeWorkspaceManager(repos), undefined, {
      enabledReposConfigService: enabled.service,
    });
    await provider.getChildren();

    const reloadCallsBefore = configReloadCalls.count;

    const betaItem = (provider as any).cachedRepositories.get("beta") as RepositoryTreeItem;
    await provider.handleCheckboxChange({
      items: [[betaItem, 0]] as any, // 0 = Unchecked
    } as any);

    // ConfigBridge.reload() must have been called at least once between the
    // write and the row repaint. Without this guarantee the cache is stale
    // and the checkbox bounces back. (#3429 root cause.)
    expect(configReloadCalls.count).toBeGreaterThan(reloadCallsBefore);

    // Persisted state matches the toggle (sanity check).
    expect(enabled.state.value).toEqual(["alpha", "gamma"]);

    // Row visual is the post-toggle state.
    const cs = (betaItem as any).checkboxState;
    const state = typeof cs === "object" && cs !== null ? cs.state : cs;
    expect(state).toBe(0); // Unchecked — no bounce
  });

  it("calls autonomous.updateAllowlist (no Stop/Start) when scheduler is running and shows a non-blocking status bar message instead of a Restart modal", async () => {
    ipcMock.autonomousStatus.mockResolvedValue({ status: "running" });

    const repos = [makeRepo("alpha"), makeRepo("beta")];
    // Repositories need .github to feed FQDN names into autonomousUpdateAllowlist.
    (repos[0] as any).github = { owner: "nightgauge", repo: "alpha" };
    (repos[1] as any).github = { owner: "nightgauge", repo: "beta" };
    const enabled = makeMockEnabledReposService();
    provider = new RepositoriesTreeProvider(makeWorkspaceManager(repos), undefined, {
      enabledReposConfigService: enabled.service,
    });
    await provider.getChildren();

    // Spy on vscode.window helpers used by the modal-replacement code path.
    const vscode = await import("vscode");
    (vscode.window.showInformationMessage as any).mockClear();
    (vscode.window.setStatusBarMessage as any).mockClear();

    const betaItem = (provider as any).cachedRepositories.get("beta") as RepositoryTreeItem;
    await provider.handleCheckboxChange({
      items: [[betaItem, 0]] as any,
    } as any);

    // The new IPC method was called with the FQDN workspace repos so the
    // server's resolveAutonomousAllowlist can intersect with YAML-tier config.
    expect(ipcMock.autonomousUpdateAllowlist).toHaveBeenCalledTimes(1);
    const arg = ipcMock.autonomousUpdateAllowlist.mock.calls[0][0] as string[];
    expect(arg.sort()).toEqual(["nightgauge/alpha", "nightgauge/beta"]);

    // No "Restart Autonomous?" modal — i.e. showInformationMessage was NOT
    // called with action buttons. Either it was not called at all, or only
    // with no buttons. The status-bar method is the user-visible signal.
    expect(vscode.window.setStatusBarMessage).toHaveBeenCalledTimes(1);
    const statusArgs = (vscode.window.setStatusBarMessage as any).mock.calls[0];
    expect(statusArgs[0]).toMatch(/Autonomous allowlist updated/);

    // Modal-style call would pass extra string args after the message.
    // Assert no call shaped like ("...Restart...", "Restart Autonomous", "Later").
    const infoCalls = (vscode.window.showInformationMessage as any).mock.calls as Array<unknown[]>;
    for (const call of infoCalls) {
      expect(call.length).toBeLessThanOrEqual(1); // no action buttons → no modal
      expect(String(call[0] ?? "")).not.toMatch(/Restart autonomous/i);
    }
  });

  it("falls back to a 'Takes effect on next Start' info message when scheduler is stopped", async () => {
    ipcMock.autonomousStatus.mockResolvedValue({ status: "stopped" });

    const repos = [makeRepo("alpha"), makeRepo("beta")];
    (repos[0] as any).github = { owner: "nightgauge", repo: "alpha" };
    (repos[1] as any).github = { owner: "nightgauge", repo: "beta" };
    const enabled = makeMockEnabledReposService();
    provider = new RepositoriesTreeProvider(makeWorkspaceManager(repos), undefined, {
      enabledReposConfigService: enabled.service,
    });
    await provider.getChildren();

    const vscode = await import("vscode");
    (vscode.window.showInformationMessage as any).mockClear();
    (vscode.window.setStatusBarMessage as any).mockClear();

    const betaItem = (provider as any).cachedRepositories.get("beta") as RepositoryTreeItem;
    await provider.handleCheckboxChange({
      items: [[betaItem, 0]] as any,
    } as any);

    // Stopped scheduler: no IPC update, just a one-shot info toast.
    expect(ipcMock.autonomousUpdateAllowlist).not.toHaveBeenCalled();
    expect(vscode.window.setStatusBarMessage).not.toHaveBeenCalled();
    expect(vscode.window.showInformationMessage).toHaveBeenCalled();
    const last = (vscode.window.showInformationMessage as any).mock.calls.at(-1) as unknown[];
    expect(String(last[0])).toMatch(/Takes effect on next Start/);
  });

  it("setAllReposEnabledForAutonomous awaits ConfigBridge.reload() and live-applies via IPC", async () => {
    ipcMock.autonomousStatus.mockResolvedValue({ status: "running" });

    const repos = [makeRepo("alpha"), makeRepo("beta")];
    (repos[0] as any).github = { owner: "nightgauge", repo: "alpha" };
    (repos[1] as any).github = { owner: "nightgauge", repo: "beta" };
    const enabled = makeMockEnabledReposService();
    provider = new RepositoriesTreeProvider(makeWorkspaceManager(repos), undefined, {
      enabledReposConfigService: enabled.service,
    });
    await provider.getChildren();

    const reloadBefore = configReloadCalls.count;

    const count = await provider.setAllReposEnabledForAutonomous(false);
    expect(count).toBe(2);

    // Cache reload happened synchronously before the row repaint (#3429).
    expect(configReloadCalls.count).toBeGreaterThan(reloadBefore);

    // Bulk path also live-applies via IPC (no Stop/Start).
    // The call is fire-and-forget (`void this.applyLiveAllowlistUpdate()`),
    // so allow microtask drain.
    await new Promise((r) => setImmediate(r));
    expect(ipcMock.autonomousUpdateAllowlist).toHaveBeenCalled();
  });
});

/**
 * Issue #3650 (Part C) — Flutter checkbox bounce regression.
 *
 * User-reported symptom: in a multi-worktree workspace where the user has
 * `nightgauge`, `acme-platform`, `acme-mobile`,
 * and `acme-dashboard` all configured, clicking the Flutter
 * checkbox briefly fires the activity indicators then visually flips back
 * to unchecked.
 *
 * Root cause: `~/.nightgauge/config.yaml` had
 *   autonomous.enabled_repos: [nightgauge, acme-platform,
 *                              acme-dashboard]
 * (Flutter omitted) — the user's pre-#3641 list. The `runLegacyKeysMigration`
 * runner is gated by a *globalState* STATE_KEY, so it ran only in the
 * first workspace VSCode opened. Every other workspace kept a stale
 * `nightgauge.runtime.autonomous.enabled_repos` workspaceState memento
 * with the same Flutter-excluded list. Because the merge engine's runtime
 * tier wins over the machine tier (`defaults ← global ← project ← local ←
 * runtime ← env ← cli`), every `ConfigBridge.reload()` re-overlaid the
 * stale list on top of the post-click machine YAML, and the row repaint
 * read the pre-click value.
 *
 * The fix has two parts:
 *   1. `enabledReposConfig.writeEnabledRepos` now clears the runtime
 *      memento at BOTH `workspace` AND `global` scope, covering every
 *      legacy write path.
 *   2. `bootstrap/services.ts` defensively clears both scopes on every
 *      activation so existing installs heal without waiting for the user
 *      to first click a checkbox.
 *
 * This describe block tests behavior (1). Behavior (2) is exercised by the
 * `enabledReposConfig` unit tests which assert both scopes are cleared.
 */
describe("RepositoriesTreeProvider — Flutter checkbox bounce (Issue #3650)", () => {
  let tmpRoot: string;
  let provider: RepositoriesTreeProvider;

  beforeEach(() => {
    tmpRoot = fs.mkdtempSync(path.join(os.tmpdir(), "flutter-bounce-3650-"));
    mockWorkspaceFolders.length = 0;
    mockWorkspaceFolders.push({ uri: { fsPath: tmpRoot } });
    configReloadCalls.count = 0;
    ipcMock.autonomousStatus.mockReset().mockResolvedValue({ status: "stopped" });
    ipcMock.autonomousUpdateAllowlist.mockReset().mockResolvedValue({ status: "running" });
  });

  afterEach(() => {
    provider?.dispose();
    mockWorkspaceFolders.length = 0;
    fs.rmSync(tmpRoot, { recursive: true, force: true });
  });

  it("clicking a single repo that's missing from enabled_repos keeps that repo checked after the write (no bounce)", async () => {
    // Replicate the user's reported config shape: 4 workspace repos but
    // `enabled_repos` lists only 3 of them. The omitted repo is the one
    // whose checkbox bounces in the bug report (Flutter in the real env).
    const repos = [
      makeRepo("nightgauge"),
      makeRepo("acme-platform"),
      makeRepo("acme-mobile"),
      makeRepo("acme-dashboard"),
    ];
    const enabled = makeMockEnabledReposService();
    // Seed the post-#3641 machine YAML view: Flutter excluded.
    enabled.state.value = ["nightgauge", "acme-platform", "acme-dashboard"];

    provider = new RepositoriesTreeProvider(makeWorkspaceManager(repos), undefined, {
      enabledReposConfigService: enabled.service,
    });
    await provider.getChildren();

    // Pre-condition sanity: Flutter row currently renders unchecked because
    // it's missing from the enabled list (and the list is non-empty so the
    // "scan all" sentinel doesn't apply).
    const flutterItem = (provider as any).cachedRepositories.get(
      "acme-mobile"
    ) as RepositoryTreeItem;
    const preCs = (flutterItem as any).checkboxState;
    expect(typeof preCs === "object" && preCs !== null ? preCs.state : preCs).toBe(0); // Unchecked

    // Click Flutter → state 1 (Checked).
    await provider.handleCheckboxChange({
      items: [[flutterItem, 1]] as any,
    } as any);

    // After the toggle, every workspace repo is checked → the handler
    // writes `[]` to machine YAML (scan-all sentinel). The post-write
    // read must produce `[]` so `isRepoEnabledForAutonomous(flutter, [])`
    // returns true and the Flutter row stays checked. With the legacy
    // runtime overlay still in place this would re-overlay the old
    // 3-repo list and Flutter would flip back to unchecked.
    expect(enabled.state.value).toEqual([]); // scan-all sentinel persisted

    const postCs = (flutterItem as any).checkboxState;
    const postState = typeof postCs === "object" && postCs !== null ? postCs.state : postCs;
    expect(postState).toBe(1); // Checked — no bounce
  });

  it("regression guard: any repo missing from a stale enabled_repos list (not just Flutter) stays checked after a single click", async () => {
    // Same shape as the Flutter case, but with a different repo missing
    // from the seeded list — proves the fix isn't keyed on a specific
    // repo name. Any future user with a similar config asymmetry
    // (machine YAML lists N-1 of N workspace repos) gets the same fix.
    const repos = [makeRepo("repo-a"), makeRepo("repo-b"), makeRepo("repo-c"), makeRepo("repo-d")];
    const enabled = makeMockEnabledReposService();
    // repo-b is the odd one out this time.
    enabled.state.value = ["repo-a", "repo-c", "repo-d"];

    provider = new RepositoriesTreeProvider(makeWorkspaceManager(repos), undefined, {
      enabledReposConfigService: enabled.service,
    });
    await provider.getChildren();

    const bItem = (provider as any).cachedRepositories.get("repo-b") as RepositoryTreeItem;
    await provider.handleCheckboxChange({
      items: [[bItem, 1]] as any,
    } as any);

    expect(enabled.state.value).toEqual([]); // scan-all sentinel
    const cs = (bItem as any).checkboxState;
    const state = typeof cs === "object" && cs !== null ? cs.state : cs;
    expect(state).toBe(1); // Checked — no bounce
  });
});
