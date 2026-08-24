/**
 * Regression tests for issue #485 — rate-limit fallback renders zero counts
 * instead of cached data.
 *
 * `getAggregatedStatusCounts()` had two bugs, confirmed by reading the code
 * (the issue's own hypothesis — a TTL-expiry cache *discard* — does not
 * match what's actually there; `fetchIssuesForStatus`/`fetchAllItemsInternal`
 * already return `cached ?? []` on a failed/rate-limited fetch and never
 * write an empty result into the cache):
 *
 *  1. It never consulted `checkRateLimit()` before calling `board.counts`,
 *     unlike the board-fetch path — so it could keep burning API quota
 *     silently and the warning/pause state (`onRateLimitState`) went
 *     inconsistent between the two paths.
 *  2. Once the cached counts expired (TTL) and the live fetch failed — for
 *     *any* reason, not just rate limiting — the `catch` handler returned a
 *     bare `{}` instead of falling back to the counts already sitting in
 *     `boardCountsCache`, so consumers rendered zeros instead of "stale by a
 *     few minutes".
 *
 * These tests seed the counts cache with one successful fetch, expire the
 * TTL, then simulate (a) a rate-limited refresh and (b) a transient IPC
 * failure, and assert the previously seeded counts are still returned — with
 * the pause/staleness state exposed via `getRateLimitState()`, the same
 * channel the board-fetch path already uses.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { ProjectBoardService } from "../../src/services/ProjectBoardService";

// Match the existing service test mocks (see ProjectBoardService.interface.test.ts).
const mockBoardList = vi.fn();
const mockConfigGetProjectConfig = vi.fn();
const mockBoardCounts = vi.fn();
const mockGithubRateLimit = vi.fn();

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

const SEEDED_COUNTS = { Ready: 3, "In progress": 2, "In review": 1, Done: 5, Backlog: 4 };
const CACHE_TTL_MS = 1000;

describe("ProjectBoardService — stale-if-error counts (#485)", () => {
  let service: ProjectBoardService;

  beforeEach(() => {
    vi.clearAllMocks();
    vi.useFakeTimers();
    mockConfigGetProjectConfig.mockResolvedValue({
      owner: "test-org",
      defaultRepo: "test-repo",
      projectNumber: 42,
      ownerType: "organization",
    });
    mockGithubRateLimit.mockResolvedValue({ remaining: 5000, limit: 5000, resetAt: 0 });
    service = new ProjectBoardService("/test/workspace", CACHE_TTL_MS);
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("serves the seeded cache — not zeros — when a post-TTL refresh is rate-limited, and exposes the pause state", async () => {
    // Seed: one successful fetch populates boardCountsCache.
    mockBoardCounts.mockResolvedValueOnce(SEEDED_COUNTS);
    const seeded = await service.getAggregatedStatusCounts();
    expect(seeded).toEqual(SEEDED_COUNTS);
    expect(mockBoardCounts).toHaveBeenCalledTimes(1);

    // Expire the TTL.
    vi.setSystemTime(new Date(Date.now() + CACHE_TTL_MS + 1));

    // Simulate a rate-limited window — the counts path must now consult the
    // same checkRateLimit() the board-fetch path uses (AC3), and must not
    // reach board.counts at all while exhausted.
    const resetAt = Math.floor(Date.now() / 1000) + 600;
    mockGithubRateLimit.mockResolvedValueOnce({ remaining: 0, limit: 5000, resetAt });

    const result = await service.getAggregatedStatusCounts();

    expect(result).toEqual(SEEDED_COUNTS); // never zeros, expired TTL included
    expect(mockBoardCounts).toHaveBeenCalledTimes(1); // no live call while exhausted
    // Called once for the seed fetch and once for this refresh — the counts
    // path now performs the same rate-limit check as the board-fetch path
    // (AC3) on every attempt, not just the first.
    expect(mockGithubRateLimit).toHaveBeenCalledTimes(2);

    // Staleness/pause state is exposed the same way the board-fetch path
    // already exposes it — consumers read this to know data is stale.
    const state = service.getRateLimitState();
    expect(state?.exhausted).toBe(true);
    expect(state?.resetAt).toBe(resetAt);
  });

  it("serves the seeded cache when quota is fine but the counts fetch itself throws (transient error)", async () => {
    mockBoardCounts.mockResolvedValueOnce(SEEDED_COUNTS);
    const seeded = await service.getAggregatedStatusCounts();
    expect(seeded).toEqual(SEEDED_COUNTS);

    vi.setSystemTime(new Date(Date.now() + CACHE_TTL_MS + 1));

    mockBoardCounts.mockRejectedValueOnce(new Error("HTTP 502: Bad Gateway"));

    const result = await service.getAggregatedStatusCounts();

    expect(result).toEqual(SEEDED_COUNTS); // stale cache, never {}
  });

  // #485 should-fix — invalidateStatusCache() is the MOST common
  // invalidation path (fires on every pipeline status move). Before this
  // fix it nulled boardCountsCache outright, so a post-invalidation failed
  // fetch had no stale data left to fall back to — reopening the exact
  // "zeros" symptom #485 was filed for, just reached via a different path
  // than the TTL-expiry cases above.
  it("invalidateStatusCache expires the counts cache without discarding it — a post-invalidation failed fetch still serves stale data", async () => {
    mockBoardCounts.mockResolvedValueOnce(SEEDED_COUNTS);
    const seeded = await service.getAggregatedStatusCounts();
    expect(seeded).toEqual(SEEDED_COUNTS);

    service.invalidateStatusCache("test-org/test-repo", ["ready"]);

    // Quota is exhausted immediately after invalidation — the counts path
    // must not proceed to board.counts, and the fallback must be the
    // seeded counts, not {}.
    const resetAt = Math.floor(Date.now() / 1000) + 600;
    mockGithubRateLimit.mockResolvedValueOnce({ remaining: 0, limit: 5000, resetAt });

    const result = await service.getAggregatedStatusCounts();

    expect(result).toEqual(SEEDED_COUNTS); // never {}, even right after invalidation
    expect(mockBoardCounts).toHaveBeenCalledTimes(1); // no live call while exhausted
  });

  // #485 — pins the spread-copy on both return paths: a caller mutating the
  // object it received back must never corrupt boardCountsCache for the
  // NEXT caller within the same TTL window.
  it("returns a copy of the cached counts on the TTL-hit path — mutating the result must not corrupt boardCountsCache", async () => {
    // Seed the cache. This call goes through the fetch-success path (a
    // DIFFERENT return statement from the one under test below), so its
    // result is not useful for pinning the TTL-hit branch specifically.
    mockBoardCounts.mockResolvedValueOnce(SEEDED_COUNTS);
    await service.getAggregatedStatusCounts();

    // This call — still within TTL — is served by the cache-hit branch
    // (`if (this.boardCountsCache && ... ) return { ...this.boardCountsCache };`).
    // Under a naive fix that returns `this.boardCountsCache` directly (no
    // copy), `first` IS the cache object, and mutating it corrupts the
    // cache for every subsequent reader.
    const first = await service.getAggregatedStatusCounts();
    expect(first).toEqual(SEEDED_COUNTS);
    (first as Record<string, number>).Ready = 999;

    // A THIRD call, still within TTL, hits the exact same cache-hit branch
    // again. If `first` above was a copy, boardCountsCache is untouched and
    // this returns Ready: 3. If `first` was the cache object itself, this
    // observes the mutation and returns Ready: 999.
    const second = await service.getAggregatedStatusCounts();
    expect(second.Ready).toBe(3);
    expect(mockBoardCounts).toHaveBeenCalledTimes(1); // all three calls served from cache/one fetch
  });
});
