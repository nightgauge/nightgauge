/**
 * HeadlessOrchestrator.pipelineStartDiagnostics.test.ts
 *
 * Verifies that early-return paths inside `runPipeline()` that fail with
 * `failedStage: "pipeline-start"` write a diagnostic marker to the per-issue
 * event stream via `eventDispatcher.onStderr(...)` BEFORE returning.
 *
 * Without this contract, the per-issue session log
 * (.nightgauge/logs/<date>_<issue>_session.log) carries no clue why the
 * pipeline failed at pipeline-start — only the bare attribution.
 *
 * Marker contract:
 *   `[pipeline-start-failure] <short-cause-tag>: <human-readable detail>\n`
 */

import { describe, it, expect, beforeEach, vi } from "vitest";
import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { PipelineStateService } from "../../src/services/PipelineStateService";
import type { Logger } from "../../src/utils/logger";

// Mock skillRunner — should never actually be hit because the run halts at
// pipeline-start, but vi requires the module mock.
vi.mock("../../src/utils/skillRunner", () => ({
  hasActiveProcess: vi.fn().mockReturnValue(false),
  killAllActiveProcesses: vi.fn(),
  getActiveInteractiveProcess: vi.fn().mockReturnValue(null),
  runStageSkillHeadless: vi.fn(),
  getNextStage: vi.fn(),
  getStageLabel: vi.fn((stage: string) => stage),
  resolveModel: vi.fn().mockReturnValue({ model: "claude-sonnet-4-6", source: "default" }),
}));

vi.mock("fs", async () => {
  const actual = await vi.importActual<typeof import("fs")>("fs");
  return {
    ...actual,
    existsSync: vi.fn().mockReturnValue(true),
    readFileSync: vi.fn().mockReturnValue("{}"),
  };
});

// Mock child_process so the closed-issue path is hit without a real gh CLI.
// Mirrors the pattern from HeadlessOrchestrator.errorRecovery.test.ts: nodejs
// promisify custom symbol must be implemented for execAsync/execFileAsync.
vi.mock("child_process", async () => {
  const actual = await vi.importActual<typeof import("child_process")>("child_process");
  const kCustom = Symbol.for("nodejs.util.promisify.custom");

  const authStatus =
    "Logged in to github.com account testuser (keyring)\n" +
    "  Token: gho_fake\n  Token scopes: 'gist', 'read:org', 'repo', 'workflow'";
  // CLOSED issue — preCheckIssue returns isClosed: true, triggering the
  // issue-closed early return at line ~5550.
  const issueJson = '{"labels":[],"state":"CLOSED","title":"Already-resolved issue #42"}';

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

function makePipelineStateMock(): PipelineStateService {
  return {
    getState: vi.fn().mockResolvedValue({
      schema_version: "1.0",
      issue_number: 42,
      stages: {},
      tokens: {
        total_input: 0,
        total_output: 0,
        total_cache_read: 0,
        total_cache_creation: 0,
        estimated_cost_usd: 0,
      },
    }),
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

describe("HeadlessOrchestrator pipeline-start diagnostic markers (#3411)", () => {
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

  it("issue-closed early return writes [pipeline-start-failure] issue-closed marker to onStderr", async () => {
    const mockState = makePipelineStateMock();
    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger, {
      contextFileWaitMs: 0,
    });

    const onStderr = vi.fn();

    const result = await orchestrator.runPipeline(42, { onStderr });

    // The pipeline halts at pipeline-start.
    expect(result.success).toBe(false);
    expect(result.failedStage).toBe("pipeline-start");

    // The diagnostic marker MUST be on the per-issue stream so the session log
    // captures *why* it failed, not just the bare attribution.
    const calls = onStderr.mock.calls.filter(([stage]) => stage === "pipeline-start");
    expect(calls.length).toBeGreaterThan(0);

    const markerCall = calls.find(
      ([, data]) => typeof data === "string" && data.includes("[pipeline-start-failure]")
    );
    expect(markerCall).toBeDefined();
    expect(markerCall![1]).toContain("[pipeline-start-failure] issue-closed:");
    expect(markerCall![1]).toMatch(/Issue #42 is already CLOSED/);
  });
});
