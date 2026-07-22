/**
 * HeadlessOrchestrator.errorRecovery.test.ts
 *
 * Tests for error propagation through the pipeline loop.
 * Verifies that stage failures are surfaced as pipeline failures,
 * that onStageError and onPipelineComplete callbacks fire correctly,
 * and that the pipeline stops at the first failing stage.
 *
 * Note: Full backtrack/escalation trigger testing requires feedback
 * signal files on disk; these tests focus on the observable failure
 * propagation behavior that flows from runStage() into PipelineRunResult.
 *
 * @see Issue #2499 - Add Tests for HeadlessOrchestrator Core Pipeline Loop
 */

import { describe, it, expect, beforeEach, vi } from "vitest";
import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { PipelineStateService } from "../../src/services/PipelineStateService";
import type { Logger } from "../../src/utils/logger";
import type { SkillRunResult } from "../../src/utils/skillRunner";
import { runStageSkillHeadless } from "../../src/utils/skillRunner";

// Mock skillRunner
vi.mock("../../src/utils/skillRunner", () => ({
  hasActiveProcess: vi.fn().mockReturnValue(false),
  killAllActiveProcesses: vi.fn(),
  getActiveInteractiveProcess: vi.fn().mockReturnValue(null),
  runStageSkillHeadless: vi.fn(),
  getNextStage: vi.fn(),
  getStageLabel: vi.fn((stage: string) => stage),
  resolveModel: vi.fn().mockReturnValue({ model: "claude-sonnet-4-6", source: "default" }),
}));

// Mock fs
vi.mock("fs", async () => {
  const actual = await vi.importActual<typeof import("fs")>("fs");
  return {
    ...actual,
    existsSync: vi.fn().mockReturnValue(true),
    readFileSync: vi.fn().mockReturnValue("{}"),
  };
});

// Mock child_process so preCheckAuth/preCheckIssue pass without real gh CLI.
// #2884: HeadlessOrchestrator uses promisify(exec) and promisify(execFile),
// so the mocks must implement the nodejs.util.promisify.custom symbol.
vi.mock("child_process", async () => {
  const actual = await vi.importActual<typeof import("child_process")>("child_process");
  const kCustom = Symbol.for("nodejs.util.promisify.custom");

  const authStatus =
    "Logged in to github.com account testuser (keyring)\n" +
    "  Token: gho_fake\n  Token scopes: 'gist', 'read:org', 'repo', 'workflow'";
  const issueJson = '{"labels":[],"state":"OPEN","title":"Test issue #42"}';

  const execMock: any = vi.fn();
  execMock[kCustom] = () => Promise.resolve({ stdout: authStatus, stderr: "" });

  const execFileMock: any = vi.fn();
  execFileMock[kCustom] = () => Promise.resolve({ stdout: issueJson, stderr: "" });

  return {
    ...actual,
    exec: execMock,
    execFile: execFileMock,
    execSync: vi.fn().mockReturnValue(authStatus),
    execFileSync: vi.fn().mockReturnValue(issueJson),
  };
});

/**
 * State with pipeline-start and pipeline-finish pre-completed (bookends),
 * and all stages after the target also pre-completed. Only the target
 * stage (feature-dev) will actually run.
 */
function makeStateWithFeatureDevPending() {
  return {
    schema_version: "1.0",
    issue_number: 42,
    stages: {
      "pipeline-start": { status: "complete", auto_retry_count: 0 },
      "issue-pickup": { status: "complete", auto_retry_count: 0 },
      "feature-planning": { status: "complete", auto_retry_count: 0 },
      // feature-dev has auto_retry_count so line 9310 can read it; status
      // is "running" so the resume check won't skip it — it will run.
      "feature-dev": { status: "running", auto_retry_count: 0 },
      "feature-validate": { status: "complete", auto_retry_count: 0 },
      "pr-create": { status: "complete", auto_retry_count: 0 },
      "pr-merge": { status: "complete", auto_retry_count: 0 },
      "pipeline-finish": { status: "complete", auto_retry_count: 0 },
    },
    tokens: {
      total_input: 0,
      total_output: 0,
      total_cache_read: 0,
      total_cache_creation: 0,
      estimated_cost_usd: 0,
    },
  };
}

function createMockStateService(): PipelineStateService {
  return {
    getState: vi.fn().mockResolvedValue(makeStateWithFeatureDevPending()),
    failStage: vi.fn().mockResolvedValue(undefined),
    clearPipeline: vi.fn().mockResolvedValue(undefined),
    initializePipeline: vi.fn().mockResolvedValue(undefined),
    startStage: vi.fn().mockResolvedValue(undefined),
    completeStage: vi.fn().mockResolvedValue(undefined),
    skipStage: vi.fn().mockResolvedValue(undefined),
    deferStage: vi.fn().mockResolvedValue(undefined),
    setExecutionMode: vi.fn().mockResolvedValue(undefined),
    setStageExecutionMode: vi.fn().mockResolvedValue(undefined),
    setStageModelSelection: vi.fn().mockResolvedValue(undefined),
    setStageContextFileSize: vi.fn().mockResolvedValue(undefined),
    updateTokens: vi.fn().mockResolvedValue(undefined),
    validateStageTransition: vi.fn().mockResolvedValue({ allowed: true }),
    onStateChanged: vi.fn().mockReturnValue({ dispose: vi.fn() }),
    clearBatchState: vi.fn().mockResolvedValue(undefined),
    batchUpdate: vi.fn().mockResolvedValue(undefined),
    isPaused: vi.fn().mockResolvedValue(false),
    recordExecutionOutcome: vi.fn().mockResolvedValue({ success: true }),
    setOutcomeType: vi.fn().mockResolvedValue(undefined),
    getBatchState: vi.fn().mockResolvedValue(null),
    clearRetrying: vi.fn().mockResolvedValue(undefined),
    markRetrying: vi.fn().mockResolvedValue(undefined),
    recordAutoRetry: vi.fn().mockResolvedValue(undefined),
    isPipelineComplete: vi.fn().mockReturnValue(false),
    recordToolCall: vi.fn(),
    startPhase: vi.fn().mockResolvedValue(undefined),
    completePhase: vi.fn().mockResolvedValue(undefined),
    hasBatchRunning: vi.fn().mockResolvedValue(false),
    getExecutionMode: vi.fn().mockResolvedValue("automatic"),
    resumePipeline: vi.fn().mockResolvedValue(undefined),
    pausePipeline: vi.fn().mockResolvedValue(undefined),
    setMeta: vi.fn(),
    setLabels: vi.fn().mockResolvedValue(undefined),
    recordBacktrack: vi.fn().mockResolvedValue(undefined),
    setStageProcessPid: vi.fn().mockResolvedValue(undefined),
    failPhase: vi.fn().mockResolvedValue(undefined),
  } as unknown as PipelineStateService;
}

describe("HeadlessOrchestrator error recovery (Issue #2499)", () => {
  let mockLogger: Logger;

  beforeEach(() => {
    vi.clearAllMocks();
    mockLogger = {
      info: vi.fn(),
      warn: vi.fn(),
      error: vi.fn(),
      debug: vi.fn(),
    } as unknown as Logger;
  });

  it("stage failure without feedback signal stops pipeline with success: false", async () => {
    const mockState = createMockStateService();
    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger, {
      contextFileWaitMs: 0,
    });

    vi.mocked(runStageSkillHeadless).mockImplementation((_stage, _issueNumber, callbacks) => {
      Promise.resolve().then(() => {
        void callbacks?.onComplete?.({
          success: false,
          exitCode: 1,
          error: new Error("Claude CLI failed unexpectedly"),
        } as SkillRunResult);
      });
      return { kill: vi.fn(), process: null } as any;
    });

    const result = await orchestrator.runPipeline(42);

    expect(result.success).toBe(false);
    expect(result.failedStage).toBe("feature-dev");
  });

  it("sets failedStage to the stage that failed", async () => {
    const mockState = createMockStateService();
    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger, {
      contextFileWaitMs: 0,
    });

    vi.mocked(runStageSkillHeadless).mockImplementation((_stage, _issueNumber, callbacks) => {
      Promise.resolve().then(() => {
        void callbacks?.onComplete?.({
          success: false,
          exitCode: 1,
          error: new Error("Stage error"),
        } as SkillRunResult);
      });
      return { kill: vi.fn(), process: null } as any;
    });

    const result = await orchestrator.runPipeline(42);

    expect(result.failedStage).toBe("feature-dev");
    expect(result.error).toBeDefined();
  });

  it("calls onStageError callback when stage fails", async () => {
    const mockState = createMockStateService();
    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger, {
      contextFileWaitMs: 0,
    });
    const onStageError = vi.fn();

    vi.mocked(runStageSkillHeadless).mockImplementation((_stage, _issueNumber, callbacks) => {
      Promise.resolve().then(() => {
        void callbacks?.onComplete?.({
          success: false,
          exitCode: 1,
          error: new Error("Stage error"),
        } as SkillRunResult);
      });
      return { kill: vi.fn(), process: null } as any;
    });

    await orchestrator.runPipeline(42, { onStageError });

    expect(onStageError).toHaveBeenCalledWith("feature-dev", expect.any(Error));
  });

  it("calls onPipelineComplete even when stage fails", async () => {
    const mockState = createMockStateService();
    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger, {
      contextFileWaitMs: 0,
    });
    const onPipelineComplete = vi.fn();

    vi.mocked(runStageSkillHeadless).mockImplementation((_stage, _issueNumber, callbacks) => {
      Promise.resolve().then(() => {
        void callbacks?.onComplete?.({
          success: false,
          exitCode: 1,
          error: new Error("Stage error"),
        } as SkillRunResult);
      });
      return { kill: vi.fn(), process: null } as any;
    });

    await orchestrator.runPipeline(42, { onPipelineComplete });

    expect(onPipelineComplete).toHaveBeenCalledWith(expect.objectContaining({ success: false }));
  });

  it("does not run stages after a failing stage", async () => {
    // Use fresh state where feature-planning is also pending (alongside feature-dev)
    const stateWithTwoPendingStages = {
      schema_version: "1.0",
      issue_number: 42,
      stages: {
        "pipeline-start": { status: "complete", auto_retry_count: 0 },
        "issue-pickup": { status: "complete", auto_retry_count: 0 },
        // feature-planning and feature-dev are "running" so resume check won't
        // skip them, but auto_retry_count is present so line 9310 won't throw.
        "feature-planning": { status: "running", auto_retry_count: 0 },
        "feature-dev": { status: "running", auto_retry_count: 0 },
        "feature-validate": { status: "complete", auto_retry_count: 0 },
        "pr-create": { status: "complete", auto_retry_count: 0 },
        "pr-merge": { status: "complete", auto_retry_count: 0 },
        "pipeline-finish": { status: "complete", auto_retry_count: 0 },
      },
      tokens: {
        total_input: 0,
        total_output: 0,
        total_cache_read: 0,
        total_cache_creation: 0,
        estimated_cost_usd: 0,
      },
    };

    const mockState = createMockStateService();
    vi.mocked(mockState.getState).mockResolvedValue(stateWithTwoPendingStages as any);

    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger, {
      contextFileWaitMs: 0,
    });

    let stagesRun: string[] = [];

    vi.mocked(runStageSkillHeadless).mockImplementation((stage, _issueNumber, callbacks) => {
      stagesRun.push(stage);
      Promise.resolve().then(() => {
        // feature-planning fails; feature-dev should never run
        void callbacks?.onComplete?.({
          success: false,
          exitCode: 1,
          error: new Error("Planning stage failed"),
        } as SkillRunResult);
      });
      return { kill: vi.fn(), process: null } as any;
    });

    const result = await orchestrator.runPipeline(42);

    // Only feature-planning should have run; feature-dev should be skipped
    expect(stagesRun).toEqual(["feature-planning"]);
    expect(result.failedStage).toBe("feature-planning");
  });

  it("calls failStage on state service when stage fails via runPipeline", async () => {
    const mockState = createMockStateService();
    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger, {
      contextFileWaitMs: 0,
    });

    vi.mocked(runStageSkillHeadless).mockImplementation((_stage, _issueNumber, callbacks) => {
      Promise.resolve().then(() => {
        void callbacks?.onComplete?.({
          success: false,
          exitCode: 1,
          error: new Error("Stage error"),
        } as SkillRunResult);
      });
      return { kill: vi.fn(), process: null } as any;
    });

    await orchestrator.runPipeline(42);

    // failStage now also carries model/adapter attribution so an early kill
    // still attributes its (real, expensive) cost instead of bucketing as
    // 'unknown'. This failed result carried no resolved model, so both fields
    // are undefined here.
    expect(mockState.failStage).toHaveBeenCalledWith("feature-dev", expect.any(String), {
      model: undefined,
      adapter: undefined,
    });
  });
});
