/**
 * HeadlessOrchestrator.postMergeVerification.test.ts
 *
 * Tests for verifyPostMergeState() warn→error escalation behavior.
 *
 * - First-attempt failure (attempt=1): logs WARN with auto-recovery wording
 * - Terminal failure (attempt=2): logs ERROR with isTerminal: true
 * - Retry attempt count is visible in the log payload
 *
 * @see Issue #3132 - Fix orchestrator post-merge verification log message
 */

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { PipelineStateService } from "../../src/services/PipelineStateService";
import type { Logger } from "../../src/utils/logger";
import type { SkillRunResult } from "../../src/utils/skillRunner";
import { runStageSkillHeadless } from "../../src/utils/skillRunner";

// #4044: the live adapter auth gate now injects a real preflight runner, so it
// probes CLI auth (codex login status / claude auth status). These pr-merge-path
// tests run with no CLI auth in the env; skip the gate (the
// pipeline.skip_auth_preflight escape hatch) so they exercise the merge behavior
// they target rather than failing fast at pipeline-start.
vi.mock("../../src/utils/nightgaugeConfig", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../src/utils/nightgaugeConfig")>()),
  getSkipAuthPreflight: () => true,
}));

// Mock skillRunner
vi.mock("../../src/utils/skillRunner", () => ({
  hasActiveProcess: vi.fn().mockReturnValue(false),
  killAllActiveProcesses: vi.fn(),
  getActiveInteractiveProcess: vi.fn().mockReturnValue(null),
  runStageSkillHeadless: vi.fn(),
  getNextStage: vi.fn(),
  getStageLabel: vi.fn((stage: string) => stage),
  resolveModel: vi.fn().mockReturnValue({ model: "claude-sonnet-4-6", source: "default" }),
}));

// #3266: BinaryResolver returns a fake path so verifyPostMergeState dispatches
// to the binary stub below.
vi.mock("../../src/services/BinaryResolver", () => ({
  BinaryResolver: { fromVSCode: () => ({ resolve: async () => "/fake/nightgauge" }) },
}));

// Mutable state shared between the hoisted factory and tests.
// vi.hoisted ensures these are initialized before vi.mock factories run.
const { prViewCallCount, prViewMergeAfter, issueStateClosed, mergedPrs, gitLog, ghCalls } =
  vi.hoisted(() => ({
    // #2689: when set, `gh pr list --state merged` returns this list.
    mergedPrs: { value: null as null | Array<Record<string, unknown>> },
    // #2689: stdout for `git log main --grep=#N`.
    gitLog: { value: "" },
    ghCalls: [] as string[][],
    prViewCallCount: { value: 0 },
    prViewMergeAfter: { value: Infinity },
    // When true, gh issue view --json state -q .state returns "CLOSED"
    // so reconcileCompletionSideEffects sees the issue as successfully closed.
    issueStateClosed: { value: false },
  }));

vi.mock("child_process", async () => {
  const actual = await vi.importActual<typeof import("child_process")>("child_process");
  const kCustom = Symbol.for("nodejs.util.promisify.custom");

  const authStatus =
    "Logged in to github.com account testuser (keyring)\n" +
    "  Token: gho_fake\n  Token scopes: 'gist', 'read:org', 'repo', 'workflow'";
  const issueJson = '{"labels":[],"state":"OPEN","title":"Test issue #3132"}';

  const execMock: any = vi.fn();
  execMock[kCustom] = () => Promise.resolve({ stdout: authStatus, stderr: "" });

  const execFileMock: any = vi.fn();
  execFileMock[kCustom] = (cmd: string, args: string[]) => {
    if (cmd === "gh" && args) ghCalls.push(args);
    // #3266: the binary's pr-merge gate. Substitute for gh polling in the
    // refactored verifyPostMergeState. cmd is the binary path; args are
    // ["gate", "verify", "pr-merge", N, ...].
    if (typeof cmd === "string" && cmd.includes("nightgauge") && args && args[0] === "gate") {
      prViewCallCount.value++;
      const merged = prViewCallCount.value > prViewMergeAfter.value;
      const payload = merged
        ? { passed: true, reason: "PR is MERGED", evidence: ["state=MERGED"] }
        : { passed: false, reason: "PR #999 is not MERGED (state=OPEN)", evidence: ["state=OPEN"] };
      const stdout = JSON.stringify({ stage: "pr-merge", gate_name: "pr-merge", ...payload });
      if (merged) {
        return Promise.resolve({ stdout, stderr: "" });
      }
      const err: any = new Error("gate failed");
      err.code = 2;
      err.stdout = stdout;
      err.stderr = "";
      return Promise.reject(err);
    }
    if (args && args[0] === "pr" && args[1] === "list" && mergedPrs.value) {
      return Promise.resolve({ stdout: JSON.stringify(mergedPrs.value), stderr: "" });
    }
    if (cmd === "git" && args && args[0] === "log") {
      return Promise.resolve({ stdout: gitLog.value, stderr: "" });
    }
    if (args && args[0] === "pr") {
      // Issue #3782: pre-merge guard queries mergeStateStatus before the skill
      // runs. Don't count these in prViewCallCount — they are not gate retries.
      if (args.includes("-q") && args.includes(".mergeStateStatus")) {
        return Promise.resolve({ stdout: "CLEAN", stderr: "" });
      }
      prViewCallCount.value++;
      const state = prViewCallCount.value > prViewMergeAfter.value ? "MERGED" : "OPEN";
      return Promise.resolve({
        stdout: JSON.stringify({ state, statusCheckRollup: [] }),
        stderr: "",
      });
    }
    // reconcileCompletionSideEffects uses -q .state; return "CLOSED" when requested
    if (args && args.includes("-q") && args.includes(".state")) {
      return Promise.resolve({ stdout: issueStateClosed.value ? "CLOSED" : "OPEN", stderr: "" });
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

// fs: pr context file exists with a valid pr_number
vi.mock("fs", async () => {
  const actual = await vi.importActual<typeof import("fs")>("fs");
  return {
    ...actual,
    existsSync: vi.fn().mockReturnValue(true),
    readFileSync: vi.fn().mockImplementation((p: string) => {
      if (typeof p === "string" && p.includes("pr-")) {
        return JSON.stringify({ pr_number: 999 });
      }
      return "{}";
    }),
  };
});

/** Pipeline state where all stages are complete except pr-merge (running). */
function makeStateWithPrMergePending() {
  return {
    schema_version: "1.0",
    issue_number: 3132,
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
    getState: vi.fn().mockResolvedValue(makeStateWithPrMergePending()),
    failStage: vi.fn().mockResolvedValue(undefined),
    clearPipeline: vi.fn().mockResolvedValue(undefined),
    // ADR-017 step 3 (#370): every run-bearing entry point receives or
    // mints an identity, and initializePipeline refuses without one.
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

/** Advance fake timers enough to drain all EC polling + retry delays. */
async function drainTimers(iterations = 20) {
  for (let i = 0; i < iterations; i++) {
    await vi.advanceTimersByTimeAsync(2_500);
  }
}

describe("HeadlessOrchestrator post-merge verification (Issue #3132)", () => {
  let mockLogger: Logger;

  beforeEach(() => {
    vi.clearAllMocks();
    vi.useFakeTimers();

    // Reset pr-view call counter and merge threshold
    prViewCallCount.value = 0;
    prViewMergeAfter.value = Infinity; // default: always OPEN (never merges)
    mergedPrs.value = null;
    gitLog.value = "";
    issueStateClosed.value = false; // default: issue stays OPEN (reconcile not needed)

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

  it("first-attempt failure logs WARN (not ERROR) with auto-recovery wording", async () => {
    const mockState = createMockStateService();
    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger, {
      contextFileWaitMs: 0,
    });

    // pr-merge skill reports success; verification finds PR still OPEN (both attempts)
    vi.mocked(runStageSkillHeadless).mockImplementation((_stage, _issueNumber, callbacks) => {
      Promise.resolve().then(() => {
        void callbacks?.onComplete?.({ success: true, exitCode: 0 } as SkillRunResult);
      });
      return { kill: vi.fn(), process: null } as any;
    });

    const runPromise = orchestrator.runPipeline(3132);
    await drainTimers();
    await runPromise;

    // First verifyPostMergeState(issueNumber, 1, 2) must log WARN
    const warnCalls = vi.mocked(mockLogger.warn).mock.calls;
    const firstVerifyWarn = warnCalls.find(
      (args) => typeof args[0] === "string" && args[0].includes("pr-merge exited without merging")
    );
    expect(firstVerifyWarn).toBeDefined();

    const payload = firstVerifyWarn![1] as Record<string, unknown>;
    expect(payload.attempt).toBe(1);
    expect(payload.maxAttempts).toBe(2);
    expect(payload.isTerminal).toBe(false);

    // Must NOT log an ERROR for attempt=1
    const errorCalls = vi.mocked(mockLogger.error).mock.calls;
    const firstAttemptError = errorCalls.find(
      (args) =>
        typeof args[0] === "string" &&
        args[0].includes("Post-merge verification FAILED") &&
        (args[1] as Record<string, unknown>)?.attempt === 1
    );
    expect(firstAttemptError).toBeUndefined();
  });

  it("second-attempt (terminal) failure logs ERROR with isTerminal: true", async () => {
    const mockState = createMockStateService();
    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger, {
      contextFileWaitMs: 0,
    });

    // Both pr-merge runs report success; verification always finds PR OPEN
    vi.mocked(runStageSkillHeadless).mockImplementation((_stage, _issueNumber, callbacks) => {
      Promise.resolve().then(() => {
        void callbacks?.onComplete?.({ success: true, exitCode: 0 } as SkillRunResult);
      });
      return { kill: vi.fn(), process: null } as any;
    });

    const runPromise = orchestrator.runPipeline(3132);
    await drainTimers(30);
    const result = await runPromise;

    // Terminal second attempt must log ERROR
    const errorCalls = vi.mocked(mockLogger.error).mock.calls;
    const terminalError = errorCalls.find(
      (args) => typeof args[0] === "string" && args[0].includes("Post-merge verification FAILED")
    );
    expect(terminalError).toBeDefined();

    const payload = terminalError![1] as Record<string, unknown>;
    expect(payload.attempt).toBe(2);
    expect(payload.maxAttempts).toBe(2);
    expect(payload.isTerminal).toBe(true);

    expect(result.success).toBe(false);
  });

  it("successful retry after first failure logs WARN then no ERROR", async () => {
    // #3266: each verifyPostMergeState now spawns the binary once (vs the old
    // 4-poll loop). After 1 call the binary returns MERGED on subsequent calls,
    // simulating: first verify (attempt=1) fails, retry, second verify passes.
    prViewMergeAfter.value = 1;
    // Issue is closed after pipeline succeeds (needed for reconcileCompletionSideEffects)
    issueStateClosed.value = true;

    const mockState = createMockStateService();
    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger, {
      contextFileWaitMs: 0,
    });

    vi.mocked(runStageSkillHeadless).mockImplementation((_stage, _issueNumber, callbacks) => {
      Promise.resolve().then(() => {
        void callbacks?.onComplete?.({ success: true, exitCode: 0 } as SkillRunResult);
      });
      return { kill: vi.fn(), process: null } as any;
    });

    const runPromise = orchestrator.runPipeline(3132);
    await drainTimers(20);
    const result = await runPromise;

    // First attempt logged WARN
    const warnCalls = vi.mocked(mockLogger.warn).mock.calls;
    const firstWarn = warnCalls.find(
      (args) => typeof args[0] === "string" && args[0].includes("pr-merge exited without merging")
    );
    expect(firstWarn).toBeDefined();

    // No ERROR from verifyPostMergeState
    const errorCalls = vi.mocked(mockLogger.error).mock.calls;
    const verifyError = errorCalls.find(
      (args) => typeof args[0] === "string" && args[0].includes("Post-merge verification FAILED")
    );
    expect(verifyError).toBeUndefined();

    expect(result.success).toBe(true);
  });
});

describe("reconcileCompletionSideEffects closing keyword (#2689)", () => {
  let mockLogger: Logger;

  beforeEach(() => {
    ghCalls.length = 0;
    mergedPrs.value = null;
    gitLog.value = "";
    issueStateClosed.value = false;
    mockLogger = {
      info: vi.fn(),
      warn: vi.fn(),
      error: vi.fn(),
      debug: vi.fn(),
    } as unknown as Logger;
  });

  async function reconcile() {
    const orchestrator = new HeadlessOrchestrator(createMockStateService(), mockLogger, {
      contextFileWaitMs: 0,
    });
    return (orchestrator as any).reconcileCompletionSideEffects(2689);
  }

  function issueCloseArgs(): string[][] {
    return ghCalls.filter((a) => a[0] === "issue" && a[1] === "close");
  }

  it("a Refs #N merge leaves the issue open by design, not failed", async () => {
    mergedPrs.value = [
      { number: 2700, title: "fix(#2689): thing", body: "Refs #2689", baseRefName: "main" },
    ];
    const result = await reconcile();
    expect(issueCloseArgs()).toEqual([]);
    expect(result).toMatchObject({
      verified: true,
      issueClosed: false,
      openByDesign: true,
      mergedPrNumber: 2700,
    });
    expect(result.error).toBeUndefined();
    const info = vi.mocked(mockLogger.info).mock.calls.map((c) => String(c[0]));
    expect(info.some((m) => m.includes("stays open by design"))).toBe(true);
  });

  it("a Closes #N merge closes the issue citing the PR", async () => {
    mergedPrs.value = [
      { number: 2701, title: "fix(#2684): other (#2689)", body: "Refs #2689", baseRefName: "main" },
      { number: 2702, title: "fix(#2689): thing", body: "Closes #2689", baseRefName: "main" },
    ];
    await reconcile();
    const closes = issueCloseArgs();
    expect(closes).toHaveLength(1);
    expect(closes[0]).toContain("2689");
    expect(closes[0].join(" ")).toContain("merged PR #2702");
  });

  it("a Refs #N commit on main without a PR closes nothing", async () => {
    mergedPrs.value = [];
    gitLog.value =
      "abc1234\x1ffix: thing\x1ffix: thing\n\nRefs #2689\n\x1e" +
      "def5678\x1ffix: other\x1ffix: other\n\nCloses #26890\n\x1e";
    const result = await reconcile();
    expect(issueCloseArgs()).toEqual([]);
    expect(result.issueClosed).toBe(false);
  });

  it("a Closes #N commit on main closes the issue", async () => {
    mergedPrs.value = [];
    gitLog.value = "abc1234\x1ffix: thing\x1ffix: thing\n\nCloses #2689\n\x1e";
    await reconcile();
    const closes = issueCloseArgs();
    expect(closes).toHaveLength(1);
    expect(closes[0].join(" ")).toContain("Commit: abc1234 fix: thing");
  });
});
