/**
 * Multi-repository board sharing (#11).
 *
 * `RepositoriesTreeProvider` builds one ProjectBoardService per repository
 * path. When several repositories sit on the same GitHub Project, every one of
 * those services used to ask the same board the same question on each refresh,
 * so refresh cost grew linearly with repository count while the upstream data
 * was identical - and a ProjectV2 read is 17 GraphQL points per page against a
 * 5,000-point hourly budget.
 *
 * These tests pin both halves of the fix: one fetch serves N repositories, and
 * each repository still sees only its own items. The second half is what makes
 * the first half safe - the shared payload is the raw board response, never one
 * repository's filtered view.
 */
import { describe, it, expect, vi, beforeEach } from "vitest";
import { ProjectBoardService } from "../../src/services/ProjectBoardService";
import { sharedBoardSnapshots } from "../../src/services/BoardSnapshotStore";

const mockBoardList = vi.fn();
const mockConfigGetProjectConfig = vi.fn();
const mockBoardCounts = vi.fn();
const mockGithubRateLimit = vi.fn().mockResolvedValue({ remaining: 5000, limit: 5000, resetAt: 0 });

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      boardList: mockBoardList,
      boardCounts: mockBoardCounts,
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

/** Every repository below shares owner + project 42 - one board. */
function repoConfig(repo: string) {
  return { owner: "acme", defaultRepo: repo, projectNumber: 42, ownerType: "organization" };
}

function boardItem(number: number, repo: string) {
  return {
    number,
    title: `Issue ${number}`,
    status: "Ready",
    priority: "P2",
    repo: `acme/${repo}`,
    url: `https://example.com/${number}`,
    labels: [],
  };
}

/** One board carrying items from three different repositories. */
const SHARED_BOARD = [
  boardItem(1, "alpha"),
  boardItem(2, "beta"),
  boardItem(3, "alpha"),
  boardItem(4, "gamma"),
];

function serviceFor(repo: string): ProjectBoardService {
  mockConfigGetProjectConfig.mockImplementation(async (root: string) => {
    const name = String(root).split("/").pop() ?? repo;
    return repoConfig(name);
  });
  return new ProjectBoardService(`/workspace/${repo}`);
}

describe("ProjectBoardService - shared board across repositories", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockGithubRateLimit.mockResolvedValue({ remaining: 5000, limit: 5000, resetAt: 0 });
    mockBoardList.mockResolvedValue(SHARED_BOARD);
  });

  it("issues one board read for three repositories on the same project", async () => {
    const services = ["alpha", "beta", "gamma"].map(serviceFor);

    await Promise.all(services.map((s) => s.getIssuesByStatus("ready")));

    expect(mockBoardList).toHaveBeenCalledTimes(1);
  });

  it("keeps the read count flat as repository count grows", async () => {
    for (const repos of [["alpha"], ["alpha", "beta"], ["alpha", "beta", "gamma"]]) {
      vi.clearAllMocks();
      mockBoardList.mockResolvedValue(SHARED_BOARD);
      // Each round measures one refresh from cold, so the store is reset here
      // as well as between tests — otherwise round two would read round one's
      // snapshot and report zero fetches, which would pass for the wrong
      // reason.
      sharedBoardSnapshots.clear();
      const services = repos.map(serviceFor);

      await Promise.all(services.map((s) => s.getIssuesByStatus("ready")));

      expect(mockBoardList, `${repos.length} repositories`).toHaveBeenCalledTimes(1);
    }
  });

  // The correctness half. Sharing the *filtered* view would hand beta alpha's
  // issues - a board-wide saving turned into a silently wrong answer.
  it("gives each repository only its own items from the shared payload", async () => {
    const alpha = serviceFor("alpha");
    const beta = serviceFor("beta");

    const alphaIssues = await alpha.getIssuesByStatus("ready");
    const betaIssues = await beta.getIssuesByStatus("ready");

    expect(mockBoardList).toHaveBeenCalledTimes(1);
    expect(alphaIssues.map((i) => i.number).sort()).toEqual([1, 3]);
    expect(betaIssues.map((i) => i.number)).toEqual([2]);
  });

  it("shares the expensive unfiltered read between prefetch and getAllItems", async () => {
    const alpha = serviceFor("alpha");
    const beta = serviceFor("beta");

    await alpha.prefetchAllItems();
    await beta.getAllItems();

    expect(mockBoardList).toHaveBeenCalledTimes(1);
  });

  it("counts a shared read as one miss and the rest as hits or joins", async () => {
    const services = ["alpha", "beta", "gamma"].map(serviceFor);

    await Promise.all(services.map((s) => s.getIssuesByStatus("ready")));

    const metrics = services[0].getBoardSnapshotMetrics();
    expect(metrics.misses).toBe(1);
    expect(metrics.hits + metrics.coalesced).toBe(2);
  });

  // A failure must not be adopted by the other repositories as an answer.
  it("does not let one repository's failure become another's cached result", async () => {
    const alpha = serviceFor("alpha");
    mockBoardList.mockRejectedValueOnce(new Error("HTTP 403"));

    const failed = await alpha.getIssuesByStatus("ready");
    expect(failed).toEqual([]);

    // The next repository gets a real attempt, not the empty failure.
    mockBoardList.mockResolvedValue(SHARED_BOARD);
    const beta = serviceFor("beta");
    const betaIssues = await beta.getIssuesByStatus("ready");

    expect(betaIssues.map((i) => i.number)).toEqual([2]);
  });

  it("force-refresh really refetches even when another repository just read", async () => {
    const alpha = serviceFor("alpha");
    const beta = serviceFor("beta");

    await alpha.prefetchAllItems();
    expect(mockBoardList).toHaveBeenCalledTimes(1);

    // Without invalidating the shared snapshot, force would silently mean
    // "force, unless someone else fetched recently".
    await beta.prefetchAllItems({ force: true });
    expect(mockBoardList).toHaveBeenCalledTimes(2);
  });
});

/**
 * The counts path (#1277). `getAggregatedStatusCounts` is what the
 * Repositories tree calls for EVERY repository row on every root render, and
 * it was the one board read cached per service instance instead of per board.
 * Three rows on one board meant three identical `board.counts` queries plus
 * three rate-limit probes, all in parallel with nothing to coalesce them.
 */
describe("ProjectBoardService - shared board counts across repositories", () => {
  const COUNTS = { ready: 3, inProgress: 1, backlog: 12 };

  beforeEach(() => {
    vi.clearAllMocks();
    sharedBoardSnapshots.clear();
    mockGithubRateLimit.mockResolvedValue({ remaining: 5000, limit: 5000, resetAt: 0 });
    mockBoardCounts.mockResolvedValue(COUNTS);
  });

  it("issues one board.counts call and one rate-limit check for three concurrent repositories", async () => {
    const services = ["alpha", "beta", "gamma"].map(serviceFor);

    const results = await Promise.all(services.map((s) => s.getAggregatedStatusCounts()));

    expect(mockBoardCounts).toHaveBeenCalledTimes(1);
    expect(mockGithubRateLimit).toHaveBeenCalledTimes(1);
    for (const r of results) expect(r).toEqual(COUNTS);
  });

  it("serves a later repository from the shared snapshot inside the TTL", async () => {
    await serviceFor("alpha").getAggregatedStatusCounts();
    await serviceFor("beta").getAggregatedStatusCounts();

    expect(mockBoardCounts).toHaveBeenCalledTimes(1);
  });

  it("hands each caller its own copy - mutating one result cannot corrupt the shared counts", async () => {
    const first = await serviceFor("alpha").getAggregatedStatusCounts();
    first.ready = 999;

    const second = await serviceFor("beta").getAggregatedStatusCounts();

    expect(second.ready).toBe(COUNTS.ready);
  });

  it("clearCache on one service makes the next read go to the network again", async () => {
    const alpha = serviceFor("alpha");
    const beta = serviceFor("beta");
    await alpha.getAggregatedStatusCounts();
    expect(mockBoardCounts).toHaveBeenCalledTimes(1);

    alpha.clearCache();
    await beta.getAggregatedStatusCounts();

    expect(mockBoardCounts).toHaveBeenCalledTimes(2);
  });

  it("an exhausted quota is not cached: joined callers get the stale counts and the next window refetches", async () => {
    const alpha = serviceFor("alpha");
    await alpha.getAggregatedStatusCounts();
    alpha.softInvalidate();

    mockGithubRateLimit.mockResolvedValue({
      remaining: 0,
      limit: 5000,
      resetAt: Math.floor(Date.now() / 1000) + 3600,
    });
    const gated = await Promise.all(
      ["alpha", "beta"].map((r) => serviceFor(r).getAggregatedStatusCounts())
    );

    // Still one network read - the refused fetch never happened - and both
    // repositories were served the last-known-good counts, not zeros.
    expect(mockBoardCounts).toHaveBeenCalledTimes(1);
    for (const r of gated) expect(r).toEqual(COUNTS);
  });
});
