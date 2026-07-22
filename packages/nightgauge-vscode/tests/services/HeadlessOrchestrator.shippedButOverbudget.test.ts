/**
 * #3274: broaden the shipped-but-overbudget override (#3108) so it also fires
 * when pr-merge fails for non-budget reasons (e.g. stale "Failure cleanup
 * complete" exit text from the subagent) AND the deterministic gate confirms
 * the PR is MERGED and the issue is CLOSED.
 *
 * Required: PR=MERGED *and* issue=CLOSED. Either gate failing leaves the
 * original failure intact (fail-closed). Reuses the existing
 * "shipped-but-overbudget" outcome — no new enum value introduced.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { PipelineStateService } from "../../src/services/PipelineStateService";
import type { Logger } from "../../src/utils/logger";
import type { SkillRunResult } from "../../src/utils/skillRunner";
import { runStageSkillHeadless } from "../../src/utils/skillRunner";

// #4044: the live adapter auth gate now injects a real preflight runner, so it
// probes CLI auth. These pr-merge-path tests run with no CLI auth in the env;
// skip the gate (pipeline.skip_auth_preflight) so they exercise the
// shipped-but-overbudget reclassification they target.
vi.mock("../../src/utils/incrediConfig", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../src/utils/incrediConfig")>()),
  getSkipAuthPreflight: () => true,
}));

vi.mock("../../src/utils/skillRunner", () => ({
  hasActiveProcess: vi.fn().mockReturnValue(false),
  killAllActiveProcesses: vi.fn(),
  getActiveInteractiveProcess: vi.fn().mockReturnValue(null),
  runStageSkillHeadless: vi.fn(),
  getNextStage: vi.fn(),
  getStageLabel: vi.fn((stage: string) => stage),
  resolveModel: vi.fn().mockReturnValue({ model: "claude-sonnet-4-6", source: "default" }),
}));

const { prState, issueState, prMergeBudgetExceeded } = vi.hoisted(() => ({
  prState: { value: "MERGED" },
  issueState: { value: "CLOSED" },
  prMergeBudgetExceeded: { value: false },
}));

vi.mock("child_process", async () => {
  const actual = await vi.importActual<typeof import("child_process")>("child_process");
  const kCustom = Symbol.for("nodejs.util.promisify.custom");

  const authStatus =
    "Logged in to github.com account testuser (keyring)\n" +
    "  Token: gho_fake\n  Token scopes: 'gist', 'read:org', 'repo', 'workflow'";

  const execMock: any = vi.fn();
  execMock[kCustom] = () => Promise.resolve({ stdout: authStatus, stderr: "" });

  const execFileMock: any = vi.fn();
  execFileMock[kCustom] = (_cmd: string, args: string[]) => {
    // gh issue view <N> --json state -q .state — used by the new
    // checkPrMergedAndIssueClosed helper.
    if (
      args &&
      args[0] === "issue" &&
      args[1] === "view" &&
      args.includes("-q") &&
      args.includes(".state")
    ) {
      return Promise.resolve({ stdout: issueState.value, stderr: "" });
    }
    // gh pr view <N> --json state -q .state — used by checkPrMergedForIssue
    if (
      args &&
      args[0] === "pr" &&
      args[1] === "view" &&
      args.includes("-q") &&
      args.includes(".state")
    ) {
      return Promise.resolve({ stdout: prState.value, stderr: "" });
    }
    // gh issue view <N> --json labels,state,title — preCheckIssue (before stages run)
    if (args && args[0] === "issue" && args[1] === "view") {
      return Promise.resolve({
        stdout: JSON.stringify({ labels: [], state: "OPEN", title: "Test #3274" }),
        stderr: "",
      });
    }
    // gh pr view <N> --json state,statusCheckRollup,... — verifyPostMergeState
    // (not exercised here because pr-merge skill returns failure before this
    // path runs, but mocked defensively).
    if (args && args[0] === "pr" && args[1] === "view") {
      return Promise.resolve({
        stdout: JSON.stringify({
          state: prState.value,
          statusCheckRollup: [],
          mergeable: "MERGEABLE",
          mergeStateStatus: "CLEAN",
        }),
        stderr: "",
      });
    }
    return Promise.resolve({ stdout: "", stderr: "" });
  };

  return {
    ...actual,
    exec: execMock,
    execFile: execFileMock,
    execSync: vi.fn().mockReturnValue(authStatus),
    execFileSync: vi.fn().mockReturnValue("{}"),
  };
});

vi.mock("fs", async () => {
  const actual = await vi.importActual<typeof import("fs")>("fs");
  return {
    ...actual,
    existsSync: vi.fn().mockReturnValue(true),
    readFileSync: vi.fn().mockImplementation((p: string) => {
      if (typeof p === "string" && p.includes("pr-")) {
        return JSON.stringify({ pr_number: 42 });
      }
      return "{}";
    }),
  };
});

function makeStateInitial() {
  return {
    schema_version: "1.0",
    issue_number: 3274,
    stages: {
      "pipeline-start": { status: "complete", auto_retry_count: 0 },
      "issue-pickup": { status: "complete", auto_retry_count: 0 },
      "feature-planning": { status: "complete", auto_retry_count: 0 },
      "feature-dev": { status: "complete", auto_retry_count: 0 },
      "feature-validate": { status: "complete", auto_retry_count: 0 },
      "pr-create": { status: "complete", auto_retry_count: 0 },
      "pr-merge": { status: "running", auto_retry_count: 0 },
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
    getState: vi.fn().mockResolvedValue(makeStateInitial()),
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

async function drainTimers(iterations = 30) {
  for (let i = 0; i < iterations; i++) {
    await vi.advanceTimersByTimeAsync(2_500);
  }
}

function setupSkillMock() {
  vi.mocked(runStageSkillHeadless).mockImplementation((stage, _issue, callbacks) => {
    Promise.resolve().then(() => {
      if (stage === "pr-merge") {
        // pr-merge skill emits a stale "Failure cleanup complete" signal —
        // the deterministic gate will have to recover.
        void callbacks?.onComplete?.({
          success: false,
          exitCode: 1,
          error: new Error("pr-merge skill exited with stale failure signal"),
          budgetExceeded: prMergeBudgetExceeded.value,
        } as SkillRunResult);
      } else {
        void callbacks?.onComplete?.({ success: true, exitCode: 0 } as SkillRunResult);
      }
    });
    return { kill: vi.fn(), process: null } as any;
  });
}

describe("HeadlessOrchestrator shipped-but-overbudget broadening (Issue #3274)", () => {
  let mockLogger: Logger;
  let mockState: PipelineStateService;

  beforeEach(() => {
    vi.clearAllMocks();
    vi.useFakeTimers();
    prState.value = "MERGED";
    issueState.value = "CLOSED";
    prMergeBudgetExceeded.value = false;
    mockLogger = {
      info: vi.fn(),
      warn: vi.fn(),
      error: vi.fn(),
      debug: vi.fn(),
    } as unknown as Logger;
    mockState = createMockStateService();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("reclassifies pr-merge failure as shipped-but-overbudget when PR=MERGED + issue=CLOSED", async () => {
    setupSkillMock();
    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger, {
      contextFileWaitMs: 0,
    });

    const runPromise = orchestrator.runPipeline(3274);
    await drainTimers();
    const result = await runPromise;

    // AC1: outcome is reclassified to shipped-but-overbudget
    expect(result.outcomeType).toBe("shipped-but-overbudget");
    // AC2: failedStage cleared by the override block
    expect(result.failedStage).toBeUndefined();
    // pr-merge promoted into completedStages by the override
    expect(result.completedStages).toContain("pr-merge");

    // AC2: failure outcome NOT recorded (Go-side LifetimeIssueFailures stays put)
    const failureCall = vi
      .mocked(mockState.recordExecutionOutcome)
      .mock.calls.find((args) => args[0] === "failure");
    expect(failureCall).toBeUndefined();

    // setOutcomeType was called with the reused enum value
    expect(mockState.setOutcomeType).toHaveBeenCalledWith("shipped-but-overbudget");

    // Reclassification log line is present and names the stale-signal reason
    const reclassify = vi
      .mocked(mockLogger.warn)
      .mock.calls.find(
        (a) =>
          typeof a[0] === "string" &&
          a[0].includes("Reclassifying pr-merge failure as shipped-but-overbudget") &&
          a[0].includes("stale failure signal")
      );
    expect(reclassify).toBeDefined();
  });

  it("does NOT reclassify when PR=MERGED but issue=OPEN (dual-gate guard)", async () => {
    issueState.value = "OPEN";
    setupSkillMock();
    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger, {
      contextFileWaitMs: 0,
    });

    const runPromise = orchestrator.runPipeline(3274);
    await drainTimers();
    const result = await runPromise;

    // Override does NOT fire — pr-merge failure is preserved
    expect(result.outcomeType).not.toBe("shipped-but-overbudget");
    expect(result.failedStage).toBe("pr-merge");
    expect(result.success).toBe(false);

    expect(mockState.setOutcomeType).not.toHaveBeenCalledWith("shipped-but-overbudget");
  });

  it("does NOT reclassify when PR is OPEN (gate fails fast)", async () => {
    prState.value = "OPEN";
    setupSkillMock();
    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger, {
      contextFileWaitMs: 0,
    });

    const runPromise = orchestrator.runPipeline(3274);
    await drainTimers();
    const result = await runPromise;

    expect(result.outcomeType).not.toBe("shipped-but-overbudget");
    expect(result.failedStage).toBe("pr-merge");
    expect(result.success).toBe(false);

    expect(mockState.setOutcomeType).not.toHaveBeenCalledWith("shipped-but-overbudget");
  });
});
