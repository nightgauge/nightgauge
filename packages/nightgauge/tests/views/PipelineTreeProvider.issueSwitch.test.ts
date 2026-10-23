/**
 * PipelineTreeProvider — a state for another issue starts from pending stages
 * (#2105).
 *
 * `syncFromState` only touches the stages the incoming state lists. When the
 * issue changes without a clear in between, as when a run adopted at connect
 * is followed by another run's first snapshot, a stage the previous issue left
 * running kept its spinner under the new issue.
 */

import { describe, it, expect, vi, afterEach } from "vitest";
import { PipelineTreeProvider } from "../../src/views/PipelineTreeProvider";
import { IssueTreeItem } from "../../src/views/items/IssueTreeItem";
import { StageTreeItem } from "../../src/views/items/StageTreeItem";

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      on: vi.fn(() => ({ dispose: vi.fn() })),
    }),
  },
}));

const createMockStateService = () => {
  let fire: (state: unknown) => void = () => {};
  return {
    onStateChanged: vi.fn((cb: (state: unknown) => void) => {
      fire = cb;
      return { dispose: vi.fn() };
    }),
    onTokenUsageUpdated: vi.fn(() => ({ dispose: vi.fn() })),
    onPhaseStart: vi.fn(() => ({ dispose: vi.fn() })),
    onPhaseComplete: vi.fn(() => ({ dispose: vi.fn() })),
    getState: vi.fn(async () => null),
    fire: (state: unknown) => fire(state),
  };
};

function state(issue: number, stages: Record<string, { status: string }>) {
  return {
    issue_number: issue,
    title: `Issue ${issue}`,
    branch: "",
    base_branch: "main",
    started_at: "2026-01-15T09:00:00Z",
    stages,
    tokens: { input: 0, output: 0 },
  };
}

async function stageStatuses(provider: PipelineTreeProvider): Promise<Record<string, string>> {
  const roots = await provider.getChildren();
  const issue = roots.find((item) => item instanceof IssueTreeItem);
  expect(issue).toBeInstanceOf(IssueTreeItem);
  const statuses: Record<string, string> = {};
  for (const child of await provider.getChildren(issue)) {
    if (child instanceof StageTreeItem) statuses[child.stage] = child.getStatus();
  }
  return statuses;
}

describe("PipelineTreeProvider — issue switch (#2105)", () => {
  let provider: PipelineTreeProvider | null = null;

  afterEach(() => {
    provider?.dispose();
    provider = null;
  });

  it("does not carry the previous issue's running stage over to the next issue", async () => {
    provider = new PipelineTreeProvider();
    const service = createMockStateService();
    provider.setStateService(service as never);

    service.fire(state(112, { "feature-dev": { status: "running" } }));
    expect((await stageStatuses(provider))["feature-dev"]).toBe("running");

    service.fire(state(133, { "pipeline-start": { status: "running" } }));
    const statuses = await stageStatuses(provider);
    expect(statuses["pipeline-start"]).toBe("running");
    expect(statuses["feature-dev"]).toBe("pending");
  });

  it("keeps stage statuses across snapshots of the same issue", async () => {
    provider = new PipelineTreeProvider();
    const service = createMockStateService();
    provider.setStateService(service as never);

    service.fire(state(133, { "pipeline-start": { status: "complete" } }));
    service.fire(state(133, { "issue-pickup": { status: "running" } }));
    const statuses = await stageStatuses(provider);
    expect(statuses["pipeline-start"]).toBe("complete");
    expect(statuses["issue-pickup"]).toBe("running");
  });
});
