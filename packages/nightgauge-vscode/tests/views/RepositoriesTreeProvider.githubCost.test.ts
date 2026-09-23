/**
 * RepositoriesTreeProvider — what the Repositories view costs the GitHub quota.
 *
 * The workspace shares one personal 5,000-point/hour GraphQL budget across the
 * pipeline, the daemon, every `gh` call and the maintainer. Opening the
 * extension and expanding this view was measured moving that budget from 88 to
 * 521 points in about sixteen minutes, with nothing else running.
 *
 * These tests drive the REAL tree provider over the REAL per-repository
 * providers, wired the way the extension wires them (bootstrap/services.ts:
 * `setProjectBoardServices` with one `ReadyIssueTreeProvider` around
 * `createWorkItemProvider(...)` per repository), with only the IPC client
 * faked, and count every IPC verb the view sends. Wiring bare
 * ProjectBoardService instances instead once hid that the wrapper dropped the
 * open read, so production kept paying three board reads per repository. The verbs are the unit because each GitHub-bound verb maps to a
 * known daemon handler (internal/ipc/server.go) and from there to a known
 * number of GraphQL reads:
 *
 *   - `board.list {status}`  one `items(query:"status:X is:open")` read per
 *                            board and status, 17 points per 100-item page;
 *   - `board.listOpen`       one `items(query:"is:open")` read per board, the
 *                            SAME daemon snapshot `board.counts` and the
 *                            attention sweeps read, 17 points per page;
 *   - `board.counts`         derived from that same open snapshot;
 *   - `github.rateLimit`     one GraphQL `rateLimit` read per user per 15s;
 *   - `config.getProjectConfig`, `autonomous.status`  local, no GitHub.
 *
 * "Expanding the view" is what VS Code does: ask the root for its rows, then
 * ask every EXPANDED row (every repository in the autonomous scan set) for
 * its children. Status nodes render collapsed, so their drilldowns are a
 * separate, user-driven cost and are not part of opening the view.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { RepositoriesTreeProvider } from "../../src/views/RepositoriesTreeProvider";
import { RepositoryTreeItem } from "../../src/views/items/RepositoryTreeItem";
import { IssueSummaryTreeItem } from "../../src/views/items/IssueSummaryTreeItem";
import { ProjectBoardService } from "../../src/services/ProjectBoardService";
import { sharedBoardSnapshots } from "../../src/services/BoardSnapshotStore";
import { ReadyIssueTreeProvider } from "../../src/views/ReadyIssueTreeProvider";
import { createWorkItemProvider } from "../../src/bootstrap/workItemProviderFactory";
import { PollingVisibilityGate } from "../../src/services/AttentionSweepService";
import type { WorkspaceManager } from "../../src/services/WorkspaceManager";
import type { Repository } from "../../src/models/Repository";
import type { IWorkItemProvider } from "../../src/services/types/WorkItemProvider";

// ─── Fake IPC: counts every verb, answers the board verbs from a fixture ────

/** Verbs whose handlers reach GitHub (see the file header). */
const GITHUB_VERBS = new Set(["boardList", "boardListOpen", "boardCounts", "githubRateLimit"]);

const ipcCalls: Array<{ verb: string; args: unknown[] }> = [];
const eventHandlers = new Map<string, Set<(data: unknown) => void>>();

function emitIpc(event: string, data?: unknown): void {
  for (const h of eventHandlers.get(event) ?? []) h(data);
}

function boardItem(number: number, repo: string, status: string, labels: string[] = []) {
  return {
    number,
    title: `Issue ${number}`,
    status,
    priority: "P2",
    repo: `acme/${repo}`,
    url: `https://example.com/${repo}/${number}`,
    labels,
  };
}

/** Each repository sits on its own board: project number = index + 1. */
const REPOS = ["alpha", "beta", "gamma", "delta", "epsilon"];

function openItemsFor(repo: string) {
  return [
    boardItem(1, repo, "Ready"),
    boardItem(2, repo, "Ready"),
    boardItem(3, repo, "In progress"),
    boardItem(4, repo, "Backlog"),
    boardItem(5, repo, "Backlog", ["type:epic"]),
    boardItem(6, repo, "In review"),
  ];
}

function repoForProject(projectNumber: number): string {
  return REPOS[projectNumber - 1];
}

const fakeIpc: Record<string, unknown> = {
  on: (event: string, handler: (data: unknown) => void) => {
    if (!eventHandlers.has(event)) eventHandlers.set(event, new Set());
    eventHandlers.get(event)!.add(handler);
    return { dispose: () => eventHandlers.get(event)?.delete(handler) };
  },
  configGetProjectConfig: async (root: string) => {
    const repo = String(root).split("/").pop()!;
    return {
      owner: "acme",
      defaultRepo: repo,
      projectNumber: REPOS.indexOf(repo) + 1,
      ownerType: "organization",
    };
  },
  githubRateLimit: async () => ({ remaining: 5000, limit: 5000, resetAt: 0 }),
  boardList: async (_owner: string, projectNumber: number, status?: string) =>
    openItemsFor(repoForProject(projectNumber)).filter(
      (i) => !status || i.status.toLowerCase() === status.toLowerCase()
    ),
  boardListOpen: async (_owner: string, projectNumber: number) =>
    openItemsFor(repoForProject(projectNumber)),
  boardCounts: async () => ({ ready: 2, inProgress: 1, inReview: 1, backlog: 2 }),
  // Composite mode's repo source: every repository issue, board-less ones included.
  issueList: async (_owner: string, repo: string) => [
    ...openItemsFor(repo).map((i) => ({ ...i, state: "OPEN" })),
    { number: 99, title: "Off-board", url: "", labels: [], state: "OPEN" },
  ],
  autonomousStatus: async () => ({ status: "stopped" }),
};

const countingIpc = new Proxy(fakeIpc, {
  get(target, prop: string) {
    const value = target[prop];
    if (typeof value !== "function" || prop === "on") return value;
    return (...args: unknown[]) => {
      ipcCalls.push({ verb: prop, args });
      return (value as (...a: unknown[]) => unknown)(...args);
    };
  },
});

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: { getInstance: () => countingIpc },
  IpcClientBase: class {},
}));

vi.mock("../../src/utils/configPathResolver", () => ({
  getRepoIdentity: async (root: string) => ({ owner: "acme", repo: String(root).split("/").pop() }),
}));

vi.mock("../../src/utils/nightgaugeConfig", () => ({
  getGitHubUser: vi.fn().mockReturnValue("test-user"),
}));

// Epic grouping ON — the shipped default (`nightgauge.projectBoard.groupByEpic`).
vi.mock("../../src/config/projectBoardSettings", () => ({
  getProjectBoardSettings: () => ({ groupByEpic: true, defaultEpicCollapsed: false }),
}));

vi.mock("../../src/services/ConfigBridge", () => ({
  ConfigBridge: {
    getInstance: () => ({
      onConfigChanged: () => ({ dispose: () => {} }),
      getEffectiveConfig: () => ({ config: {} }),
      reload: vi.fn(async () => {}),
    }),
  },
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
      public color?: unknown
    ) {}
  },
  ThemeColor: class {
    constructor(public id: string) {}
  },
  MarkdownString: class {
    value = "";
    constructor(value = "") {
      this.value = value;
    }
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
    showWarningMessage: vi.fn(),
    state: { focused: true },
    onDidChangeWindowState: vi.fn(() => ({ dispose: () => {} })),
  },
  workspace: { workspaceFolders: [] },
  Disposable: class {
    dispose() {}
  },
  TreeCheckboxChangeEvent: class {},
}));

// ─── Workspace fixture ──────────────────────────────────────────────────────

function makeRepo(name: string): Repository {
  return {
    name,
    path: `/workspace/${name}`,
    role: "primary",
    isConfigLoaded: true,
    github: { owner: "acme", repo: name },
    loadConfig: vi.fn().mockResolvedValue(undefined),
  } as unknown as Repository;
}

function makeWorkspaceManager(repos: Repository[]): WorkspaceManager {
  return {
    isInitialized: () => true,
    isMultiWorkspace: () => true,
    getAllRepositories: () => repos,
    getRepository: (name: string) => repos.find((r) => r.name === name),
    getRepositoryCount: () => repos.length,
    findRepositoryByGitHub: (slug: string) => repos.find((r) => `acme/${r.name}` === slug),
    getSharedProjectNumber: () => undefined,
    areReposDerivedFromProject: () => false,
    onWorkspaceChanged: () => ({ dispose: () => {} }),
  } as unknown as WorkspaceManager;
}

// ─── Helpers ────────────────────────────────────────────────────────────────

function githubBoundCalls(): string[] {
  return ipcCalls.filter((c) => GITHUB_VERBS.has(c.verb)).map((c) => c.verb);
}

/**
 * The extension's own wiring (bootstrap/services.ts, onWorkspaceChanged and the
 * startup sync): one ReadyIssueTreeProvider around the configured provider per
 * repository, handed to the view through setProjectBoardServices.
 */
function wireLikeProduction(
  provider: RepositoriesTreeProvider,
  repos: Repository[],
  mode: "github" | "composite" = "github"
): void {
  const services = new Map<string, IWorkItemProvider>();
  for (const repo of repos) {
    services.set(
      repo.name,
      new ReadyIssueTreeProvider(createWorkItemProvider({ mode }, repo.path))
    );
  }
  provider.setProjectBoardServices(services);
}

function makeProvider(repos: Repository[], mode: "github" | "composite" = "github") {
  const provider = new RepositoriesTreeProvider(
    makeWorkspaceManager(repos),
    (path) => new ProjectBoardService(path)
  );
  wireLikeProduction(provider, repos, mode);
  return provider;
}

/** What VS Code does when the view opens or re-renders from the root. */
async function expandView(provider: RepositoriesTreeProvider): Promise<IssueSummaryTreeItem[][]> {
  const rows = (await provider.getChildren()).filter(
    (i): i is RepositoryTreeItem => i instanceof RepositoryTreeItem
  );
  const out: IssueSummaryTreeItem[][] = [];
  for (const row of rows) {
    if (row.collapsibleState !== 2) continue; // VS Code only asks expanded rows
    out.push((await provider.getChildren(row)) as IssueSummaryTreeItem[]);
  }
  return out;
}

describe("RepositoriesTreeProvider — GitHub cost of the view", () => {
  let provider: RepositoriesTreeProvider;

  beforeEach(() => {
    ipcCalls.length = 0;
    eventHandlers.clear();
    // Production services use the process-wide store; start each test cold.
    sharedBoardSnapshots.clear();
    PollingVisibilityGate.instance.setViewVisible("repositoriesView", true);
    provider = makeProvider(REPOS.map(makeRepo));
  });

  afterEach(() => {
    provider.dispose();
    vi.useRealTimers();
  });

  it("opening the view with 5 repositories issues at most one GitHub-bound call per board", async () => {
    await expandView(provider);

    // Each repository is on its own board, so 5 boards → at most 5 board
    // reads plus one rate-limit gate per read. Before the fix this was
    // 3 × board.list + 3 × github.rateLimit per repository = 30.
    const verbs = githubBoundCalls();
    expect(verbs.filter((v) => v.startsWith("board")).length).toBeLessThanOrEqual(5);
    expect(verbs.length).toBeLessThanOrEqual(10);
    expect(verbs).not.toContain("boardList");
  });

  it("per-repo counts still exclude epics and other statuses", async () => {
    const children = await expandView(provider);
    expect(children).toHaveLength(5);
    for (const row of children) {
      const counts = Object.fromEntries(row.map((c) => [c.statusType, c.count]));
      // Ready 2, In progress 1, Backlog 2 minus the type:epic item = 1.
      expect(counts).toEqual({ ready: 2, inProgress: 1, backlog: 1 });
    }
  });

  it("a second expand within the cache TTL issues no GitHub-bound call", async () => {
    await expandView(provider);
    ipcCalls.length = 0;

    await expandView(provider);

    expect(githubBoundCalls()).toEqual([]);
  });

  it("ipc.ready (daemon restart) with a fresh cache re-renders but issues no GitHub-bound call", async () => {
    await expandView(provider);
    ipcCalls.length = 0;

    vi.useFakeTimers();
    let rerenders = 0;
    provider.onDidChangeTreeData(() => {
      rerenders += 1;
    });
    emitIpc("ipc.ready", { protocolVersion: 2 });
    await vi.advanceTimersByTimeAsync(600);
    vi.useRealTimers();

    // The #484 MF-4 guarantee stands: the reconnect still re-renders the view.
    expect(rerenders).toBe(1);
    await expandView(provider);
    expect(githubBoundCalls()).toEqual([]);
  });

  it("an explicit Refresh still refetches every repository", async () => {
    await expandView(provider);
    ipcCalls.length = 0;

    provider.invalidateAndRefreshRepo();
    await expandView(provider);

    const boardReads = githubBoundCalls().filter((v) => v.startsWith("board"));
    expect(boardReads.length).toBe(5);
  });

  it("repositories sharing one board share one read", async () => {
    provider.dispose();
    ipcCalls.length = 0;
    const sharedRepos = ["alpha", "beta", "gamma"].map((name) => ({
      ...makeRepo(name),
      path: `/workspace/alpha`, // same config → same board (project 1)
      name,
    })) as unknown as Repository[];
    provider = makeProvider(sharedRepos);

    await expandView(provider);

    expect(githubBoundCalls().filter((v) => v.startsWith("board")).length).toBe(1);
  });

  it("composite mode reads each board's open snapshot, not the per-status lists", async () => {
    provider.dispose();
    ipcCalls.length = 0;
    provider = makeProvider(REPOS.map(makeRepo), "composite");

    const children = await expandView(provider);

    const verbs = githubBoundCalls();
    expect(verbs).not.toContain("boardList");
    expect(verbs.filter((v) => v === "boardListOpen").length).toBe(5);
    // The off-board repository issue (#99) counts under its inferred status
    // (Ready: open, unlabelled, unblocked), as composite reads always counted it.
    for (const row of children) {
      const counts = Object.fromEntries(row.map((c) => [c.statusType, c.count]));
      expect(counts).toEqual({ ready: 3, inProgress: 1, backlog: 1 });
    }
  });
});
