/**
 * HeadlessOrchestrator.contextHandoff.test.ts
 *
 * Tests for validateStageContextOutput() — the context file validation
 * called after each skill stage. Verifies that missing files fail the
 * pipeline, malformed JSON fails the pipeline, and valid JSON (even with
 * schema mismatches) allows the pipeline to continue.
 *
 * These tests exercise runPipeline() with pre-completed surrounding stages
 * so that only the target stage (feature-planning) actually runs.
 *
 * @see Issue #2499 - Add Tests for HeadlessOrchestrator Core Pipeline Loop
 */

import { describe, it, expect, beforeEach, vi } from "vitest";
import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { PipelineStateService } from "../../src/services/PipelineStateService";
import type { Logger } from "../../src/utils/logger";
import type { SkillRunResult } from "../../src/utils/skillRunner";
import { runStageSkillHeadless } from "../../src/utils/skillRunner";
import { existsSync, readFileSync } from "fs";

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

// Mock fs — control existsSync and readFileSync per test
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
// so the mocks must implement the nodejs.util.promisify.custom symbol or
// the await chain returns undefined and pipeline-start fails before
// dispatching the test stage.
vi.mock("child_process", async () => {
  const actual = await vi.importActual<typeof import("child_process")>("child_process");
  const kCustom = Symbol.for("nodejs.util.promisify.custom");

  const authStatus =
    "Logged in to github.com account testuser (keyring)\n" +
    "  Token: gho_fake\n  Token scopes: 'gist', 'read:org', 'repo', 'workflow'";
  const issueJson = '{"labels":[],"state":"OPEN","title":"Test issue #42"}';

  // promisify(exec) — used by preCheckAuth ("gh auth status 2>&1")
  const execMock: any = vi.fn();
  execMock[kCustom] = () => Promise.resolve({ stdout: authStatus, stderr: "" });

  // promisify(execFile) — used by preCheckIssue and other gh/git calls
  const execFileMock: any = vi.fn();
  execFileMock[kCustom] = () => Promise.resolve({ stdout: issueJson, stderr: "" });

  return {
    ...actual,
    exec: execMock,
    execFile: execFileMock,
    // Keep sync mocks for cold paths still using them
    execSync: vi.fn().mockReturnValue(authStatus),
    execFileSync: vi.fn().mockReturnValue(issueJson),
  };
});

/**
 * State with all stages pre-completed except feature-planning.
 * The pipeline loop will skip all pre-completed stages and only
 * run feature-planning.
 */
function makeStateWithoutFeaturePlanning() {
  return {
    schema_version: "1.0",
    issue_number: 42,
    stages: {
      "pipeline-start": { status: "complete", auto_retry_count: 0 },
      "issue-pickup": { status: "complete", auto_retry_count: 0 },
      // feature-planning intentionally absent — will be run
      // feature-dev is "skipped" so earlyExitHandled=true, which prevents
      // reconcileCompletionSideEffects from running (it calls live gh CLI).
      "feature-dev": { status: "skipped", auto_retry_count: 0 },
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
    getState: vi.fn().mockResolvedValue(makeStateWithoutFeaturePlanning()),
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

describe("HeadlessOrchestrator context handoff validation (Issue #2499)", () => {
  let mockLogger: Logger;

  beforeEach(() => {
    vi.clearAllMocks();
    mockLogger = {
      info: vi.fn(),
      warn: vi.fn(),
      error: vi.fn(),
      debug: vi.fn(),
    } as unknown as Logger;

    // Default: all files exist and contain valid (empty) JSON
    vi.mocked(existsSync).mockReturnValue(true);
    vi.mocked(readFileSync).mockReturnValue("{}");
  });

  it("allows pipeline to continue when context file contains valid JSON", async () => {
    const mockState = createMockStateService();
    // contextFileWaitMs: 0 skips polling loop so tests run instantly
    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger, {
      contextFileWaitMs: 0,
    });

    // feature-planning succeeds; context file exists and has valid JSON (schema mismatch is warn-only)
    vi.mocked(existsSync).mockReturnValue(true);
    vi.mocked(readFileSync).mockReturnValue("{}");

    vi.mocked(runStageSkillHeadless).mockImplementation((_stage, _issueNumber, callbacks) => {
      Promise.resolve().then(() => {
        void callbacks?.onComplete?.({
          success: true,
          exitCode: 0,
        } as SkillRunResult);
      });
      return { kill: vi.fn(), process: null } as any;
    });

    const result = await orchestrator.runPipeline(42);

    // Pipeline continues past context validation (schema mismatch is warn-only)
    expect(result.success).toBe(true);
    expect(runStageSkillHeadless).toHaveBeenCalled();
  });

  it("fails pipeline when output context file is missing after stage success", async () => {
    const mockState = createMockStateService();
    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger, {
      contextFileWaitMs: 0,
    });

    // issue-42.json must exist (prerequisite for feature-planning)
    // planning-42.json must NOT exist (the output context file)
    vi.mocked(existsSync).mockImplementation((p) => {
      if (String(p).includes("planning-42")) return false;
      return true;
    });

    vi.mocked(runStageSkillHeadless).mockImplementation((_stage, _issueNumber, callbacks) => {
      Promise.resolve().then(() => {
        void callbacks?.onComplete?.({
          success: true,
          exitCode: 0,
        } as SkillRunResult);
      });
      return { kill: vi.fn(), process: null } as any;
    });

    const result = await orchestrator.runPipeline(42);

    expect(result.success).toBe(false);
    expect(result.failedStage).toBe("feature-planning");
    expect(result.error?.message).toContain("context file not found");
  });

  it("fails pipeline when output context file contains malformed JSON", async () => {
    const mockState = createMockStateService();
    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger, {
      contextFileWaitMs: 0,
    });

    // All files exist, but planning-42.json contains invalid JSON
    vi.mocked(existsSync).mockReturnValue(true);
    vi.mocked(readFileSync).mockImplementation((p) => {
      if (String(p).includes("planning-42")) return "not valid json {{}}";
      return "{}";
    });

    vi.mocked(runStageSkillHeadless).mockImplementation((_stage, _issueNumber, callbacks) => {
      Promise.resolve().then(() => {
        void callbacks?.onComplete?.({
          success: true,
          exitCode: 0,
        } as SkillRunResult);
      });
      return { kill: vi.fn(), process: null } as any;
    });

    const result = await orchestrator.runPipeline(42);

    expect(result.success).toBe(false);
    expect(result.failedStage).toBe("feature-planning");
    expect(result.error?.message).toContain("invalid JSON");
  });

  it("warns but continues when context JSON has schema mismatches", async () => {
    const mockState = createMockStateService();
    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger, {
      contextFileWaitMs: 0,
    });

    // Valid JSON but schema mismatch (empty object fails most Zod schemas)
    vi.mocked(existsSync).mockReturnValue(true);
    vi.mocked(readFileSync).mockReturnValue('{"unexpected_field": true}');

    vi.mocked(runStageSkillHeadless).mockImplementation((_stage, _issueNumber, callbacks) => {
      Promise.resolve().then(() => {
        void callbacks?.onComplete?.({
          success: true,
          exitCode: 0,
        } as SkillRunResult);
      });
      return { kill: vi.fn(), process: null } as any;
    });

    const result = await orchestrator.runPipeline(42);

    // Schema mismatches are warn-only — pipeline continues
    expect(result.success).toBe(true);
    // Logger.warn should be called for schema mismatch
    expect(mockLogger.warn).toHaveBeenCalled();
  });

  it("fails pipeline preconditions when prerequisite context file is missing", async () => {
    const mockState = createMockStateService();
    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger, {
      contextFileWaitMs: 0,
    });

    // issue-42.json is the prerequisite for feature-planning (produced by issue-pickup).
    // issue-pickup is 'complete' in state, so the resume check reads issue-42.json first.
    // We return true on that first check (so resume-skip succeeds), then false on the
    // second check (feature-planning's precondition check), so the precondition fails.
    let issueFileCheckCount = 0;
    vi.mocked(existsSync).mockImplementation((p) => {
      if (String(p).includes("issue-42")) {
        issueFileCheckCount++;
        return issueFileCheckCount <= 1; // true on resume check, false on precondition
      }
      return true;
    });

    const result = await orchestrator.runPipeline(42);

    // Pipeline fails before even running feature-planning
    expect(result.success).toBe(false);
    expect(result.failedStage).toBe("feature-planning");
    expect(result.error?.message).toContain("required input file");
    // runStageSkillHeadless should NOT have been called
    expect(runStageSkillHeadless).not.toHaveBeenCalled();
  });
});
