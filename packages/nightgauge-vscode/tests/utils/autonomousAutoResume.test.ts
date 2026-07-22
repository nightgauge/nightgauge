/**
 * Tests for autonomousAutoResume (#3307) — auto-resume path triggered when
 * the stall watchdog observes a successful sweep AND autonomous was paused
 * with a self-clearing reason.
 *
 * Pins the deadlock the bug surfaced: a rate-limit-triggered pause never
 * resolves on its own because paused autonomous never makes new GitHub calls.
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const autonomousStatus = vi.fn();
const autonomousResume = vi.fn();

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      autonomousStatus: () => autonomousStatus(),
      autonomousResume: () => autonomousResume(),
    }),
  },
}));

import {
  autoResumeAfterRecovery,
  _selfClearingReasonsForTests,
} from "../../src/utils/autonomousAutoResume";

const mockLogger = {
  debug: vi.fn(),
  info: vi.fn(),
  warn: vi.fn(),
  error: vi.fn(),
} as unknown as Parameters<typeof autoResumeAfterRecovery>[0];

beforeEach(() => {
  autonomousStatus.mockReset();
  autonomousResume.mockReset();
  (mockLogger.info as ReturnType<typeof vi.fn>).mockReset();
  (mockLogger.warn as ReturnType<typeof vi.fn>).mockReset();
});

afterEach(() => {
  vi.clearAllMocks();
});

describe("autoResumeAfterRecovery", () => {
  it("resumes when paused with rate-limit-circuit-breaker reason", async () => {
    autonomousStatus.mockResolvedValue({
      status: "paused",
      pauseReason: "GitHub API rate limit hit — circuit breaker opened",
      pauseTriggeredBy: "rate-limit-circuit-breaker",
    });
    autonomousResume.mockResolvedValue({ status: "running" });

    const resumed = await autoResumeAfterRecovery(mockLogger);

    expect(resumed).toBe(true);
    expect(autonomousResume).toHaveBeenCalledTimes(1);
    expect(mockLogger.info).toHaveBeenCalledWith(
      "Auto-resuming autonomous after transient pause cleared",
      expect.objectContaining({ pauseTriggeredBy: "rate-limit-circuit-breaker" })
    );
  });

  it("resumes when paused with network-outage-circuit-breaker reason", async () => {
    autonomousStatus.mockResolvedValue({
      status: "paused",
      pauseReason: "GitHub unreachable — outage breaker opened",
      pauseTriggeredBy: "network-outage-circuit-breaker",
    });
    autonomousResume.mockResolvedValue({ status: "running" });

    const resumed = await autoResumeAfterRecovery(mockLogger);

    expect(resumed).toBe(true);
    expect(autonomousResume).toHaveBeenCalledTimes(1);
  });

  it("does NOT resume when paused by user (manual pause)", async () => {
    autonomousStatus.mockResolvedValue({
      status: "paused",
      pauseReason: "User pause via status bar",
      pauseTriggeredBy: "user",
    });

    const resumed = await autoResumeAfterRecovery(mockLogger);

    expect(resumed).toBe(false);
    expect(autonomousResume).not.toHaveBeenCalled();
  });

  it("does NOT resume when paused by lifetime-failure-cap (#3020)", async () => {
    autonomousStatus.mockResolvedValue({
      status: "paused",
      pauseReason: "issue X has failed 2 times — manual triage required",
      pauseTriggeredBy: "safety:lifetime-failure-cap",
    });

    const resumed = await autoResumeAfterRecovery(mockLogger);

    expect(resumed).toBe(false);
    expect(autonomousResume).not.toHaveBeenCalled();
  });

  // Issue #3605 bullet C invariant: cascade-failure pauses MUST require
  // explicit operator Resume(). Promoting `safety:cascading-failures` into
  // the self-clearing set would defeat the whole point of the breaker.
  it("does NOT resume when paused by cascading-failures (#3605 C)", async () => {
    autonomousStatus.mockResolvedValue({
      status: "safety_tripped",
      pauseReason:
        "cascading-failures: 3 pipeline failures in the last 30m0s (...). Manual triage required.",
      pauseTriggeredBy: "safety:cascading-failures",
    });

    const resumed = await autoResumeAfterRecovery(mockLogger);

    expect(resumed).toBe(false);
    expect(autonomousResume).not.toHaveBeenCalled();
  });

  it("does NOT resume when status is running", async () => {
    autonomousStatus.mockResolvedValue({ status: "running" });

    const resumed = await autoResumeAfterRecovery(mockLogger);

    expect(resumed).toBe(false);
    expect(autonomousResume).not.toHaveBeenCalled();
  });

  it("does NOT resume when status is safety_tripped", async () => {
    // safety_tripped requires manual triage — auto-resume must never bypass it.
    autonomousStatus.mockResolvedValue({
      status: "safety_tripped",
      pauseTriggeredBy: "safety:rail-check",
    });

    const resumed = await autoResumeAfterRecovery(mockLogger);

    expect(resumed).toBe(false);
    expect(autonomousResume).not.toHaveBeenCalled();
  });

  it("does NOT resume when pauseTriggeredBy is missing", async () => {
    // Defensive: an old/legacy pause without provenance must not auto-resume.
    autonomousStatus.mockResolvedValue({
      status: "paused",
      pauseReason: "no reason provided",
    });

    const resumed = await autoResumeAfterRecovery(mockLogger);

    expect(resumed).toBe(false);
    expect(autonomousResume).not.toHaveBeenCalled();
  });

  it("swallows IPC errors and returns false", async () => {
    autonomousStatus.mockRejectedValue(new Error("IPC connection lost"));

    const resumed = await autoResumeAfterRecovery(mockLogger);

    expect(resumed).toBe(false);
    expect(mockLogger.warn).toHaveBeenCalledWith(
      "Auto-resume after recovery failed (best-effort)",
      expect.objectContaining({ error: "IPC connection lost" })
    );
  });

  it("swallows resume errors and returns false (defense-in-depth)", async () => {
    autonomousStatus.mockResolvedValue({
      status: "paused",
      pauseTriggeredBy: "rate-limit-circuit-breaker",
    });
    autonomousResume.mockRejectedValue(new Error("scheduler unavailable"));

    const resumed = await autoResumeAfterRecovery(mockLogger);

    expect(resumed).toBe(false);
    expect(mockLogger.warn).toHaveBeenCalled();
  });

  it("self-clearing reason allowlist matches expected breakers", () => {
    const reasons = _selfClearingReasonsForTests();
    expect(reasons.has("rate-limit-circuit-breaker")).toBe(true);
    expect(reasons.has("network-outage-circuit-breaker")).toBe(true);
    expect(reasons.has("user")).toBe(false);
    expect(reasons.has("safety:lifetime-failure-cap")).toBe(false);
    expect(reasons.has("safety:rail-check")).toBe(false);
    // Issue #3605 bullet C — cascade-failure pauses are explicitly NOT
    // self-clearing. Promoting them would mask the structural problem
    // the breaker fired on.
    expect(reasons.has("safety:cascading-failures")).toBe(false);
  });
});
