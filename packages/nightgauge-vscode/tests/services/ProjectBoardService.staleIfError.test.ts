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

vi.mock("../../src/utils/incrediConfig", () => ({
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
});
