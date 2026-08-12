/**
 * Shared idle-state polling gate (#484) — core-gate proof + AttentionSweepService
 * consumption.
 *
 * Nightgauge's GitHub-touching timers previously ignored view visibility and
 * window focus entirely: the attention sweep's periodic timer, the board
 * tree's auto-refresh, and the Repositories view's `ipc.ready` refresh all
 * kept polling with the sidebar collapsed or the machine untouched overnight.
 * This file pins the shared `PollingVisibilityGate` primitive directly (the
 * "core gate") and this file's own consumer (the attention sweep's periodic
 * timer) — the other two consumers are pinned in
 * ProjectBoardTreeProvider.visibilityGating.test.ts and
 * RepositoriesTreeProvider.visibilityGating.test.ts. The active-run exemption
 * (AC3 — a stall watchdog must never go quiet because a window is hidden) is
 * pinned separately in autonomousCommands.visibilityGating.test.ts, since that
 * subsystem deliberately never consults this gate.
 */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import {
  AttentionSweepService,
  PollingVisibilityGate,
  _resetWindowFocusTrackingForTests,
  WINDOW_FOCUS_GRACE_MS,
  SWEEP_MIN_GAP_MS,
  type AttentionSweepConfig,
  type AttentionSweepIpc,
} from "../../src/services/AttentionSweepService";
import type { AttentionSweepResult } from "../../src/services/IpcClientBase";
import type { Logger } from "../../src/utils/logger";

// A mutable window-focus fixture the test controls directly, mirroring how
// VS Code's real `window.state` / `onDidChangeWindowState` behave: a live
// object plus a change event. Kept file-local (not the shared tests/setup.ts
// mock) so this suite can flip focus deterministically without touching
// other suites' defaults.
let windowState = { focused: true };
const windowStateListeners = new Set<(e: { focused: boolean }) => void>();

function fireWindowFocusChange(focused: boolean): void {
  windowState = { focused };
  for (const listener of [...windowStateListeners]) listener({ focused });
}

vi.mock("vscode", () => ({
  workspace: {
    getConfiguration: vi.fn(() => ({ get: vi.fn() })),
  },
  window: {
    get state() {
      return windowState;
    },
    onDidChangeWindowState: vi.fn((listener: (e: { focused: boolean }) => void) => {
      windowStateListeners.add(listener);
      return { dispose: () => windowStateListeners.delete(listener) };
    }),
  },
}));

const emptyResult = (): AttentionSweepResult => ({
  repos: [],
  created: 0,
  updated: 0,
  autoResolved: 0,
});

/** A fake IPC surface that records every attentionSweep() call — the stand-in
 * for a "GitHub client call" throughout this suite. */
class FakeIpc implements AttentionSweepIpc {
  calls: Array<{ repos?: string[]; reason?: string }> = [];
  result: AttentionSweepResult = emptyResult();

  async attentionSweep(repos?: string[], reason?: string): Promise<AttentionSweepResult> {
    this.calls.push({ repos, reason });
    return this.result;
  }

  on(_event: string, _handler: (data: unknown) => void): { dispose(): void } {
    return { dispose: () => {} };
  }
}

const createLogger = (): Logger =>
  ({ info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn() }) as unknown as Logger;

function makeService(overrides: { ipc?: FakeIpc; config?: Partial<AttentionSweepConfig> } = {}) {
  const ipc = overrides.ipc ?? new FakeIpc();
  const logger = createLogger();
  const config: AttentionSweepConfig = {
    enabled: true,
    intervalMs: 60_000,
    minGapMs: SWEEP_MIN_GAP_MS,
    ...overrides.config,
  };
  const service = new AttentionSweepService({
    ipc,
    logger,
    resolveRepos: () => ["octocat/acme-web"],
    readConfig: () => config,
    now: () => Date.now(),
  });
  return { service, ipc, logger };
}

describe("PollingVisibilityGate — core gate (#484)", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    PollingVisibilityGate.resetForTests();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("is allowed by default (no views registered, window focused)", () => {
    expect(PollingVisibilityGate.instance.isPollingAllowed()).toBe(true);
  });

  it("is NOT allowed once every view is hidden and the window has been unfocused past the grace window", () => {
    const gate = PollingVisibilityGate.instance;
    gate.setViewVisible("repositoriesView", false);
    gate.setWindowFocused(false);

    expect(gate.isPollingAllowed()).toBe(true); // still inside the grace window
    vi.advanceTimersByTime(WINDOW_FOCUS_GRACE_MS + 1);
    expect(gate.isPollingAllowed()).toBe(false);
  });

  it("stays allowed while ANY registered view is visible, even if others are hidden", () => {
    const gate = PollingVisibilityGate.instance;
    gate.setWindowFocused(false);
    vi.advanceTimersByTime(WINDOW_FOCUS_GRACE_MS + 1);
    gate.setViewVisible("projectBoard:ready", false);
    gate.setViewVisible("repositoriesView", true);

    expect(gate.isPollingAllowed()).toBe(true);
  });

  it("fires onDidBecomeAllowed exactly once on a closed→open transition, and not on open→open", () => {
    const gate = PollingVisibilityGate.instance;
    gate.setWindowFocused(false);
    vi.advanceTimersByTime(WINDOW_FOCUS_GRACE_MS + 1);
    expect(gate.isPollingAllowed()).toBe(false);

    const listener = vi.fn();
    gate.onDidBecomeAllowed(listener);

    gate.setViewVisible("repositoriesView", true); // closed → open
    expect(listener).toHaveBeenCalledTimes(1);

    gate.setViewVisible("projectBoard:ready", true); // open → open (still allowed)
    expect(listener).toHaveBeenCalledTimes(1);
  });
});

describe("AttentionSweepService — timer respects the shared gate (#484 AC1, AC2)", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    windowState = { focused: true };
    windowStateListeners.clear();
    PollingVisibilityGate.resetForTests();
    _resetWindowFocusTrackingForTests();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it(
    "AC1 core gate: suspends ALL timer-driven sweeps while hidden and unfocused — " +
      "exact zero calls across several intervals",
    async () => {
      windowState = { focused: false };
      const { service, ipc } = makeService({ config: { intervalMs: 60_000 } });

      service.start();
      await vi.advanceTimersByTimeAsync(0);
      // Activation always sweeps once regardless of visibility — it models
      // the operator opening the window, which just happened.
      expect(ipc.calls).toHaveLength(1);
      expect(ipc.calls[0].reason).toBe("activation");

      // Move past the grace window so the gate is definitely closed, then
      // advance through five full timer intervals.
      await vi.advanceTimersByTimeAsync(WINDOW_FOCUS_GRACE_MS);
      const callsBeforeTimers = ipc.calls.length;
      await vi.advanceTimersByTimeAsync(60_000 * 5);

      expect(ipc.calls.length).toBe(callsBeforeTimers); // zero new calls
      expect(ipc.calls.filter((c) => c.reason === "timer")).toHaveLength(0);

      service.dispose();
    }
  );

  it("ticks normally on the configured interval while the window is focused", async () => {
    const { service, ipc } = makeService({ config: { intervalMs: 60_000 } });

    service.start();
    await vi.advanceTimersByTimeAsync(0);
    expect(ipc.calls).toHaveLength(1); // activation

    await vi.advanceTimersByTimeAsync(60_000);
    await vi.advanceTimersByTimeAsync(60_000);

    expect(ipc.calls.map((c) => c.reason)).toEqual(["activation", "timer", "timer"]);

    service.dispose();
  });

  it("AC2: fires exactly one coalesced sweep when the window regains focus, then resumes normal cadence", async () => {
    windowState = { focused: false };
    const { service, ipc } = makeService({ config: { intervalMs: 60_000 } });

    service.start();
    await vi.advanceTimersByTimeAsync(0);
    expect(ipc.calls).toHaveLength(1); // activation

    // Hidden well past the grace window — several intervals produce nothing.
    await vi.advanceTimersByTimeAsync(WINDOW_FOCUS_GRACE_MS);
    await vi.advanceTimersByTimeAsync(60_000 * 3);
    expect(ipc.calls).toHaveLength(1);

    // Regain focus mid-interval (not on a tick boundary) — the coalesced
    // refresh must fire immediately, without waiting for the next tick.
    fireWindowFocusChange(true);
    await vi.advanceTimersByTimeAsync(0);
    expect(ipc.calls.map((c) => c.reason)).toEqual(["activation", "visibility-regained"]);

    // A burst of further "became visible" signals inside the throttle
    // window collapses to the one refresh already fired — "at most one".
    fireWindowFocusChange(false);
    fireWindowFocusChange(true);
    fireWindowFocusChange(false);
    fireWindowFocusChange(true);
    await vi.advanceTimersByTimeAsync(0);
    expect(ipc.calls).toHaveLength(2); // unchanged — still throttled

    // Normal cadence resumes: advancing a full interval past the coalesced
    // refresh produces exactly one further tick.
    await vi.advanceTimersByTimeAsync(60_000);
    expect(ipc.calls.map((c) => c.reason)).toEqual(["activation", "visibility-regained", "timer"]);

    service.dispose();
  });

  it("does not register the visibility-regain trigger when the timer itself is disabled (intervalMs 0)", async () => {
    windowState = { focused: false };
    const { service, ipc } = makeService({ config: { intervalMs: 0 } });

    service.start();
    await vi.advanceTimersByTimeAsync(0);
    expect(ipc.calls).toHaveLength(1); // activation only

    fireWindowFocusChange(true);
    await vi.advanceTimersByTimeAsync(0);
    expect(ipc.calls).toHaveLength(1); // no visibility-regained sweep — nothing to "resume"

    service.dispose();
  });
});
