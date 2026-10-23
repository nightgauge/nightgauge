/**
 * HeadlessOrchestrator.stageRunningTransition.test.ts
 *
 * ADR-017 §7.2 (#370) — THE RELOCATED `running` TRANSITION, PINNED AT THE
 * ORCHESTRATOR LEVEL.
 *
 * Step 3 moved the `running` transition ~1 170 lines, from just before the
 * pre-stage work to just after the (synchronous) spawn, so the transition can
 * carry the stage child's pid. A latched `markStageRunning(pid?)` closure
 * keeps it to EXACTLY ONE per stage attempt — a second would re-enter Go's
 * `BeginStage` and reset the stage clock (F26) — and the one exit that never
 * reaches the spawn, the pre-stage budget-ceiling refusal, sends it itself so
 * a refused stage books as begun-then-failed rather than failed-with-no-begin.
 *
 * The sibling suite (`PipelineStateService.stagePid.test.ts`) pins the other
 * half of the chain: `startStage({stagePid})` → the `pipeline.notify
 * StageTransition` wire shape. This one pins the orchestrator's side — that
 * the latch exists, fires once, and carries the pid the spawn callback
 * captured. Without it the invariant D8 exists to hold has a code comment as
 * its only guard.
 *
 * @see docs/decisions/017-runtime-identity-keying.md — §7.2
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { PipelineStateService } from "../../src/services/PipelineStateService";
import type { Logger } from "../../src/utils/logger";
import type { SkillRunResult } from "../../src/utils/skillRunner";
import { runStageSkillHeadless } from "../../src/utils/skillRunner";
import * as nightgaugeConfig from "../../src/utils/nightgaugeConfig";
import type { PipelineCeilingConfig } from "../../src/utils/pipelineBudgetCeiling";

const CHILD_PID = 424242;

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
    if (args && args[0] === "repo" && args[1] === "view") {
      return Promise.resolve({ stdout: "nightgauge/nightgauge", stderr: "" });
    }
    if (args && args[0] === "issue" && args[1] === "view") {
      return Promise.resolve({
        stdout: JSON.stringify({ labels: [], state: "OPEN", title: "Test issue #370" }),
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

function makeState(costUsd: number) {
  return {
    schema_version: "1.0",
    issue_number: 370,
    stages: {
      "pipeline-start": { status: "complete", auto_retry_count: 0 },
      "issue-pickup": { status: "complete", auto_retry_count: 0 },
      "feature-planning": { status: "complete", auto_retry_count: 0 },
      "feature-dev": { status: "running", auto_retry_count: 0 },
      "feature-validate": { status: "pending", auto_retry_count: 0 },
      "pr-create": { status: "pending", auto_retry_count: 0 },
      "pr-merge": { status: "pending", auto_retry_count: 0 },
      "pipeline-finish": { status: "pending", auto_retry_count: 0 },
    },
    tokens: {
      total_input: 0,
      total_output: 0,
      total_cache_read: 0,
      total_cache_creation: 0,
      estimated_cost_usd: costUsd,
    },
  };
}

function createMockStateService(state: ReturnType<typeof makeState>): PipelineStateService {
  return {
    getState: vi.fn().mockImplementation(() => Promise.resolve(state)),
    failStage: vi.fn().mockResolvedValue(undefined),
    clearPipeline: vi.fn().mockResolvedValue(undefined),
    getRunId: vi.fn().mockReturnValue(null),
    getRunRepo: vi.fn().mockReturnValue("nightgauge/nightgauge"),
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
    resumePipeline: vi.fn().mockResolvedValue(true),
    pausePipeline: vi.fn().mockResolvedValue(true),
    setMeta: vi.fn(),
    setLabels: vi.fn().mockResolvedValue(undefined),
    recordBacktrack: vi.fn().mockResolvedValue(undefined),
    failPhase: vi.fn().mockResolvedValue(undefined),
  } as unknown as PipelineStateService;
}

function ceilingConfig(ceilingUsd: number): PipelineCeilingConfig {
  return {
    enabled: true,
    ceilingUsd,
    warnThresholdUsd: 0,
    warningThresholdPercent: 70,
    checkpointThresholdPercent: 85,
  };
}

describe("HeadlessOrchestrator — exactly one `running` transition per stage attempt (D8)", () => {
  let mockLogger: Logger;

  beforeEach(() => {
    vi.clearAllMocks();
    mockLogger = {
      info: vi.fn(),
      warn: vi.fn(),
      error: vi.fn(),
      debug: vi.fn(),
    } as unknown as Logger;
    vi.spyOn(nightgaugeConfig, "getPipelineCeilingConfig").mockReturnValue(ceilingConfig(1000));
  });

  it("sends ONE running transition, carrying the pid the spawn callback captured", async () => {
    const mockState = createMockStateService(makeState(0));

    vi.mocked(runStageSkillHeadless).mockImplementation((stage, _issue, callbacks) => {
      // The production runner fires this SYNCHRONOUSLY right after `spawn`,
      // before it returns the handle — which is what lets the transition
      // carry the pid at all.
      callbacks?.onStageChildSpawned?.(CHILD_PID);
      setTimeout(() => {
        callbacks?.onComplete?.({
          success: true,
          stage,
          exitCode: 0,
          durationMs: 5,
        } as unknown as SkillRunResult);
      }, 0);
      return { process: { pid: CHILD_PID } } as never;
    });

    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger, {
      workspaceRoot: "/tmp/repo",
    } as never);

    await orchestrator.runStage("feature-dev", 370);

    const running = vi.mocked(mockState.startStage).mock.calls;
    expect(running).toHaveLength(1);
    expect(running[0][0]).toBe("feature-dev");
    expect(running[0][1]).toMatchObject({ stagePid: CHILD_PID });
  });

  it("the pre-stage ceiling refusal books BEGUN-then-failed: running (no pid), then failed", async () => {
    // The one exit that never reaches the spawn. Without its own
    // `markStageRunning()` Go would receive a `failed` for a stage it never
    // saw begin.
    const mockState = createMockStateService(makeState(500));
    vi.spyOn(nightgaugeConfig, "getPipelineCeilingConfig").mockReturnValue(ceilingConfig(10));

    vi.mocked(runStageSkillHeadless).mockImplementation(() => {
      throw new Error("the spawn must never be reached on the ceiling-refusal path");
    });

    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger, {
      workspaceRoot: "/tmp/repo",
    } as never);

    const result = await orchestrator.runStage("feature-dev", 370);

    expect(result.success).toBe(false);
    expect(runStageSkillHeadless).not.toHaveBeenCalled();

    const running = vi.mocked(mockState.startStage).mock.calls;
    expect(running).toHaveLength(1);
    expect(running[0][0]).toBe("feature-dev");
    // No child was created, so no pid may be claimed for one.
    expect(running[0][1]?.stagePid).toBeUndefined();

    // …and the failure follows the begin, not the other way round.
    expect(mockState.failStage).toHaveBeenCalledWith("feature-dev", expect.any(String));
    const startOrder = vi.mocked(mockState.startStage).mock.invocationCallOrder[0];
    const failOrder = vi.mocked(mockState.failStage).mock.invocationCallOrder[0];
    expect(startOrder).toBeLessThan(failOrder);
  });
});
