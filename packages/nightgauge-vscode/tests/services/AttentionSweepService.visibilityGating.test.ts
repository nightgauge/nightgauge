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
 *
 * Review round (#484 fixups) — the gate now exposes two EXPLICIT predicates,
 * isWindowActive() and isViewVisible(key), instead of one combined
 * isPollingAllowed() OR. This file's own consumer (the sweep timer) composes
 * isWindowActive() ALONE — view visibility is irrelevant to it, see the
 * gate's module doc comment in AttentionSweepService.ts for the full
 * per-consumer rationale (DESIGN RULING).
 */
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import {
  AttentionSweepService,
  PollingVisibilityGate,
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
 * for a "GitHub client call" throughout this suite. Also records `on()`
 * handlers per event and exposes `emit()` so event-driven triggers
 * (pipeline.complete/error) can be pinned, not just the timer. */
class FakeIpc implements AttentionSweepIpc {
  calls: Array<{ repos?: string[]; reason?: string }> = [];
  result: AttentionSweepResult = emptyResult();
  private handlers = new Map<string, Set<(data: unknown) => void>>();

  async attentionSweep(repos?: string[], reason?: string): Promise<AttentionSweepResult> {
    this.calls.push({ repos, reason });
    return this.result;
  }

  on(event: string, handler: (data: unknown) => void): { dispose(): void } {
    if (!this.handlers.has(event)) this.handlers.set(event, new Set());
    this.handlers.get(event)!.add(handler);
    return { dispose: () => this.handlers.get(event)?.delete(handler) };
  }

  emit(event: string, data?: unknown): void {
    for (const h of this.handlers.get(event) ?? []) h(data);
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

  it("isWindowActive() is true by default (window focused)", () => {
    expect(PollingVisibilityGate.instance.isWindowActive()).toBe(true);
  });

  it("isWindowActive() stays true within the grace window after losing focus, false past it", () => {
    const gate = PollingVisibilityGate.instance;
    gate.setWindowFocused(false);

    expect(gate.isWindowActive()).toBe(true); // still inside the grace window
    vi.advanceTimersByTime(WINDOW_FOCUS_GRACE_MS + 1);
    expect(gate.isWindowActive()).toBe(false);
  });

  it("MF-1: the grace window is measured from the blur instant, not the last focus GAIN", () => {
    const gate = PollingVisibilityGate.instance;
    // Stay focused well past what would be a grace window's worth of time —
    // this must have NO bearing on when the grace period starts counting.
    vi.advanceTimersByTime(WINDOW_FOCUS_GRACE_MS * 10);
    gate.setWindowFocused(false);

    expect(gate.isWindowActive()).toBe(true); // just blurred — full grace remains
    vi.advanceTimersByTime(WINDOW_FOCUS_GRACE_MS - 1);
    expect(gate.isWindowActive()).toBe(true);
    vi.advanceTimersByTime(2);
    expect(gate.isWindowActive()).toBe(false);
  });

  it("MF-1: repeated blur notifications do not extend the grace window", () => {
    const gate = PollingVisibilityGate.instance;
    gate.setWindowFocused(false);
    vi.advanceTimersByTime(WINDOW_FOCUS_GRACE_MS - 1);
    gate.setWindowFocused(false); // a second, redundant blur call
    vi.advanceTimersByTime(2);

    expect(gate.isWindowActive()).toBe(false); // grace measured from the FIRST blur
  });

  it("isViewVisible(key) tracks each key independently, and is false for an unregistered key", () => {
    const gate = PollingVisibilityGate.instance;
    gate.setViewVisible("a", true);
    gate.setViewVisible("b", false);

    expect(gate.isViewVisible("a")).toBe(true);
    expect(gate.isViewVisible("b")).toBe(false);
    expect(gate.isViewVisible("never-registered")).toBe(false);
  });

  it("onDidBecomeAllowed(predicate, listener) fires exactly once on a false→true transition of THAT predicate, and not on true→true", () => {
    const gate = PollingVisibilityGate.instance;
    const listener = vi.fn();
    gate.onDidBecomeAllowed(() => gate.isViewVisible("repositoriesView"), listener);

    gate.setViewVisible("repositoriesView", true); // false → true
    expect(listener).toHaveBeenCalledTimes(1);

    gate.setViewVisible("projectBoard:ready", true); // unrelated key — predicate still true
    expect(listener).toHaveBeenCalledTimes(1);
  });

  it("onDidBecomeAllowed still fires after a silent time gap crosses the grace window with no intervening gate call", () => {
    // Regression guard for the #484 review-round redesign: edge detection
    // must not go stale just because nothing touched the gate while time
    // passed (the dominant real scenario — an overnight-idle window).
    const gate = PollingVisibilityGate.instance;
    const listener = vi.fn();
    gate.onDidBecomeAllowed(() => gate.isWindowActive(), listener);

    gate.setWindowFocused(false);
    // Nothing calls the gate for well over a grace window's worth of time.
    vi.advanceTimersByTime(WINDOW_FOCUS_GRACE_MS * 5);
    // A single regain call must still be recognised as a genuine edge, even
    // though the LAST gate call (the blur above) recorded "still allowed"
    // (it was still inside the grace period at that instant).
    gate.setWindowFocused(true);

    expect(listener).toHaveBeenCalledTimes(1);
  });

  it("two consumers with different composite predicates edge independently off the same underlying change", () => {
    // DESIGN RULING: a tree poller's isViewVisible(key) && isWindowActive()
    // vs. the sweep timer's isWindowActive() alone must not share bookkeeping.
    const gate = PollingVisibilityGate.instance;
    const sweepStyle = vi.fn();
    const treePollerStyle = vi.fn();
    gate.onDidBecomeAllowed(() => gate.isWindowActive(), sweepStyle);
    gate.onDidBecomeAllowed(
      () => gate.isViewVisible("repositoriesView") && gate.isWindowActive(),
      treePollerStyle
    );

    gate.setWindowFocused(false);
    vi.advanceTimersByTime(WINDOW_FOCUS_GRACE_MS + 1);
    gate.setWindowFocused(true); // window-only predicate crosses false→true

    expect(sweepStyle).toHaveBeenCalledTimes(1);
    expect(treePollerStyle).not.toHaveBeenCalled(); // no view registered — AND still false
  });

  it("SF-1: one throwing listener does not stop the others from running", () => {
    const gate = PollingVisibilityGate.instance;
    const throwing = vi.fn(() => {
      throw new Error("boom");
    });
    const recorder = vi.fn();
    gate.onDidBecomeAllowed(() => gate.isWindowActive(), throwing);
    gate.onDidBecomeAllowed(() => gate.isWindowActive(), recorder);

    gate.setWindowFocused(false);
    vi.advanceTimersByTime(WINDOW_FOCUS_GRACE_MS + 1);
    gate.setWindowFocused(false); // re-affirm the now-closed state for both listeners
    expect(gate.isWindowActive()).toBe(false);

    gate.setWindowFocused(true); // false → true edge for both

    expect(throwing).toHaveBeenCalledTimes(1);
    expect(recorder).toHaveBeenCalledTimes(1);
  });
});

describe("AttentionSweepService — timer respects the shared gate (#484 AC1, AC2)", () => {
  beforeEach(() => {
    vi.useFakeTimers();
    windowState = { focused: true };
    windowStateListeners.clear();
    PollingVisibilityGate.resetForTests();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it(
    "AC1 core gate: suspends ALL timer-driven sweeps while the window is unfocused past grace — " +
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

  it("SF-5: activation sweeps even with the gate closed OUTSIDE the grace window (the exemption itself, not an in-grace coincidence)", async () => {
    windowState = { focused: false };
    // Force the gate closed BEFORE start() ever calls ensureWindowFocusTracking(),
    // and advance past the grace window so isWindowActive() is genuinely
    // false — not merely "inside the grace period since construction".
    PollingVisibilityGate.instance.setWindowFocused(false);
    vi.advanceTimersByTime(WINDOW_FOCUS_GRACE_MS + 1);
    expect(PollingVisibilityGate.instance.isWindowActive()).toBe(false);

    const { service, ipc } = makeService({ config: { intervalMs: 60_000 } });
    service.start();
    await vi.advanceTimersByTimeAsync(0);

    expect(ipc.calls).toHaveLength(1);
    expect(ipc.calls[0].reason).toBe("activation");

    service.dispose();
  });

  it("SF-2: pipeline.complete / pipeline.error sweeps are exempt from the gate — they fire even with the gate closed", async () => {
    windowState = { focused: false };
    const { service, ipc } = makeService({ config: { intervalMs: 60_000 } });
    service.start();
    await vi.advanceTimersByTimeAsync(0); // activation sweep

    // Move well past both the focus grace and the sweep min-gap so the gate
    // is closed and the throttle can't mask the assertion.
    await vi.advanceTimersByTimeAsync(WINDOW_FOCUS_GRACE_MS + SWEEP_MIN_GAP_MS);
    expect(PollingVisibilityGate.instance.isWindowActive()).toBe(false);

    ipc.emit("pipeline.complete");
    await vi.advanceTimersByTimeAsync(0);
    expect(ipc.calls.some((c) => c.reason === "run-terminated")).toBe(true);

    // A second terminal event, spaced past the min-gap throttle, also fires.
    await vi.advanceTimersByTimeAsync(SWEEP_MIN_GAP_MS + 1);
    const before = ipc.calls.filter((c) => c.reason === "run-terminated").length;
    ipc.emit("pipeline.error");
    await vi.advanceTimersByTimeAsync(0);
    expect(ipc.calls.filter((c) => c.reason === "run-terminated").length).toBe(before + 1);

    service.dispose();
  });

  it("SF-4: stops sweeping on visibility regain after dispose (no leaked gate listener)", async () => {
    windowState = { focused: false };
    const { service, ipc } = makeService({ config: { intervalMs: 60_000 } });
    service.start();
    await vi.advanceTimersByTimeAsync(0);
    await vi.advanceTimersByTimeAsync(WINDOW_FOCUS_GRACE_MS);
    service.dispose();
    const before = ipc.calls.length;
    fireWindowFocusChange(true);
    await vi.advanceTimersByTimeAsync(0);
    expect(ipc.calls.length).toBe(before);
  });
});
