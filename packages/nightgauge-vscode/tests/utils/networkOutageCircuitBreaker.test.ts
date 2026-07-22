/**
 * Tests for the network-outage circuit breaker (Issue #3296).
 *
 * The breaker is exercised through observeWatchdogResult — the same surface
 * the autonomous stall watchdog uses. Tests cover:
 *   1. Connectivity-error classifier accuracy on real production strings.
 *   2. Threshold gating (no trip below threshold; trip exactly at threshold).
 *   3. Idempotency (only one trip per outage; alreadyTripped flag).
 *   4. IPC interaction (calls pipelineCancelActiveForNetworkOutage on trip).
 *   5. Auto-recovery (success-shaped observation resets counter).
 *   6. Best-effort behavior (IPC failure doesn't throw).
 */
import { describe, it, expect, beforeEach, vi } from "vitest";
import {
  isConnectivityError,
  observeWatchdogResult,
  _resetForTests,
  _stateForTests,
  DEFAULT_CONNECTIVITY_THRESHOLD,
} from "../../src/utils/networkOutageCircuitBreaker";

const mockCancelActive = vi.fn();
vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      pipelineCancelActiveForNetworkOutage: mockCancelActive,
    }),
  },
}));

const silentLogger = {
  info: vi.fn(),
  warn: vi.fn(),
  error: vi.fn(),
  debug: vi.fn(),
} as unknown as import("../../src/utils/logger").Logger;

beforeEach(() => {
  _resetForTests();
  mockCancelActive.mockReset();
  mockCancelActive.mockResolvedValue({ cancelledIssues: [3216, 3230] });
});

describe("isConnectivityError", () => {
  it.each([
    ["dial tcp: lookup api.github.com: no such host"],
    [
      'fetch board items (filtered): Post "https://api.github.com/graphql": dial tcp: lookup api.github.com: no such host',
    ],
    ["IPC request board.list timed out after 30000ms"],
    ["TypeError: fetch failed"],
    ["getaddrinfo ENOTFOUND api.github.com"],
    ["connect ECONNREFUSED 140.82.112.6:443"],
    ["connect ETIMEDOUT 140.82.112.6:443"],
    ["read ECONNRESET"],
    ["Network is unreachable"],
    ["error connecting to api.github.com\ncheck your internet connection"],
  ])("classifies %p as connectivity error", (msg) => {
    expect(isConnectivityError(new Error(msg))).toBe(true);
  });

  it.each([
    ["GitHub API rate limit exceeded"],
    ["secondary rate limit hit"],
    ["403 forbidden — bad credentials"],
    ["schema validation failed: missing field 'foo'"],
    ["pipeline_budget_exceeded: stage feature-dev"],
    ["exit 1: subagent crashed"],
    [""],
  ])("does NOT classify %p as connectivity error", (msg) => {
    expect(isConnectivityError(msg ? new Error(msg) : null)).toBe(false);
  });

  it("handles null and undefined inputs", () => {
    expect(isConnectivityError(null)).toBe(false);
    expect(isConnectivityError(undefined)).toBe(false);
  });
});

describe("observeWatchdogResult — threshold gating", () => {
  it("does not trip below threshold", async () => {
    for (let i = 0; i < DEFAULT_CONNECTIVITY_THRESHOLD - 1; i++) {
      const result = await observeWatchdogResult(
        new Error("dial tcp: lookup api.github.com: no such host"),
        silentLogger
      );
      expect(result.tripped).toBe(false);
      expect(result.classified).toBe("connectivity");
      expect(result.consecutiveFailures).toBe(i + 1);
    }
    expect(_stateForTests().tripped).toBe(false);
    expect(mockCancelActive).not.toHaveBeenCalled();
  });

  it("trips exactly at threshold", async () => {
    let lastResult: Awaited<ReturnType<typeof observeWatchdogResult>> | undefined;
    for (let i = 0; i < DEFAULT_CONNECTIVITY_THRESHOLD; i++) {
      lastResult = await observeWatchdogResult(
        new Error("getaddrinfo ENOTFOUND api.github.com"),
        silentLogger
      );
    }
    expect(lastResult?.tripped).toBe(true);
    expect(_stateForTests().tripped).toBe(true);
    expect(mockCancelActive).toHaveBeenCalledTimes(1);
  });

  it("respects custom threshold", async () => {
    const r1 = await observeWatchdogResult(new Error("ECONNREFUSED"), silentLogger, {
      source: "test",
      threshold: 1,
    });
    expect(r1.tripped).toBe(true);
    expect(mockCancelActive).toHaveBeenCalledTimes(1);
  });
});

describe("observeWatchdogResult — idempotency", () => {
  it("does not double-trip during a single outage", async () => {
    // Cross threshold.
    for (let i = 0; i < DEFAULT_CONNECTIVITY_THRESHOLD; i++) {
      await observeWatchdogResult(new Error("ENOTFOUND"), silentLogger);
    }
    expect(mockCancelActive).toHaveBeenCalledTimes(1);

    // Continued failures shouldn't fire IPC again.
    const more = await observeWatchdogResult(new Error("ENOTFOUND"), silentLogger);
    expect(more.tripped).toBe(false);
    expect(more.alreadyTripped).toBe(true);
    expect(mockCancelActive).toHaveBeenCalledTimes(1);
  });
});

describe("observeWatchdogResult — non-connectivity errors", () => {
  it("does not trip on rate-limit errors", async () => {
    for (let i = 0; i < DEFAULT_CONNECTIVITY_THRESHOLD + 2; i++) {
      const result = await observeWatchdogResult(
        new Error("API rate limit exceeded"),
        silentLogger
      );
      expect(result.tripped).toBe(false);
      expect(result.classified).toBe("other");
    }
    expect(mockCancelActive).not.toHaveBeenCalled();
  });

  it("does not reset connectivity counter on non-connectivity error mid-outage", async () => {
    // Two connectivity failures.
    await observeWatchdogResult(new Error("ENOTFOUND"), silentLogger);
    await observeWatchdogResult(new Error("ENOTFOUND"), silentLogger);
    expect(_stateForTests().consecutiveFailures).toBe(2);

    // One non-connectivity error in the middle (e.g., a transient parse error).
    await observeWatchdogResult(new Error("schema validation failed"), silentLogger);
    expect(_stateForTests().consecutiveFailures).toBe(2);

    // Third connectivity failure trips the breaker.
    const r = await observeWatchdogResult(new Error("ENOTFOUND"), silentLogger);
    expect(r.tripped).toBe(true);
  });
});

describe("observeWatchdogResult — auto-recovery", () => {
  it("resets counter and re-arms breaker on first success", async () => {
    // Trip the breaker.
    for (let i = 0; i < DEFAULT_CONNECTIVITY_THRESHOLD; i++) {
      await observeWatchdogResult(new Error("ECONNREFUSED"), silentLogger);
    }
    expect(_stateForTests().tripped).toBe(true);

    // Successful sweep.
    const recovery = await observeWatchdogResult(null, silentLogger);
    expect(recovery.tripped).toBe(false);
    expect(_stateForTests()).toEqual({
      consecutiveFailures: 0,
      tripped: false,
    });

    // Now another outage trips again.
    for (let i = 0; i < DEFAULT_CONNECTIVITY_THRESHOLD; i++) {
      await observeWatchdogResult(new Error("ENOTFOUND"), silentLogger);
    }
    expect(_stateForTests().tripped).toBe(true);
    expect(mockCancelActive).toHaveBeenCalledTimes(2);
  });

  it("logs recovery only when there were prior failures", async () => {
    const logger = {
      info: vi.fn(),
      warn: vi.fn(),
      error: vi.fn(),
      debug: vi.fn(),
    } as unknown as import("../../src/utils/logger").Logger;

    // Pure success — no prior failures, no info log.
    await observeWatchdogResult(null, logger);
    expect((logger.info as ReturnType<typeof vi.fn>).mock.calls).toHaveLength(0);

    // After failures, success logs the recovery.
    await observeWatchdogResult(new Error("ENOTFOUND"), logger);
    await observeWatchdogResult(null, logger);
    expect((logger.info as ReturnType<typeof vi.fn>).mock.calls.length).toBeGreaterThan(0);
  });
});

describe("observeWatchdogResult — best-effort IPC", () => {
  it("does not throw when the IPC call fails", async () => {
    mockCancelActive.mockRejectedValueOnce(new Error("backend unavailable"));
    let trippedResult: Awaited<ReturnType<typeof observeWatchdogResult>> | undefined;
    for (let i = 0; i < DEFAULT_CONNECTIVITY_THRESHOLD; i++) {
      trippedResult = await observeWatchdogResult(new Error("ENOTFOUND"), silentLogger);
    }
    expect(trippedResult?.tripped).toBe(true);
    // Breaker still flips even if IPC fails — we'll just have no cancelled list.
    expect(_stateForTests().tripped).toBe(true);
  });
});
