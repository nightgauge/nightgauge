import { describe, it, expect, vi, beforeEach } from "vitest";
import { DependabotPRService } from "../../src/services/DependabotPRService";
import type { PullRequestDetail } from "../../src/services/IpcClientBase";
import type { IpcClient } from "../../src/services/IpcClient";
import type { Logger } from "../../src/utils/logger";

// ---------------------------------------------------------------------------
// Mocks
// ---------------------------------------------------------------------------

vi.mock("../../src/utils/rateLimitCircuitBreaker", () => ({
  tripBreakerIfRateLimited: vi.fn().mockResolvedValue(false),
}));

const mockPrList = vi.fn<[string, string, object?], Promise<PullRequestDetail[]>>();

const mockIpc = {
  prList: mockPrList,
} as unknown as IpcClient;

const mockLogger = {
  error: vi.fn(),
  warn: vi.fn(),
  info: vi.fn(),
} as unknown as Logger;

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

const OWNER = "acme";
const REPO = "myrepo";

function makePR(overrides: Partial<PullRequestDetail> = {}): PullRequestDetail {
  return {
    nodeId: "PR_1",
    number: 1,
    title: "Bump lodash from 4.17.20 to 4.17.21",
    state: "OPEN",
    headRef: "dependabot/npm_and_yarn/lodash-4.17.21",
    baseRef: "main",
    repo: `${OWNER}/${REPO}`,
    url: "https://github.com/acme/myrepo/pull/1",
    isDraft: false,
    labels: ["dependencies"],
    createdAt: new Date(Date.now() - 8 * 86_400_000).toISOString(), // 8 days ago
    ...overrides,
  };
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("DependabotPRService", () => {
  let service: DependabotPRService;

  beforeEach(() => {
    vi.clearAllMocks();
    service = new DependabotPRService(mockIpc, OWNER, REPO, mockLogger);
  });

  it("returns only dependabot PRs (filters non-dependabot PRs)", async () => {
    mockPrList.mockResolvedValueOnce([
      makePR({ labels: ["dependencies"] }),
      { ...makePR({ nodeId: "PR_2", number: 2, title: "Feature work" }), labels: ["enhancement"] },
    ]);

    const data = await service.getData();
    expect(data.prs).toHaveLength(1);
    expect(data.prs[0].number).toBe(1);
  });

  it("computes staleDays correctly based on createdAt", async () => {
    const createdAt = new Date(Date.now() - 10 * 86_400_000).toISOString(); // 10 days ago
    mockPrList.mockResolvedValueOnce([makePR({ createdAt })]);

    const data = await service.getData();
    expect(data.prs[0].staleDays).toBe(10);
  });

  it("flags isStale when staleDays >= 7", async () => {
    const staleCreatedAt = new Date(Date.now() - 8 * 86_400_000).toISOString();
    const freshCreatedAt = new Date(Date.now() - 3 * 86_400_000).toISOString();
    mockPrList.mockResolvedValueOnce([
      makePR({ nodeId: "PR_1", number: 1, createdAt: staleCreatedAt }),
      makePR({ nodeId: "PR_2", number: 2, createdAt: freshCreatedAt }),
    ]);

    const data = await service.getData();
    expect(data.prs[0].isStale).toBe(true);
    expect(data.prs[1].isStale).toBe(false);
    expect(data.staleCount).toBe(1);
  });

  it("classifies security PRs via labels", async () => {
    mockPrList.mockResolvedValueOnce([
      makePR({ labels: ["dependencies", "security"] }),
      makePR({ nodeId: "PR_2", number: 2, labels: ["dependencies"] }),
    ]);

    const data = await service.getData();
    expect(data.securityCount).toBe(1);
    expect(data.prs[0].prType).toBe("security");
    expect(data.prs[1].prType).toBe("dependency");
  });

  it("uses cache on second call within TTL", async () => {
    mockPrList.mockResolvedValue([makePR()]);

    await service.getData();
    await service.getData();

    expect(mockPrList).toHaveBeenCalledTimes(1);
  });

  it("invalidates cache when invalidate() is called", async () => {
    mockPrList.mockResolvedValue([makePR()]);

    await service.getData();
    service.invalidate();
    await service.getData();

    expect(mockPrList).toHaveBeenCalledTimes(2);
  });

  it("force-refreshes when forceRefresh=true even within TTL", async () => {
    mockPrList.mockResolvedValue([makePR()]);

    await service.getData();
    await service.getData(true);

    expect(mockPrList).toHaveBeenCalledTimes(2);
  });

  it("trips rate-limit breaker and returns cached data on rate-limit error", async () => {
    const { tripBreakerIfRateLimited } = await import("../../src/utils/rateLimitCircuitBreaker");
    vi.mocked(tripBreakerIfRateLimited).mockResolvedValue(true);

    // Prime the cache first
    mockPrList.mockResolvedValueOnce([makePR()]);
    const firstData = await service.getData();

    // Second call throws a rate-limit-like error
    mockPrList.mockRejectedValueOnce(new Error("rate limit exceeded"));
    service.invalidate();
    const secondData = await service.getData(true);

    expect(secondData.prs).toEqual(firstData.prs);
  });

  it("returns empty data when no cache exists and rate-limit tripped", async () => {
    const { tripBreakerIfRateLimited } = await import("../../src/utils/rateLimitCircuitBreaker");
    vi.mocked(tripBreakerIfRateLimited).mockResolvedValue(true);

    mockPrList.mockRejectedValueOnce(new Error("rate limit exceeded"));
    const data = await service.getData();

    expect(data.prs).toHaveLength(0);
    expect(data.staleCount).toBe(0);
  });
});
