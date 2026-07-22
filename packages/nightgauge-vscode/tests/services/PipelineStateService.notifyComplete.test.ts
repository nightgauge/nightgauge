/**
 * PipelineStateService.notifyPipelineComplete — prMerged forwarding (#266).
 *
 * The escalation-race fix threads a forge-confirmed merge signal from the
 * HeadlessOrchestrator through pipeline.notifyComplete so the Go recording
 * boundary can book a MERGED run as complete even when a late per-stage kill
 * reported failure at pr-merge. These tests pin that the `prMerged` flag is
 * forwarded verbatim in the IPC params (and defaults to false).
 */

import { describe, it, expect, vi, beforeEach } from "vitest";

const { callSpy } = vi.hoisted(() => ({
  callSpy: vi.fn().mockResolvedValue({ status: "ok" }),
}));

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      on: vi.fn(() => ({ dispose: vi.fn() })),
      call: callSpy,
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

import { PipelineStateService } from "../../src/services/PipelineStateService";

describe("PipelineStateService.notifyPipelineComplete — prMerged forwarding (#266)", () => {
  beforeEach(() => callSpy.mockClear());

  function make(): PipelineStateService {
    const svc = PipelineStateService.createForWorktree("/tmp/worktree-266", 266);
    svc.setRunRepo("nightgauge/acmeapp");
    return svc;
  }

  it("forwards prMerged=true so the Go recorder can honor merge ground truth", async () => {
    await make().notifyPipelineComplete({
      success: false,
      totalDurationMs: 1234,
      stagesRun: ["pr-merge"],
      prMerged: true,
    });

    expect(callSpy).toHaveBeenCalledTimes(1);
    const [method, params] = callSpy.mock.calls[0] as [string, Record<string, unknown>];
    expect(method).toBe("pipeline.notifyComplete");
    expect(params).toMatchObject({
      repo: "nightgauge/acmeapp",
      issueNumber: 266,
      success: false,
      prMerged: true,
    });
  });

  it("defaults prMerged=false when the run's PR did not merge", async () => {
    await make().notifyPipelineComplete({
      success: true,
      totalDurationMs: 10,
      stagesRun: [],
    });

    const [, params] = callSpy.mock.calls[0] as [string, Record<string, unknown>];
    expect(params).toMatchObject({ success: true, prMerged: false });
  });

  // #297/#309: the per-stage execution-path decisions must be forwarded verbatim
  // so the Go notifyComplete handler can replay them onto the authoritative
  // history stage record (execution_path / punt_reason).
  it("forwards stageExecutionPaths + stagePuntReasons verbatim", async () => {
    await make().notifyPipelineComplete({
      success: true,
      totalDurationMs: 100,
      stagesRun: ["pr-create", "pr-merge"],
      stageExecutionPaths: { "pr-create": "llm", "pr-merge": "deterministic" },
      stagePuntReasons: { "pr-create": "missing-validate-context" },
    });

    const [, params] = callSpy.mock.calls[0] as [string, Record<string, unknown>];
    expect(params).toMatchObject({
      stageExecutionPaths: { "pr-create": "llm", "pr-merge": "deterministic" },
      stagePuntReasons: { "pr-create": "missing-validate-context" },
    });
  });

  // Absent maps default to empty objects (Go omitempty drops them on the wire).
  it("defaults the execution-path maps to empty objects when omitted", async () => {
    await make().notifyPipelineComplete({
      success: true,
      totalDurationMs: 10,
      stagesRun: [],
    });

    const [, params] = callSpy.mock.calls[0] as [string, Record<string, unknown>];
    expect(params).toMatchObject({ stageExecutionPaths: {}, stagePuntReasons: {} });
  });
});
