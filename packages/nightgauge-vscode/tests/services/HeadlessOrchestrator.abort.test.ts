/**
 * HeadlessOrchestrator.abort.test.ts
 *
 * Tests for stop() — the pipeline abort method.
 * Verifies that stop() calls failStage for the current stage,
 * calls killAllActiveProcesses when there are orphaned processes,
 * and that calling stop() before any stage runs is a safe no-op.
 *
 * @see Issue #2499 - Add Tests for HeadlessOrchestrator Core Pipeline Loop
 */

import { describe, it, expect, beforeEach, vi } from "vitest";
import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { PipelineStateService } from "../../src/services/PipelineStateService";
import type { Logger } from "../../src/utils/logger";
import type { SkillRunResult } from "../../src/utils/skillRunner";
import {
  runStageSkillHeadless,
  hasActiveProcess,
  killAllActiveProcesses,
} from "../../src/utils/skillRunner";

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

describe("HeadlessOrchestrator abort handling (Issue #2499)", () => {
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

  it("stop() before any stage runs does not call failStage", () => {
    // stop() guards with: if (this.stateService && this.currentStage)
    // Before runStage() is called, currentStage is null
    const mockState = createMockStateService();
    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger);

    orchestrator.stop();

    expect(mockState.failStage).not.toHaveBeenCalled();
  });

  it('stop() calls failStage with "Pipeline stopped by user" after a stage has run', async () => {
    const mockState = createMockStateService();
    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger);

    // Run a stage to set this.currentStage
    vi.mocked(runStageSkillHeadless).mockImplementation((_stage, _issueNumber, callbacks) => {
      Promise.resolve().then(() => {
        void callbacks?.onComplete?.({
          success: true,
          exitCode: 0,
        } as SkillRunResult);
      });
      return { kill: vi.fn(), process: null } as any;
    });

    await orchestrator.runStage("feature-dev", 42);
    // currentStage is now 'feature-dev'

    vi.clearAllMocks(); // reset failStage mock to detect the stop() call specifically
    vi.mocked(mockState.failStage).mockResolvedValue(undefined);

    orchestrator.stop();

    // failStage is called asynchronously via .catch() — give it a tick
    await Promise.resolve();

    expect(mockState.failStage).toHaveBeenCalledWith("feature-dev", "Pipeline stopped by user");
  });

  it("stop() calls killAllActiveProcesses when hasActiveProcess returns true", () => {
    const mockState = createMockStateService();
    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger);

    vi.mocked(hasActiveProcess).mockReturnValue(true);

    orchestrator.stop();

    expect(killAllActiveProcesses).toHaveBeenCalled();
  });

  it("stop() calls killAllActiveProcesses unconditionally as a cleanup backstop", () => {
    // Cleanup is now unconditional — previously gated on hasActiveProcess()
    // returning true at entry, but a race between child_process close events
    // and stop() could leave the registry populated. Unconditional cleanup is
    // safe because the registry only holds handles this extension spawned.
    const mockState = createMockStateService();
    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger);

    vi.mocked(hasActiveProcess).mockReturnValue(false);

    orchestrator.stop();

    expect(killAllActiveProcesses).toHaveBeenCalled();
  });

  it("stop() is safe to call multiple times without throwing", () => {
    const mockState = createMockStateService();
    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger);

    expect(() => {
      orchestrator.stop();
      orchestrator.stop();
      orchestrator.stop();
    }).not.toThrow();
  });

  it("abort mid-pipeline stops execution at the aborted stage", async () => {
    // State with feature-dev pending so it will run
    const stateWithFeatureDevPending = {
      ...ALL_STAGES_STATE,
      stages: {
        ...ALL_STAGES_STATE.stages,
        "feature-planning": { status: "complete", auto_retry_count: 0 },
        // feature-dev absent — will run
        "feature-dev": undefined,
      },
    };

    const mockState = createMockStateService();
    vi.mocked(mockState.getState).mockResolvedValue(stateWithFeatureDevPending as any);

    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger, {
      contextFileWaitMs: 0,
    });

    let orchestratorRef = orchestrator;

    vi.mocked(runStageSkillHeadless).mockImplementation((_stage, _issueNumber, callbacks) => {
      // Abort the pipeline before calling onComplete
      orchestratorRef.stop();
      Promise.resolve().then(() => {
        // onComplete fires after abort — pipeline checks abort flag at next loop iteration
        void callbacks?.onComplete?.({
          success: true,
          exitCode: 0,
        } as SkillRunResult);
      });
      return { kill: vi.fn(), process: null } as any;
    });

    const result = await orchestrator.runPipeline(42);

    // Pipeline was aborted — should not complete successfully
    expect(result.success).toBe(false);
  });

  it("getIsRunning returns false before and after runPipeline", async () => {
    const mockState = createMockStateService();
    // All stages already complete so runPipeline exits quickly
    vi.mocked(mockState.getState).mockResolvedValue(ALL_STAGES_STATE);
    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger, {
      contextFileWaitMs: 0,
    });

    expect(orchestrator.getIsRunning()).toBe(false);

    const runPromise = orchestrator.runPipeline(42);
    // During run (before await), isRunning may be true
    await runPromise;

    expect(orchestrator.getIsRunning()).toBe(false);
  });
});
