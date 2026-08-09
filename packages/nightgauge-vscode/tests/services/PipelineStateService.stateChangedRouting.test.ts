/**
 * PipelineStateService.stateChangedRouting.test.ts
 *
 * ADR-017 Decision 6 (F19) — inbound events route by RUN IDENTITY, with an
 * empty-id fallback that can never make a UX surface go dark.
 *
 * The issue number is not an identity: a force-cleared run and its live
 * successor share one, so a late `pipeline.stateChanged` from the dead run
 * repainted the successor's state and the operator watched a finished run's
 * stages overwrite a running one. Strict equality decides when BOTH sides
 * have an id. When either side has none — every extension-path event until
 * the step-4 re-key — the event still applies through the issue-number
 * pre-filter and is COUNTED, because a strict filter must never be the reason
 * a slot card goes blank (C9).
 *
 * @see docs/decisions/017-runtime-identity-keying.md — Decision 6
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

/**
 * A `pipeline.stateChanged` envelope in the shape the Go server emits: the
 * run's snapshot nested under `state`, with the identity available at the
 * envelope level, the snapshot level, or neither.
 */
function stateChanged(opts: {
  issueNumber: number;
  runId?: string;
  nestedRunId?: string;
  stage: string;
}): unknown {
  return {
    issueNumber: opts.issueNumber,
    repo: "nightgauge/nightgauge",
    ...(opts.runId ? { runId: opts.runId } : {}),
    state: {
      issueNumber: opts.issueNumber,
      stage: opts.stage,
      stageStart: new Date().toISOString(),
      ...(opts.nestedRunId ? { runId: opts.nestedRunId } : {}),
    },
  };
}

describe("PipelineStateService — stateChanged routes by run identity", () => {
  beforeEach(() => {
    handlers.clear();
  });

  it("applies an event whose runId matches the installed identity", async () => {
    const svc = await makeService(370);
    const runId = await mint();
    svc.beginRun(runId, "nightgauge/nightgauge", 370);

    emit("pipeline.stateChanged", stateChanged({ issueNumber: 370, runId, stage: "feature-dev" }));

    const state = await svc.getState();
    expect(state?.stages["feature-dev"]?.status).toBe("running");
    expect(svc.getEmptyIdFallbackCount()).toBe(0);
  });

  it("IGNORES the same issue under a DIFFERENT runId — the dead-predecessor case", async () => {
    const svc = await makeService(370);
    const live = await mint();
    const dead = await mint();
    svc.beginRun(live, "nightgauge/nightgauge", 370);

    emit(
      "pipeline.stateChanged",
      stateChanged({ issueNumber: 370, runId: live, stage: "pr-merge" })
    );
    emit(
      "pipeline.stateChanged",
      stateChanged({ issueNumber: 370, runId: dead, stage: "issue-pickup" })
    );

    const state = await svc.getState();
    // The live run's stage survived; the predecessor's did not land at all.
    expect(state?.stages["pr-merge"]?.status).toBe("running");
    expect(state?.stages["issue-pickup"]).toBeUndefined();
    expect(svc.getEmptyIdFallbackCount()).toBe(0);
  });

  it("honours a nested state.runId when the envelope carries none", async () => {
    const svc = await makeService(370);
    const live = await mint();
    const dead = await mint();
    svc.beginRun(live, "nightgauge/nightgauge", 370);

    emit(
      "pipeline.stateChanged",
      stateChanged({ issueNumber: 370, nestedRunId: dead, stage: "issue-pickup" })
    );
    expect((await svc.getState())?.stages["issue-pickup"]).toBeUndefined();

    emit(
      "pipeline.stateChanged",
      stateChanged({ issueNumber: 370, nestedRunId: live, stage: "feature-dev" })
    );
    expect((await svc.getState())?.stages["feature-dev"]?.status).toBe("running");
    expect(svc.getEmptyIdFallbackCount()).toBe(0);
  });

  it("APPLIES an empty-id event through the issue pre-filter and counts the fallback", async () => {
    const svc = await makeService(370);
    svc.beginRun(await mint(), "nightgauge/nightgauge", 370);

    emit("pipeline.stateChanged", stateChanged({ issueNumber: 370, stage: "feature-validate" }));

    expect((await svc.getState())?.stages["feature-validate"]?.status).toBe("running");
    expect(svc.getEmptyIdFallbackCount()).toBe(1);
  });

  it("still drops another issue's event on the fallback arm", async () => {
    const svc = await makeService(370);
    svc.beginRun(await mint(), "nightgauge/nightgauge", 370);

    emit("pipeline.stateChanged", stateChanged({ issueNumber: 999, stage: "feature-dev" }));

    expect(await svc.getState()).toBeNull();
    expect(svc.getEmptyIdFallbackCount()).toBe(0);
  });

  it("falls back when the SERVICE has no identity even though the event does", async () => {
    const svc = await makeService(370);

    emit(
      "pipeline.stateChanged",
      stateChanged({ issueNumber: 370, runId: await mint(), stage: "pr-create" })
    );

    expect((await svc.getState())?.stages["pr-create"]?.status).toBe("running");
    expect(svc.getEmptyIdFallbackCount()).toBe(1);
  });

  it("stage.complete takes the same fallback until step 4 stamps its envelope", async () => {
    const svc = await makeService(370);
    const live = await mint();
    svc.beginRun(live, "nightgauge/nightgauge", 370);
    emit(
      "pipeline.stateChanged",
      stateChanged({ issueNumber: 370, runId: live, stage: "pr-merge" })
    );

    emit("stage.complete", {
      issueNumber: 370,
      stage: "pr-merge",
      repo: "nightgauge/nightgauge",
      error: "",
      inputTokens: 0,
      outputTokens: 0,
      cacheReadTokens: 0,
      costUsd: 0,
      model: "",
    });

    expect((await svc.getState())?.stages["pr-merge"]?.status).toBe("complete");
    expect(svc.getEmptyIdFallbackCount()).toBe(1);
  });

  it("stage.complete under a DIFFERENT runId is ignored once the envelope carries one", async () => {
    const svc = await makeService(370);
    const live = await mint();
    svc.beginRun(live, "nightgauge/nightgauge", 370);
    emit(
      "pipeline.stateChanged",
      stateChanged({ issueNumber: 370, runId: live, stage: "pr-merge" })
    );

    emit("stage.complete", {
      issueNumber: 370,
      stage: "pr-merge",
      repo: "nightgauge/nightgauge",
      runId: await mint(),
      error: "boom",
      inputTokens: 0,
      outputTokens: 0,
      cacheReadTokens: 0,
      costUsd: 0,
      model: "",
    });

    expect((await svc.getState())?.stages["pr-merge"]?.status).toBe("running");
    expect(svc.getEmptyIdFallbackCount()).toBe(0);
  });
});
