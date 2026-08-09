/**
 * PipelineStateService.stagePid.test.ts
 *
 * ADR-017 §7.2 — the extension path gets a PID.
 *
 * Without one, the reconciler's liveness ladder has no arm that can answer
 * "is this run alive?" for a stage that emits no assistant tokens for a whole
 * grace window — a `pr-merge` polling CI is the ordinary case. Arm 1 never
 * fires, arms 2 and 3 are structurally false, arm 4 is stale because the last
 * TRANSITION was 40 minutes ago, arm 5 expires: the backend auto-restarts and
 * at T+120s a LIVE run is booked terminal with a signal-free learning row
 * (F26).
 *
 * The wire contract this pins:
 *   - the `running` transition carries the spawned child's pid;
 *   - `complete` / `failed` carry `stagePid: 0`, so a finished child can
 *     never vouch for the run and the PID-reuse window is bounded by one
 *     stage rather than by the run;
 *   - `initialized` / `model-resolved` / `skipped` / `deferred` OMIT the
 *     field entirely — they describe no child, and `0` there would be an
 *     assertion about a process rather than the absence of one.
 *
 * @see docs/decisions/017-runtime-identity-keying.md — §7.2
 * @see internal/ipc/protocol.go — `PipelineNotifyStageTransitionParams.StagePid`
 * @see tests/utils/skillRunner.test.ts — the spawn-side half (onStageChildSpawned)
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
  const { uuidV7 } = await import("@nightgauge/sdk");
  PipelineStateService.resetInstance();
  const svc = PipelineStateService.createForWorktree("/tmp/repo", issueNumber);
  svc.beginRun(uuidV7(), "nightgauge/nightgauge", issueNumber);
  return svc;
}

function transitions() {
  return ipcCalls.filter((c) => c.method === "pipeline.notifyStageTransition");
}

describe("PipelineStateService — stagePid on the wire (ADR-017 §7.2)", () => {
  beforeEach(() => {
    ipcCalls.length = 0;
  });

  it("the running transition carries the spawned child's pid", async () => {
    const svc = await makeService(370);

    await svc.startStage("feature-dev", { stagePid: 424242 });

    expect(transitions()).toHaveLength(1);
    expect(transitions()[0].params.status).toBe("running");
    expect(transitions()[0].params.stagePid).toBe(424242);
  });

  it("omits stagePid on a running transition with no child (nothing spawned)", async () => {
    const svc = await makeService(370);

    await svc.startStage("feature-dev");

    const params = transitions()[0].params;
    expect(params.status).toBe("running");
    expect("stagePid" in params).toBe(false);
  });

  it("complete and failed send stagePid: 0 so a dead child cannot vouch", async () => {
    const svc = await makeService(370);

    await svc.completeStage("feature-dev");
    await svc.failStage("feature-validate", "boom");

    const [complete, failed] = transitions();
    expect(complete.params.status).toBe("complete");
    expect(complete.params.stagePid).toBe(0);
    expect(failed.params.status).toBe("failed");
    expect(failed.params.stagePid).toBe(0);
  });

  it("initialized / model-resolved / skipped / deferred omit the field entirely", async () => {
    const svc = await makeService(370);

    await svc.initializePipeline(370, "Title", "feat/370");
    await svc.recordStageModel("feature-dev", { model: "opus", adapter: "claude" });
    await svc.skipStage("feature-planning");
    await svc.deferStage("pr-merge");

    const statuses = transitions().map((t) => t.params.status);
    expect(statuses).toEqual(["initialized", "model-resolved", "skipped", "deferred"]);
    for (const t of transitions()) {
      expect("stagePid" in t.params).toBe(false);
    }
  });
});
