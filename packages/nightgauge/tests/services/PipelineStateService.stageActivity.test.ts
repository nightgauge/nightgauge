/**
 * PipelineStateService — stage activity ingestion (#1897).
 *
 * Go runtime state carries `stageGateResults` and `stageRecoveryAttempts`;
 * the service used to drop both. And `recordToolCall` was a no-op stub, so
 * `onToolCallRecorded` never fired.
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

const ISSUE = 1897;

async function makeService() {
  const { PipelineStateService } = await import("../../src/services/PipelineStateService");
  PipelineStateService.resetInstance();
  return PipelineStateService.createForWorktree("/tmp/repo", ISSUE);
}

describe("stage activity ingestion", () => {
  it("maps stageGateResults, stageRecoveryAttempts and escalationHistory from a snapshot", async () => {
    const svc = await makeService();
    const gate = {
      gate_name: "pr-merge",
      passed: false,
      reason: "PR not MERGED",
      timestamp: "2026-10-01T10:30:00Z",
    };
    const recovery = {
      action: "skill-exited-without-merging",
      recovered: false,
      at: "2026-10-01T10:31:00Z",
    };
    const escalation = {
      stage: "pr-merge",
      fromModel: "sonnet",
      toModel: "opus",
      reason: "gate_failed",
      at: "2026-10-01T10:32:00Z",
    };

    svc.applyRuntimeSnapshot({
      issueNumber: ISSUE,
      stage: "pr-merge",
      stageStart: "2026-10-01T10:00:00Z",
      stageGateResults: { "pr-merge": [gate] },
      stageRecoveryAttempts: { "pr-merge": [recovery] },
      escalationHistory: [escalation],
    });

    const state = await svc.getState();
    expect(state?.stage_gate_results).toEqual({ "pr-merge": [gate] });
    expect(state?.stage_recovery_attempts).toEqual({ "pr-merge": [recovery] });
    expect(state?.escalation_history).toEqual([escalation]);
  });

  it("recordToolCall fires onToolCallRecorded", async () => {
    const svc = await makeService();
    const seen: unknown[] = [];
    svc.onToolCallRecorded((e) => seen.push(e));
    const event = {
      tool: "tool_result",
      stage: "pr-merge",
      target: "pr-merge",
      timestamp: new Date(),
      result: "x",
    };
    svc.recordToolCall(event);
    expect(seen).toEqual([event]);
  });
});
