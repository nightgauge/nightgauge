/**
 * PipelineStateService.pausedFallback.test.ts
 *
 * Issue #1189 — `PipelineStateService.ts` converts a `pipeline.stateChanged`
 * Go snapshot into `PipelineState` with
 * `paused: (goState.paused as boolean | undefined) ?? this._lastState?.paused`.
 * Nothing executed the real conversion against both arms of that `??`, so
 * deleting or breaking the fallback left the suite green.
 *
 * Note (per the issue): the mirror test deleted in #1187
 * (`PipelineStateService.pauseResume.test.ts`) did NOT cover this — it
 * exercised a hand-reimplemented `pausePipeline`/`resumePipeline` pair on
 * local variables, never the real `??` expression in the stateChanged
 * converter. This file drives the REAL `PipelineStateService` through a real
 * `pipeline.stateChanged` IPC event, following the pattern in
 * `PipelineStateService.stateChangedRouting.test.ts`.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

type Handler = (data: unknown) => void;
const handlers = new Map<string, Handler[]>();

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      on: vi.fn((event: string, cb: Handler) => {
        const list = handlers.get(event) ?? [];
        list.push(cb);
        handlers.set(event, list);
        return { dispose: vi.fn() };
      }),
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

async function makeService(issueNumber: number) {
  const { PipelineStateService } = await import("../../src/services/PipelineStateService");
  PipelineStateService.resetInstance();
  return PipelineStateService.createForWorktree("/tmp/repo", issueNumber);
}

async function mint(): Promise<string> {
  const { uuidV7 } = await import("@nightgauge/sdk");
  return uuidV7();
}

function emit(event: string, data: unknown): void {
  for (const h of handlers.get(event) ?? []) h(data);
}

/** A `pipeline.stateChanged` envelope, optionally carrying `state.paused`. */
function stateChanged(opts: {
  issueNumber: number;
  runId: string;
  stage: string;
  paused?: boolean;
}): unknown {
  return {
    issueNumber: opts.issueNumber,
    repo: "nightgauge/nightgauge",
    runId: opts.runId,
    state: {
      issueNumber: opts.issueNumber,
      stage: opts.stage,
      stageStart: new Date().toISOString(),
      runId: opts.runId,
      ...(opts.paused !== undefined ? { paused: opts.paused } : {}),
    },
  };
}

describe("PipelineStateService — paused fallback on pipeline.stateChanged (Issue #1189)", () => {
  beforeEach(() => {
    handlers.clear();
  });

  it("takes Go's value on the non-fallback path (state supplies paused)", async () => {
    const svc = await makeService(1189);
    const runId = await mint();
    svc.beginRun(runId, "nightgauge/nightgauge", 1189);

    emit(
      "pipeline.stateChanged",
      stateChanged({ issueNumber: 1189, runId, stage: "feature-dev", paused: true })
    );

    expect((await svc.getState())?.paused).toBe(true);
  });

  it("flips from true to false when a later snapshot supplies paused: false", async () => {
    const svc = await makeService(1189);
    const runId = await mint();
    svc.beginRun(runId, "nightgauge/nightgauge", 1189);

    emit(
      "pipeline.stateChanged",
      stateChanged({ issueNumber: 1189, runId, stage: "feature-dev", paused: true })
    );
    expect((await svc.getState())?.paused).toBe(true);

    emit(
      "pipeline.stateChanged",
      stateChanged({ issueNumber: 1189, runId, stage: "feature-validate", paused: false })
    );

    // Go explicitly said false — the fallback to the stale in-memory `true`
    // must NOT win.
    expect((await svc.getState())?.paused).toBe(false);
  });

  it("falls back to the last known paused state when Go's snapshot omits it", async () => {
    const svc = await makeService(1189);
    const runId = await mint();
    svc.beginRun(runId, "nightgauge/nightgauge", 1189);

    // First snapshot establishes paused: true in _lastState.
    emit(
      "pipeline.stateChanged",
      stateChanged({ issueNumber: 1189, runId, stage: "feature-dev", paused: true })
    );
    expect((await svc.getState())?.paused).toBe(true);

    // A later snapshot from an older Go build (or a partial one) that never
    // sends `paused` at all — the field must be carried forward, not reset.
    emit(
      "pipeline.stateChanged",
      stateChanged({ issueNumber: 1189, runId, stage: "feature-validate" })
    );

    expect((await svc.getState())?.paused).toBe(true);
  });

  it("stays undefined on the fallback path when nothing has ever set paused", async () => {
    const svc = await makeService(1189);
    const runId = await mint();
    svc.beginRun(runId, "nightgauge/nightgauge", 1189);

    emit("pipeline.stateChanged", stateChanged({ issueNumber: 1189, runId, stage: "feature-dev" }));

    expect((await svc.getState())?.paused).toBeUndefined();
  });
});
