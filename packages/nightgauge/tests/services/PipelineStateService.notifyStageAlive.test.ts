/**
 * PipelineStateService.notifyStageAlive.test.ts
 *
 * #431 (ADR-017 C18) — the consumer half of the Codex TUI liveness producer.
 * `notifyStageAlive` sends `pipeline.notifyStageAlive` under the run's identity
 * and swallows a refusal; `createStageAliveNotifier` drops activity ahead of
 * the stage's `running` transition and throttles the rest.
 *
 * @see internal/ipc/server.go — `pipeline.notifyStageAlive`
 * @see tests/utils/skillRunner.codexInteractive.test.ts — the producer half
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

const ipcCalls: Array<{ method: string; params: Record<string, unknown> }> = [];
let failNext = false;

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      on: vi.fn(() => ({ dispose: vi.fn() })),
      call: vi.fn((method: string, params: Record<string, unknown>) => {
        ipcCalls.push({ method, params });
        if (failNext) {
          failNext = false;
          return Promise.reject(new Error("rpc error -32000: run_closed"));
        }
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

async function makeService(issueNumber: number, runId: string) {
  const { PipelineStateService } = await import("../../src/services/PipelineStateService");
  PipelineStateService.resetInstance();
  const svc = PipelineStateService.createForWorktree("/tmp/repo", issueNumber);
  svc.beginRun(runId, "nightgauge/nightgauge", issueNumber);
  return svc;
}

function aliveCalls() {
  return ipcCalls.filter((c) => c.method === "pipeline.notifyStageAlive");
}

describe("PipelineStateService.notifyStageAlive (#431)", () => {
  beforeEach(() => {
    ipcCalls.length = 0;
    failNext = false;
  });

  it("sends the run's identity and the stage, nothing else", async () => {
    const { uuidV7 } = await import("@nightgauge/sdk");
    const runId = uuidV7();
    const svc = await makeService(431, runId);
    await svc.notifyStageAlive("feature-dev");
    expect(aliveCalls()).toEqual([
      {
        method: "pipeline.notifyStageAlive",
        params: { repo: "nightgauge/nightgauge", issueNumber: 431, stage: "feature-dev", runId },
      },
    ]);
  });

  it("swallows a refusal rather than failing the stage", async () => {
    const { uuidV7 } = await import("@nightgauge/sdk");
    const svc = await makeService(431, uuidV7());
    failNext = true;
    await expect(svc.notifyStageAlive("feature-dev")).resolves.toBeUndefined();
    expect(aliveCalls()).toHaveLength(1);
  });
});

describe("createStageAliveNotifier (#431)", () => {
  it("drops activity before the running transition, then throttles to one per window", async () => {
    const { createStageAliveNotifier, STAGE_ALIVE_THROTTLE_MS } =
      await import("../../src/services/PipelineStateService");
    const service = { notifyStageAlive: vi.fn(() => Promise.resolve()) };
    let started = false;
    let now = 1_000_000;
    const alive = createStageAliveNotifier(
      service,
      "feature-dev",
      () => started,
      () => now
    );

    alive();
    expect(service.notifyStageAlive).not.toHaveBeenCalled();

    started = true;
    alive();
    alive();
    expect(service.notifyStageAlive).toHaveBeenCalledTimes(1);

    now += STAGE_ALIVE_THROTTLE_MS;
    alive();
    expect(service.notifyStageAlive).toHaveBeenCalledTimes(2);
    expect(service.notifyStageAlive).toHaveBeenLastCalledWith("feature-dev");
  });

  it("is inert without a state service", async () => {
    const { createStageAliveNotifier } = await import("../../src/services/PipelineStateService");
    expect(() => createStageAliveNotifier(null, "feature-dev", () => true)()).not.toThrow();
  });
});
