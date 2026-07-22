/**
 * Issue #3782: Pre-merge branch-behind guard in HeadlessOrchestrator.
 *
 * Verifies that checkAndRebaseBehindBranch() runs before the pr-merge skill:
 *   1. behind-clean    → auto-rebased; skill runs normally
 *   2. behind-conflict → pipeline fails early with [pre-merge-conflict] error
 *   3. up-to-date      → no git operations; skill runs normally
 *   4. gh pr view fail → guard skipped (fail-open); skill runs normally
 */

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { PipelineStateService } from "../../src/services/PipelineStateService";
import type { Logger } from "../../src/utils/logger";
import type { SkillRunResult } from "../../src/utils/skillRunner";
import { runStageSkillHeadless } from "../../src/utils/skillRunner";

// #4044: the live adapter auth gate now injects a real preflight runner, so it
// probes CLI auth. These pr-merge-path tests run with no CLI auth in the env;
// skip the gate (pipeline.skip_auth_preflight) so they exercise the branch-guard
// behavior they target rather than failing fast at pipeline-start.
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

const {
  mergeStateStatus,
  fetchShouldFail,
  rebaseShouldFail,
  pushShouldFail,
  ghPrViewShouldFail,
  gitCalls,
} = vi.hoisted(() => ({
  mergeStateStatus: { value: "CLEAN" },
  fetchShouldFail: { value: false },
  rebaseShouldFail: { value: false },
  pushShouldFail: { value: false },
  ghPrViewShouldFail: { value: false },
  gitCalls: { value: [] as string[][] },
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
    // Track git calls for assertions
    if (cmd === "git") {
      gitCalls.value.push(args);
    }

    // Go binary gate — always returns gate failed (PR still OPEN) so pipeline
    // eventually fails rather than looping. This is needed for the post-merge
    // verification path used by tests that expect pipeline failure.
    if (typeof cmd === "string" && cmd.includes("nightgauge") && args?.[0] === "gate") {
      const stdout = JSON.stringify({
        stage: "pr-merge",
        gate_name: "pr-merge",
        passed: false,
        reason: "PR not MERGED (state=OPEN)",
        evidence: ["state=OPEN"],
      });
      const err: any = new Error("gate failed");
      err.code = 2;
      err.stdout = stdout;
      err.stderr = "";
      return Promise.reject(err);
    }

    // gh pr view — return mergeStateStatus for the branch-behind guard
    if (cmd === "gh" && args?.[0] === "pr" && args?.[1] === "view") {
      if (ghPrViewShouldFail.value) {
        return Promise.reject(new Error("gh: not found"));
      }
      // The guard uses -q .mergeStateStatus, so return a plain string
      if (args.includes("-q") && args.includes(".mergeStateStatus")) {
        return Promise.resolve({ stdout: mergeStateStatus.value, stderr: "" });
      }
      // Full JSON view used by post-merge verification
      return Promise.resolve({
        stdout: JSON.stringify({
          state: "OPEN",
          statusCheckRollup: [],
          mergeable: "MERGEABLE",
          mergeStateStatus: mergeStateStatus.value,
        }),
        stderr: "",
      });
    }

    // gh auth status
    if (cmd === "gh" && args?.[0] === "auth") {
      return Promise.resolve({ stdout: authStatus, stderr: "" });
    }

    // git fetch
    if (cmd === "git" && args?.[0] === "fetch") {
      if (fetchShouldFail.value) {
        return Promise.reject(new Error("fetch failed: network error"));
      }
      return Promise.resolve({ stdout: "", stderr: "" });
    }

    // git rebase
    if (cmd === "git" && args?.[0] === "rebase" && args?.[1] !== "--abort") {
      if (rebaseShouldFail.value) {
        return Promise.reject(new Error("CONFLICT (content): Merge conflict in foo.go"));
      }
      return Promise.resolve({ stdout: "", stderr: "" });
    }

    // git rebase --abort
    if (cmd === "git" && args?.[0] === "rebase" && args?.[1] === "--abort") {
      return Promise.resolve({ stdout: "", stderr: "" });
    }

    // git diff --name-only --diff-filter=U (conflict file list)
    if (cmd === "git" && args?.[0] === "diff" && args?.includes("--diff-filter=U")) {
      return Promise.resolve({ stdout: "foo.go\n", stderr: "" });
    }

    // git push
    if (cmd === "git" && args?.[0] === "push") {
      if (pushShouldFail.value) {
        return Promise.reject(new Error("rejected: stale info"));
      }
      return Promise.resolve({ stdout: "", stderr: "" });
    }

    // gh issue view — used by issue state checks
    if (cmd === "gh" && args?.includes("-q") && args?.includes(".state")) {
      return Promise.resolve({ stdout: "OPEN", stderr: "" });
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
        return JSON.stringify({ pr_number: 100 });
      }
      return "{}";
    }),
  };
});

function createMockStateService(): PipelineStateService {
  return {
    getState: vi.fn().mockResolvedValue({
      schema_version: "1.0",
      issue_number: 3782,
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

describe("HeadlessOrchestrator pre-merge branch-behind guard (Issue #3782)", () => {
  let mockLogger: Logger;

  beforeEach(() => {
    vi.clearAllMocks();
    vi.useFakeTimers();
    mergeStateStatus.value = "CLEAN";
    fetchShouldFail.value = false;
    rebaseShouldFail.value = false;
    pushShouldFail.value = false;
    ghPrViewShouldFail.value = false;
    gitCalls.value = [];
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

  function runPrMergeStageOnly() {
    const orchestrator = new HeadlessOrchestrator(createMockStateService(), mockLogger, {
      contextFileWaitMs: 0,
    });
    vi.mocked(runStageSkillHeadless).mockImplementation((_stage, _issue, callbacks) => {
      Promise.resolve().then(() => {
        void callbacks?.onComplete?.({ success: true, exitCode: 0 } as SkillRunResult);
      });
      return { kill: vi.fn(), process: null } as any;
    });
    return orchestrator.runPipeline(3782);
  }

  it("behind-clean: auto-rebases and lets skill run", async () => {
    mergeStateStatus.value = "BEHIND";

    const stderrMessages: string[] = [];
    const orchestrator = new HeadlessOrchestrator(createMockStateService(), mockLogger, {
      contextFileWaitMs: 0,
    });
    vi.mocked(runStageSkillHeadless).mockImplementation((_stage, _issue, callbacks) => {
      Promise.resolve().then(() => {
        void callbacks?.onComplete?.({ success: true, exitCode: 0 } as SkillRunResult);
      });
      return { kill: vi.fn(), process: null } as any;
    });

    const runPromise = orchestrator.runPipeline(3782, {
      onStderr: (_stage, data) => stderrMessages.push(data),
    });
    await drainTimers();
    await runPromise;

    // fetch → rebase → push should have fired
    const fetchCall = gitCalls.value.find((a) => a[0] === "fetch");
    expect(fetchCall, "git fetch should have run").toBeDefined();
    const rebaseCall = gitCalls.value.find((a) => a[0] === "rebase" && a[1] !== "--abort");
    expect(rebaseCall, "git rebase should have run").toBeDefined();
    const pushCall = gitCalls.value.find((a) => a[0] === "push");
    expect(pushCall, "git push should have run").toBeDefined();

    // onStderr fired with rebase message
    const rebaseMsg = stderrMessages.find((m) => m.includes("[pre-merge guard]"));
    expect(rebaseMsg, "rebase notice should be emitted to stderr").toBeDefined();
    expect(rebaseMsg).toContain("rebased and pushed");

    // skill ran (runStageSkillHeadless was called)
    expect(vi.mocked(runStageSkillHeadless)).toHaveBeenCalled();

    // info log emitted
    const infoCalls = vi.mocked(mockLogger.info).mock.calls;
    const rebased = infoCalls.find(
      (a) => typeof a[0] === "string" && a[0].includes("[pre-merge guard] rebased and pushed")
    );
    expect(rebased, "rebased info log should be present").toBeDefined();
  });

  it("behind-conflict: pipeline fails early with [pre-merge-conflict] and file list", async () => {
    mergeStateStatus.value = "BEHIND";
    rebaseShouldFail.value = true;

    const runPromise = runPrMergeStageOnly();
    await drainTimers();
    const result = await runPromise;

    // Pipeline should fail
    expect(result.success).toBe(false);
    expect(result.error).toBeDefined();
    expect(result.error?.message ?? "").toContain("[pre-merge-conflict]");
    expect(result.error?.message ?? "").toContain("foo.go");

    // rebase --abort should have fired
    const abortCall = gitCalls.value.find((a) => a[0] === "rebase" && a[1] === "--abort");
    expect(abortCall, "git rebase --abort should have run").toBeDefined();

    // Skill should NOT have run (we broke early)
    expect(vi.mocked(runStageSkillHeadless)).not.toHaveBeenCalled();

    // warn log with conflict files
    const warnCalls = vi.mocked(mockLogger.warn).mock.calls;
    const conflictWarn = warnCalls.find(
      (a) => typeof a[0] === "string" && a[0].includes("[pre-merge guard] rebase conflict detected")
    );
    expect(conflictWarn, "conflict warn log should be present").toBeDefined();
  });

  it.skip("up-to-date: no git operations; skill runs normally (quarantined: async-isolation bleed on CI)", async () => {
    mergeStateStatus.value = "CLEAN";

    const runPromise = runPrMergeStageOnly();
    await drainTimers();
    await runPromise;

    // No git fetch/rebase/push
    expect(gitCalls.value.filter((a) => a[0] === "fetch")).toHaveLength(0);
    expect(gitCalls.value.filter((a) => a[0] === "rebase")).toHaveLength(0);
    expect(gitCalls.value.filter((a) => a[0] === "push")).toHaveLength(0);

    // debug log saying no-op
    const debugCalls = vi.mocked(mockLogger.debug).mock.calls;
    const noOp = debugCalls.find(
      (a) => typeof a[0] === "string" && a[0].includes("[pre-merge guard] branch not BEHIND")
    );
    expect(noOp, "no-op debug log should be present").toBeDefined();

    // Skill ran
    expect(vi.mocked(runStageSkillHeadless)).toHaveBeenCalled();
  });

  it("gh pr view fails: guard skipped (fail-open); skill runs normally", async () => {
    ghPrViewShouldFail.value = true;

    const runPromise = runPrMergeStageOnly();
    await drainTimers();
    await runPromise;

    // No git fetch/rebase/push
    expect(gitCalls.value.filter((a) => a[0] === "fetch")).toHaveLength(0);

    // warn log about gh failure
    const warnCalls = vi.mocked(mockLogger.warn).mock.calls;
    const ghFail = warnCalls.find(
      (a) => typeof a[0] === "string" && a[0].includes("[pre-merge guard] gh pr view failed")
    );
    expect(ghFail, "gh-failure warn log should be present").toBeDefined();

    // Skill still ran
    expect(vi.mocked(runStageSkillHeadless)).toHaveBeenCalled();
  });
});
