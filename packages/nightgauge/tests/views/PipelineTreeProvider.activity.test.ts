/**
 * PipelineTreeProvider — stage activity wiring (#1897).
 *
 * A tool-call commit becomes a child node of the running stage, runtime-state
 * gate failures reach the stage, and the 30-second ticker flips a silent
 * stage to idle with no event.
 */

import { describe, it, expect, vi, afterEach } from "vitest";
import { PipelineTreeProvider, ACTIVITY_TICK_MS } from "../../src/views/PipelineTreeProvider";
import { StageTreeItem } from "../../src/views/items/StageTreeItem";
import { StageActivityTreeItem } from "../../src/views/items/StageActivityTreeItem";

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      on: vi.fn(() => ({ dispose: vi.fn() })),
    }),
  },
}));

const T0 = Date.parse("2026-10-01T10:00:00.000Z");

function mockStateService(state: unknown) {
  const cbs: Record<string, (v: unknown) => void> = {};
  const sub = (name: string) => (cb: (v: unknown) => void) => {
    cbs[name] = cb;
    return { dispose: () => {} };
  };
  return {
    service: {
      onStateChanged: sub("state"),
      onToolCallRecorded: sub("tool"),
      onTokenUsageUpdated: sub("token"),
      onPhaseStart: sub("phaseStart"),
      onPhaseComplete: sub("phaseComplete"),
      getState: async () => state,
    },
    fire: (name: string, v: unknown) => cbs[name]?.(v),
  };
}

function makeState(extra: Record<string, unknown> = {}) {
  return {
    issue_number: 1644,
    title: "t",
    branch: "feat/1644",
    started_at: new Date(T0).toISOString(),
    stages: {
      "pr-merge": { status: "running", started_at: new Date(T0).toISOString() },
    },
    ...extra,
  };
}

function prMerge(provider: PipelineTreeProvider): StageTreeItem {
  return (provider as unknown as { stages: Map<string, StageTreeItem> }).stages.get("pr-merge")!;
}

describe("PipelineTreeProvider activity", () => {
  afterEach(() => vi.useRealTimers());

  it("a tool-call commit becomes a child of the running stage", () => {
    vi.useFakeTimers();
    vi.setSystemTime(T0 + 60_000);
    const provider = new PipelineTreeProvider();
    const { service, fire } = mockStateService(null);
    provider.setStateService(service as never);
    fire("state", makeState());

    fire("tool", {
      tool: "tool_result",
      stage: "pr-merge",
      target: "pr-merge",
      timestamp: new Date(T0 + 30_000),
      result: "[feat/1644 fea8ce8d] resolve address-less sends against their fd\n",
    });

    const children = prMerge(provider)
      .getChildren()
      .filter((c) => c instanceof StageActivityTreeItem);
    expect(children.map((c) => c.label)).toEqual([
      "fea8ce8d  resolve address-less sends against their fd",
    ]);
    expect(String(prMerge(provider).description)).toMatch(/^remediating · round 1/);
    provider.dispose();
  });

  it("runtime-state gate failures reach the stage", () => {
    vi.useFakeTimers();
    vi.setSystemTime(T0 + 60_000);
    const provider = new PipelineTreeProvider();
    const { service, fire } = mockStateService(null);
    provider.setStateService(service as never);
    fire(
      "state",
      makeState({
        stage_gate_results: {
          "pr-merge": [
            {
              gate_name: "pr-merge",
              passed: false,
              reason: "PR not MERGED",
              timestamp: new Date(T0 + 30_000).toISOString(),
            },
          ],
        },
      })
    );
    expect(String(prMerge(provider).description)).toMatch(/^retrying after gate: PR not MERGED/);
    provider.dispose();
  });

  it("the ticker flips a silent running stage to idle with no event", () => {
    vi.useFakeTimers();
    vi.setSystemTime(T0 + 60_000);
    const provider = new PipelineTreeProvider();
    provider.setIdleIndicatorSeconds(600);
    const { service, fire } = mockStateService(null);
    provider.setStateService(service as never);
    fire("state", makeState());
    expect(prMerge(provider).getActivitySummary()?.state).toBe("working");

    vi.advanceTimersByTime(10 * 60_000 + ACTIVITY_TICK_MS);
    expect(prMerge(provider).getActivitySummary()?.state).toBe("idle");
    expect(String(prMerge(provider).description)).toMatch(/^idle 1[01]m/);
    provider.dispose();
  });
});
