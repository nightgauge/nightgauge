/**
 * Issue #2638: on the extension path (concurrent slots run HeadlessOrchestrator,
 * not the Go scheduler's stage loop) nothing invoked the followup materializer,
 * so every run's discovered-work capture was left behind. #2119's log went
 * straight from "Post-merge hook completed" to the pipeline-finish bookend.
 *
 * These tests pin the wiring on the real spawn a clean run produces, and the
 * non-blocking outcome handling (empty capture, cap refusal).
 */

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import * as path from "path";
import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { PipelineStateService } from "../../src/services/PipelineStateService";
import type { Logger } from "../../src/utils/logger";
import type { SkillRunResult } from "../../src/utils/skillRunner";
import { runStageSkillHeadless } from "../../src/utils/skillRunner";
import { fakeCloneLayout } from "../helpers/cloneLayout";

vi.mock("../../src/utils/nightgaugeConfig", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../src/utils/nightgaugeConfig")>()),
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

/** Every `followup materialize` spawn, and how the stub answers it. */
const { followupSpawns, followupReply, recordStageExit } = vi.hoisted(() => ({
  followupSpawns: [] as Array<{ args: string[]; cwd: string }>,
  followupReply: { value: "empty" as "empty" | "filed" | "cap" },
  recordStageExit: vi.fn(),
}));

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      diagnosticsRecordStageExit: recordStageExit,
      call: vi.fn().mockResolvedValue({}),
      on: vi.fn(() => ({ dispose: vi.fn() })),
    }),
  },
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
  execFileMock[kCustom] = (cmd: string, args: string[], opts?: { cwd?: string }) => {
    if (typeof cmd === "string" && cmd.includes("nightgauge") && args?.[0] === "followup") {
      followupSpawns.push({ args: [...args], cwd: opts?.cwd ?? "" });
      if (followupReply.value === "cap") {
        const err: any = new Error("Command failed");
        err.code = 3;
        err.stderr = "followup materialize: per-run cap refused 2 records";
        return Promise.reject(err);
      }
      const items = followupReply.value === "filed" ? [{ outcome: "filed" }] : [];
      return Promise.resolve({
        stdout: JSON.stringify({
          issue: 2119,
          reason: "empty capture: no discovered-work records",
          items,
        }),
        stderr: "",
      });
    }

    if (typeof cmd === "string" && cmd.includes("nightgauge") && args?.[0] === "hook") {
      return Promise.resolve({
        stdout: JSON.stringify({ autoClosed: false, reason: "no_parent" }),
        stderr: "",
      });
    }

    if (typeof cmd === "string" && cmd.includes("nightgauge") && args?.[0] === "gate") {
      return Promise.resolve({
        stdout: JSON.stringify({
          stage: "pr-merge",
          gate_name: "pr-merge",
          passed: true,
          reason: "PR is MERGED",
          evidence: ["state=MERGED"],
        }),
        stderr: "",
      });
    }

    if (cmd === "gh" && args?.[0] === "pr" && args?.[1] === "view") {
      if (args.includes("-q") && args.includes(".state")) {
        return Promise.resolve({ stdout: "MERGED", stderr: "" });
      }
      if (args.includes("-q") && args.includes(".mergeStateStatus")) {
        return Promise.resolve({ stdout: "CLEAN", stderr: "" });
      }
      return Promise.resolve({
        stdout: JSON.stringify({
          state: "MERGED",
          statusCheckRollup: [],
          mergeable: "MERGEABLE",
          mergeStateStatus: "CLEAN",
        }),
        stderr: "",
      });
    }

    // The repo-view fallback must never be the source when repoOverride is set.
    if (cmd === "gh" && args?.[0] === "repo" && args?.[1] === "view") {
      return Promise.resolve({ stdout: "wrong-owner/wrong-repo", stderr: "" });
    }

    if (cmd === "gh" && args?.[0] === "auth") {
      return Promise.resolve({ stdout: authStatus, stderr: "" });
    }

    if (cmd === "gh" && args?.includes("-q") && args?.includes(".state")) {
      return Promise.resolve({ stdout: "CLOSED", stderr: "" });
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
        return JSON.stringify({ pr_number: 4200 });
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
      issue_number: 2119,
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
    notifyPipelineComplete: vi.fn().mockResolvedValue(undefined),
  } as unknown as PipelineStateService;
}

// Captured before vi.useFakeTimers(), which fakes setImmediate too.
const realSetImmediate = globalThis.setImmediate;

/**
 * Advance fake time until `run` settles. A fixed number of ticks is not enough:
 * part of the run awaits real I/O, and on a loaded runner that can finish after
 * a fixed drain has returned, leaving the run's next fake timer unadvanced and
 * the test hung until its timeout (#2684).
 */
async function drainUntilSettled(run: Promise<unknown>, maxTicks = 2_000) {
  let settled = false;
  void run.then(
    () => (settled = true),
    () => (settled = true)
  );
  for (let i = 0; i < maxTicks && !settled; i++) {
    await vi.advanceTimersByTimeAsync(2_500);
    // Give real I/O a macrotask to land before the next fake tick.
    if (!settled) await new Promise<void>((r) => realSetImmediate(() => r()));
  }
  await run;
}

const LAUNCH_ROOT = "/launch-root";
const WORKTREE = "/launch-root/.worktrees/issue-2119";

/** argv value that follows `flag`, or undefined when the flag is absent. */
function flagValue(args: string[], flag: string): string | undefined {
  const i = args.indexOf(flag);
  return i >= 0 ? args[i + 1] : undefined;
}

describe("HeadlessOrchestrator materializes discovered work after pr-merge (#2638)", () => {
  let mockLogger: Logger;

  beforeEach(() => {
    vi.clearAllMocks();
    vi.useFakeTimers();
    followupSpawns.length = 0;
    followupReply.value = "empty";
    recordStageExit.mockResolvedValue({ recorded: true });
    fakeCloneLayout(LAUNCH_ROOT);
    fakeCloneLayout(WORKTREE, path.join(LAUNCH_ROOT, ".git"));
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

  function newOrchestrator() {
    const orchestrator = new HeadlessOrchestrator(createMockStateService(), mockLogger, {
      contextFileWaitMs: 0,
    });
    orchestrator.setMainRepoRoot(LAUNCH_ROOT);
    orchestrator.setWorktreeOverride(WORKTREE);
    orchestrator.setRepoOverride("nightgauge/target");
    return orchestrator;
  }

  function followupExitRecords() {
    return recordStageExit.mock.calls.filter((c) => c[2] === "followup-materialize");
  }

  it("a clean run invokes followup materialize, rooted at the launch root", async () => {
    vi.mocked(runStageSkillHeadless).mockImplementation((_stage, _issue, callbacks) => {
      Promise.resolve().then(() => {
        void callbacks?.onComplete?.({ success: true, exitCode: 0 } as SkillRunResult);
      });
      return { kill: vi.fn(), process: null } as any;
    });
    const p = newOrchestrator().runPipeline(2119);
    await drainUntilSettled(p);

    expect(followupSpawns.length, "followup materialize was never reached").toBe(1);
    const { args } = followupSpawns[0];
    expect(args.slice(0, 3)).toEqual(["followup", "materialize", "2119"]);
    expect(flagValue(args, "--workdir")).toBe(LAUNCH_ROOT);
    expect(flagValue(args, "--owner")).toBe("nightgauge");
    expect(flagValue(args, "--repo")).toBe("target");
    expect(args).toContain("--json");
    const records = followupExitRecords();
    expect(records.length).toBe(1);
    expect(records[0][3]).toBe(true);
  }, 60_000);

  type Invoker = { invokeFollowupMaterialize(issueNumber: number): Promise<void> };

  it("logs an empty capture as a skip, distinct from never reached", async () => {
    await (newOrchestrator() as unknown as Invoker).invokeFollowupMaterialize(2119);
    expect(mockLogger.info).toHaveBeenCalledWith(
      "followup-materialize skipped: capture empty",
      expect.objectContaining({ issueNumber: 2119 })
    );
    expect(followupExitRecords()[0][3]).toBe(true);
  });

  it("logs filed records as completed", async () => {
    followupReply.value = "filed";
    await (newOrchestrator() as unknown as Invoker).invokeFollowupMaterialize(2119);
    expect(mockLogger.info).toHaveBeenCalledWith(
      "followup-materialize completed",
      expect.objectContaining({ filed: 1, records: 1 })
    );
  });

  it("records a cap refusal as a failed exit without throwing", async () => {
    followupReply.value = "cap";
    await expect(
      (newOrchestrator() as unknown as Invoker).invokeFollowupMaterialize(2119)
    ).resolves.toBeUndefined();
    const [rec] = followupExitRecords();
    expect(rec[3]).toBe(false);
    expect(rec[7]).toBe(3);
    expect(rec[9]).toContain("per-run cap refused");
  });
});
