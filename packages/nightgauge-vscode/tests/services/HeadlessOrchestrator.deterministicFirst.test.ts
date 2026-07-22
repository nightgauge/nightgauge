/**
 * HeadlessOrchestrator.deterministicFirst.test.ts
 *
 * Tests for deterministic-first execution of issue-pickup (Issue #2614).
 * Verifies that when generateDeterministicIssueContext() produces valid context,
 * the LLM subagent is NOT spawned. When deterministic generation fails,
 * the LLM subagent is used as a fallback.
 *
 * @see Issue #2614 - Deterministic-first execution for issue-pickup
 */

import { describe, it, expect, beforeEach, vi } from "vitest";
import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { PipelineStateService } from "../../src/services/PipelineStateService";
import type { Logger } from "../../src/utils/logger";
import type { SkillRunResult } from "../../src/utils/skillRunner";
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

// Mock child_process so preCheckAuth and generateDeterministicIssueContext work.
// #2884: HeadlessOrchestrator now uses promisify(exec)/promisify(execFile)
// for hot-path subprocess calls; the mocks must implement
// nodejs.util.promisify.custom so the awaited calls resolve.
vi.mock("child_process", async () => {
  const actual = await vi.importActual<typeof import("child_process")>("child_process");
  const kCustom = Symbol.for("nodejs.util.promisify.custom");

  const authStatus =
    "Logged in to github.com account testuser (keyring)\n" +
    "  Token: gho_fake\n  Token scopes: 'gist', 'read:org', 'repo', 'workflow'";

  const issuePayload = JSON.stringify({
    title: "Test issue #42",
    labels: [{ name: "type:feature" }],
    body: "## Summary\nTest summary\n## Acceptance Criteria\n- [ ] AC1",
  });

  // Default dispatcher — overridable per-test by re-implementing execFileSync.
  const defaultDispatch = (cmd: string, args: string[] | undefined): string => {
    const a = args ?? [];
    if (cmd === "git" && a[0] === "branch") return "feat/2614-test\n";
    if (cmd === "gh" && a[0] === "issue") return issuePayload;
    if (cmd === "gh" && a[0] === "repo") return "TestOrg/test-repo";
    return "";
  };

  const execFileSyncMock = vi
    .fn()
    .mockImplementation((cmd: string, args: string[]) => defaultDispatch(cmd, args));

  const execMock: any = vi.fn();
  execMock[kCustom] = () => Promise.resolve({ stdout: authStatus, stderr: "" });

  // execFile.promisify delegates to execFileSyncMock so per-test overrides
  // (e.g. mockImplementation that throws) propagate through both code paths.
  // #2884: HeadlessOrchestrator/ContextAssembler use promisify(execFile).
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
 * State with all stages pre-completed except issue-pickup.
 * The pipeline loop will skip pre-completed stages and only
 * attempt issue-pickup (where deterministic-first kicks in).
 */
function makeStateWithoutIssuePickup() {
  return {
    schema_version: "1.0",
    issue_number: 42,
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

describe("HeadlessOrchestrator deterministic-first issue-pickup (Issue #2614)", () => {
  let mockLogger: Logger;

  beforeEach(() => {
    vi.clearAllMocks();
    mockLogger = {
      info: vi.fn(),
      warn: vi.fn(),
      error: vi.fn(),
      debug: vi.fn(),
    } as unknown as Logger;

    // Default: all files exist, contain valid JSON, writeFileSync is no-op
    vi.mocked(existsSync).mockReturnValue(true);
    vi.mocked(readFileSync).mockReturnValue("{}");
    vi.mocked(writeFileSync).mockImplementation(() => {});
    vi.mocked(mkdirSync).mockImplementation(() => undefined as any);

    // Restore child_process mocks (vi.clearAllMocks only clears call data, not implementations)
    vi.mocked(execSync).mockReturnValue(
      "Logged in to github.com account testuser (keyring)\n  Token: gho_fake\n  Token scopes: 'gist', 'read:org', 'repo', 'workflow'"
    );
    vi.mocked(execFileSync).mockImplementation((cmd: string, args: unknown[]) => {
      const argsArr = args as string[];
      if (cmd === "git" && argsArr[0] === "branch") return "feat/2614-test\n";
      if (cmd === "gh" && argsArr[0] === "issue")
        return JSON.stringify({
          title: "Test issue #42",
          labels: [{ name: "type:feature" }],
          body: "## Summary\nTest summary\n## Acceptance Criteria\n- [ ] AC1",
        });
      if (cmd === "gh" && argsArr[0] === "repo") return "TestOrg/test-repo";
      return "";
    });
  });

  it("skips LLM subagent when deterministic context generation succeeds", async () => {
    const mockState = createMockStateService();
    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger, {
      contextFileWaitMs: 0,
    });

    // generateDeterministicIssueContext will succeed (execFileSync returns good data,
    // writeFileSync is mocked, existsSync returns true for validation)
    // readFileSync returns valid JSON for context validation
    vi.mocked(readFileSync).mockReturnValue(
      JSON.stringify({
        schema_version: "1.3",
        issue_number: 42,
        title: "Test issue #42",
        type: "feature",
        branch: "feat/2614-test",
        base_branch: "main",
        _deterministic: true,
      })
    );

    const result = await orchestrator.runPipeline(42);

    // Deterministic path succeeded — LLM should NOT have been called
    expect(runStageSkillHeadless).not.toHaveBeenCalled();
    expect(result.success).toBe(true);

    // State service should have been told stage completed
    expect(mockState.completeStage).toHaveBeenCalledWith("issue-pickup");

    // Logger should show deterministic path was taken
    expect(mockLogger.info).toHaveBeenCalledWith(
      expect.stringContaining("deterministic context written and validated"),
      expect.any(Object)
    );
  });

  it("falls back to LLM when deterministic context generation fails", async () => {
    const mockState = createMockStateService();
    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger, {
      contextFileWaitMs: 0,
    });

    // Make execFileSync throw for git calls so generateDeterministicIssueContext fails
    vi.mocked(execFileSync).mockImplementation(() => {
      throw new Error("git not available");
    });

    // readFileSync returns valid JSON for the post-LLM context validation
    vi.mocked(readFileSync).mockReturnValue("{}");

    // LLM fallback succeeds
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

    // Deterministic failed, LLM was called as fallback
    expect(runStageSkillHeadless).toHaveBeenCalled();
    expect(result.success).toBe(true);

    // Logger should show fallback to LLM
    expect(mockLogger.warn).toHaveBeenCalledWith(
      expect.stringContaining("deterministic context generation failed"),
      expect.any(Object)
    );
  });

  it("calls completeStage and logs deterministic path on success", async () => {
    const mockState = createMockStateService();
    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger, {
      contextFileWaitMs: 0,
    });

    vi.mocked(readFileSync).mockReturnValue(
      JSON.stringify({
        schema_version: "1.3",
        issue_number: 42,
        title: "Test issue #42",
        type: "feature",
        branch: "feat/2614-test",
        base_branch: "main",
        _deterministic: true,
      })
    );

    const result = await orchestrator.runPipeline(42);

    // Deterministic path succeeded
    expect(result.success).toBe(true);
    expect(runStageSkillHeadless).not.toHaveBeenCalled();

    // State service marked stage as started then completed
    expect(mockState.startStage).toHaveBeenCalledWith("issue-pickup", { forceBackward: true });
    expect(mockState.completeStage).toHaveBeenCalledWith("issue-pickup");

    // Logger shows the deterministic-first attempt and success
    expect(mockLogger.info).toHaveBeenCalledWith(
      expect.stringContaining("attempting deterministic context generation"),
      expect.any(Object)
    );
    expect(mockLogger.info).toHaveBeenCalledWith(
      expect.stringContaining("deterministic context written and validated"),
      expect.any(Object)
    );
  });
});
