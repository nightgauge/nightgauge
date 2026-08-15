/**
 * PipelineStateService.liveStageFromSnapshot.test.ts
 *
 * #534 — the consumer half of "persist the runtime snapshot at stage start".
 *
 * A scheduler-owned run (`nightgauge run`, and `nightgauge serve`'s autonomous
 * dispatch) reaches the Pipeline tree through
 * `CliPipelineReconciliationService`, which reads `current-run.json` for the run
 * identity, loads `runtime-{issue}-{runId}.json`, and hands that snapshot to
 * `applyRuntimeSnapshot`. So whether the tree shows the live stage is decided
 * entirely by the SHAPE of the snapshot the Go scheduler wrote.
 *
 * THIS FILE CHANGES NO PRODUCTION CODE, deliberately. The `!stages[goState.stage]`
 * guard in `applyRuntimeSnapshot` is CORRECT and is kept: it is what stops a
 * TERMINAL snapshot — whose `stage` is by construction the last completed stage —
 * from flipping a finished stage back to `running`. The bug was never here; it
 * was that the scheduler only ever produced the stale shape. These two cases pin
 * that reading, so a future "fix" applied to this file instead of the scheduler
 * fails loudly:
 *
 *   - the STALE shape (stage == the last completed stage) marks nothing running.
 *     This is what the Go side must never emit mid-run, and what the tree looked
 *     like for the whole of #520.
 *   - the FIXED shape (stage == the LIVE stage, not yet in completedStages)
 *     marks that stage running.
 *
 * @see internal/orchestrator/scheduler.go — the stage-start persist
 * @see internal/orchestrator/scheduler_stage_start_snapshot_test.go — the
 *      producer-side assertion that only the second shape is ever written
 */

import { describe, it, expect, vi } from "vitest";

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      on: vi.fn(() => ({ dispose: vi.fn() })),
      call: vi.fn(() => Promise.resolve({ status: "ok" })),
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

const ISSUE = 534;

async function makeService() {
  const { PipelineStateService } = await import("../../src/services/PipelineStateService");
  PipelineStateService.resetInstance();
  return PipelineStateService.createForWorktree("/tmp/repo", ISSUE);
}

/** The pickup row every snapshot below carries, live or stale. */
const PICKUP_COMPLETE = {
  stage: "issue-pickup",
  startedAt: "2026-08-14T09:40:00.000Z",
};

describe("applyRuntimeSnapshot — which snapshot shape shows the live stage", () => {
  it("a STALE snapshot (stage == the last completed stage) marks nothing running", async () => {
    const svc = await makeService();

    // The pre-#534 shape, verbatim: pickup has completed, the scheduler is
    // already dispatching feature-dev, but nothing rewrote the snapshot at that
    // stage's start so `stage` still names the completed one.
    svc.applyRuntimeSnapshot({
      issueNumber: ISSUE,
      stage: "issue-pickup",
      stageStart: PICKUP_COMPLETE.startedAt,
      completedStages: [PICKUP_COMPLETE],
    });

    const state = await svc.getState();
    expect(state?.stages["issue-pickup"]).toMatchObject({ status: "complete" });

    // The whole defect in one assertion: NO stage is running, so the tree draws
    // the live stage as pending while the adapter is actively working. Kept as a
    // scan over every stage rather than a lookup on "feature-dev" — the snapshot
    // does not mention feature-dev at all, which is precisely the problem.
    const running = Object.entries(state?.stages ?? {}).filter(
      ([, s]) => (s as { status: string }).status === "running"
    );
    expect(running).toEqual([]);

    // And the completed stage was NOT flipped back to running by the fallthrough.
    // This is the property the `!stages[goState.stage]` guard exists for, and the
    // reason the fix belongs in the scheduler rather than here: a TERMINAL
    // snapshot has exactly this shape legitimately.
    expect(state?.stages["issue-pickup"]).toMatchObject({ status: "complete" });
  });

  it("a stage-start snapshot (stage == the live stage) marks that stage running", async () => {
    const svc = await makeService();

    // The post-#534 shape: the scheduler persisted at feature-dev's START, so
    // `stage` names the stage that is actually executing and pickup stays in
    // completedStages where it belongs.
    svc.applyRuntimeSnapshot({
      issueNumber: ISSUE,
      stage: "feature-dev",
      stageStart: "2026-08-14T09:42:00.000Z",
      completedStages: [PICKUP_COMPLETE],
    });

    const state = await svc.getState();

    expect(state?.stages["issue-pickup"]).toMatchObject({ status: "complete" });
    expect(state?.stages["feature-dev"]).toMatchObject({
      status: "running",
      started_at: "2026-08-14T09:42:00.000Z",
    });

    // Exactly one running stage — the tree renders a single live row.
    const running = Object.entries(state?.stages ?? {})
      .filter(([, s]) => (s as { status: string }).status === "running")
      .map(([stage]) => stage);
    expect(running).toEqual(["feature-dev"]);

    // The summary fields the tree's header reads follow the same stage.
    expect(state?.current_stage).toBe("feature-dev");
    expect(state?.current_stage_label).toBeTruthy();
  });
});
