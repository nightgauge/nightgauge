/**
 * HeadlessOrchestrator.blockedByDeferral.test.ts
 *
 * Tests for the blockedBy deferral on the deterministic issue-pickup path
 * (Issue #189 / #305). When the #189 fail-closed guard detects open native
 * `blockedBy` dependencies, the run must terminate as a NON-FAILURE deferral:
 *   - result.deferred === true, result.success === false
 *   - NO failedStage, NO error (never routed as a failure)
 *   - outcome_type "deferred" recorded (never failStage / subagent_crash)
 *   - notifyPipelineComplete carries deferred:true so the Go record write
 *     books outcome="cancelled" with no terminal_failure_kind
 *   - no LLM subagent is spawned (zero tokens)
 *
 * @see Issue #305 - Blocked-issue deferral must not record a failed run
 */

import { describe, it, expect, beforeEach, vi } from "vitest";
import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { PipelineStateService } from "../../src/services/PipelineStateService";
import type { Logger } from "../../src/utils/logger";
import { runStageSkillHeadless } from "../../src/utils/skillRunner";
import { existsSync, readFileSync, writeFileSync, mkdirSync } from "fs";
import { execFileSync, execSync } from "child_process";

// Mock skillRunner (imported by HeadlessOrchestrator)
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

// Mock fs — control existsSync, readFileSync, writeFileSync per test
vi.mock("fs", async () => {
  const actual = await vi.importActual<typeof import("fs")>("fs");
  return {
    ...actual,
    existsSync: vi.fn().mockReturnValue(true),
    readFileSync: vi.fn().mockReturnValue("{}"),
    writeFileSync: vi.fn(),
    mkdirSync: vi.fn(),
  };
});

// Mock child_process so preCheckAuth succeeds (auth OK, scopes present).
vi.mock("child_process", async () => {
  const actual = await vi.importActual<typeof import("child_process")>("child_process");
  const kCustom = Symbol.for("nodejs.util.promisify.custom");

  const authStatus =
    "Logged in to github.com account testuser (keyring)\n" +
    "  Token: gho_fake\n  Token scopes: 'gist', 'read:org', 'repo', 'workflow'";

  const execFileSyncMock = vi.fn().mockImplementation((cmd: string, args: string[]) => {
    const a = args ?? [];
    if (cmd === "git" && a[0] === "branch") return "feat/305-test\n";
    if (cmd === "gh" && a[0] === "repo") return "TestOrg/test-repo";
    return "";
  });

  const execMock: any = vi.fn();
  execMock[kCustom] = () => Promise.resolve({ stdout: authStatus, stderr: "" });

  const execFileMock: any = vi.fn();
  execFileMock[kCustom] = (cmd: string, args: string[], opts?: unknown) => {
    try {
      const stdout = execFileSyncMock(cmd, args, opts) ?? "";
      return Promise.resolve({ stdout: String(stdout), stderr: "" });
    } catch (err) {
      return Promise.reject(err);
    }
  };

  return {
    ...actual,
    exec: execMock,
    execFile: execFileMock,
    execSync: vi.fn().mockReturnValue(authStatus),
    execFileSync: execFileSyncMock,
  };
});

/**
 * State with all stages pre-completed except issue-pickup, so the pipeline
 * loop skips straight to issue-pickup where the blockedBy guard fires.
 */
function makeStateWithoutIssuePickup() {
  return {
    schema_version: "1.0",
    issue_number: 304,
    stages: {
      "pipeline-start": { status: "complete", auto_retry_count: 0 },
      // issue-pickup intentionally absent — will be run
      "feature-planning": { status: "skipped", auto_retry_count: 0 },
      "feature-dev": { status: "skipped", auto_retry_count: 0 },
      "feature-validate": { status: "skipped", auto_retry_count: 0 },
      "pr-create": { status: "skipped", auto_retry_count: 0 },
      "pr-merge": { status: "skipped", auto_retry_count: 0 },
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
    getState: vi.fn().mockResolvedValue(makeStateWithoutIssuePickup()),
    failStage: vi.fn().mockResolvedValue(undefined),
    deferStage: vi.fn().mockResolvedValue(undefined),
    clearPipeline: vi.fn().mockResolvedValue(undefined),
    initializePipeline: vi.fn().mockResolvedValue(undefined),
    startStage: vi.fn().mockResolvedValue(undefined),
    completeStage: vi.fn().mockResolvedValue(undefined),
    skipStage: vi.fn().mockResolvedValue(undefined),
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
    notifyPipelineComplete: vi.fn().mockResolvedValue(undefined),
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

describe("HeadlessOrchestrator blockedBy deferral (Issue #305)", () => {
  let mockLogger: Logger;

  beforeEach(() => {
    vi.clearAllMocks();
    mockLogger = {
      info: vi.fn(),
      warn: vi.fn(),
      error: vi.fn(),
      debug: vi.fn(),
    } as unknown as Logger;

    vi.mocked(existsSync).mockReturnValue(true);
    vi.mocked(readFileSync).mockReturnValue("{}");
    vi.mocked(writeFileSync).mockImplementation(() => {});
    vi.mocked(mkdirSync).mockImplementation(() => undefined as any);

    vi.mocked(execSync).mockReturnValue(
      "Logged in to github.com account testuser (keyring)\n  Token: gho_fake\n  Token scopes: 'gist', 'read:org', 'repo', 'workflow'"
    );
    vi.mocked(execFileSync).mockImplementation((cmd: string, args: unknown[]) => {
      const a = args as string[];
      if (cmd === "git" && a[0] === "branch") return "feat/305-test\n";
      if (cmd === "gh" && a[0] === "repo") return "TestOrg/test-repo";
      return "";
    });
  });

  function makeOrchestratorWithOpenBlockers() {
    const mockState = createMockStateService();
    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger, {
      contextFileWaitMs: 0,
    });
    // Force the deterministic issue-pickup context to report open blockedBy
    // dependencies (the #189 fail-closed condition).
    (
      orchestrator as unknown as {
        contextAssembler: { generateDeterministicContext: unknown };
      }
    ).contextAssembler.generateDeterministicContext = vi.fn().mockResolvedValue({
      generated: false,
      blockedBy: [{ repo: "bowlsheet-flutter", number: 209, title: "Epic still open" }],
    });
    return { orchestrator, mockState };
  }

  it("terminates with a deferred (non-failure) outcome, no failedStage/error", async () => {
    const { orchestrator, mockState } = makeOrchestratorWithOpenBlockers();

    const result = await orchestrator.runPipeline(304);

    // Distinct non-failure outcome — NOT a failure.
    expect(result.deferred).toBe(true);
    expect(result.success).toBe(false);
    expect(result.failedStage).toBeUndefined();
    expect(result.error).toBeUndefined();
    expect(result.outcomeType).toBe("deferred");
    expect(result.deferredStages).toContain("issue-pickup");

    // The stage is recorded as deferred — never failed (no subagent_crash).
    expect(mockState.deferStage).toHaveBeenCalledWith("issue-pickup");
    expect(mockState.setOutcomeType).toHaveBeenCalledWith("deferred");
    expect(mockState.failStage).not.toHaveBeenCalled();

    // No LLM subagent spawned — a deferral spends zero tokens.
    expect(runStageSkillHeadless).not.toHaveBeenCalled();

    // Surfaced at info level, not error level.
    expect(mockLogger.info).toHaveBeenCalledWith(
      expect.stringContaining("issue-pickup deferred — open blockedBy dependencies"),
      expect.any(Object)
    );
  });

  it("threads deferred:true into notifyPipelineComplete so the record books as a non-failure", async () => {
    const { orchestrator, mockState } = makeOrchestratorWithOpenBlockers();

    await orchestrator.runPipeline(304);

    expect(mockState.notifyPipelineComplete).toHaveBeenCalledTimes(1);
    expect(mockState.notifyPipelineComplete).toHaveBeenCalledWith(
      expect.objectContaining({ success: false, deferred: true })
    );
  });
});
