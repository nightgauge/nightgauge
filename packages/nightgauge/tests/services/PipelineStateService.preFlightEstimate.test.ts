/**
 * The pre-flight estimate rides the run's FIRST `running` transition only
 * (#3455), so Go puts it on exactly one stage_started platform event. An
 * unpriced or absent estimate is omitted, never sent as 0.
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
    event = () => ({ dispose: () => {} });
    fire() {}
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

async function makeRun(meta: Record<string, unknown> | undefined) {
  const { PipelineStateService } = await import("../../src/services/PipelineStateService");
  const { uuidV7 } = await import("@nightgauge/sdk");
  PipelineStateService.resetInstance();
  const svc = PipelineStateService.createForWorktree("/tmp/repo", 42);
  svc.beginRun(uuidV7(), "acme/app", 42);
  (svc as unknown as { _lastState: unknown })._lastState = {
    issue_number: 42,
    stages: {},
    tokens: { input: 0, output: 0 },
    pipeline_meta: meta,
  };
  return svc;
}

function runningTransitions() {
  return ipcCalls
    .filter((c) => c.method === "pipeline.notifyStageTransition" && c.params.status === "running")
    .map((c) => c.params);
}

describe("pre-flight estimate on the first running transition (#3455)", () => {
  beforeEach(() => {
    ipcCalls.length = 0;
  });

  it("sends the estimate once, on the first stage only", async () => {
    const svc = await makeRun({
      budget_estimate_usd: 4.5,
      budget_estimate_source: "historical-p75",
    });
    await svc.startStage("issue-pickup");
    await svc.startStage("feature-planning");
    expect(runningTransitions()).toHaveLength(2);
    const [first, second] = runningTransitions();
    expect(first.estimatedCostUsd).toBe(4.5);
    expect(first.estimateBasis).toBe("historical-p75");
    expect(second).not.toHaveProperty("estimatedCostUsd");
    expect(second).not.toHaveProperty("estimateBasis");
  });

  it("sends it again on the next run of the same service", async () => {
    const svc = await makeRun({ budget_estimate_usd: 2, budget_estimate_source: "historical" });
    await svc.startStage("issue-pickup");
    const { uuidV7 } = await import("@nightgauge/sdk");
    const firstRun = (svc as unknown as { runId: string }).runId;
    svc.endRun(firstRun);
    svc.beginRun(uuidV7(), "acme/app", 42);
    (svc as unknown as { _lastState: unknown })._lastState = {
      issue_number: 42,
      stages: {},
      tokens: { input: 0, output: 0 },
      pipeline_meta: { budget_estimate_usd: 3, budget_estimate_source: "historical" },
    };
    await svc.startStage("issue-pickup");
    const running = runningTransitions();
    expect(running).toHaveLength(2);
    expect(running[0].estimatedCostUsd).toBe(2);
    expect(running[1].estimatedCostUsd).toBe(3);
  });

  it("caps the basis at 40 characters and omits a non-finite cost", async () => {
    const svc = await makeRun({ budget_estimate_usd: 1, budget_estimate_source: "b".repeat(60) });
    await svc.startStage("issue-pickup");
    expect(runningTransitions()[0].estimateBasis).toBe("b".repeat(40));
    ipcCalls.length = 0;
    const svc2 = await makeRun({
      budget_estimate_usd: Number.NaN,
      budget_estimate_source: "historical",
    });
    await svc2.startStage("issue-pickup");
    expect(runningTransitions()[0]).not.toHaveProperty("estimatedCostUsd");
  });

  it("omits an unpriced estimate", async () => {
    const svc = await makeRun({ budget_estimate_usd: 0, budget_estimate_source: "unpriced" });
    await svc.startStage("issue-pickup");
    expect(runningTransitions()[0]).not.toHaveProperty("estimatedCostUsd");
  });

  it("omits the fields when no estimate was computed", async () => {
    const svc = await makeRun(undefined);
    await svc.startStage("issue-pickup");
    const [first] = runningTransitions();
    expect(first).not.toHaveProperty("estimatedCostUsd");
    expect(first).not.toHaveProperty("estimateBasis");
  });
});
