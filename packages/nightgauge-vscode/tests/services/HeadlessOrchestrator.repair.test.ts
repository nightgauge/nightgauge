/**
 * HeadlessOrchestrator.repair.test.ts
 *
 * Tests for context schema repair loop (Issue #2552).
 * Verifies that when repair is enabled and context validation fails,
 * the orchestrator re-invokes the stage with error-fix instructions
 * and re-validates the output.
 *
 * @see Issue #2552 - Pipeline context schema self-correction
 */

import { describe, it, expect, beforeEach, vi } from "vitest";
import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { PipelineStateService } from "../../src/services/PipelineStateService";
import type { Logger } from "../../src/utils/logger";
import type { SkillRunResult } from "../../src/utils/skillRunner";
import { runStageSkillHeadless } from "../../src/utils/skillRunner";
import { existsSync, readFileSync } from "fs";
import * as incrediConfig from "../../src/utils/incrediConfig";

// Mock skillRunner
vi.mock("../../src/utils/skillRunner", () => ({
  hasActiveProcess: vi.fn().mockReturnValue(false),
  killAllActiveProcesses: vi.fn(),
  getActiveInteractiveProcess: vi.fn().mockReturnValue(null),
  runStageSkillHeadless: vi.fn(),
  findSkillFile: vi.fn().mockReturnValue("/fake/skills/SKILL.md"),
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

// Mock child_process — see #2884 for promisify.custom rationale.
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
 * State with all stages pre-completed except feature-planning.
 */
function makeStateWithoutFeaturePlanning() {
  return {
    schema_version: "1.0",
    issue_number: 42,
    stages: {
      "pipeline-start": { status: "complete", auto_retry_count: 0 },
      "issue-pickup": { status: "complete", auto_retry_count: 0 },
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

describe("HeadlessOrchestrator context schema repair (Issue #2552)", () => {
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

  it("does not attempt repair when config is disabled (default)", async () => {
    const mockState = createMockStateService();
    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger, {
      contextFileWaitMs: 0,
    });

    // Stage produces context with schema mismatches
    vi.mocked(existsSync).mockReturnValue(true);
    vi.mocked(readFileSync).mockReturnValue("{}"); // Empty JSON = schema mismatch

    let callCount = 0;
    vi.mocked(runStageSkillHeadless).mockImplementation((_stage, _issueNumber, callbacks) => {
      callCount++;
      Promise.resolve().then(() => {
        void callbacks?.onComplete?.({
          success: true,
          exitCode: 0,
        } as SkillRunResult);
      });
      return { kill: vi.fn(), process: null } as any;
    });

    await orchestrator.runPipeline(42);

    // Only 1 invocation (the original stage run), no repair attempt
    expect(callCount).toBe(1);
  });

  it("attempts repair when config is enabled and validation fails", async () => {
    // Mock repair config as enabled
    vi.spyOn(incrediConfig, "getContextSchemaRepairConfig").mockReturnValue({
      enabled: true,
      max_attempts: 1,
    });

    const mockState = createMockStateService();
    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger, {
      contextFileWaitMs: 0,
    });

    // First call: stage runs successfully, returns context with schema mismatch
    // Second call: repair re-invocation, also returns
    let callCount = 0;
    vi.mocked(runStageSkillHeadless).mockImplementation(
      (_stage, _issueNumber, callbacks, ..._rest) => {
        callCount++;
        Promise.resolve().then(() => {
          void callbacks?.onComplete?.({
            success: true,
            exitCode: 0,
          } as SkillRunResult);
        });
        return { kill: vi.fn(), process: {} } as any;
      }
    );

    // Context file always returns empty JSON (schema mismatch)
    vi.mocked(existsSync).mockReturnValue(true);
    vi.mocked(readFileSync).mockReturnValue("{}");

    await orchestrator.runPipeline(42);

    // Should have 2 invocations: original + repair attempt
    expect(callCount).toBe(2);
    // Logger should record the repair attempt
    expect(mockLogger.info).toHaveBeenCalledWith(
      expect.stringContaining("Context schema repair: attempting repair"),
      expect.objectContaining({ stage: "feature-planning" })
    );
  });

  it("clears validation errors when repair succeeds", async () => {
    vi.spyOn(incrediConfig, "getContextSchemaRepairConfig").mockReturnValue({
      enabled: true,
      max_attempts: 1,
    });

    const mockState = createMockStateService();
    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger, {
      contextFileWaitMs: 0,
    });

    // Track readFileSync calls — first returns invalid, repair returns valid
    let readCount = 0;
    vi.mocked(existsSync).mockReturnValue(true);
    vi.mocked(readFileSync).mockImplementation((p) => {
      const pathStr = String(p);
      if (pathStr.includes("planning-42")) {
        readCount++;
        // First read returns empty JSON (schema mismatch), subsequent reads return valid
        if (readCount <= 1) return "{}";
        // After repair, return valid planning context matching PlanningContextSchema
        return JSON.stringify({
          schema_version: "1.5",
          issue_number: 42,
          plan_file: "/tmp/plan.md",
          approach: "direct implementation",
          files_to_create: [],
          files_to_modify: [],
          created_at: new Date().toISOString(),
        });
      }
      if (pathStr.includes("SKILL.md")) {
        return "---\nname: test\n---\nTest skill content";
      }
      return "{}";
    });

    vi.mocked(runStageSkillHeadless).mockImplementation(
      (_stage, _issueNumber, callbacks, ..._rest) => {
        Promise.resolve().then(() => {
          void callbacks?.onComplete?.({
            success: true,
            exitCode: 0,
          } as SkillRunResult);
        });
        return { kill: vi.fn(), process: {} } as any;
      }
    );

    await orchestrator.runPipeline(42);

    // Should log repair success
    expect(mockLogger.info).toHaveBeenCalledWith(
      expect.stringContaining("Context schema repair: succeeded"),
      expect.objectContaining({ stage: "feature-planning" })
    );
  });

  it("falls through to warn-and-continue when repair fails", async () => {
    vi.spyOn(incrediConfig, "getContextSchemaRepairConfig").mockReturnValue({
      enabled: true,
      max_attempts: 1,
    });

    const mockState = createMockStateService();
    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger, {
      contextFileWaitMs: 0,
    });

    // Context file always returns empty JSON (schema mismatch persists after repair)
    vi.mocked(existsSync).mockReturnValue(true);
    vi.mocked(readFileSync).mockReturnValue("{}");

    vi.mocked(runStageSkillHeadless).mockImplementation(
      (_stage, _issueNumber, callbacks, ..._rest) => {
        Promise.resolve().then(() => {
          void callbacks?.onComplete?.({
            success: true,
            exitCode: 0,
          } as SkillRunResult);
        });
        return { kill: vi.fn(), process: {} } as any;
      }
    );

    const result = await orchestrator.runPipeline(42);

    // Pipeline should continue (warn-and-continue) — not fail
    expect(result.success).toBe(true);
    // Should log repair failure
    expect(mockLogger.warn).toHaveBeenCalledWith(
      expect.stringContaining("Context schema repair: failed"),
      expect.objectContaining({ stage: "feature-planning" })
    );
  });

  it("respects max_attempts limit", async () => {
    vi.spyOn(incrediConfig, "getContextSchemaRepairConfig").mockReturnValue({
      enabled: true,
      max_attempts: 1,
    });

    const mockState = createMockStateService();
    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger, {
      contextFileWaitMs: 0,
    });

    // Context file always invalid
    vi.mocked(existsSync).mockReturnValue(true);
    vi.mocked(readFileSync).mockReturnValue("{}");

    let callCount = 0;
    vi.mocked(runStageSkillHeadless).mockImplementation(
      (_stage, _issueNumber, callbacks, ..._rest) => {
        callCount++;
        Promise.resolve().then(() => {
          void callbacks?.onComplete?.({
            success: true,
            exitCode: 0,
          } as SkillRunResult);
        });
        return { kill: vi.fn(), process: {} } as any;
      }
    );

    await orchestrator.runPipeline(42);

    // Only 2 calls: original + 1 repair attempt (max_attempts: 1)
    expect(callCount).toBe(2);
  });
});
