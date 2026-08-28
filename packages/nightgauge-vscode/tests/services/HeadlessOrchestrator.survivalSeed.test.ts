/**
 * Issue #1019: an extension-merged PR seeded no survival record, so the one
 * execution path the dogfood runbook mandates could not produce the evidence
 * the handover gate is measured on.
 *
 * The record WRITER was never unreachable — it was reached and gated off by a
 * missing argument. `hooks.EvaluatePostMerge` only fetches the merge SHA and
 * mergedAt when `--pr` is present, and `SurvivalEligible` requires both, so the
 * seeding block in the CLI verb was dead on this path. A second defect sat
 * directly behind it: the journal is rooted at the hook's process cwd, and the
 * extension spawns it in the run's git WORKTREE, which post-merge cleanup
 * removes — the record would have been written and then deleted.
 *
 * Both are argv contracts with no caller-side guard, which is why nothing could
 * go red when a caller omitted either. These tests are that guard: they assert
 * on the actual spawn the pipeline produces, not on a directly-invoked method.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { PipelineStateService } from "../../src/services/PipelineStateService";
import type { Logger } from "../../src/utils/logger";
import type { SkillRunResult } from "../../src/utils/skillRunner";
import { runStageSkillHeadless } from "../../src/utils/skillRunner";

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

/** Every `hook post-merge` spawn the run produced: its argv and its cwd. */
const { hookSpawns } = vi.hoisted(() => ({
  hookSpawns: [] as Array<{ args: string[]; cwd: string }>,
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
    if (typeof cmd === "string" && cmd.includes("nightgauge") && args?.[0] === "hook") {
      hookSpawns.push({ args: [...args], cwd: opts?.cwd ?? "" });
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
      issue_number: 4151,
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

async function drainTimers(iterations = 40) {
  for (let i = 0; i < iterations; i++) {
    await vi.advanceTimersByTimeAsync(2_500);
  }
}

const LAUNCH_ROOT = "/launch-root";
const WORKTREE = "/launch-root/.worktrees/issue-4151";

/** argv value that follows `flag`, or undefined when the flag is absent. */
function flagValue(args: string[], flag: string): string | undefined {
  const i = args.indexOf(flag);
  return i >= 0 ? args[i + 1] : undefined;
}

describe("HeadlessOrchestrator seeds a survival record on the extension merge path (#1019)", () => {
  let mockLogger: Logger;

  beforeEach(() => {
    vi.clearAllMocks();
    vi.useFakeTimers();
    hookSpawns.length = 0;
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

  async function runCleanPipeline() {
    const orchestrator = new HeadlessOrchestrator(createMockStateService(), mockLogger, {
      contextFileWaitMs: 0,
    });
    // The concurrent-slot shape: the run executes inside a worktree that
    // post-merge cleanup deletes, while durable state belongs to the launch root.
    orchestrator.setMainRepoRoot(LAUNCH_ROOT);
    orchestrator.setWorktreeOverride(WORKTREE);
    orchestrator.setRepoOverride("nightgauge/target");

    vi.mocked(runStageSkillHeadless).mockImplementation((_stage, _issue, callbacks) => {
      Promise.resolve().then(() => {
        void callbacks?.onComplete?.({ success: true, exitCode: 0 } as SkillRunResult);
      });
      return { kill: vi.fn(), process: null } as any;
    });

    const p = orchestrator.runPipeline(4151);
    await drainTimers();
    return p;
  }

  /**
   * ONE pipeline run for all three wiring assertions.
   *
   * Each of these drove its own full eight-stage run at first, and that made
   * the file cost three pipelines to assert three flags on one argv. Even with
   * faked timers the drain loop burns real wall-clock — module churn, promise
   * scheduling, the mocked spawns — and on a loaded CI runner the third test
   * crossed vitest's 15s budget and timed out while passing locally every time.
   *
   * The redundancy was the defect, not the budget: the run is identical in all
   * three, so raising the timeout would have kept paying 3x for one measurement.
   * The mutation guarantee is unchanged — deleting the production call site
   * leaves `spawn` undefined and turns all three red together.
   */
  describe("the argv a clean run produces", () => {
    let spawn: { args: string[]; cwd: string };
    let cached: { args: string[]; cwd: string } | null = null;

    // Drive the pipeline ONCE for all three assertions, then reuse the captured
    // argv. `beforeEach` is the right place to assign `spawn` (the outer
    // beforeEach resets fake timers and clears hookSpawns per test), but the
    // expensive run happens only on the first pass — the `cached` guard is what
    // makes this share work rather than merely relocate it.
    //
    // An explicit timeout because the default HOOK budget is 10s, tighter than
    // the 15s test budget: driving a real eight-stage pipeline burns real
    // wall-clock even with faked timers, and a loaded CI runner crosses it.
    beforeEach(async () => {
      if (!cached) {
        await runCleanPipeline();
        expect(hookSpawns.length, "the post-merge hook should have been invoked").toBeGreaterThan(
          0
        );
        cached = hookSpawns[hookSpawns.length - 1];
      }
      spawn = cached;
    }, 60_000);

    it("passes --pr, so the hook can fetch the merge SHA the record is made of", () => {
      // Without --pr, EvaluatePostMerge never resolves MergedCommitSha/MergedAt,
      // SurvivalEligible stays false, and the seeding block never runs. This is
      // the exact argv omission that produced zero records.
      expect(flagValue(spawn.args, "--pr")).toBe("4200");
    });

    it("roots the journal at the launch root, not the worktree that is about to be deleted", () => {
      expect(flagValue(spawn.args, "--workdir")).toBe(LAUNCH_ROOT);
      expect(flagValue(spawn.args, "--workdir")).not.toBe(WORKTREE);
      // The spawn still runs IN the worktree — only the journal's root moves.
      expect(spawn.cwd).toBe(WORKTREE);
    });

    it("names the repo the run targeted, not whatever repo the cwd happens to be", () => {
      // `gh repo view` is stubbed to answer wrong-owner/wrong-repo: on a
      // cross-repo slot dispatch, asking the cwd is how a survival record comes
      // to name the wrong repository.
      expect(flagValue(spawn.args, "--owner")).toBe("nightgauge");
      expect(flagValue(spawn.args, "--repo")).toBe("target");
    });
  });

  /**
   * Degradation, driven directly rather than through runPipeline.
   *
   * The three tests above already pin the WIRING — deleting the call site turns
   * all of them red — so this one only needs to pin the argv SHAPE when the PR
   * number cannot be read. Driving the whole pipeline for it is both redundant
   * and non-deterministic: with no pr_number the run does not settle inside the
   * drained window, which passed locally and timed out on a loaded CI runner.
   */
  it("still invokes the hook when the PR context file carries no pr_number", async () => {
    const fs = await import("fs");
    vi.mocked(fs.readFileSync).mockImplementation(() => "{}" as any);

    const orchestrator = new HeadlessOrchestrator(createMockStateService(), mockLogger, {
      contextFileWaitMs: 0,
    });
    orchestrator.setMainRepoRoot(LAUNCH_ROOT);
    orchestrator.setWorktreeOverride(WORKTREE);
    orchestrator.setRepoOverride("nightgauge/target");

    await (
      orchestrator as unknown as {
        invokePostMergeHook(issueNumber: number): Promise<void>;
      }
    ).invokePostMergeHook(4151);

    // Degrades to the pre-#1019 behaviour — epic check only — rather than
    // skipping the hook and losing the epic rollup as well.
    expect(hookSpawns.length).toBe(1);
    const args = hookSpawns[0].args;
    expect(flagValue(args, "--pr")).toBeUndefined();
    // The root contract does NOT depend on the PR number being readable.
    expect(flagValue(args, "--workdir")).toBe(LAUNCH_ROOT);
    expect(flagValue(args, "--issue")).toBe("4151");
  });
});
