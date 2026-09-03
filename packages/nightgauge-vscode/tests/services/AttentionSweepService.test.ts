/**
 * Tests for AttentionSweepService — the invocation points of the repo-scoped
 * attention sweep (issue #93).
 *
 * The sweep is deliberately not a daemon, so what needs pinning is WHEN it
 * fires and that it stays cheap when several triggers overlap: activation, an
 * Action Center / repositories refresh, the configured timer, and a run
 * terminating can all land inside the same second.
 *
 * A sweep is the most expensive thing the extension asks the daemon to do, so
 * the second thing pinned is that the event-driven triggers consult the
 * one-point board probe first and sweep only when a board moved or the
 * interval has elapsed — while the timer and the operator's command never ask.
 *
 * Fake timers throughout — a test that waits out a 15-minute interval is not a
 * test, it is a hang.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import {
  AttentionSweepService,
  resolveSweepRepos,
  readSweepConfig,
  DEFAULT_SWEEP_INTERVAL_MINUTES,
  MIN_SWEEP_INTERVAL_MINUTES,
  type AttentionSweepConfig,
  type AttentionSweepIpc,
} from "../../src/services/AttentionSweepService";
import type { AttentionSweepResult, BoardChangedResult } from "../../src/services/IpcClientBase";
import type { Logger } from "../../src/utils/logger";

const getConfiguration = vi.fn();

vi.mock("vscode", () => ({
  workspace: {
    getConfiguration: (section: string) => getConfiguration(section),
  },
}));

const emptyResult = (): AttentionSweepResult => ({
  repos: [],
  created: 0,
  updated: 0,
  autoResolved: 0,
});

/** A fake IPC surface that records sweeps and probes and lets a test emit
 * pushes. The probe answers "nothing moved" unless a test flips `changed` —
 * the quiet workspace is the case the gating exists for. */
class FakeIpc implements AttentionSweepIpc {
  calls: Array<{ repos?: string[]; reason?: string }> = [];
  probes: Array<{ repos?: string[]; since?: string }> = [];
  result: AttentionSweepResult = emptyResult();
  error: Error | null = null;
  changed = false;
  probeError: Error | null = null;
  private handlers = new Map<string, Array<(data: unknown) => void>>();

  async attentionSweep(repos?: string[], reason?: string): Promise<AttentionSweepResult> {
    this.calls.push({ repos, reason });
    if (this.error) throw this.error;
    return this.result;
  }

  async boardChanged(repos?: string[], since?: string): Promise<BoardChangedResult> {
    this.probes.push({ repos, since });
    if (this.probeError) throw this.probeError;
    return { changed: this.changed, repos: [], probed: repos?.length ?? 0, unprobeable: 0 };
  }

  on(event: string, handler: (data: unknown) => void): { dispose(): void } {
    const list = this.handlers.get(event) ?? [];
    list.push(handler);
    this.handlers.set(event, list);
    return {
      dispose: () =>
        this.handlers.set(
          event,
          (this.handlers.get(event) ?? []).filter((h) => h !== handler)
        ),
    };
  }

  emit(event: string): void {
    for (const handler of this.handlers.get(event) ?? []) handler({});
  }

  listenerCount(event: string): number {
    return (this.handlers.get(event) ?? []).length;
  }
}

const createLogger = (): Logger =>
  ({ info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn() }) as unknown as Logger;

function makeService(
  overrides: {
    ipc?: FakeIpc;
    config?: Partial<AttentionSweepConfig>;
    repos?: string[] | (() => Promise<string[]> | string[]);
    onChanged?: () => void;
    onRerender?: () => void;
  } = {}
) {
  const ipc = overrides.ipc ?? new FakeIpc();
  const logger = createLogger();
  const config: AttentionSweepConfig = {
    enabled: true,
    intervalMs: 15 * 60_000,
    ...overrides.config,
  };
  const repos = overrides.repos ?? ["octocat/acme-web"];
  const service = new AttentionSweepService({
    ipc,
    logger,
    resolveRepos: typeof repos === "function" ? repos : () => repos,
    onChanged: overrides.onChanged,
    onRerender: overrides.onRerender,
    readConfig: () => config,
    now: () => Date.now(),
  });
  return { service, ipc, logger };
}

describe("AttentionSweepService", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.useFakeTimers();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("sweeps once on activation, passing the workspace repos and the trigger", async () => {
    const { service, ipc } = makeService({ repos: ["octocat/acme-web", "octocat/acme-api"] });

    service.start();
    await vi.advanceTimersByTimeAsync(0);

    expect(ipc.calls).toHaveLength(1);
    expect(ipc.calls[0].repos).toEqual(["octocat/acme-web", "octocat/acme-api"]);
    expect(ipc.calls[0].reason).toBe("activation");

    service.dispose();
  });

  it("sweeps on the configured interval while the window is open", async () => {
    const { service, ipc } = makeService({ config: { intervalMs: 15 * 60_000 } });

    service.start();
    await vi.advanceTimersByTimeAsync(0);
    expect(ipc.calls).toHaveLength(1); // activation

    await vi.advanceTimersByTimeAsync(15 * 60_000);
    await vi.advanceTimersByTimeAsync(15 * 60_000);

    expect(ipc.calls.map((c) => c.reason)).toEqual(["activation", "timer", "timer"]);

    service.dispose();
  });

  it("stops the timer on dispose — no background work outlives the window", async () => {
    const { service, ipc } = makeService({ config: { intervalMs: 60_000 } });

    service.start();
    await vi.advanceTimersByTimeAsync(0);
    service.dispose();

    await vi.advanceTimersByTimeAsync(10 * 60_000);

    expect(ipc.calls).toHaveLength(1);
    expect(ipc.listenerCount("pipeline.complete")).toBe(0);
  });

  it("sweeps after a run terminates, for both the success and the error path", async () => {
    const { service, ipc } = makeService({ config: { intervalMs: 0 } });
    // A run terminating moves the board (an issue closed, an item to Done),
    // which is what the probe reports here.
    ipc.changed = true;

    service.start();
    await vi.advanceTimersByTimeAsync(0);

    ipc.emit("pipeline.complete");
    await vi.advanceTimersByTimeAsync(0);
    ipc.emit("pipeline.error");
    await vi.advanceTimersByTimeAsync(0);

    expect(ipc.calls.map((c) => c.reason)).toEqual([
      "activation",
      "run-terminated",
      "run-terminated",
    ]);

    service.dispose();
  });

  it("coalesces a burst of triggers into one sweep", async () => {
    const { service, ipc } = makeService({ config: { intervalMs: 0 } });

    service.start(); // activation
    // A run terminating during activation is exactly the overlap the throttle
    // exists for — the same evaluation would be repeated for no new information.
    ipc.emit("pipeline.complete");
    ipc.emit("pipeline.error");
    await vi.advanceTimersByTimeAsync(0);

    expect(ipc.calls).toHaveLength(1);

    service.dispose();
  });

  it("answers an ambient trigger from the last sweep when no board moved, re-rendering instead", async () => {
    const onRerender = vi.fn();
    const { service, ipc } = makeService({ config: { intervalMs: 15 * 60_000 }, onRerender });

    service.start();
    await vi.advanceTimersByTimeAsync(0);
    expect(ipc.calls).toHaveLength(1); // activation — nothing to serve yet, no probe
    expect(ipc.probes).toHaveLength(0);

    await vi.advanceTimersByTimeAsync(60_000);
    await expect(service.sweep("view-refresh")).resolves.toBeUndefined();

    expect(ipc.calls).toHaveLength(1);
    expect(ipc.probes).toHaveLength(1);
    expect(ipc.probes[0].repos).toEqual(["octocat/acme-web"]);
    // `since` is the last sweep's start, so a board that moved during the
    // sweep still reads as moved on the next probe.
    expect(ipc.probes[0].since).toBe(new Date(Date.now() - 60_000).toISOString());
    expect(onRerender).toHaveBeenCalledTimes(1);

    service.dispose();
  });

  it("sweeps on an ambient trigger when the probe says a board moved", async () => {
    const { service, ipc } = makeService({ config: { intervalMs: 15 * 60_000 } });

    service.start();
    await vi.advanceTimersByTimeAsync(0);
    expect(ipc.calls).toHaveLength(1);

    // Thirty seconds after activation — inside any floor a fixed gap would
    // impose — a board moved. The probe, not the clock, decides.
    ipc.changed = true;
    await vi.advanceTimersByTimeAsync(30_000);
    await service.sweep("view-refresh");

    expect(ipc.probes).toHaveLength(1);
    expect(ipc.calls.map((c) => c.reason)).toEqual(["activation", "view-refresh"]);

    service.dispose();
  });

  it("the forced manual command always sweeps — never a probe, never a throttle", async () => {
    const { service, ipc } = makeService({ config: { intervalMs: 15 * 60_000 } });

    service.start();
    await vi.advanceTimersByTimeAsync(0);
    expect(ipc.calls).toHaveLength(1);

    // Seconds after activation, with a probe that would say "nothing moved":
    // the operator pressing the button is answered, or it reads as broken.
    ipc.changed = false;
    await vi.advanceTimersByTimeAsync(1_000);
    await service.sweep("manual");
    await service.sweep("manual");

    expect(ipc.probes).toHaveLength(0);
    expect(ipc.calls.map((c) => c.reason)).toEqual(["activation", "manual", "manual"]);

    service.dispose();
  });

  it("sweeps on an ambient trigger once a full interval has elapsed, without asking the probe", async () => {
    const { service, ipc } = makeService({ config: { intervalMs: 5 * 60_000 } });

    service.start();
    await vi.advanceTimersByTimeAsync(0);
    expect(ipc.calls).toHaveLength(1);

    // Just short of the interval: probe says no, so no sweep.
    await vi.advanceTimersByTimeAsync(5 * 60_000 - 1);
    await service.sweep("view-refresh");
    expect(ipc.calls).toHaveLength(1);
    expect(ipc.probes).toHaveLength(1);

    // At the interval: the answer is stale on its own terms — sweep, no probe.
    // (The timer tick at exactly this instant is the same sweep; a second
    // view-refresh rides it or is answered from it.)
    service.dispose(); // stop the timer so the elapsed path is what we observe
    await vi.advanceTimersByTimeAsync(1);
    await service.sweep("view-refresh");
    expect(ipc.calls.map((c) => c.reason)).toEqual(["activation", "view-refresh"]);
    expect(ipc.probes).toHaveLength(1);
  });

  it("with the timer disabled, the default interval still bounds an ambient trigger", async () => {
    const { service, ipc } = makeService({ config: { intervalMs: 0 } });

    service.start();
    await vi.advanceTimersByTimeAsync(0);
    expect(ipc.calls).toHaveLength(1);

    await vi.advanceTimersByTimeAsync(DEFAULT_SWEEP_INTERVAL_MINUTES * 60_000 - 1);
    await service.sweep("view-refresh");
    expect(ipc.calls).toHaveLength(1); // probe said no

    await vi.advanceTimersByTimeAsync(1);
    await service.sweep("view-refresh");
    expect(ipc.calls).toHaveLength(2); // elapsed

    service.dispose();
  });

  it("fails open — a probe the daemon cannot answer sweeps rather than suppresses", async () => {
    const { service, ipc, logger } = makeService({ config: { intervalMs: 15 * 60_000 } });

    service.start();
    await vi.advanceTimersByTimeAsync(0);

    ipc.probeError = new Error("method not found: board.changed");
    await vi.advanceTimersByTimeAsync(1_000);
    await service.sweep("view-refresh");

    expect(ipc.calls.map((c) => c.reason)).toEqual(["activation", "view-refresh"]);
    expect(logger.warn).not.toHaveBeenCalled(); // an ambient trigger stays quiet

    service.dispose();
  });

  it("a sweep the daemon declined does not move the baseline the next probe compares against", async () => {
    const { service, ipc } = makeService({ config: { intervalMs: 15 * 60_000 } });

    service.start();
    await vi.advanceTimersByTimeAsync(0);
    const activationAt = new Date(Date.now()).toISOString();

    // The daemon throttled the manual sweep: nothing was evaluated.
    ipc.result = { ...emptyResult(), throttled: true, throttledForMs: 30_000 };
    await vi.advanceTimersByTimeAsync(10_000);
    await service.sweep("manual");

    ipc.result = emptyResult();
    await vi.advanceTimersByTimeAsync(10_000);
    await service.sweep("view-refresh");
    expect(ipc.probes[0].since).toBe(activationAt);

    service.dispose();
  });

  it("does nothing at all when disabled by configuration", async () => {
    const { service, ipc } = makeService({ config: { enabled: false } });

    service.start();
    await vi.advanceTimersByTimeAsync(0);
    await service.sweep("manual");
    await vi.advanceTimersByTimeAsync(60 * 60_000);

    expect(ipc.calls).toHaveLength(0);
    expect(ipc.listenerCount("pipeline.complete")).toBe(0);

    service.dispose();
  });

  it("runs no timer when the interval is zero, but keeps the event triggers", async () => {
    const { service, ipc } = makeService({ config: { intervalMs: 0 } });
    ipc.changed = true;

    service.start();
    await vi.advanceTimersByTimeAsync(60 * 60_000);
    expect(ipc.calls).toHaveLength(1); // activation only

    ipc.emit("pipeline.complete");
    await vi.advanceTimersByTimeAsync(0);
    expect(ipc.calls).toHaveLength(2);

    service.dispose();
  });

  it("skips the IPC call entirely when the workspace has no resolvable repos", async () => {
    const { service, ipc } = makeService({ repos: [] });

    service.start();
    await vi.advanceTimersByTimeAsync(0);

    expect(ipc.calls).toHaveLength(0);

    service.dispose();
  });

  it("swallows an IPC failure — an ambient trigger must never surface an error", async () => {
    const ipc = new FakeIpc();
    ipc.error = new Error("daemon not running");
    const { service, logger } = makeService({ ipc, config: { intervalMs: 0 } });

    service.start();
    await vi.advanceTimersByTimeAsync(0);

    expect(logger.warn).toHaveBeenCalledWith("Attention sweep failed", expect.anything());

    service.dispose();
  });

  it("notifies onChanged only when the sweep actually changed the inbox", async () => {
    const onChanged = vi.fn();
    const ipc = new FakeIpc();
    const { service } = makeService({ ipc, onChanged, config: { intervalMs: 0 } });

    service.start();
    await vi.advanceTimersByTimeAsync(0);
    expect(onChanged).not.toHaveBeenCalled(); // an all-zero reconcile is silence

    ipc.result = { ...emptyResult(), created: 1 };
    await service.sweep("manual");
    expect(onChanged).toHaveBeenCalledTimes(1);

    // Auto-resolution is a change too — a card vanishing is worth a re-render.
    ipc.result = { ...emptyResult(), autoResolved: 2 };
    await service.sweep("manual");
    expect(onChanged).toHaveBeenCalledTimes(2);

    service.dispose();
  });

  it("start is idempotent — a second call does not add a second timer", async () => {
    const { service, ipc } = makeService({ config: { intervalMs: 60_000 } });

    service.start();
    service.start();
    await vi.advanceTimersByTimeAsync(0);
    await vi.advanceTimersByTimeAsync(60_000);

    expect(ipc.calls).toHaveLength(2); // activation + one timer tick, not two

    service.dispose();
  });
});

describe("resolveSweepRepos", () => {
  it("prefers the workspace manifest over folder inference", async () => {
    const source = {
      getAllRepositories: () => [
        { github: { owner: "octocat", repo: "acme-web" } },
        { github: { owner: "octocat", repo: "acme-api" } },
        { github: null },
      ],
    };
    const fallback = vi.fn();

    await expect(resolveSweepRepos(source, fallback as never)).resolves.toEqual([
      "octocat/acme-web",
      "octocat/acme-api",
    ]);
    expect(fallback).not.toHaveBeenCalled();
  });

  it("falls back to folder identities when the manifest yields nothing", async () => {
    await expect(
      resolveSweepRepos({ getAllRepositories: () => [] }, async () => ["octocat/solo"])
    ).resolves.toEqual(["octocat/solo"]);

    await expect(resolveSweepRepos(null, async () => ["octocat/solo"])).resolves.toEqual([
      "octocat/solo",
    ]);
  });

  it("returns nothing rather than throwing when the fallback fails", async () => {
    await expect(
      resolveSweepRepos(undefined, async () => {
        throw new Error("no git remote");
      })
    ).resolves.toEqual([]);
  });
});

describe("readSweepConfig", () => {
  function mockSettings(values: Record<string, unknown>) {
    getConfiguration.mockReturnValue({
      get: (key: string, fallback: unknown) => (key in values ? values[key] : fallback),
    });
  }

  it("defaults to enabled at a conservative interval", () => {
    mockSettings({});
    expect(readSweepConfig()).toEqual({
      enabled: true,
      intervalMs: DEFAULT_SWEEP_INTERVAL_MINUTES * 60_000,
    });
  });

  it("raises a too-eager interval to the floor rather than honouring it", () => {
    mockSettings({ sweepIntervalMinutes: 1 });
    expect(readSweepConfig().intervalMs).toBe(MIN_SWEEP_INTERVAL_MINUTES * 60_000);
  });

  it("treats zero as 'only on demand' — no timer, triggers still live", () => {
    mockSettings({ sweepIntervalMinutes: 0 });
    const config = readSweepConfig();
    expect(config.intervalMs).toBe(0);
    expect(config.enabled).toBe(true);
  });

  it("falls back to the default for a non-numeric setting", () => {
    mockSettings({ sweepIntervalMinutes: Number.NaN });
    expect(readSweepConfig().intervalMs).toBe(DEFAULT_SWEEP_INTERVAL_MINUTES * 60_000);
  });

  it("respects the disable switch", () => {
    mockSettings({ sweepEnabled: false });
    expect(readSweepConfig().enabled).toBe(false);
  });
});
