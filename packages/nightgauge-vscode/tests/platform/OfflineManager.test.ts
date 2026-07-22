/**
 * OfflineManager.test.ts
 *
 * Comprehensive unit tests for the OfflineManager class covering:
 * - Initial state (starts offline)
 * - start()/stop() lifecycle
 * - State machine transitions (online → degraded → offline, recovery)
 * - Event emission with ConnectionStateEvent fields
 * - Timeout handling (AbortError treated as failure)
 * - Fallback registry (register, getStrategy)
 * - IHealthChecker injection
 * - dispose() cleanup
 * - Configurable thresholds
 *
 * @see Issue #1459 - Offline detection and degraded mode fallback framework
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import type { ConnectionStateEvent } from "../../src/platform/types";

// ---------------------------------------------------------------------------
// vscode mock — functional EventEmitter that supports subscribe + fire
// ---------------------------------------------------------------------------
vi.mock("vscode", () => {
  class MockEventEmitter<T> {
    private listeners: ((e: T) => void)[] = [];
    event = (listener: (e: T) => void) => {
      this.listeners.push(listener);
      return {
        dispose: () => {
          const idx = this.listeners.indexOf(listener);
          if (idx >= 0) this.listeners.splice(idx, 1);
        },
      };
    };
    fire(data: T) {
      for (const l of this.listeners) l(data);
    }
    dispose() {
      this.listeners = [];
    }
  }
  return { EventEmitter: MockEventEmitter };
});

// ---------------------------------------------------------------------------
// Global fetch mock
// ---------------------------------------------------------------------------
const fetchMock = vi.fn<(input: RequestInfo | URL, init?: RequestInit) => Promise<Response>>();
vi.stubGlobal("fetch", fetchMock);

// Import after mocks
import { OfflineManager } from "../../src/platform/OfflineManager";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function createManager(
  overrides: Partial<{
    baseUrl: string;
    intervalMs: number;
    timeoutMs: number;
    failureThreshold: number;
  }> = {},
  checker?: {
    checkHealth: () => Promise<{ reachable: boolean; degraded?: boolean }>;
  }
) {
  const baseUrl = overrides.baseUrl ?? "https://api.test.com";
  return new OfflineManager(
    {
      getBaseUrl: () => baseUrl,
      intervalMs: overrides.intervalMs ?? 60_000,
      timeoutMs: overrides.timeoutMs ?? 10_000,
      failureThreshold: overrides.failureThreshold ?? 3,
    },
    checker
  );
}

function okResponse(): Promise<Response> {
  return Promise.resolve({ ok: true } as Response);
}

function failResponse(): Promise<Response> {
  return Promise.resolve({ ok: false, status: 500 } as Response);
}

function networkError(): Promise<Response> {
  return Promise.reject(new Error("fetch failed"));
}

// ---------------------------------------------------------------------------
// Tests
// ---------------------------------------------------------------------------

describe("OfflineManager", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    fetchMock.mockReset();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  // =========================================================================
  // Initial state
  // =========================================================================
  describe("initial state", () => {
    it("starts in offline state", () => {
      const mgr = createManager();
      expect(mgr.state).toBe("offline");
      mgr.dispose();
    });

    it("does not emit state events before start() is called", () => {
      const mgr = createManager();
      const handler = vi.fn();
      mgr.onStateChanged(handler);
      expect(handler).not.toHaveBeenCalled();
      mgr.dispose();
    });
  });

  // =========================================================================
  // start() / stop()
  // =========================================================================
  describe("start() / stop()", () => {
    it("runs health check immediately on start()", async () => {
      fetchMock.mockImplementation(okResponse);
      const mgr = createManager();
      mgr.start();

      // Flush the immediate _tick() microtask
      await vi.advanceTimersByTimeAsync(0);

      expect(fetchMock).toHaveBeenCalledTimes(1);
      expect(fetchMock).toHaveBeenCalledWith(
        "https://api.test.com/v1/health",
        expect.objectContaining({ signal: expect.any(AbortSignal) })
      );
      mgr.dispose();
    });

    it("runs health check on configured interval", async () => {
      fetchMock.mockImplementation(okResponse);
      const mgr = createManager({ intervalMs: 30_000 });
      mgr.start();

      await vi.advanceTimersByTimeAsync(0); // immediate tick
      expect(fetchMock).toHaveBeenCalledTimes(1);

      await vi.advanceTimersByTimeAsync(30_000); // 1st interval
      expect(fetchMock).toHaveBeenCalledTimes(2);

      await vi.advanceTimersByTimeAsync(30_000); // 2nd interval
      expect(fetchMock).toHaveBeenCalledTimes(3);

      mgr.dispose();
    });

    it("stop() cancels the timer", async () => {
      fetchMock.mockImplementation(okResponse);
      const mgr = createManager({ intervalMs: 10_000 });
      mgr.start();
      await vi.advanceTimersByTimeAsync(0);
      expect(fetchMock).toHaveBeenCalledTimes(1);

      mgr.stop();
      await vi.advanceTimersByTimeAsync(30_000);
      // No additional calls after stop
      expect(fetchMock).toHaveBeenCalledTimes(1);
      mgr.dispose();
    });

    it("start() is idempotent — does not start duplicate timers", async () => {
      fetchMock.mockImplementation(okResponse);
      const mgr = createManager({ intervalMs: 10_000 });
      mgr.start();
      mgr.start(); // second call should be no-op

      await vi.advanceTimersByTimeAsync(0);
      // Only 1 immediate tick, not 2
      expect(fetchMock).toHaveBeenCalledTimes(1);

      await vi.advanceTimersByTimeAsync(10_000);
      // Only 1 interval tick, not 2
      expect(fetchMock).toHaveBeenCalledTimes(2);
      mgr.dispose();
    });
  });

  // =========================================================================
  // State machine — going offline
  // =========================================================================
  describe("state machine — going offline", () => {
    it("transitions offline → degraded → online on first success, then online → degraded on first failure", async () => {
      fetchMock.mockImplementationOnce(okResponse); // tick 0 → online
      const mgr = createManager();
      mgr.start();
      await vi.advanceTimersByTimeAsync(0);
      expect(mgr.state).toBe("online");

      fetchMock.mockImplementationOnce(failResponse); // tick 1 → degraded
      await vi.advanceTimersByTimeAsync(60_000);
      expect(mgr.state).toBe("degraded");
      mgr.dispose();
    });

    it("stays degraded after 2 failures (below threshold=3)", async () => {
      // Start online
      fetchMock.mockImplementationOnce(okResponse);
      const mgr = createManager();
      mgr.start();
      await vi.advanceTimersByTimeAsync(0);
      expect(mgr.state).toBe("online");

      // 1st failure → degraded
      fetchMock.mockImplementationOnce(failResponse);
      await vi.advanceTimersByTimeAsync(60_000);
      expect(mgr.state).toBe("degraded");

      // 2nd failure → still degraded (threshold=3)
      fetchMock.mockImplementationOnce(failResponse);
      await vi.advanceTimersByTimeAsync(60_000);
      expect(mgr.state).toBe("degraded");
      mgr.dispose();
    });

    it("transitions degraded → offline after 3 consecutive failures", async () => {
      fetchMock.mockImplementationOnce(okResponse);
      const mgr = createManager();
      mgr.start();
      await vi.advanceTimersByTimeAsync(0);
      expect(mgr.state).toBe("online");

      // 3 consecutive failures
      fetchMock.mockImplementation(failResponse);
      await vi.advanceTimersByTimeAsync(60_000); // failure 1 → degraded
      expect(mgr.state).toBe("degraded");
      await vi.advanceTimersByTimeAsync(60_000); // failure 2 → still degraded
      expect(mgr.state).toBe("degraded");
      await vi.advanceTimersByTimeAsync(60_000); // failure 3 → offline
      expect(mgr.state).toBe("offline");
      mgr.dispose();
    });

    it("resets consecutive failure count on success", async () => {
      fetchMock.mockImplementationOnce(okResponse);
      const mgr = createManager();
      mgr.start();
      await vi.advanceTimersByTimeAsync(0);

      // 2 failures
      fetchMock.mockImplementationOnce(failResponse);
      await vi.advanceTimersByTimeAsync(60_000);
      fetchMock.mockImplementationOnce(failResponse);
      await vi.advanceTimersByTimeAsync(60_000);
      expect(mgr.state).toBe("degraded");

      // 1 success → back to online, counter reset
      fetchMock.mockImplementationOnce(okResponse);
      await vi.advanceTimersByTimeAsync(60_000);
      expect(mgr.state).toBe("online");

      // 2 more failures — should NOT go offline (counter was reset)
      fetchMock.mockImplementationOnce(failResponse);
      await vi.advanceTimersByTimeAsync(60_000);
      fetchMock.mockImplementationOnce(failResponse);
      await vi.advanceTimersByTimeAsync(60_000);
      expect(mgr.state).toBe("degraded"); // not offline
      mgr.dispose();
    });
  });

  // =========================================================================
  // State machine — coming back online
  // =========================================================================
  describe("state machine — coming back online", () => {
    it("transitions offline → degraded → online on 1 success", async () => {
      const events: ConnectionStateEvent[] = [];
      fetchMock.mockImplementationOnce(okResponse);
      const mgr = createManager();
      mgr.start();
      await vi.advanceTimersByTimeAsync(0); // online

      // Go offline (3 failures)
      fetchMock.mockImplementation(failResponse);
      await vi.advanceTimersByTimeAsync(60_000);
      await vi.advanceTimersByTimeAsync(60_000);
      await vi.advanceTimersByTimeAsync(60_000);
      expect(mgr.state).toBe("offline");

      // Subscribe to events
      mgr.onStateChanged((e) => events.push(e));

      // 1 success → should go offline → degraded → online
      fetchMock.mockImplementationOnce(okResponse);
      await vi.advanceTimersByTimeAsync(60_000);
      expect(mgr.state).toBe("online");

      // Should have 2 events: offline→degraded, degraded→online
      expect(events).toHaveLength(2);
      expect(events[0].previous).toBe("offline");
      expect(events[0].current).toBe("degraded");
      expect(events[1].previous).toBe("degraded");
      expect(events[1].current).toBe("online");
      mgr.dispose();
    });

    it("transitions degraded → online on 1 success", async () => {
      fetchMock.mockImplementationOnce(okResponse);
      const mgr = createManager();
      mgr.start();
      await vi.advanceTimersByTimeAsync(0);

      // 1 failure → degraded
      fetchMock.mockImplementationOnce(failResponse);
      await vi.advanceTimersByTimeAsync(60_000);
      expect(mgr.state).toBe("degraded");

      // 1 success → online
      fetchMock.mockImplementationOnce(okResponse);
      await vi.advanceTimersByTimeAsync(60_000);
      expect(mgr.state).toBe("online");
      mgr.dispose();
    });

    it("does not re-emit if already online and check succeeds", async () => {
      fetchMock.mockImplementation(okResponse);
      const mgr = createManager();
      const handler = vi.fn();

      mgr.start();
      await vi.advanceTimersByTimeAsync(0); // offline → online

      mgr.onStateChanged(handler);

      // Subsequent successes should not fire events
      await vi.advanceTimersByTimeAsync(60_000);
      await vi.advanceTimersByTimeAsync(60_000);
      expect(handler).not.toHaveBeenCalled();
      mgr.dispose();
    });
  });

  // =========================================================================
  // State machine — event emission
  // =========================================================================
  describe("state machine — event emission", () => {
    it("emits ConnectionStateEvent with previous, current, reason, at fields", async () => {
      fetchMock.mockImplementationOnce(okResponse);
      const mgr = createManager();
      const events: ConnectionStateEvent[] = [];
      mgr.onStateChanged((e) => events.push(e));

      mgr.start();
      await vi.advanceTimersByTimeAsync(0);

      // offline → degraded → online (recovery)
      expect(events.length).toBeGreaterThanOrEqual(1);
      const firstEvent = events[0];
      expect(firstEvent).toHaveProperty("previous");
      expect(firstEvent).toHaveProperty("current");
      expect(firstEvent).toHaveProperty("reason");
      expect(firstEvent).toHaveProperty("at");
      expect(typeof firstEvent.at).toBe("string");
      // at should be a valid ISO 8601 timestamp
      expect(new Date(firstEvent.at).toISOString()).toBe(firstEvent.at);
      mgr.dispose();
    });

    it("emits degraded event before online event during recovery from offline", async () => {
      fetchMock.mockImplementationOnce(okResponse);
      const mgr = createManager();
      mgr.start();
      await vi.advanceTimersByTimeAsync(0); // go online first

      // Go offline
      fetchMock.mockImplementation(failResponse);
      await vi.advanceTimersByTimeAsync(60_000);
      await vi.advanceTimersByTimeAsync(60_000);
      await vi.advanceTimersByTimeAsync(60_000);
      expect(mgr.state).toBe("offline");

      const events: ConnectionStateEvent[] = [];
      mgr.onStateChanged((e) => events.push(e));

      // Recover
      fetchMock.mockImplementationOnce(okResponse);
      await vi.advanceTimersByTimeAsync(60_000);

      expect(events).toHaveLength(2);
      expect(events[0].current).toBe("degraded");
      expect(events[1].current).toBe("online");
      mgr.dispose();
    });

    it("does not emit if state does not change", async () => {
      const mgr = createManager();
      const handler = vi.fn();
      mgr.onStateChanged(handler);

      // Stay offline (already offline, fetch fails)
      fetchMock.mockImplementation(failResponse);
      mgr.start();
      await vi.advanceTimersByTimeAsync(0);
      await vi.advanceTimersByTimeAsync(60_000);
      await vi.advanceTimersByTimeAsync(60_000);

      // No transitions emitted — still offline the whole time
      expect(handler).not.toHaveBeenCalled();
      mgr.dispose();
    });
  });

  // =========================================================================
  // Timeout handling
  // =========================================================================
  describe("timeout handling", () => {
    it("treats fetch timeout (AbortError) as failure", async () => {
      fetchMock.mockImplementation(
        () =>
          new Promise((_resolve, reject) => {
            // Simulate AbortError after timeout
            setTimeout(() => reject(new DOMException("Aborted", "AbortError")), 15_000);
          })
      );

      const mgr = createManager({ timeoutMs: 10_000 });
      mgr.start();

      // The tick fires immediately but fetch hangs. Advance past timeout.
      await vi.advanceTimersByTimeAsync(15_000);

      // Should still be offline (failure from initial offline state)
      expect(mgr.state).toBe("offline");
      mgr.dispose();
    });

    it("increments consecutive failures on timeout", async () => {
      // First go online
      fetchMock.mockImplementationOnce(okResponse);
      const mgr = createManager({ timeoutMs: 5_000 });
      mgr.start();
      await vi.advanceTimersByTimeAsync(0);
      expect(mgr.state).toBe("online");

      // Now simulate network errors (treated as failures)
      fetchMock.mockImplementation(networkError);
      await vi.advanceTimersByTimeAsync(60_000); // failure 1 → degraded
      expect(mgr.state).toBe("degraded");
      await vi.advanceTimersByTimeAsync(60_000); // failure 2
      await vi.advanceTimersByTimeAsync(60_000); // failure 3 → offline
      expect(mgr.state).toBe("offline");
      mgr.dispose();
    });
  });

  // =========================================================================
  // Fallback registry
  // =========================================================================
  describe("fallback registry", () => {
    it("register() stores strategy for operationName", () => {
      const mgr = createManager();
      const strategy = () => "fallback-result";
      mgr.register("skillResolve", strategy);
      expect(mgr.getStrategy("skillResolve")).toBe(strategy);
      mgr.dispose();
    });

    it("getStrategy() returns registered strategy", () => {
      const mgr = createManager();
      const strategy = async () => ({ data: 42 });
      mgr.register("analytics", strategy);
      expect(mgr.getStrategy("analytics")).toBe(strategy);
      mgr.dispose();
    });

    it("getStrategy() returns undefined for unknown operation", () => {
      const mgr = createManager();
      expect(mgr.getStrategy("nonexistent")).toBeUndefined();
      mgr.dispose();
    });

    it("getStrategy() is type-safe — generic parameter flows through", () => {
      const mgr = createManager();
      const strategy = () => 42;
      mgr.register<number>("counter", strategy);

      const retrieved = mgr.getStrategy<number>("counter");
      expect(retrieved).toBeDefined();
      // Verify the strategy returns the expected type
      expect(retrieved!()).toBe(42);
      mgr.dispose();
    });
  });

  // =========================================================================
  // IHealthChecker injection
  // =========================================================================
  describe("IHealthChecker injection", () => {
    it("uses injected checker instead of fetch when provided", async () => {
      const checker = {
        checkHealth: vi.fn().mockResolvedValue({ reachable: true }),
      };
      const mgr = createManager({}, checker);
      mgr.start();
      await vi.advanceTimersByTimeAsync(0);

      expect(checker.checkHealth).toHaveBeenCalledTimes(1);
      expect(fetchMock).not.toHaveBeenCalled();
      expect(mgr.state).toBe("online");
      mgr.dispose();
    });

    it("treats checker exception as failure", async () => {
      const checker = {
        checkHealth: vi.fn().mockRejectedValue(new Error("network down")),
      };
      const mgr = createManager({}, checker);
      mgr.start();
      await vi.advanceTimersByTimeAsync(0);

      // Should remain offline (failure from initial offline)
      expect(mgr.state).toBe("offline");
      mgr.dispose();
    });

    it("treats checker { reachable: false } as failure", async () => {
      const checker = {
        checkHealth: vi.fn().mockResolvedValue({ reachable: false }),
      };
      // First go online via fetch, then switch to checker
      fetchMock.mockImplementationOnce(okResponse);
      const mgr = createManager();
      mgr.start();
      await vi.advanceTimersByTimeAsync(0);
      mgr.dispose();

      // Fresh manager with checker
      const mgr2 = createManager({}, checker);
      mgr2.start();
      await vi.advanceTimersByTimeAsync(0);
      expect(mgr2.state).toBe("offline"); // no transition from offline
      mgr2.dispose();
    });
  });

  // =========================================================================
  // dispose()
  // =========================================================================
  describe("dispose()", () => {
    it("stops the timer on dispose", async () => {
      fetchMock.mockImplementation(okResponse);
      const mgr = createManager({ intervalMs: 10_000 });
      mgr.start();
      await vi.advanceTimersByTimeAsync(0);
      expect(fetchMock).toHaveBeenCalledTimes(1);

      mgr.dispose();
      await vi.advanceTimersByTimeAsync(30_000);
      expect(fetchMock).toHaveBeenCalledTimes(1); // no more calls
    });

    it("clears the fallback registry on dispose", () => {
      const mgr = createManager();
      mgr.register("op1", () => "a");
      mgr.register("op2", () => "b");
      expect(mgr.getStrategy("op1")).toBeDefined();

      mgr.dispose();
      expect(mgr.getStrategy("op1")).toBeUndefined();
      expect(mgr.getStrategy("op2")).toBeUndefined();
    });

    it("disposes EventEmitter on dispose", async () => {
      fetchMock.mockImplementation(okResponse);
      const mgr = createManager();
      const handler = vi.fn();
      mgr.onStateChanged(handler);

      mgr.start();
      await vi.advanceTimersByTimeAsync(0); // offline → online
      // handler was called during initial transition
      const callCount = handler.mock.calls.length;

      mgr.dispose();

      // After dispose, no new events should be delivered
      // (EventEmitter.dispose clears listeners)
      expect(handler.mock.calls.length).toBe(callCount);
    });
  });

  // =========================================================================
  // Configurable thresholds
  // =========================================================================
  describe("configurable thresholds", () => {
    it("uses custom failureThreshold of 5 to go offline", async () => {
      fetchMock.mockImplementationOnce(okResponse);
      const mgr = createManager({ failureThreshold: 5 });
      mgr.start();
      await vi.advanceTimersByTimeAsync(0); // online

      fetchMock.mockImplementation(failResponse);

      // 4 failures → still degraded
      for (let i = 0; i < 4; i++) {
        await vi.advanceTimersByTimeAsync(60_000);
      }
      expect(mgr.state).toBe("degraded");

      // 5th failure → offline
      await vi.advanceTimersByTimeAsync(60_000);
      expect(mgr.state).toBe("offline");
      mgr.dispose();
    });

    it("uses custom intervalMs for timer scheduling", async () => {
      fetchMock.mockImplementation(okResponse);
      const mgr = createManager({ intervalMs: 5_000 });
      mgr.start();
      await vi.advanceTimersByTimeAsync(0);
      expect(fetchMock).toHaveBeenCalledTimes(1);

      await vi.advanceTimersByTimeAsync(5_000);
      expect(fetchMock).toHaveBeenCalledTimes(2);

      await vi.advanceTimersByTimeAsync(5_000);
      expect(fetchMock).toHaveBeenCalledTimes(3);
      mgr.dispose();
    });
  });
});
