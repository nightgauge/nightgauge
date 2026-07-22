/**
 * HeadlessOrchestrator.stageTransitions.test.ts
 *
 * Tests for runStage() — the stage-level execution method.
 * Verifies callback invocation, state service updates, transition
 * blocking, and StageRunResult shape.
 *
 * @see Issue #2499 - Add Tests for HeadlessOrchestrator Core Pipeline Loop
 */

import { describe, it, expect, beforeEach, vi } from "vitest";
import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { PipelineStateService } from "../../src/services/PipelineStateService";
import type { Logger } from "../../src/utils/logger";
import type { SkillRunResult } from "../../src/utils/skillRunner";
import { runStageSkillHeadless } from "../../src/utils/skillRunner";

// Mock skillRunner (imported by HeadlessOrchestrator)
vi.mock("../../src/utils/skillRunner", () => ({
  hasActiveProcess: vi.fn().mockReturnValue(false),
  killAllActiveProcesses: vi.fn(),
  getActiveInteractiveProcess: vi.fn().mockReturnValue(null),
  runStageSkillHeadless: vi.fn(),
  getNextStage: vi.fn(),
  getStageLabel: vi.fn((stage: string) => stage),
  resolveModel: vi.fn().mockReturnValue({ model: "claude-sonnet-4-6", source: "default" }),
}));

// Mock fs to avoid reading real disk files
vi.mock("fs", async () => {
  const actual = await vi.importActual<typeof import("fs")>("fs");
  return {
    ...actual,
    existsSync: vi.fn().mockReturnValue(true),
    readFileSync: vi.fn().mockReturnValue("{}"),
  };
});

const ALL_STAGES_STATE = {
  schema_version: "1.0",
  issue_number: 42,
  stages: {
    "pipeline-start": { status: "complete", auto_retry_count: 0 },
    "issue-pickup": { status: "complete", auto_retry_count: 0 },
    "feature-planning": { status: "complete", auto_retry_count: 0 },
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

function createMockStateService(): PipelineStateService {
  return {
    getState: vi.fn().mockResolvedValue(ALL_STAGES_STATE),
    failStage: vi.fn().mockResolvedValue(undefined),
    clearPipeline: vi.fn().mockResolvedValue(undefined),
    initializePipeline: vi.fn().mockResolvedValue(undefined),
    startStage: vi.fn().mockResolvedValue(undefined),
    completeStage: vi.fn().mockResolvedValue(undefined),
    recordStageModel: vi.fn().mockResolvedValue(undefined),
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
    // Additional methods required by runStage() code paths
    setMeta: vi.fn(),
    setLabels: vi.fn().mockResolvedValue(undefined),
    recordBacktrack: vi.fn().mockResolvedValue(undefined),
    setStageProcessPid: vi.fn().mockResolvedValue(undefined),
    failPhase: vi.fn().mockResolvedValue(undefined),
  } as unknown as PipelineStateService;
}

/** Helper: mock runStageSkillHeadless to resolve with success */
function mockSkillSuccess(attribution?: { servedModel?: string; adapter?: string }) {
  vi.mocked(runStageSkillHeadless).mockImplementation((_stage, _issueNumber, callbacks) => {
    Promise.resolve().then(() => {
      void callbacks?.onComplete?.({
        success: true,
        exitCode: 0,
        // #268: served-model + adapter attribution the orchestrator threads into
        // completeStage. Absent by default so tests that don't care are unchanged.
        servedModel: attribution?.servedModel,
        adapterDecision: attribution?.adapter
          ? ({ adapter: attribution.adapter, source: "test" } as any)
          : undefined,
      } as SkillRunResult);
    });
    return { kill: vi.fn(), process: null } as any;
  });
}

/** Helper: mock runStageSkillHeadless to resolve with failure */
function mockSkillFailure(message = "Claude CLI failed") {
  vi.mocked(runStageSkillHeadless).mockImplementation((_stage, _issueNumber, callbacks) => {
    Promise.resolve().then(() => {
      void callbacks?.onComplete?.({
        success: false,
        exitCode: 1,
        error: new Error(message),
      } as SkillRunResult);
    });
    return { kill: vi.fn(), process: null } as any;
  });
}

describe("HeadlessOrchestrator stage transitions (Issue #2499)", () => {
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

  it("calls onStageStart when stage begins", async () => {
    const mockState = createMockStateService();
    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger);
    const onStageStart = vi.fn();
    mockSkillSuccess();

    await orchestrator.runStage("feature-dev", 42, { onStageStart });

    expect(onStageStart).toHaveBeenCalledWith("feature-dev");
  });

  it("calls onStageComplete with correct stage and success result", async () => {
    const mockState = createMockStateService();
    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger);
    const onStageComplete = vi.fn();
    mockSkillSuccess();

    const result = await orchestrator.runStage("feature-dev", 42, {
      onStageComplete,
    });

    expect(result.success).toBe(true);
    expect(result.stage).toBe("feature-dev");
    expect(typeof result.durationMs).toBe("number");
    expect(onStageComplete).toHaveBeenCalledWith(
      "feature-dev",
      expect.objectContaining({ success: true, stage: "feature-dev" })
    );
  });

  it("calls onStageError when stage fails", async () => {
    const mockState = createMockStateService();
    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger);
    const onStageError = vi.fn();
    mockSkillFailure("Claude CLI exited with code 1");

    const result = await orchestrator.runStage("feature-dev", 42, {
      onStageError,
    });

    expect(result.success).toBe(false);
    expect(result.stage).toBe("feature-dev");
    expect(onStageError).toHaveBeenCalledWith("feature-dev", expect.any(Error));
  });

  it("returns blocked result when validateStageTransition disallows transition", async () => {
    const mockState = createMockStateService();
    vi.mocked(mockState.validateStageTransition).mockResolvedValue({
      allowed: false,
      error: "Stage already completed",
    } as any);
    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger);

    const result = await orchestrator.runStage("feature-dev", 42);

    expect(result.success).toBe(false);
    expect(result.stage).toBe("feature-dev");
    expect(runStageSkillHeadless).not.toHaveBeenCalled();
  });

  it("calls startStage on state service when stage begins", async () => {
    const mockState = createMockStateService();
    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger);
    mockSkillSuccess();

    await orchestrator.runStage("feature-dev", 42);

    expect(mockState.startStage).toHaveBeenCalledWith("feature-dev", expect.any(Object));
  });

  it("calls completeStage on state service when stage succeeds, threading served model + adapter (#268)", async () => {
    const mockState = createMockStateService();
    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger);
    mockSkillSuccess({ servedModel: "claude-opus-4-8", adapter: "claude" });

    await orchestrator.runStage("feature-dev", 42);

    // #268: the served model + executing adapter must be forwarded so the Go
    // notify handler attributes the stage (by-model cost breakdown + Adapter
    // Mix donut). Before the fix completeStage was called with the stage alone.
    expect(mockState.completeStage).toHaveBeenCalledWith("feature-dev", {
      model: "claude-opus-4-8",
      adapter: "claude",
    });
  });

  it("calls failStage on state service when stage fails", async () => {
    const mockState = createMockStateService();
    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger);
    mockSkillFailure();

    await orchestrator.runStage("feature-dev", 42);

    // failStage now also carries model/adapter attribution so an early kill
    // still attributes its (real, expensive) cost instead of bucketing as
    // 'unknown'. This failed result carried no resolved model, so both fields
    // are undefined here.
    expect(mockState.failStage).toHaveBeenCalledWith("feature-dev", expect.any(String), {
      model: undefined,
      adapter: undefined,
    });
  });

  it("records the resolved stage model up-front via onModelResolved → recordStageModel (#367)", async () => {
    const mockState = createMockStateService();
    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger);

    // Drive onModelResolved before completion, mirroring skillRunner firing it
    // once the model is resolved (before the CLI spawns).
    vi.mocked(runStageSkillHeadless).mockImplementation((_stage, _issueNumber, callbacks) => {
      Promise.resolve().then(() => {
        callbacks?.onModelResolved?.("feature-dev", "claude-fable-5", "claude");
        void callbacks?.onComplete?.({
          success: true,
          exitCode: 0,
        } as SkillRunResult);
      });
      return { kill: vi.fn(), process: null } as any;
    });

    await orchestrator.runStage("feature-dev", 42);

    // The orchestrator forwards the up-front attribution to recordStageModel so
    // a stage killed before completeStage/failStage still attributes its true
    // model instead of 'unknown' (#367).
    expect(mockState.recordStageModel).toHaveBeenCalledWith("feature-dev", {
      model: "claude-fable-5",
      adapter: "claude",
    });
  });

  it("runs bookend stage pipeline-start without invoking runStageSkillHeadless", async () => {
    const mockState = createMockStateService();
    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger);

    const result = await orchestrator.runStage("pipeline-start", 42);

    expect(runStageSkillHeadless).not.toHaveBeenCalled();
    expect(result.stage).toBe("pipeline-start");
  });

  it("onStageStart is called before runStageSkillHeadless is invoked", async () => {
    const mockState = createMockStateService();
    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger);
    const callOrder: string[] = [];

    const onStageStart = vi.fn(() => {
      callOrder.push("onStageStart");
    });

    vi.mocked(runStageSkillHeadless).mockImplementation((_stage, _issueNumber, callbacks) => {
      callOrder.push("runStageSkillHeadless");
      Promise.resolve().then(() => {
        void callbacks?.onComplete?.({
          success: true,
          exitCode: 0,
        } as SkillRunResult);
      });
      return { kill: vi.fn(), process: null } as any;
    });

    await orchestrator.runStage("feature-dev", 42, { onStageStart });

    expect(callOrder[0]).toBe("onStageStart");
    expect(callOrder[1]).toBe("runStageSkillHeadless");
  });
});
