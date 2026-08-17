/**
 * PipelineTreeProvider — undetermined-branch propagation (#448 reader sweep).
 *
 * `syncFromState` is the path that carries `PipelineState.branch` into the
 * tree: `setIssue({ branch: state.branch })` on first sight, `update()` on
 * every later sync. Post-#448 that field is legitimately `""` from
 * `initializePipeline` and becomes a real branch once issue-pickup resolves
 * one — within the same run, under the same issue number.
 *
 * Two things must hold, and the old code satisfied neither:
 *
 *   1. `""` reaches the item and renders as the shared undetermined label,
 *      not as a blank description.
 *   2. The later real branch actually replaces it. `syncFromState` used to
 *      re-read only `base_branch` on an existing issue, so the item was pinned
 *      at whatever it saw first. Before #448 that staleness was invisible —
 *      the seed was a fabricated `feat/{N}` that merely looked like an answer.
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { PipelineTreeProvider } from "../../src/views/PipelineTreeProvider";
import { IssueTreeItem } from "../../src/views/items/IssueTreeItem";
import { UNDETERMINED_BRANCH_LABEL } from "../../src/views/dashboard/DashboardComponents";

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      on: vi.fn(() => ({ dispose: vi.fn() })),
    }),
  },
}));

/** Minimal PipelineStateService stand-in that lets a test fire state changes. */
const createMockStateService = () => {
  const callbacks: Record<string, Function> = {};
  let currentState: any = null;

  return {
    onStateChanged: vi.fn((cb: Function) => {
      callbacks["stateChanged"] = cb;
      return { dispose: vi.fn() };
    }),
    onTokenUsageUpdated: vi.fn(() => ({ dispose: vi.fn() })),
    onPhaseStart: vi.fn(() => ({ dispose: vi.fn() })),
    onPhaseComplete: vi.fn(() => ({ dispose: vi.fn() })),
    getState: vi.fn(async () => currentState),
    _fireStateChanged: (state: any) => {
      currentState = state;
      callbacks["stateChanged"]?.(state);
    },
  };
};

function makeState(overrides?: Record<string, unknown>): any {
  return {
    schema_version: "1.0",
    issue_number: 448,
    title: "Pipeline-state path still fabricates feat/{N}",
    // The honest seed `initializePipeline` now writes (#448).
    branch: "",
    base_branch: "main",
    started_at: "2026-08-16T00:00:00Z",
    updated_at: "2026-08-16T00:00:00Z",
    execution_mode: "automatic",
    paused: false,
    stages: {
      "pipeline-start": { status: "complete" },
      "issue-pickup": { status: "running" },
      "feature-planning": { status: "pending" },
      "feature-dev": { status: "pending" },
      "feature-validate": { status: "pending" },
      "pr-create": { status: "pending" },
      "pr-merge": { status: "pending" },
      "pipeline-finish": { status: "pending" },
    },
    tokens: {
      total_input: 0,
      total_output: 0,
      total_cache_read: 0,
      total_cache_creation: 0,
      estimated_cost_usd: 0,
    },
    ...overrides,
  };
}

async function currentIssueItem(provider: PipelineTreeProvider): Promise<IssueTreeItem> {
  const roots = await provider.getChildren();
  const issue = roots.find((item) => item instanceof IssueTreeItem);
  expect(issue).toBeInstanceOf(IssueTreeItem);
  return issue as IssueTreeItem;
}

describe("PipelineTreeProvider — undetermined branch (#448)", () => {
  let provider: PipelineTreeProvider | null = null;

  beforeEach(() => {
    vi.clearAllMocks();
  });

  afterEach(() => {
    provider?.dispose();
    provider = null;
  });

  it("renders the undetermined label for a state seeded with an empty branch", async () => {
    provider = new PipelineTreeProvider();
    const stateService = createMockStateService();
    provider.setStateService(stateService as any);

    stateService._fireStateChanged(makeState());

    const issue = await currentIssueItem(provider);
    expect(issue.getInfo().branch).toBe("");
    expect(issue.description).toBe(UNDETERMINED_BRANCH_LABEL);
    expect(issue.description).not.toBe("");
    expect(issue.description).not.toContain("feat/448");
  });

  it("replaces the undetermined label once issue-pickup reports a real branch", async () => {
    provider = new PipelineTreeProvider();
    const stateService = createMockStateService();
    provider.setStateService(stateService as any);

    stateService._fireStateChanged(makeState());
    expect((await currentIssueItem(provider)).description).toBe(UNDETERMINED_BRANCH_LABEL);

    // Same run, same issue number — only the branch resolved.
    stateService._fireStateChanged(
      makeState({ branch: "fix/448-undetermined-branch-reader-sweep" })
    );

    const issue = await currentIssueItem(provider);
    expect(issue.getInfo().branch).toBe("fix/448-undetermined-branch-reader-sweep");
    expect(issue.description).toBe("fix/448-undetermined-branch-reader-sweep");
    expect(issue.description).not.toContain(UNDETERMINED_BRANCH_LABEL);
  });

  it("leaves a resolved branch alone across an unrelated state change", async () => {
    provider = new PipelineTreeProvider();
    const stateService = createMockStateService();
    provider.setStateService(stateService as any);

    stateService._fireStateChanged(makeState({ branch: "feat/448-real" }));
    stateService._fireStateChanged(
      makeState({ branch: "feat/448-real", updated_at: "2026-08-16T01:00:00Z" })
    );

    const issue = await currentIssueItem(provider);
    expect(issue.description).toBe("feat/448-real");
  });
});
