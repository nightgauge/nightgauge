import { describe, it, expect, vi, beforeEach } from "vitest";
import {
  DependabotPRService,
  PRODUCER_DEPENDABOT_STALE_REMEDIATION,
  STALE_DAYS_THRESHOLD,
} from "../../src/services/DependabotPRService";
import type { AdvisorySource, RemediationAdvisory } from "../../src/services/DependabotPRService";
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

    mockPrList.mockRejectedValue(new Error("rate limit exceeded"));
    const data = await service.getData();

    expect(data.prs).toHaveLength(0);
    expect(data.staleCount).toBe(0);
    // Nobody looked. The empty escalation list must not read as "nothing stale".
    const report = await service.getEscalations();
    expect(report.escalations).toEqual([]);
    expect(report.measured).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// Staleness escalation (#345)
//
// The defect these pin is not a wrong count — the count was always right. It is
// that a measured, threshold-crossing condition produced NOTHING an operator
// could act on. Asserting `staleCount` therefore passes against the bug; every
// assertion below is about the ESCALATION, and about the fact that its severity
// comes from the advisory rather than from a `security` label.
// ---------------------------------------------------------------------------

describe("DependabotPRService — staleness escalates", () => {
  const daysAgo = (n: number) => new Date(Date.now() - n * 86_400_000).toISOString();

  function advisorySourceOf(...advisories: RemediationAdvisory[]): AdvisorySource {
    return vi.fn(async () => advisories);
  }

  function serviceWith(source?: AdvisorySource): DependabotPRService {
    return new DependabotPRService(mockIpc, OWNER, REPO, mockLogger, source);
  }

  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("escalates a remediation PR past the threshold — not merely a count", async () => {
    mockPrList.mockResolvedValueOnce([
      makePR({
        number: 41,
        createdAt: daysAgo(STALE_DAYS_THRESHOLD + 33),
        labels: ["dependencies"],
      }),
    ]);
    const service = serviceWith(
      advisorySourceOf({
        alertNumber: 7,
        prNumber: 41,
        severity: "critical",
        advisoryId: "GHSA-xxxx-yyyy-zzzz",
        packageName: "lodash",
      })
    );

    const data = await service.getData();
    const report = await service.getEscalations();

    expect(data.staleCount).toBe(1);
    expect(report.measured).toBe(true);
    expect(report.escalations).toHaveLength(1);
    const escalation = report.escalations[0];
    expect(escalation.producer).toBe(PRODUCER_DEPENDABOT_STALE_REMEDIATION);
    expect(escalation.prNumber).toBe(41);
    expect(escalation.alertNumber).toBe(7);
    expect(escalation.severity).toBe("critical");
    expect(escalation.advisoryId).toBe("GHSA-xxxx-yyyy-zzzz");
    expect(escalation.idempotencyKey).toBe(
      `${PRODUCER_DEPENDABOT_STALE_REMEDIATION}:${OWNER}/${REPO}#41`
    );
    expect(escalation.fingerprint).toBe("sev:critical;pr:41;over:5");
  });

  it("does not escalate a remediation PR under the threshold", async () => {
    mockPrList.mockResolvedValueOnce([
      makePR({ number: 42, createdAt: daysAgo(STALE_DAYS_THRESHOLD - 1) }),
    ]);
    const service = serviceWith(
      advisorySourceOf({ alertNumber: 8, prNumber: 42, severity: "high" })
    );

    const data = await service.getData();
    const report = await service.getEscalations();

    expect(data.prs[0].isStale).toBe(false);
    expect(report.measured).toBe(true);
    expect(report.escalations).toEqual([]);
  });

  it("reads severity from the advisory, not from the `security` label", async () => {
    // #43 wears the `security` label and has NO advisory behind it — a routine
    // bump somebody labelled. #44 wears no `security` label and IS the
    // remediation for a critical advisory. A label-driven escalation gets both
    // of these backwards.
    mockPrList.mockResolvedValueOnce([
      makePR({ number: 43, createdAt: daysAgo(30), labels: ["dependencies", "security"] }),
      makePR({ number: 44, createdAt: daysAgo(30), labels: ["dependencies"] }),
    ]);
    const service = serviceWith(
      advisorySourceOf({ alertNumber: 9, prNumber: 44, severity: "critical" })
    );

    const data = await service.getData();
    const report = await service.getEscalations();

    // The label-derived tab classification is untouched...
    expect(data.securityCount).toBe(1);
    expect(data.prs[0].prType).toBe("security");
    // ...and it is emphatically not what decided the escalation.
    expect(report.escalations.map((e) => e.prNumber)).toEqual([44]);
    expect(report.escalations[0].severity).toBe("critical");
  });

  it("orders escalations most-severe-first, then by PR number", async () => {
    mockPrList.mockResolvedValueOnce([
      makePR({ number: 50, createdAt: daysAgo(20) }),
      makePR({ number: 51, createdAt: daysAgo(20) }),
      makePR({ number: 52, createdAt: daysAgo(20) }),
    ]);
    const service = serviceWith(
      advisorySourceOf(
        { alertNumber: 1, prNumber: 50, severity: "moderate" },
        { alertNumber: 2, prNumber: 51, severity: "critical" },
        { alertNumber: 3, prNumber: 52, severity: "moderate" }
      )
    );

    const report = await service.getEscalations();

    expect(report.escalations.map((e) => e.prNumber)).toEqual([51, 50, 52]);
  });

  it("names the worst advisory when several are grouped behind one PR", async () => {
    mockPrList.mockResolvedValueOnce([makePR({ number: 60, createdAt: daysAgo(14) })]);
    const service = serviceWith(
      advisorySourceOf(
        { alertNumber: 11, prNumber: 60, severity: "low" },
        { alertNumber: 12, prNumber: 60, severity: "high" },
        { alertNumber: 13, prNumber: 60, severity: "moderate" }
      )
    );

    const report = await service.getEscalations();

    expect(report.escalations).toHaveLength(1);
    expect(report.escalations[0].severity).toBe("high");
    expect(report.escalations[0].alertNumber).toBe(12);
  });

  it("reports unmeasured — not clean — when no advisory source is attached", async () => {
    mockPrList.mockResolvedValueOnce([makePR({ number: 70, createdAt: daysAgo(40) })]);
    const service = serviceWith(undefined);

    const data = await service.getData();
    const report = await service.getEscalations();

    expect(data.staleCount).toBe(1);
    expect(report.escalations).toEqual([]);
    expect(report.measured).toBe(false);
  });

  it("reports unmeasured when the advisory read fails, and still returns the PRs", async () => {
    mockPrList.mockResolvedValueOnce([makePR({ number: 71, createdAt: daysAgo(40) })]);
    const service = serviceWith(
      vi.fn(async () => {
        throw new Error("security_events scope missing");
      })
    );

    const data = await service.getData();
    const report = await service.getEscalations();

    expect(data.prs).toHaveLength(1);
    expect(report.escalations).toEqual([]);
    expect(report.measured).toBe(false);
    expect(mockLogger.warn).toHaveBeenCalled();
  });

  it("holds the fingerprint steady within a staleness bucket and moves it across one", async () => {
    const source = advisorySourceOf({ alertNumber: 20, prNumber: 80, severity: "high" });

    mockPrList.mockResolvedValueOnce([makePR({ number: 80, createdAt: daysAgo(8) })]);
    const first = await serviceWith(source).getEscalations();

    mockPrList.mockResolvedValueOnce([makePR({ number: 80, createdAt: daysAgo(13) })]);
    const sameBucket = await serviceWith(source).getEscalations();

    mockPrList.mockResolvedValueOnce([makePR({ number: 80, createdAt: daysAgo(15) })]);
    const nextBucket = await serviceWith(source).getEscalations();

    expect(sameBucket.escalations[0].fingerprint).toBe(first.escalations[0].fingerprint);
    expect(nextBucket.escalations[0].fingerprint).not.toBe(first.escalations[0].fingerprint);
    // Sticky identity never moves — it is the same condition throughout.
    expect(nextBucket.escalations[0].idempotencyKey).toBe(first.escalations[0].idempotencyKey);
  });
});
