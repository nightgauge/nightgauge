/**
 * Issue #266: Budget-escalation race — a pr-merge killed AFTER the merge landed
 * must not record a MERGED run as failed.
 *
 * Scenario: the pr-merge stage's escalation lets the stage keep running, the
 * merge lands on the forge, then a late progress-runaway kill fires and the
 * failed StageRunResult would otherwise win. These tests pin:
 *
 *   1. Merged + late runaway kill → the post-merge ground-truth override
 *      resolves the stage as success; the run completes (fix a + c).
 *   2. Unmerged + runaway kill → the run fails and the recorded error preserves
 *      the honest `[runaway-progress-exceeded]` marker — never the misattributed
 *      `[runaway-ceiling-exceeded] ... exceeded ceiling ($75.00)` synthetic
 *      string (fix b).
 */

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { PipelineStateService } from "../../src/services/PipelineStateService";
import type { Logger } from "../../src/utils/logger";
import type { SkillRunResult } from "../../src/utils/skillRunner";
import { runStageSkillHeadless } from "../../src/utils/skillRunner";

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

vi.mock("../../src/services/BinaryResolver", () => ({
  BinaryResolver: { fromVSCode: () => ({ resolve: async () => "/fake/nightgauge" }) },
}));

const { prMergedOnForge } = vi.hoisted(() => ({
  prMergedOnForge: { value: true },
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
  execFileMock[kCustom] = (cmd: string, args: string[]) => {
    const state = prMergedOnForge.value ? "MERGED" : "OPEN";

    // Go binary pr-merge gate: passed=true mirrors a MERGED PR.
    if (typeof cmd === "string" && cmd.includes("nightgauge") && args?.[0] === "gate") {
      const passed = prMergedOnForge.value;
      const payload = JSON.stringify({
        stage: "pr-merge",
        gate_name: "pr-merge",
        passed,
        reason: passed ? "PR is MERGED" : "PR not MERGED (state=OPEN)",
        evidence: passed ? ["state=MERGED"] : ["state=OPEN"],
      });
      if (passed) {
        return Promise.resolve({ stdout: payload, stderr: "" });
      }
      const err: any = new Error("gate failed");
      err.code = 2;
      err.stdout = payload;
      err.stderr = "";
      return Promise.reject(err);
    }

    if (cmd === "gh" && args?.[0] === "pr" && args?.[1] === "view") {
      // checkPrMergedForIssue / checkPrMergedAndIssueClosed use `-q .state`.
      if (args.includes("-q") && args.includes(".state")) {
        return Promise.resolve({ stdout: state, stderr: "" });
      }
      // Pre-merge branch guard uses `-q .mergeStateStatus` — CLEAN = no rebase.
      if (args.includes("-q") && args.includes(".mergeStateStatus")) {
        return Promise.resolve({ stdout: "CLEAN", stderr: "" });
      }
      // Full JSON view fallback.
      return Promise.resolve({
        stdout: JSON.stringify({
          state,
          statusCheckRollup: [],
          mergeable: "MERGEABLE",
          mergeStateStatus: "CLEAN",
        }),
        stderr: "",
      });
    }

    if (cmd === "gh" && args?.[0] === "auth") {
      return Promise.resolve({ stdout: authStatus, stderr: "" });
    }

    // gh issue view — issue state lookups.
    if (cmd === "gh" && args?.includes("-q") && args?.includes(".state")) {
      return Promise.resolve({ stdout: prMergedOnForge.value ? "CLOSED" : "OPEN", stderr: "" });
    }

    return Promise.resolve({ stdout: "{}", stderr: "" });
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
        return JSON.stringify({ pr_number: 291 });
      }
      return "{}";
    }),
    writeFileSync: vi.fn(),
  };
});

function createMockStateService(): PipelineStateService {
  return {
    getState: vi.fn().mockResolvedValue({
      schema_version: "1.0",
      issue_number: 266,
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
    setStageAdapter: vi.fn().mockResolvedValue(undefined),
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
    notifyPipelineComplete: vi.fn().mockResolvedValue(undefined),
  } as unknown as PipelineStateService;
}

async function drainTimers(iterations = 40) {
  for (let i = 0; i < iterations; i++) {
    await vi.advanceTimersByTimeAsync(2_500);
  }
}

const RUNAWAY_PROGRESS_ERROR = new Error(
  "[runaway-progress-exceeded] Stage pr-merge terminated: progress stalled. " +
    "Cost $4.5071, signals seen: 0. Treated as transient (stall-kill path)."
);

describe("HeadlessOrchestrator post-merge ground-truth override (Issue #266)", () => {
  let mockLogger: Logger;

  beforeEach(() => {
    vi.clearAllMocks();
    vi.useFakeTimers();
    prMergedOnForge.value = true;
    mockLogger = {
      info: vi.fn(),
      warn: vi.fn(),
      error: vi.fn(),
      debug: vi.fn(),
    } as unknown as Logger;
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  function runWithPrMergeRunawayKill() {
    const orchestrator = new HeadlessOrchestrator(createMockStateService(), mockLogger, {
      contextFileWaitMs: 0,
    });
    vi.mocked(runStageSkillHeadless).mockImplementation((stage, _issue, callbacks) => {
      Promise.resolve().then(() => {
        if (stage === "pr-merge") {
          // Merge already landed on the forge; a late progress-runaway kill fires.
          void callbacks?.onComplete?.({
            success: false,
            exitCode: 1,
            costCapExceeded: true,
            costAtTerminationUsd: 4.5071,
            error: RUNAWAY_PROGRESS_ERROR,
          } as SkillRunResult);
        } else {
          void callbacks?.onComplete?.({ success: true, exitCode: 0 } as SkillRunResult);
        }
      });
      return { kill: vi.fn(), process: null } as any;
    });
    return orchestrator.runPipeline(266);
  }

  it("merged: a late runaway kill at pr-merge is overridden — the run completes", async () => {
    prMergedOnForge.value = true;

    const runPromise = runWithPrMergeRunawayKill();
    await drainTimers();
    const result = await runPromise;

    expect(result.success).toBe(true);
    expect(result.failedStage).toBeUndefined();
    expect(result.completedStages).toContain("pr-merge");

    // The ground-truth override logged the reclassification.
    const warnCalls = vi.mocked(mockLogger.warn).mock.calls;
    const overrideLog = warnCalls.find(
      (a) => typeof a[0] === "string" && a[0].includes("PR is MERGED; recording success")
    );
    expect(
      overrideLog,
      "post-merge override should log the ground-truth reclassification"
    ).toBeDefined();
  });

  it("unmerged: the run fails with the honest [runaway-progress-exceeded] marker, not a $75 ceiling", async () => {
    prMergedOnForge.value = false;

    const runPromise = runWithPrMergeRunawayKill();
    await drainTimers();
    const result = await runPromise;

    expect(result.success).toBe(false);

    const errText = result.error?.message ?? "";
    expect(errText).toContain("[runaway-progress-exceeded]");
    // fix (b): never the misattributed dollar-ceiling synthesis.
    expect(errText).not.toContain("[runaway-ceiling-exceeded]");
    expect(errText).not.toContain("exceeded ceiling ($75");
  });
});
