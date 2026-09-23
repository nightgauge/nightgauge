/**
 * The open read (`board.listOpen`) and the caches around it.
 *
 * The Repositories tree takes its row counts from getOpenIssues instead of one
 * `board.list` per status. Two things the per-status reads used to do as a side
 * effect have to survive that switch:
 *
 *   - they filled the per-status `cache`, which the cache-only readers scan:
 *     the filter picker's Component section and epic metadata;
 *   - a status move (invalidateStatusCache) had to make the next drilldown
 *     refetch, so a row count and the list under it agree.
 */
import { describe, it, expect, vi, beforeEach } from "vitest";
import { ProjectBoardService } from "../../src/services/ProjectBoardService";
import { sharedBoardSnapshots } from "../../src/services/BoardSnapshotStore";

const mockBoardList = vi.fn();
const mockBoardListOpen = vi.fn();
const mockConfigGetProjectConfig = vi.fn();
const mockGithubRateLimit = vi.fn();

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      boardList: mockBoardList,
      boardListOpen: mockBoardListOpen,
      configGetProjectConfig: mockConfigGetProjectConfig,
      githubRateLimit: mockGithubRateLimit,
    }),
  },
}));

vi.mock("vscode", () => ({
  EventEmitter: class {
    private _handlers: Array<(v: unknown) => void> = [];
    event = (cb: (v: unknown) => void) => {
      this._handlers.push(cb);
      return { dispose: () => {} };
    };
    fire(value?: unknown) {
      for (const h of this._handlers) h(value);
    }
    dispose() {}
  },
  window: {
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
    showWarningMessage: vi.fn(),
  },
  Disposable: class {
    dispose() {}
  },
}));

vi.mock("../../src/utils/nightgaugeConfig", () => ({
  getGitHubUser: vi.fn().mockReturnValue("test-user"),
}));

function item(number: number, status: string, extra: Record<string, unknown> = {}) {
  return {
    number,
    title: `Issue ${number}`,
    status,
    priority: "P2",
    repo: "acme/alpha",
    url: `https://example.com/${number}`,
    labels: [] as string[],
    ...extra,
  };
}

const OPEN_BOARD = [
  item(1, "Ready", { labels: ["component:vscode"] }),
  item(2, "In progress", { labels: ["component:ipc"] }),
  item(3, "Backlog", { labels: ["type:epic"], isEpic: true }),
];

function service(): ProjectBoardService {
  return new ProjectBoardService("/workspace/alpha");
}

describe("ProjectBoardService.getOpenIssues", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    sharedBoardSnapshots.clear();
    mockConfigGetProjectConfig.mockResolvedValue({
      owner: "acme",
      defaultRepo: "alpha",
      projectNumber: 7,
      ownerType: "organization",
    });
    mockGithubRateLimit.mockResolvedValue({ remaining: 5000, limit: 5000, resetAt: 0 });
    mockBoardListOpen.mockResolvedValue(OPEN_BOARD);
  });

  it("fills the per-status cache the Component filter and epic metadata read", async () => {
    const svc = service();

    await svc.getOpenIssues();

    expect(svc.getObservedComponents()).toEqual(["ipc", "vscode"]);
    expect([...svc.getEpicMetadataFromCache().keys()]).toEqual([3]);
    expect(svc.getItemsByStatusFromCache("Ready").map((i) => i.number)).toEqual([1]);
    expect(svc.getItemsByStatusFromCache("In progress").map((i) => i.number)).toEqual([2]);
    expect(svc.getItemsByStatusFromCache("Backlog").map((i) => i.number)).toEqual([3]);
    // One read, no per-status reads behind it.
    expect(mockBoardListOpen).toHaveBeenCalledTimes(1);
    expect(mockBoardList).not.toHaveBeenCalled();
  });

  it("empties a status bucket whose last issue moved out of it", async () => {
    const svc = service();
    await svc.getOpenIssues();

    mockBoardListOpen.mockResolvedValue([item(1, "In progress"), ...OPEN_BOARD.slice(1)]);
    svc.invalidateStatusCache("acme/alpha", ["ready", "in-progress"]);
    await svc.getOpenIssues();

    expect(svc.getItemsByStatusFromCache("Ready")).toEqual([]);
    expect(svc.getItemsByStatusFromCache("In progress").map((i) => i.number)).toEqual([1, 2]);
  });
});

describe("ProjectBoardService.invalidateStatusCache", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    sharedBoardSnapshots.clear();
    mockConfigGetProjectConfig.mockResolvedValue({
      owner: "acme",
      defaultRepo: "alpha",
      projectNumber: 7,
      ownerType: "organization",
    });
    mockGithubRateLimit.mockResolvedValue({ remaining: 5000, limit: 5000, resetAt: 0 });
    mockBoardListOpen.mockResolvedValue(OPEN_BOARD);
    mockBoardList.mockImplementation(async (_o: string, _p: number, status: string) =>
      OPEN_BOARD.filter((i) => i.status === status)
    );
  });

  it("expires the shared per-status snapshot, so a drilldown agrees with the row count", async () => {
    const svc = service();
    // Warm both reads: the row count (open) and the Ready drilldown (per status).
    await svc.getOpenIssues();
    expect((await svc.getIssuesByStatus("ready")).map((i) => i.number)).toEqual([1]);

    // #1 moves Ready -> In progress.
    const moved = [item(1, "In progress"), ...OPEN_BOARD.slice(1)];
    mockBoardListOpen.mockResolvedValue(moved);
    mockBoardList.mockImplementation(async (_o: string, _p: number, status: string) =>
      moved.filter((i) => i.status === status)
    );
    svc.invalidateStatusCache("acme/alpha", ["ready", "in-progress"]);

    const open = await svc.getOpenIssues();
    const readyCount = open.filter((i) => i.status === "Ready").length;
    const readyDrilldown = await svc.getIssuesByStatus("ready");
    const inProgressDrilldown = await svc.getIssuesByStatus("in-progress");

    expect(readyCount).toBe(0);
    expect(readyDrilldown).toEqual([]);
    expect(inProgressDrilldown.map((i) => i.number).sort()).toEqual([1, 2]);
  });

  it("keeps the expired per-status snapshot as the stale-if-error fallback", async () => {
    const svc = service();
    await svc.getIssuesByStatus("ready");

    svc.invalidateStatusCache("acme/alpha", ["ready"]);
    mockBoardList.mockRejectedValue(new Error("network down"));

    expect((await svc.getIssuesByStatus("ready")).map((i) => i.number)).toEqual([1]);
    expect(mockBoardList).toHaveBeenCalledTimes(2);
  });

  it("leaves statuses it was not told about fresh", async () => {
    const svc = service();
    await svc.getIssuesByStatus("backlog");

    svc.invalidateStatusCache("acme/alpha", ["ready", "in-progress"]);
    await svc.getIssuesByStatus("backlog");

    expect(mockBoardList).toHaveBeenCalledTimes(1);
  });
});
