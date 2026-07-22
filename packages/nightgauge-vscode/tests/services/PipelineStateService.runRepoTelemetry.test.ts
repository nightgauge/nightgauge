/**
 * Tests that PipelineStateService threads the run's repo into every stage
 * transition and signals pipeline completion — the run-creation context the Go
 * IPC layer needs to materialise/finalise the platform's live pipeline_runs row.
 *
 * Regression guard for the "No pipeline runs yet" bug: extension/Headless
 * orchestrator runs sent stage transitions with an empty repo, so the platform
 * never materialised a run. setRunRepo must propagate to the IPC payload.
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

// Capture every ipc.call(method, params) — resolving so the service takes the
// real IPC path (not the local-fallback catch branch).
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

function callsTo(method: string) {
  return ipcCalls.filter((c) => c.method === method);
}

describe("PipelineStateService — run repo + completion telemetry", () => {
  beforeEach(() => {
    ipcCalls.length = 0;
  });

  it("threads setRunRepo into stage transitions (start/complete/fail)", async () => {
    const svc = await makeService(153);
    svc.setRunRepo("nightgauge/acmeapp");

    await svc.startStage("issue-pickup");
    await svc.completeStage("issue-pickup");
    await svc.failStage("feature-dev", "boom");

    const transitions = callsTo("pipeline.notifyStageTransition");
    expect(transitions.length).toBe(3);
    for (const t of transitions) {
      expect(t.params.repo).toBe("nightgauge/acmeapp");
      expect(t.params.issueNumber).toBe(153);
    }
    expect(transitions[0].params.status).toBe("running");
    expect(transitions[1].params.status).toBe("complete");
    expect(transitions[2].params.status).toBe("failed");
  });

  it("completeStage forwards the served model + adapter attribution (#268)", async () => {
    const svc = await makeService(268);
    svc.setRunRepo("nightgauge/acmeapp");

    await svc.completeStage("feature-dev", { model: "claude-opus-4-8", adapter: "claude" });

    const complete = callsTo("pipeline.notifyStageTransition").find(
      (c) => c.params.status === "complete"
    );
    expect(complete).toBeDefined();
    expect(complete?.params.model).toBe("claude-opus-4-8");
    expect(complete?.params.adapter).toBe("claude");
  });

  it("completeStage omits model/adapter keys when no attribution is passed (#268)", async () => {
    const svc = await makeService(268);
    svc.setRunRepo("nightgauge/acmeapp");

    await svc.completeStage("feature-dev");

    const complete = callsTo("pipeline.notifyStageTransition").find(
      (c) => c.params.status === "complete"
    );
    expect(complete).toBeDefined();
    // Absent attribution must not put empty model/adapter keys on the wire —
    // the Go recorders treat absence as "unknown", never a defaulted value.
    expect("model" in (complete?.params ?? {})).toBe(false);
    expect("adapter" in (complete?.params ?? {})).toBe(false);
  });

  it("initializePipeline carries the run repo (not an empty string)", async () => {
    const svc = await makeService(42);
    svc.setRunRepo("nightgauge/nightgauge");

    await svc.initializePipeline(42, "Title", "feat/42");

    const init = callsTo("pipeline.notifyStageTransition").find(
      (c) => c.params.status === "initialized"
    );
    expect(init).toBeDefined();
    expect(init?.params.repo).toBe("nightgauge/nightgauge");
  });

  it("notifyPipelineComplete sends the terminal pipeline_done signal", async () => {
    const svc = await makeService(153);
    svc.setRunRepo("nightgauge/acmeapp");

    await svc.notifyPipelineComplete({
      success: true,
      totalDurationMs: 99000,
      stagesRun: ["issue-pickup", "feature-dev", "pr-merge"],
    });

    const done = callsTo("pipeline.notifyComplete");
    expect(done.length).toBe(1);
    expect(done[0].params).toMatchObject({
      repo: "nightgauge/acmeapp",
      issueNumber: 153,
      success: true,
      totalDurationMs: 99000,
      stagesRun: ["issue-pickup", "feature-dev", "pr-merge"],
    });
  });

  it("defaults repo to empty string before setRunRepo is called", async () => {
    const svc = await makeService(7);
    await svc.startStage("issue-pickup");
    const t = callsTo("pipeline.notifyStageTransition")[0];
    expect(t.params.repo).toBe("");
  });
});
