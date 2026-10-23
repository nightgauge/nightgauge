/**
 * HeadlessOrchestrator.resumeAtPrMerge.test.ts
 *
 * Issue #1531 — a re-dispatched issue whose head already has an OPEN pipeline
 * PR must resume at pr-merge, not re-plan.
 *
 * The #500 restore path only fired when pipeline state REMEMBERED pr-create
 * completing. A re-dispatch after a killed pr-create satisfies neither half of
 * that: the new slot gets a fresh PipelineStateService with no state.json (so
 * every stage reads pending), while the reused worktree still holds
 * `pr-{N}.json` naming an open PR. platform#1431 therefore re-ran
 * issue-pickup → feature-planning → feature-dev → feature-validate → pr-create
 * ($2.11, 16 minutes) only to rediscover PR #1447 and push more commits onto it.
 *
 * An open PR recorded on disk is proof the work shipped, whatever state
 * remembers.
 */

import { describe, it, expect, beforeEach, vi } from "vitest";
import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { PipelineStateService } from "../../src/services/PipelineStateService";
import type { Logger } from "../../src/utils/logger";
import { existsSync, readFileSync } from "fs";

vi.mock("../../src/utils/skillRunner", () => ({
  hasActiveProcess: vi.fn().mockReturnValue(false),
  killAllActiveProcesses: vi.fn(),
  getActiveInteractiveProcess: vi.fn().mockReturnValue(null),
  runStageSkillHeadless: vi.fn(),
  getNextStage: vi.fn(),
  getStageLabel: vi.fn((stage: string) => stage),
  resolveModel: vi
    .fn()
    .mockReturnValue({ model: "claude-haiku-4-5-20251001", source: "stage-default" }),
}));

vi.mock("fs", async () => {
  const actual = await vi.importActual<typeof import("fs")>("fs");
  return {
    ...actual,
    existsSync: vi.fn().mockReturnValue(false),
    readFileSync: vi.fn().mockReturnValue("{}"),
    writeFileSync: vi.fn(),
    mkdirSync: vi.fn(),
  };
});

vi.mock("vscode", () => ({
  window: {
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
    showWarningMessage: vi.fn(),
    showInformationMessage: vi.fn(),
  },
  workspace: { workspaceFolders: [], getConfiguration: vi.fn(() => ({ get: vi.fn() })) },
}));

/** A fresh slot's state: nothing has run yet. */
function freshState(issueNumber: number) {
  return {
    schema_version: "1.0",
    issue_number: issueNumber,
    stages: {},
    tokens: {
      total_input: 0,
      total_output: 0,
      total_cache_read: 0,
      total_cache_creation: 0,
      estimated_cost_usd: 0,
    },
  };
}

function createMockStateService(state: unknown): PipelineStateService {
  return {
    getState: vi.fn().mockResolvedValue(state),
    completeStage: vi.fn().mockResolvedValue(undefined),
    skipStage: vi.fn().mockResolvedValue(undefined),
    setMeta: vi.fn(),
    getRunId: vi.fn().mockReturnValue(null),
    getRunRepo: vi.fn().mockReturnValue(""),
    onStateChanged: vi.fn().mockReturnValue({ dispose: vi.fn() }),
  } as unknown as PipelineStateService;
}

const PR_CONTEXT_SUFFIX = "pr-1431.json";

describe("HeadlessOrchestrator — resume at pr-merge when an open PR exists (Issue #1531)", () => {
  let logger: Logger;

  beforeEach(() => {
    vi.clearAllMocks();
    logger = {
      info: vi.fn(),
      warn: vi.fn(),
      error: vi.fn(),
      debug: vi.fn(),
    } as unknown as Logger;
  });

  /** Make only `pr-1431.json` exist, with the given contents. */
  function withPrContext(contents: unknown | null) {
    vi.mocked(existsSync).mockImplementation((p: unknown) =>
      String(p).endsWith(PR_CONTEXT_SUFFIX) && contents !== null ? true : false
    );
    vi.mocked(readFileSync).mockImplementation((p: unknown) => {
      if (String(p).endsWith(PR_CONTEXT_SUFFIX) && contents !== null) {
        return typeof contents === "string" ? contents : JSON.stringify(contents);
      }
      return "{}";
    });
  }

  function makeOrchestrator(state: unknown) {
    const mockState = createMockStateService(state);
    const orchestrator = new HeadlessOrchestrator(mockState, logger, { contextFileWaitMs: 0 });
    return { orchestrator, mockState };
  }

  async function detect(orchestrator: HeadlessOrchestrator, issueNumber: number) {
    await (
      orchestrator as unknown as {
        detectAndRestoreExistingPr(n: number): Promise<void>;
      }
    ).detectAndRestoreExistingPr(issueNumber);
  }

  it("fast-forwards a fresh run to pr-merge when pr-{N}.json names an OPEN PR", async () => {
    withPrContext({
      schema_version: "1.0",
      issue_number: 1431,
      pr_number: 1447,
      pr_url: "https://github.com/acme/acme-platform/pull/1447",
      status: "open",
    });

    const { orchestrator, mockState } = makeOrchestrator(freshState(1431));
    await detect(orchestrator, 1431);

    // Every AI stage before the merge is recorded skipped, with the reason.
    for (const stage of ["issue-pickup", "feature-planning", "feature-dev", "feature-validate"]) {
      expect(mockState.skipStage).toHaveBeenCalledWith(stage, expect.stringContaining("PR #1447"));
    }
    // pr-create is complete, not skipped — its deliverable really exists, and
    // pr-merge's pre-condition check needs it to point at pr-{N}.json.
    expect(mockState.completeStage).toHaveBeenCalledWith("pr-create");
    expect(mockState.skipStage).not.toHaveBeenCalledWith("pr-create", expect.anything());
    // pr-merge itself is never pre-empted.
    expect(mockState.skipStage).not.toHaveBeenCalledWith("pr-merge", expect.anything());
    expect(mockState.completeStage).not.toHaveBeenCalledWith("pr-merge");
    // The PR is recorded on the run so downstream consumers can name it.
    expect(mockState.setMeta).toHaveBeenCalledWith({ pr_number: 1447 });
  });

  it("does not skip anything when no PR context exists (the ordinary new issue)", async () => {
    withPrContext(null);

    const { orchestrator, mockState } = makeOrchestrator(freshState(1431));
    await detect(orchestrator, 1431);

    expect(mockState.skipStage).not.toHaveBeenCalled();
    expect(mockState.completeStage).not.toHaveBeenCalled();
  });

  it("does not skip anything when the recorded PR is closed", async () => {
    withPrContext({ issue_number: 1431, pr_number: 1447, status: "closed" });

    const { orchestrator, mockState } = makeOrchestrator(freshState(1431));
    await detect(orchestrator, 1431);

    expect(mockState.skipStage).not.toHaveBeenCalled();
    expect(mockState.completeStage).not.toHaveBeenCalled();
  });

  it("does not skip anything when the PR context is unreadable", async () => {
    withPrContext("{ not json");

    const { orchestrator, mockState } = makeOrchestrator(freshState(1431));
    await detect(orchestrator, 1431);

    expect(mockState.skipStage).not.toHaveBeenCalled();
    expect(mockState.completeStage).not.toHaveBeenCalled();
  });

  it("does not skip anything when the context records no PR number", async () => {
    withPrContext({ issue_number: 1431, status: "open" });

    const { orchestrator, mockState } = makeOrchestrator(freshState(1431));
    await detect(orchestrator, 1431);

    expect(mockState.skipStage).not.toHaveBeenCalled();
    expect(mockState.completeStage).not.toHaveBeenCalled();
  });

  it("leaves the #500 path alone when state already remembers pr-create completing", async () => {
    withPrContext({ issue_number: 1431, pr_number: 1447, status: "open" });

    const state = freshState(1431) as unknown as {
      stages: Record<string, { status: string }>;
    };
    state.stages["pr-create"] = { status: "complete" };

    const { orchestrator, mockState } = makeOrchestrator(state);
    await detect(orchestrator, 1431);

    // Resume already works on its own — nothing is re-marked.
    expect(mockState.skipStage).not.toHaveBeenCalled();
    expect(mockState.completeStage).not.toHaveBeenCalled();
  });
});
