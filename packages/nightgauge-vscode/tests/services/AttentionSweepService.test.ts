/**
 * Tests for AttentionSweepService — the four invocation points of the
 * repo-scoped attention sweep (issue #93).
 *
 * The sweep is deliberately not a daemon, so what needs pinning is WHEN it
 * fires and that it stays cheap when several triggers overlap: activation, an
 * Action Center / repositories refresh, the configured timer, and a run
 * terminating can all land inside the same second.
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
  SWEEP_MIN_GAP_MS,
  type AttentionSweepConfig,
  type AttentionSweepIpc,
} from "../../src/services/AttentionSweepService";
import type { AttentionSweepResult } from "../../src/services/IpcClientBase";
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

/** A fake IPC surface that records sweeps and lets a test emit pushes. */
class FakeIpc implements AttentionSweepIpc {
  calls: Array<{ repos?: string[]; reason?: string }> = [];
  result: AttentionSweepResult = emptyResult();
  error: Error | null = null;
  private handlers = new Map<string, Array<(data: unknown) => void>>();

  async attentionSweep(repos?: string[], reason?: string): Promise<AttentionSweepResult> {
    this.calls.push({ repos, reason });
    if (this.error) throw this.error;
    return this.result;
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
  } = {}
) {
  const ipc = overrides.ipc ?? new FakeIpc();
  const logger = createLogger();
  const config: AttentionSweepConfig = {
    enabled: true,
    intervalMs: 15 * 60_000,
    minGapMs: SWEEP_MIN_GAP_MS,
    ...overrides.config,
  };
  const repos = overrides.repos ?? ["octocat/acme-web"];
  const service = new AttentionSweepService({
    ipc,
    logger,
    resolveRepos: typeof repos === "function" ? repos : () => repos,
    onChanged: overrides.onChanged,
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
    const { service, ipc } = makeService({ config: { minGapMs: 0, intervalMs: 0 } });

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

  it("honours the throttle window for ambient triggers but never for a manual one", async () => {
    const { service, ipc } = makeService({ config: { intervalMs: 0 } });

    service.start();
    await vi.advanceTimersByTimeAsync(0);
    expect(ipc.calls).toHaveLength(1);

    // Inside the gap: an ambient trigger is dropped…
    await expect(service.sweep("view-refresh")).resolves.toBeUndefined();
    expect(ipc.calls).toHaveLength(1);

    // …but the operator pressing the button is answered, or it reads as broken.
    await service.sweep("manual");
    expect(ipc.calls).toHaveLength(2);
    expect(ipc.calls[1].reason).toBe("manual");

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
    const { service, ipc } = makeService({ config: { intervalMs: 0, minGapMs: 0 } });

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
    const { service } = makeService({ ipc, onChanged, config: { intervalMs: 0, minGapMs: 0 } });

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
      minGapMs: SWEEP_MIN_GAP_MS,
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
