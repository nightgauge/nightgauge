/**
 * SkillRunner.test.ts — Unit tests for the SkillRunner service.
 *
 * Tests the thin executor that spawns Claude CLI for a single pipeline stage,
 * streams output, and reports the result (exit code, tokens, success flag).
 *
 * The real `runStageSkillHeadless` is mocked so no actual processes are spawned.
 *
 * Coverage:
 *  - runStage returns success result with correct fields
 *  - runStage returns failure result
 *  - abort kills the active process handle
 *  - isRunning reflects active state
 *  - Callbacks (onStdout, onStderr, onTokenUsage) are forwarded
 *
 * @see Issue #1901 — Decompose HeadlessOrchestrator
 * @see src/services/SkillRunner.ts — implementation under test
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import type {
  SkillRunResult,
  SkillProcessHandle,
  SkillRunCallbacks,
} from "../../src/utils/skillRunner";
import type { PipelineStage } from "@nightgauge/sdk";

// ─── Mocks ──────────────────────────────────────────────────────────────────

// vi.hoisted() runs before vi.mock hoisting, so these are available inside
// the mock factory without triggering "Cannot access before initialization".
const { mockKill, mockHasActiveProcess, capturedState } = vi.hoisted(() => {
  const state = { callbacks: undefined as SkillRunCallbacks | undefined };
  return {
    mockKill: vi.fn(),
    mockHasActiveProcess: vi.fn().mockReturnValue(false),
    capturedState: state,
  };
});

vi.mock("../../src/utils/skillRunner", () => ({
  runStageSkillHeadless: vi.fn(
    (
      _stage: string,
      _issueNumber: number | undefined,
      callbacks?: SkillRunCallbacks
    ): SkillProcessHandle => {
      capturedState.callbacks = callbacks;
      return {
        process: {} as any,
        stage: _stage,
        issueNumber: _issueNumber,
        kill: mockKill,
      } as unknown as SkillProcessHandle;
    }
  ),
  killAllActiveProcesses: vi.fn(),
  hasActiveProcess: (...args: unknown[]) => mockHasActiveProcess(...args),
}));

// Mock precomputeCalibratedStallThresholds so it doesn't require fs/StageDurationAnalyzer
vi.mock("../../src/utils/incrediConfig", () => ({
  precomputeCalibratedStallThresholds: vi.fn().mockResolvedValue(undefined),
}));

// Minimal vscode mock — Logger uses vscode.window.createOutputChannel
vi.mock("vscode", () => ({
  window: {
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
  },
  workspace: {
    workspaceFolders: [{ uri: { fsPath: "/mock/workspace" } }],
  },
}));

// Import after mocks are established
import { SkillRunner, type RunStageParams } from "../../src/services/SkillRunner";
import { Logger } from "../../src/utils/logger";
import { runStageSkillHeadless, killAllActiveProcesses } from "../../src/utils/skillRunner";

// ─── Helpers ────────────────────────────────────────────────────────────────

function createLogger(): Logger {
  return new Logger("SkillRunner-Test");
}

function createDefaultParams(overrides: Partial<RunStageParams> = {}): RunStageParams {
  return {
    stage: "feature-dev" as PipelineStage,
    issueNumber: 42,
    model: "claude-sonnet-4-20250514",
    timeout: 60_000,
    worktreeDir: "/mock/worktree",
    ...overrides,
  };
}

function makeSuccessResult(overrides: Partial<SkillRunResult> = {}): SkillRunResult {
  return {
    success: true,
    exitCode: 0,
    tokenUsage: {
      inputTokens: 1000,
      outputTokens: 500,
      cacheReadTokens: 200,
      cacheCreationTokens: 100,
      costUsd: 0.05,
    },
    ...overrides,
  };
}

function makeFailureResult(overrides: Partial<SkillRunResult> = {}): SkillRunResult {
  return {
    success: false,
    exitCode: 1,
    tokenUsage: {
      inputTokens: 800,
      outputTokens: 300,
      cacheReadTokens: 0,
      cacheCreationTokens: 0,
      costUsd: 0.02,
    },
    ...overrides,
  };
}

// ─── Tests ──────────────────────────────────────────────────────────────────

describe("SkillRunner", () => {
  let runner: SkillRunner;
  let logger: Logger;

  beforeEach(() => {
    vi.clearAllMocks();
    capturedState.callbacks = undefined;
    logger = createLogger();
    // Pass null for IpcClient — SkillRunner does not use it directly
    runner = new SkillRunner(null, logger);
  });

  // ── Success path ────────────────────────────────────────────────────────

  it("runStage returns success result", async () => {
    const params = createDefaultParams();
    const successResult = makeSuccessResult();

    const promise = runner.runStage(params);
    // Flush microtasks: precomputeCalibratedStallThresholds() is awaited before
    // runStageSkillHeadless() is called, so callbacks are set asynchronously.
    await Promise.resolve();

    // Simulate the subprocess completing successfully
    expect(capturedState.callbacks).toBeDefined();
    capturedState.callbacks!.onComplete!(successResult);

    const result = await promise;

    expect(result.success).toBe(true);
    expect(result.exitCode).toBe(0);
    expect(result.inputTokens).toBe(1000);
    expect(result.outputTokens).toBe(500);
    expect(result.cacheReadTokens).toBe(200);
    expect(result.cacheCreationTokens).toBe(100);
    expect(result.costUsd).toBe(0.05);
    expect(result.durationMs).toBeGreaterThanOrEqual(0);

    // Verify runStageSkillHeadless was called with the right stage/issue
    expect(runStageSkillHeadless).toHaveBeenCalledWith(
      "feature-dev",
      42,
      expect.any(Object), // callbacks
      undefined, // issueMetadata
      undefined, // batchContext
      undefined, // skipToPhase
      undefined, // modelOverride
      undefined, // pauseAutoRouting
      "/mock/worktree", // pinnedWorkspaceRoot
      undefined, // modelOverrideSource
      undefined, // injectedSkillContent
      undefined, // autonomousMode (Issue #2656)
      undefined, // warnThresholdUsd (Go scheduler enforces budget)
      undefined, // targetRepoOverride — params.repo (Issue #3867)
      undefined // runId — params.runId (#228)
    );
  });

  // Issue #3867: the Go scheduler's per-issue repo (params.repo) must be
  // forwarded to runStageSkillHeadless as the targetRepoOverride so
  // NIGHTGAUGE_TARGET_REPO reflects the issue's repo, not the workspace
  // primary. Regression: AcmeApp #42 mis-routed to acmeapp-infra.
  it("forwards params.repo as the targetRepoOverride", async () => {
    const params = createDefaultParams({ repo: "nightgauge/acmeapp-platform" });

    const promise = runner.runStage(params);
    await Promise.resolve();
    capturedState.callbacks!.onComplete!(makeSuccessResult());
    await promise;

    const call = vi.mocked(runStageSkillHeadless).mock.calls[0];
    // targetRepoOverride is the 14th positional argument; runId (#228) is the
    // 15th and last, so targetRepoOverride is now second-to-last.
    expect(call[call.length - 2]).toBe("nightgauge/acmeapp-platform");
  });

  // #228: the run's UUID (params.runId) must be forwarded to
  // runStageSkillHeadless as the last (15th) positional argument so the SDK
  // TraceRecorder writes to the run's <run_id>.jsonl.
  it("forwards params.runId as the trailing runId argument", async () => {
    const params = createDefaultParams({ runId: "01890a5d-ac96-774b-bcce-b302099a8057" });

    const promise = runner.runStage(params);
    await Promise.resolve();
    capturedState.callbacks!.onComplete!(makeSuccessResult());
    await promise;

    const call = vi.mocked(runStageSkillHeadless).mock.calls[0];
    expect(call[call.length - 1]).toBe("01890a5d-ac96-774b-bcce-b302099a8057");
  });

  // ── Failure path ────────────────────────────────────────────────────────

  it("runStage returns failure result", async () => {
    const params = createDefaultParams();
    const failureResult = makeFailureResult();

    const promise = runner.runStage(params);
    await Promise.resolve();

    capturedState.callbacks!.onComplete!(failureResult);

    const result = await promise;

    expect(result.success).toBe(false);
    expect(result.exitCode).toBe(1);
    expect(result.inputTokens).toBe(800);
    expect(result.outputTokens).toBe(300);
    expect(result.cacheReadTokens).toBe(0);
    expect(result.cacheCreationTokens).toBe(0);
    expect(result.costUsd).toBe(0.02);
  });

  // ── Killed-stage cost flows to the IPC result (#296) ─────────────────────
  // A runaway/stall/budget kill books the live estimate as the stage's
  // tokenUsage (see skillRunner close handler). The service must forward that
  // cost verbatim into `costUsd`, which the PipelineBridge sends to Go as
  // pipeline.stageResult.costUsd → per_stage + telemetry, and which the Discord
  // notifier renders. Pre-#296 the killed stage's tokenUsage was undefined, so
  // this mapped to $0 and the run under-reported its real burn.
  it("forwards the booked live-estimate cost of a killed stage into costUsd (#296)", async () => {
    const params = createDefaultParams();
    const killedResult = makeFailureResult({
      exitCode: 143,
      costCapExceeded: true,
      costEstimated: true,
      tokenUsage: {
        inputTokens: 55_000,
        outputTokens: 8_500,
        cacheReadTokens: 12_000,
        cacheCreationTokens: 0,
        costUsd: 9.1496,
      },
    });

    const promise = runner.runStage(params);
    await Promise.resolve();
    capturedState.callbacks!.onComplete!(killedResult);

    const result = await promise;

    expect(result.success).toBe(false);
    // The real burn — NOT $0 — is what Go books and Discord renders.
    expect(result.costUsd).toBeCloseTo(9.1496, 4);
    expect(result.inputTokens).toBe(55_000);
    expect(result.outputTokens).toBe(8_500);
    expect(result.cacheReadTokens).toBe(12_000);
    expect(result.costCapExceeded).toBe(true);
  });

  // ── promptDetected suppresses success ───────────────────────────────────

  it("runStage reports success=false when promptDetected is true", async () => {
    const params = createDefaultParams();
    const result = makeSuccessResult({ promptDetected: true });

    const promise = runner.runStage(params);
    await Promise.resolve();
    capturedState.callbacks!.onComplete!(result);

    const stageResult = await promise;

    expect(stageResult.success).toBe(false);
    expect(stageResult.promptDetected).toBe(true);
  });

  // ── Abort ───────────────────────────────────────────────────────────────

  it("abort kills the active process", async () => {
    const params = createDefaultParams();

    // Start a stage (do not resolve the promise yet)
    const promise = runner.runStage(params);
    // Flush microtasks so runStageSkillHeadless is called and activeHandle is set
    await Promise.resolve();

    // Abort before completion
    runner.abort();
    expect(mockKill).toHaveBeenCalledTimes(1);

    // Clean up: resolve the pending promise so the test does not hang
    capturedState.callbacks!.onComplete!(makeSuccessResult());
    await promise;
  });

  it("abort is a no-op when no process is active", () => {
    // Should not throw
    runner.abort();
    expect(mockKill).not.toHaveBeenCalled();
  });

  // ── isRunning ───────────────────────────────────────────────────────────

  it("isRunning reflects active state", async () => {
    expect(runner.isRunning).toBe(false);

    const params = createDefaultParams();
    const promise = runner.runStage(params);
    // Flush microtasks so runStageSkillHeadless is called and activeHandle is set
    await Promise.resolve();

    // After spawning, hasActiveProcess returns true
    mockHasActiveProcess.mockReturnValue(true);
    expect(runner.isRunning).toBe(true);

    // Complete the run
    capturedState.callbacks!.onComplete!(makeSuccessResult());
    await promise;

    // After completion, activeHandle is cleared
    mockHasActiveProcess.mockReturnValue(false);
    expect(runner.isRunning).toBe(false);
  });

  // ── Callbacks forwarding ──────────────────────────────────────────────

  it("onStdout callback is forwarded", async () => {
    const params = createDefaultParams({
      stage: "feature-planning" as PipelineStage,
    });
    const onStdout = vi.fn();

    const promise = runner.runStage(params, { onStdout });
    await Promise.resolve();

    // Simulate stdout from subprocess
    capturedState.callbacks!.onStdout!("hello world");

    expect(onStdout).toHaveBeenCalledWith("feature-planning", "hello world");

    capturedState.callbacks!.onComplete!(makeSuccessResult());
    await promise;
  });

  it("onStderr callback is forwarded", async () => {
    const params = createDefaultParams({
      stage: "feature-validate" as PipelineStage,
    });
    const onStderr = vi.fn();

    const promise = runner.runStage(params, { onStderr });
    await Promise.resolve();

    capturedState.callbacks!.onStderr!("error output");

    expect(onStderr).toHaveBeenCalledWith("feature-validate", "error output");

    capturedState.callbacks!.onComplete!(makeSuccessResult());
    await promise;
  });

  it("onTokenUsage callback is forwarded", async () => {
    const params = createDefaultParams();
    const onTokenUsage = vi.fn();

    const promise = runner.runStage(params, { onTokenUsage });
    await Promise.resolve();

    const usage = {
      inputTokens: 500,
      outputTokens: 250,
      cacheReadTokens: 100,
      cacheCreationTokens: 50,
      costUsd: 0.03,
    };
    capturedState.callbacks!.onTokenUsage!(usage);

    expect(onTokenUsage).toHaveBeenCalledWith(usage);

    capturedState.callbacks!.onComplete!(makeSuccessResult());
    await promise;
  });

  it("onToolCall callback is forwarded with file_path target", async () => {
    const params = createDefaultParams();
    const onToolCall = vi.fn();

    const promise = runner.runStage(params, { onToolCall });
    await Promise.resolve();

    capturedState.callbacks!.onToolCall!("Read", {
      file_path: "/src/index.ts",
    });

    expect(onToolCall).toHaveBeenCalledWith("feature-dev", {
      tool: "Read",
      target: "/src/index.ts",
    });

    capturedState.callbacks!.onComplete!(makeSuccessResult());
    await promise;
  });

  it("onToolCall extracts command target when file_path is absent", async () => {
    const params = createDefaultParams();
    const onToolCall = vi.fn();

    const promise = runner.runStage(params, { onToolCall });
    await Promise.resolve();

    capturedState.callbacks!.onToolCall!("Bash", { command: "npm run build" });

    expect(onToolCall).toHaveBeenCalledWith("feature-dev", {
      tool: "Bash",
      target: "npm run build",
    });

    capturedState.callbacks!.onComplete!(makeSuccessResult());
    await promise;
  });

  // ── Default token values when tokenUsage is undefined ─────────────────

  it("runStage defaults token fields to 0 when tokenUsage is undefined", async () => {
    const params = createDefaultParams();
    const resultWithoutTokens: SkillRunResult = {
      success: true,
      exitCode: 0,
      // no tokenUsage
    };

    const promise = runner.runStage(params);
    await Promise.resolve();
    capturedState.callbacks!.onComplete!(resultWithoutTokens);

    const result = await promise;

    expect(result.inputTokens).toBe(0);
    expect(result.outputTokens).toBe(0);
    expect(result.cacheReadTokens).toBe(0);
    expect(result.cacheCreationTokens).toBe(0);
    expect(result.costUsd).toBe(0);
  });

  // ── Static killAll ────────────────────────────────────────────────────

  it("killAll delegates to killAllActiveProcesses", () => {
    SkillRunner.killAll();
    expect(killAllActiveProcesses).toHaveBeenCalledTimes(1);
  });
});
