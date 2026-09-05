/**
 * HeadlessOrchestrator.pauseHold.test.ts
 *
 * #423: pausing mid-pipeline must HOLD the stage loop at the pause boundary,
 * not break out of it. Before this fix, `isPaused()` tripping caused an
 * immediate `break`, so `runPipeline()` resolved early with
 * `{success:false, failedStage:undefined}` while the run was merely paused.
 * On a ConcurrentPipelineManager slot that result has no "paused" arm in the
 * terminal classification — it was booked as a slot FAILURE, the slot was
 * deleted, and the run could never be targeted by Resume again.
 *
 * The fix polls `isPaused()` at the boundary and only lets the loop continue
 * once it flips back to false, so the SAME `runPipeline()` call —
 * `getIsRunning()` stays true throughout — resumes exactly where it left
 * off instead of returning.
 *
 * @see src/services/HeadlessOrchestrator.ts — PAUSE_POLL_INTERVAL_MS
 * @see tests/services/ConcurrentPipelineManager.pauseHold.test.ts — the
 *      consequence for a concurrent slot
 */

import { describe, it, expect, beforeEach, vi } from "vitest";
import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { PipelineStateService, PipelineState } from "../../src/services/PipelineStateService";
import type { Logger } from "../../src/utils/logger";
import type { SkillRunResult } from "../../src/utils/skillRunner";
import { runStageSkillHeadless } from "../../src/utils/skillRunner";

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

// Mock child_process so preCheckAuth/preCheckIssue pass without real gh CLI.
vi.mock("child_process", async () => {
  const actual = await vi.importActual<typeof import("child_process")>("child_process");
  const kCustom = Symbol.for("nodejs.util.promisify.custom");

  const authStatus =
    "Logged in to github.com account testuser (keyring)\n" +
    "  Token: gho_fake\n  Token scopes: 'gist', 'read:org', 'repo', 'workflow'";
  const issueJson = '{"labels":[],"state":"OPEN","title":"Test issue #42"}';

  const execMock: any = vi.fn();
  execMock[kCustom] = () => Promise.resolve({ stdout: authStatus, stderr: "" });

  // promisify(execFile) — used by preCheckIssue AND by the generic
  // `gate verify --record` post-condition check every stage runs after
  // completion. Report a pass for the gate call specifically so this
  // suite's pause/resume assertions aren't tripped by that unrelated gate
  // wiring — the canned issue JSON below has no `passed` field and would
  // otherwise parse as a failing gate result (see
  // HeadlessOrchestrator.contextHandoff.test.ts, same fixture).
  const execFileMock: any = vi.fn();
  execFileMock[kCustom] = (_cmd: string, args?: string[]) => {
    if (args?.[0] === "gate" && args?.[1] === "verify") {
      return Promise.resolve({
        stdout: JSON.stringify({ passed: true, reason: "ok", gate_name: args[2] }),
        stderr: "",
      });
    }
    return Promise.resolve({ stdout: issueJson, stderr: "" });
  };

  return {
    ...actual,
    exec: execMock,
    execFile: execFileMock,
    execSync: vi.fn().mockReturnValue(authStatus),
    execFileSync: vi.fn().mockReturnValue(issueJson),
  };
});

const ALL_STAGES_STATE: PipelineState = {
  issue_number: 42,
  title: "Test issue",
  branch: "feat/42-test",
  started_at: "2026-01-01T00:00:00.000Z",
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
    input: 0,
    output: 0,
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
    getRunId: vi.fn().mockReturnValue(null),
    getRunRepo: vi.fn().mockReturnValue(""),
    beginRun: vi.fn(),
    endRun: vi.fn(),
    initializePipeline: vi.fn().mockResolvedValue(undefined),
    startStage: vi.fn().mockResolvedValue(undefined),
    completeStage: vi.fn().mockResolvedValue(undefined),
    skipStage: vi.fn().mockResolvedValue(undefined),
    deferStage: vi.fn().mockResolvedValue(undefined),
    setExecutionMode: vi.fn().mockResolvedValue(undefined),
    setStageExecutionMode: vi.fn().mockResolvedValue(undefined),
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
    failPhase: vi.fn().mockResolvedValue(undefined),
  } as unknown as PipelineStateService;
}

describe("HeadlessOrchestrator pause hold (#423)", () => {
  let mockLogger: Logger;

  beforeEach(() => {
    vi.clearAllMocks();
    vi.useRealTimers();
    mockLogger = {
      info: vi.fn(),
      warn: vi.fn(),
      error: vi.fn(),
      debug: vi.fn(),
    } as unknown as Logger;
  });

  it("holds at the pause boundary — runPipeline() stays in flight — then continues to completion once resumed", async () => {
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

    // Paused flips true the instant feature-dev completes, exactly like
    // pausePipeline.ts calling PipelineStateService.pausePipeline() mid-stage.
    let paused = false;
    vi.mocked(mockState.isPaused).mockImplementation(() => Promise.resolve(paused) as any);

    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger, {
      contextFileWaitMs: 0,
      // Real timers, shrunk to make a pause/resume cycle cost milliseconds
      // instead of the production 1s poll interval.
      pausePollIntervalMs: 5,
    });

    // Pause only once, right when feature-dev — the one pending stage —
    // starts. Any later stage this fixture's static getState() snapshot
    // causes to re-run also completes normally, but must NOT re-trip the
    // pause: this test is pinning the ONE hold/resume cycle, not asserting
    // anything about whichever stages run after it.
    let stageCalls = 0;
    vi.mocked(runStageSkillHeadless).mockImplementation((_stage, _issueNumber, callbacks) => {
      stageCalls += 1;
      if (stageCalls === 1) {
        paused = true;
      }
      Promise.resolve().then(() => {
        void callbacks?.onComplete?.({
          success: true,
          exitCode: 0,
        } as SkillRunResult);
      });
      return { kill: vi.fn(), process: null } as any;
    });

    const runPromise = orchestrator.runPipeline(42);

    // Wait for the hold log line itself rather than a fixed sleep — the
    // pre-feature-dev pre-checks (auth, adapter pre-flight) run for a
    // variable amount of real time before feature-dev even starts, and a
    // fixed delay here was flaky under load.
    const deadline = Date.now() + 5000;
    while (
      !vi
        .mocked(mockLogger.info)
        .mock.calls.some(([msg]) => msg === "Pipeline paused after stage complete — holding")
    ) {
      if (Date.now() > deadline) {
        throw new Error("Timed out waiting for the pause-hold log line");
      }
      await new Promise((resolve) => setTimeout(resolve, 5));
    }

    // Pre-fix, this is exactly where runPipeline() would have already
    // RESOLVED with {success:false, failedStage:undefined} (the break).
    // Post-fix it must still be in flight, holding.
    expect(orchestrator.getIsRunning()).toBe(true);
    expect(vi.mocked(mockLogger.info)).toHaveBeenCalledWith(
      "Pipeline paused after stage complete — holding",
      expect.objectContaining({ stage: "feature-dev" })
    );
    expect(vi.mocked(mockLogger.info)).not.toHaveBeenCalledWith(
      "Pipeline resumed — continuing stage loop",
      expect.anything()
    );

    // Resume: clear the flag (as PipelineStateService.resumePipeline() does)
    // and let the poll loop's next tick observe it. Again polled rather than
    // a fixed sleep — the 5ms `pausePollIntervalMs` is a lower bound, not a
    // guarantee, under load.
    paused = false;
    const resumeDeadline = Date.now() + 5000;
    while (
      !vi
        .mocked(mockLogger.info)
        .mock.calls.some(([msg]) => msg === "Pipeline resumed — continuing stage loop")
    ) {
      if (Date.now() > resumeDeadline) {
        throw new Error("Timed out waiting for the pause-resume log line");
      }
      await new Promise((resolve) => setTimeout(resolve, 5));
    }

    // Whatever the rest of this fixture-driven run ultimately does, it must
    // never be the pre-fix "impossible" shape — success:false with no
    // attributed failedStage — which is exactly what a `break` on pause used
    // to return.
    const result = await runPromise;
    expect(result.success === false && result.failedStage === undefined).toBe(false);
    expect(orchestrator.getIsRunning()).toBe(false);
  });
});
