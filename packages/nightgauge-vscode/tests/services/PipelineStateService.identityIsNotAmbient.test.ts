/**
 * PipelineStateService.identityIsNotAmbient.test.ts
 *
 * ADR-017 Decision 10 (F23) — "identity is not ambient".
 *
 * `retryFailedIssue`, `bootstrap/services.ts` and every `HeadlessOrchestrator`
 * direct entry point drive the SAME singleton `PipelineStateService`. Before
 * this change its identity was implicit, so retrying an issue that was still
 * executing fell straight through to `initializePipeline`: the last minter
 * won, run 1's remaining transitions were booked under run 2's identity, and
 * run 1 went silent forever — never terminal, leaked, later reconciled with a
 * spurious `pipeline_done`.
 *
 * These tests pin the three properties that close it:
 *   1. `beginRun` over a LIVE identity throws and mutates NOTHING;
 *   2. `initializePipeline` refuses to run with no identity installed (it
 *      stopped being the implicit mint) and refuses a mismatched issue;
 *   3. a malformed id is refused before it can become a Go map key or a
 *      `runtime-{issue}-{runId}.json` filename component.
 *
 * @see docs/decisions/017-runtime-identity-keying.md — Decision 10
 * @see internal/runstate/identity.go — the identity shape being enforced
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

const ipcCalls: Array<{ method: string; params: Record<string, unknown> }> = [];

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      on: vi.fn(() => ({ dispose: vi.fn() })),
      call: vi.fn((method: string, params: Record<string, unknown>) => {
        ipcCalls.push({ method, params });
        return Promise.resolve({ status: "ok" });
      }),
    }),
  },
}));

vi.mock("vscode", () => ({
  EventEmitter: class {
    private _handlers: Array<(v: unknown) => void> = [];
    event = (cb: (v: unknown) => void) => {
      this._handlers.push(cb);
      return { dispose: () => {} };
    };
    fire(value: unknown) {
      for (const h of this._handlers) h(value);
    }
    dispose() {}
  },
  Disposable: class {
    dispose() {}
  },
  window: {
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
  },
}));

async function makeService(issueNumber: number) {
  const { PipelineStateService } = await import("../../src/services/PipelineStateService");
  PipelineStateService.resetInstance();
  return PipelineStateService.createForWorktree("/tmp/repo", issueNumber);
}

/** Real identities, minted by the production minter — never hand-authored (#166). */
async function mint(): Promise<string> {
  const { uuidV7 } = await import("@nightgauge/sdk");
  return uuidV7();
}

describe("PipelineStateService — identity is not ambient (ADR-017 F23)", () => {
  beforeEach(() => {
    ipcCalls.length = 0;
  });

  it("beginRun installs the identity, the repo and the issue", async () => {
    const svc = await makeService(370);
    const runId = await mint();

    svc.beginRun(runId, "nightgauge/nightgauge", 370);

    expect(svc.getRunId()).toBe(runId);
    expect(svc.getRunRepo()).toBe("nightgauge/nightgauge");
  });

  it("beginRun over a LIVE identity throws and mutates nothing", async () => {
    const svc = await makeService(370);
    const first = await mint();
    const second = await mint();
    svc.beginRun(first, "nightgauge/nightgauge", 370);

    expect(() => svc.beginRun(second, "nightgauge/other", 370)).toThrowError(
      /already running \(run [0-9a-f]{8}…\)/
    );

    // Nothing moved: the LIVE run keeps its identity and its repo.
    expect(svc.getRunId()).toBe(first);
    expect(svc.getRunRepo()).toBe("nightgauge/nightgauge");
    expect(ipcCalls).toHaveLength(0);
  });

  it("the refusal names the issue and the first 8 chars of the live run", async () => {
    const svc = await makeService(221);
    const first = await mint();
    svc.beginRun(first, "nightgauge/nightgauge", 221);

    const second = await mint();
    expect(() => svc.beginRun(second, "nightgauge/nightgauge", 221)).toThrowError(
      `Issue #221 is already running (run ${first.slice(0, 8)}…). Stop or clear it before retrying.`
    );
  });

  it("refuses a malformed identity before it can become a filename component", async () => {
    const svc = await makeService(370);

    for (const bad of [
      "",
      "not-a-uuid",
      "../../etc/passwd",
      // UUIDv4 — right shape, wrong version nibble. The Go side would reject
      // it as run_id_invalid at step 4; refusing here keeps the two agreed.
      "0d8f1b2c-4a5e-4f6a-9b7c-1d2e3f4a5b6c",
      // Uppercase: the Go pattern is lowercase-only.
      "0195B2C3-D4E5-7F60-8A1B-2C3D4E5F6071",
    ]) {
      expect(() => svc.beginRun(bad, "nightgauge/nightgauge", 370)).toThrowError(
        /malformed identity/
      );
      expect(svc.getRunId()).toBeNull();
    }
  });

  it("initializePipeline throws when no identity is installed", async () => {
    const svc = await makeService(370);

    await expect(svc.initializePipeline(370, "Title", "feat/370")).rejects.toThrowError(
      /no run identity installed/
    );
    expect(ipcCalls).toHaveLength(0);
  });

  it("initializePipeline throws when the installed identity is for another issue", async () => {
    const svc = await makeService(370);
    svc.beginRun(await mint(), "nightgauge/nightgauge", 370);

    await expect(svc.initializePipeline(999, "Title", "feat/999")).rejects.toThrowError(
      /installed for issue #370, refusing to initialize it as #999/
    );
    expect(ipcCalls).toHaveLength(0);
  });

  it("endRun releases the identity so the next dispatch may begin", async () => {
    const svc = await makeService(370);
    const first = await mint();
    svc.beginRun(first, "nightgauge/nightgauge", 370);
    svc.endRun(first);
    expect(svc.getRunId()).toBeNull();

    const second = await mint();
    expect(() => svc.beginRun(second, "nightgauge/nightgauge", 370)).not.toThrow();
    expect(svc.getRunId()).toBe(second);
  });

  it("endRun is idempotent", async () => {
    const svc = await makeService(370);
    const runId = await mint();
    svc.beginRun(runId, "nightgauge/nightgauge", 370);
    svc.endRun(runId);
    svc.endRun(runId);
    expect(svc.getRunId()).toBeNull();
  });

  it("endRun with a STALE id is a no-op — a loser never releases the winner", async () => {
    // The shape this guards: a producer captures its id, crosses an await
    // (an IPC round trip, a stage), and by the time its `finally` runs the
    // singleton has already been cleared and re-begun by a SUCCESSOR. An
    // un-keyed release nulls the successor's identity and it emits
    // `runId: ""` for the rest of its life.
    const svc = await makeService(370);
    const dead = await mint();
    const live = await mint();
    svc.beginRun(live, "nightgauge/nightgauge", 370);

    svc.endRun(dead);

    expect(svc.getRunId()).toBe(live);
    expect(svc.getRunRepo()).toBe("nightgauge/nightgauge");
  });

  it("a mint race loser neither continues nor releases the winner's identity", async () => {
    // The await-free check-and-claim contract the dispatch sites keep: a
    // second `beginRun` against a live identity THROWS and mutates nothing,
    // so the loser has no id to release and its keyed `finally` is inert.
    const svc = await makeService(370);
    const winner = await mint();
    const loser = await mint();
    svc.beginRun(winner, "nightgauge/nightgauge", 370);

    let mintedByLoser: string | null = null;
    expect(() => {
      svc.beginRun(loser, "nightgauge/nightgauge", 370);
      mintedByLoser = loser;
    }).toThrowError(/already running/);
    expect(mintedByLoser).toBeNull();

    // The loser's `finally` fires with nothing latched — and even if it fired
    // with its candidate id, the keyed release refuses it.
    svc.endRun(loser);
    expect(svc.getRunId()).toBe(winner);
  });

  it("notifyPipelineComplete releases the identity after sending it", async () => {
    const svc = await makeService(370);
    const runId = await mint();
    svc.beginRun(runId, "nightgauge/nightgauge", 370);

    await svc.notifyPipelineComplete({ success: true, totalDurationMs: 10, stagesRun: [] });

    const complete = ipcCalls.filter((c) => c.method === "pipeline.notifyComplete");
    expect(complete).toHaveLength(1);
    expect(complete[0].params.runId).toBe(runId);
    // Released AFTER the send — the terminal claim is the last place the id
    // is needed, and holding it afterwards would refuse the issue's next run.
    expect(svc.getRunId()).toBeNull();
  });

  it("clearPipeline releases the identity; dispose deliberately KEEPS it", async () => {
    const cleared = await makeService(370);
    cleared.beginRun(await mint(), "nightgauge/nightgauge", 370);
    await cleared.clearPipeline();
    expect(cleared.getRunId()).toBeNull();

    // `cleanupSlot` disposes a force-cleared slot's service while its run may
    // still be in flight, and that run's own late `notifyPipelineComplete` is
    // the sole writer of its history record and learning outcome. Releasing
    // here would send that call unattributed — refused `run_id_required` at
    // step 4 — so both records would silently stop being written. Nothing
    // recycles a disposed service (`createForWorktree` returns a NEW instance
    // and the singleton pairs dispose with `resetInstance`), so keeping the
    // id costs nothing and keeps the dead run attributable.
    const disposed = await makeService(371);
    const survivingId = await mint();
    disposed.beginRun(survivingId, "nightgauge/nightgauge", 371);
    disposed.dispose();
    expect(disposed.getRunId()).toBe(survivingId);
  });

  it("pause/resume refuse to write with no identity, and carry repo+runId with one", async () => {
    const svc = await makeService(370);

    await svc.pausePipeline();
    expect(ipcCalls.filter((c) => c.method === "pipeline.setPaused")).toHaveLength(0);

    const runId = await mint();
    svc.beginRun(runId, "nightgauge/nightgauge", 370);
    await svc.pausePipeline();
    await svc.resumePipeline();

    const paused = ipcCalls.filter((c) => c.method === "pipeline.setPaused");
    expect(paused).toHaveLength(2);
    expect(paused[0].params).toEqual({
      issueNumber: 370,
      paused: true,
      repo: "nightgauge/nightgauge",
      runId,
    });
    expect(paused[1].params).toEqual({
      issueNumber: 370,
      paused: false,
      repo: "nightgauge/nightgauge",
      runId,
    });
  });
});
