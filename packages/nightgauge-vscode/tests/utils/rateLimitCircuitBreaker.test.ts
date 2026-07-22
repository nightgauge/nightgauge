/**
 * Tests for rate-limit circuit breaker — #3020.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const showWarningMessage = vi.fn();
const autonomousStatus = vi.fn();
const autonomousPause = vi.fn();
const githubRateLimit = vi.fn();

vi.mock("vscode", () => ({
  window: {
    showWarningMessage: (...args: unknown[]) => showWarningMessage(...args),
  },
}));

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      autonomousStatus: () => autonomousStatus(),
      autonomousPause: () => autonomousPause(),
      githubRateLimit: () => githubRateLimit(),
    }),
  },
}));

import {
  _isBreakerTrippedForTests,
  _resetBreakerForTests,
  isBreakerTripped,
  isGithubRateLimitError,
  noteRateLimitOk,
  tripBreakerIfRateLimited,
} from "../../src/utils/rateLimitCircuitBreaker";

const mockLogger = {
  debug: vi.fn(),
  info: vi.fn(),
  warn: vi.fn(),
  error: vi.fn(),
} as unknown as Parameters<typeof tripBreakerIfRateLimited>[1];

beforeEach(() => {
  _resetBreakerForTests();
  showWarningMessage.mockReset();
  autonomousStatus.mockReset();
  autonomousPause.mockReset();
  githubRateLimit.mockReset();
});

afterEach(() => {
  _resetBreakerForTests();
});

describe("isBreakerTripped", () => {
  it("returns false when breaker has never been tripped", () => {
    expect(isBreakerTripped()).toBe(false);
  });

  it("returns true after a rate-limit error trips the breaker", async () => {
    autonomousStatus.mockResolvedValue({ status: "running" });
    autonomousPause.mockResolvedValue(undefined);
    githubRateLimit.mockResolvedValue({ remaining: 0, limit: 5000, resetAt: 0 });

    await tripBreakerIfRateLimited(new Error("API rate limit already exceeded"), mockLogger, {
      source: "watchdog",
    });

    expect(isBreakerTripped()).toBe(true);
  });

  it("returns false after noteRateLimitOk re-arms the breaker", async () => {
    autonomousStatus.mockResolvedValue({ status: "running" });
    autonomousPause.mockResolvedValue(undefined);
    githubRateLimit.mockResolvedValue({ remaining: 0, limit: 5000, resetAt: 0 });

    await tripBreakerIfRateLimited(new Error("rate limit exceeded"), mockLogger, {
      source: "test",
    });
    expect(isBreakerTripped()).toBe(true);

    noteRateLimitOk();
    expect(isBreakerTripped()).toBe(false);
  });
});

describe("isGithubRateLimitError", () => {
  it("matches the gh CLI rate limit error", () => {
    expect(
      isGithubRateLimitError(new Error("API rate limit already exceeded for user ID 1459355."))
    ).toBe(true);
  });

  it("matches the secondary rate limit", () => {
    expect(isGithubRateLimitError("You have triggered an abuse detection (secondary rate)")).toBe(
      true
    );
  });

  it("matches a 429 status code in stderr", () => {
    expect(isGithubRateLimitError("HTTP 429: Too Many Requests")).toBe(true);
  });

  it("ignores unrelated errors", () => {
    expect(isGithubRateLimitError(new Error("network unreachable"))).toBe(false);
    expect(isGithubRateLimitError(undefined)).toBe(false);
    expect(isGithubRateLimitError("")).toBe(false);
  });
});

describe("tripBreakerIfRateLimited", () => {
  it("trips and pauses autonomous on a rate-limit error", async () => {
    autonomousStatus.mockResolvedValue({ status: "running" });
    autonomousPause.mockResolvedValue(undefined);
    githubRateLimit.mockResolvedValue({
      remaining: 0,
      limit: 5000,
      resetAt: Date.now() / 1000 + 600,
    });

    const tripped = await tripBreakerIfRateLimited(
      new Error("API rate limit already exceeded for user ID 1459355."),
      mockLogger,
      { source: "post-merge verification", issueNumber: 283 }
    );

    expect(tripped).toBe(true);
    expect(autonomousPause).toHaveBeenCalledTimes(1);
    expect(showWarningMessage).toHaveBeenCalledTimes(1);
    expect(_isBreakerTrippedForTests()).toBe(true);
  });

  it("does not trip on unrelated errors", async () => {
    const tripped = await tripBreakerIfRateLimited(new Error("git push failed"), mockLogger, {
      source: "test",
    });
    expect(tripped).toBe(false);
    expect(autonomousPause).not.toHaveBeenCalled();
    expect(_isBreakerTrippedForTests()).toBe(false);
  });

  it("dedupes concurrent rate-limit failures into a single pause", async () => {
    autonomousStatus.mockResolvedValue({ status: "running" });
    autonomousPause.mockResolvedValue(undefined);
    githubRateLimit.mockResolvedValue({ remaining: 0, limit: 5000, resetAt: 0 });

    const err = new Error("API rate limit already exceeded");
    await tripBreakerIfRateLimited(err, mockLogger, { source: "a" });
    await tripBreakerIfRateLimited(err, mockLogger, { source: "b" });
    await tripBreakerIfRateLimited(err, mockLogger, { source: "c" });

    expect(autonomousPause).toHaveBeenCalledTimes(1);
    expect(showWarningMessage).toHaveBeenCalledTimes(1);
  });

  it("does not call autonomousPause when already paused", async () => {
    autonomousStatus.mockResolvedValue({ status: "paused" });
    githubRateLimit.mockResolvedValue({ remaining: 0, limit: 5000, resetAt: 0 });

    await tripBreakerIfRateLimited(new Error("API rate limit already exceeded"), mockLogger, {
      source: "watchdog",
    });

    expect(autonomousPause).not.toHaveBeenCalled();
    // Toast still fires so the user sees the rate limit notice.
    expect(showWarningMessage).toHaveBeenCalledTimes(1);
  });

  it("noteRateLimitOk re-arms the breaker", async () => {
    autonomousStatus.mockResolvedValue({ status: "running" });
    autonomousPause.mockResolvedValue(undefined);
    githubRateLimit.mockResolvedValue({ remaining: 0, limit: 5000, resetAt: 0 });

    await tripBreakerIfRateLimited(new Error("rate limit exceeded"), mockLogger, {
      source: "first",
    });
    expect(_isBreakerTrippedForTests()).toBe(true);

    noteRateLimitOk();
    expect(_isBreakerTrippedForTests()).toBe(false);

    // After re-arming, a new rate-limit hit must trigger pause again.
    await tripBreakerIfRateLimited(new Error("rate limit exceeded"), mockLogger, {
      source: "second",
    });
    expect(autonomousPause).toHaveBeenCalledTimes(2);
  });

  it("swallows IPC failures so the original error path is never blocked", async () => {
    autonomousStatus.mockRejectedValue(new Error("IPC down"));
    autonomousPause.mockRejectedValue(new Error("IPC down"));
    githubRateLimit.mockRejectedValue(new Error("IPC down"));

    const tripped = await tripBreakerIfRateLimited(new Error("rate limit exceeded"), mockLogger, {
      source: "x",
    });

    expect(tripped).toBe(true); // breaker still flips; caller can short-circuit
  });
});
