/**
 * #3259: deterministic merge fallback in verifyPostMergeState.
 *
 * When the pr-merge skill exits "successfully" twice without actually
 * invoking the merge API and the PR is independently clean (mergeable +
 * CLEAN + no failed checks), the orchestrator runs `gh pr merge` itself
 * — a single API call that costs zero LLM tokens — instead of stranding
 * the work behind a confused subagent.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { PipelineStateService } from "../../src/services/PipelineStateService";
import type { Logger } from "../../src/utils/logger";
import type { SkillRunResult } from "../../src/utils/skillRunner";
import { runStageSkillHeadless } from "../../src/utils/skillRunner";
import { getSkipAuthPreflight } from "../../src/utils/incrediConfig";
import { runAdapterAuthPreflight } from "@nightgauge/sdk";

// #4044: the live adapter auth gate now injects a real preflight runner, so it
// probes CLI auth. The merge-path tests skip the gate (skip_auth_preflight) so
// they exercise merge behavior; the regression test below flips both mocks to
// exercise the gate itself. Controllable vi.fns so per-test overrides work.
vi.mock("../../src/utils/incrediConfig", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../src/utils/incrediConfig")>()),
  getSkipAuthPreflight: vi.fn(() => true),
}));

// runAdapterAuthPreflight is overridden so the auth verdict is deterministic
// (the real runner shells out to `codex login status` etc.). Defaults to a pass;
// the regression test forces a failure. All other SDK exports are preserved.
vi.mock("@nightgauge/sdk", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@nightgauge/sdk")>()),
  runAdapterAuthPreflight: vi.fn().mockResolvedValue({ ok: true, failures: [] }),
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

// #3266: BinaryResolver returns a fake path so verifyPostMergeState dispatches
// to the binary stub below.
vi.mock("../../src/services/BinaryResolver", () => ({
  BinaryResolver: { fromVSCode: () => ({ resolve: async () => "/fake/nightgauge" }) },
}));

const {
  prViewCallCount,
  prMergeCalls,
  prMergeShouldFail,
  mergedAfterMergeCall,
  prMergeable,
  prMergeStateStatus,
  prFailedChecks,
  issueStateClosed,
} = vi.hoisted(() => ({
  prViewCallCount: { value: 0 },
  prMergeCalls: { value: 0 },
  prMergeShouldFail: { value: false },
  /** When true, gh pr view returns state="MERGED" after the merge call landed. */
  mergedAfterMergeCall: { value: true },
  prMergeable: { value: "MERGEABLE" },
  prMergeStateStatus: { value: "CLEAN" },
  prFailedChecks: { value: 0 },
  issueStateClosed: { value: true },
}));

vi.mock("child_process", async () => {
  const actual = await vi.importActual<typeof import("child_process")>("child_process");
  const kCustom = Symbol.for("nodejs.util.promisify.custom");

  const authStatus =
    "Logged in to github.com account testuser (keyring)\n" +
    "  Token: gho_fake\n  Token scopes: 'gist', 'read:org', 'repo', 'workflow'";
  const issueJson = '{"labels":[],"state":"OPEN","title":"#3259 test"}';

  const execMock: any = vi.fn();
  execMock[kCustom] = () => Promise.resolve({ stdout: authStatus, stderr: "" });

  const execFileMock: any = vi.fn();
  execFileMock[kCustom] = (cmd: string, args: string[]) => {
    // #3266: binary's pr-merge gate. Always returns passed=false (state=OPEN)
    // so the deterministic merge fallback path triggers.
    if (typeof cmd === "string" && cmd.includes("nightgauge") && args && args[0] === "gate") {
      const stdout = JSON.stringify({
        stage: "pr-merge",
        gate_name: "pr-merge",
        passed: false,
        reason: "PR #3258 is not MERGED (state=OPEN)",
        evidence: ["state=OPEN"],
      });
      const err: any = new Error("gate failed");
      err.code = 2;
      err.stdout = stdout;
      err.stderr = "";
      return Promise.reject(err);
    }
    if (args && args[0] === "pr" && args[1] === "view") {
      prViewCallCount.value++;
      // After the merge call lands, return MERGED so the post-fallback
      // re-poll loop reports success.
      const state = mergedAfterMergeCall.value && prMergeCalls.value > 0 ? "MERGED" : "OPEN";
      const checks = [];
      for (let i = 0; i < prFailedChecks.value; i++) {
        checks.push({ name: `failing-${i}`, conclusion: "FAILURE" });
      }
      return Promise.resolve({
        stdout: JSON.stringify({
          state,
          statusCheckRollup: checks,
          mergeable: prMergeable.value,
          mergeStateStatus: prMergeStateStatus.value,
        }),
        stderr: "",
      });
    }
    if (args && args[0] === "pr" && args[1] === "merge") {
      prMergeCalls.value++;
      if (prMergeShouldFail.value) {
        const err: any = new Error("merge failed");
        err.stderr = "Pull request is not mergeable: branch is out-of-date";
        return Promise.reject(err);
      }
      return Promise.resolve({ stdout: "", stderr: "" });
    }
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

vi.mock("fs", async () => {
  const actual = await vi.importActual<typeof import("fs")>("fs");
  return {
    ...actual,
    existsSync: vi.fn().mockReturnValue(true),
    readFileSync: vi.fn().mockImplementation((p: string) => {
      if (typeof p === "string" && p.includes("pr-")) {
        return JSON.stringify({ pr_number: 3258 });
      }
      return "{}";
    }),
  };
});

function makeStateWithPrMergePending() {
  return {
    schema_version: "1.0",
    issue_number: 3224,
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

describe("HeadlessOrchestrator deterministic merge fallback (Issue #3259)", () => {
  let mockLogger: Logger;

  beforeEach(() => {
    vi.clearAllMocks();
    vi.useFakeTimers();
    prViewCallCount.value = 0;
    prMergeCalls.value = 0;
    prMergeShouldFail.value = false;
    mergedAfterMergeCall.value = true;
    prMergeable.value = "MERGEABLE";
    prMergeStateStatus.value = "CLEAN";
    prFailedChecks.value = 0;
    issueStateClosed.value = true;
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

  function runPipeline() {
    const orchestrator = new HeadlessOrchestrator(createMockStateService(), mockLogger, {
      contextFileWaitMs: 0,
    });
    vi.mocked(runStageSkillHeadless).mockImplementation((_stage, _issue, callbacks) => {
      Promise.resolve().then(() => {
        void callbacks?.onComplete?.({ success: true, exitCode: 0 } as SkillRunResult);
      });
      return { kill: vi.fn(), process: null } as any;
    });
    return orchestrator.runPipeline(3224);
  }

  // #4044 regression: with the gate live (not skipped), an unauthenticated CLI
  // adapter must fail the pipeline FAST at pipeline-start — and the gate must be
  // wired with a real preflight runner (the bug was that no runner was passed,
  // so CLI adapters short-circuited to "passed").
  it("fails fast at pipeline-start when the CLI adapter is unauthenticated (#4044)", async () => {
    // Exercise the REAL gate: clear the suite-wide env skip (set in tests/setup.ts)
    // and make getSkipAuthPreflight return false so neither escape hatch fires.
    const prevEnvSkip = process.env.NIGHTGAUGE_SKIP_AUTH_PREFLIGHT;
    delete process.env.NIGHTGAUGE_SKIP_AUTH_PREFLIGHT;
    vi.mocked(getSkipAuthPreflight).mockReturnValueOnce(false);
    vi.mocked(runAdapterAuthPreflight).mockResolvedValueOnce({
      ok: false,
      failures: [{ adapter: "codex", reason: "not logged in", suggestedFix: "Run `codex login`" }],
    });

    try {
      const runPromise = runPipeline();
      await drainTimers();
      const result = await runPromise;

      // The fix: the gate is invoked WITH a preflight runner so CLI adapters
      // actually probe auth (rather than short-circuiting to "passed").
      expect(vi.mocked(runAdapterAuthPreflight)).toHaveBeenCalledWith(
        expect.anything(),
        expect.objectContaining({ runner: expect.anything() })
      );
      // Fail-fast before any stage executes.
      expect(result.success).toBe(false);
      expect(result.failedStage).toBe("pipeline-start");
      expect(result.error?.message).toContain("Auth pre-flight failed");
      expect(vi.mocked(runStageSkillHeadless)).not.toHaveBeenCalled();
    } finally {
      if (prevEnvSkip === undefined) {
        delete process.env.NIGHTGAUGE_SKIP_AUTH_PREFLIGHT;
      } else {
        process.env.NIGHTGAUGE_SKIP_AUTH_PREFLIGHT = prevEnvSkip;
      }
    }
  });

  it("merges deterministically when terminal verification finds a clean PR", async () => {
    const runPromise = runPipeline();
    await drainTimers();
    const result = await runPromise;

    // The fallback ran exactly once
    expect(prMergeCalls.value).toBe(1);

    // Pipeline reports success
    expect(result.success).toBe(true);

    // Logged the trigger
    const infoCalls = vi.mocked(mockLogger.info).mock.calls;
    const triggered = infoCalls.find(
      (a) => typeof a[0] === "string" && a[0].includes("deterministic merge fallback triggered")
    );
    expect(triggered, "fallback-triggered log line should be present").toBeDefined();

    const succeeded = infoCalls.find(
      (a) => typeof a[0] === "string" && a[0].includes("deterministic merge fallback succeeded")
    );
    expect(succeeded, "fallback-succeeded log line should be present").toBeDefined();
  });

  it("does NOT merge when PR has failing checks", async () => {
    prFailedChecks.value = 2;
    mergedAfterMergeCall.value = false;
    issueStateClosed.value = false;

    const runPromise = runPipeline();
    await drainTimers();
    const result = await runPromise;

    expect(prMergeCalls.value).toBe(0);
    expect(result.success).toBe(false);

    const infoCalls = vi.mocked(mockLogger.info).mock.calls;
    const ineligible = infoCalls.find(
      (a) => typeof a[0] === "string" && a[0].includes("fallback NOT eligible")
    );
    expect(ineligible).toBeDefined();
    const payload = ineligible![1] as Record<string, unknown>;
    expect(payload.failedCheckCount).toBe(2);
  });

  it("does NOT merge when PR is not mergeable (mergeStateStatus != CLEAN)", async () => {
    prMergeStateStatus.value = "BLOCKED";
    mergedAfterMergeCall.value = false;
    issueStateClosed.value = false;

    const runPromise = runPipeline();
    await drainTimers();
    const result = await runPromise;

    expect(prMergeCalls.value).toBe(0);
    expect(result.success).toBe(false);
  });

  it("does NOT merge when mergeable is CONFLICTING", async () => {
    prMergeable.value = "CONFLICTING";
    mergedAfterMergeCall.value = false;
    issueStateClosed.value = false;

    const runPromise = runPipeline();
    await drainTimers();
    const result = await runPromise;

    expect(prMergeCalls.value).toBe(0);
    expect(result.success).toBe(false);
  });

  it("falls through to existing failure path when the merge call itself fails", async () => {
    prMergeShouldFail.value = true;
    mergedAfterMergeCall.value = false;
    issueStateClosed.value = false;

    const runPromise = runPipeline();
    await drainTimers();
    const result = await runPromise;

    expect(prMergeCalls.value).toBe(1);
    expect(result.success).toBe(false);

    const warnCalls = vi.mocked(mockLogger.warn).mock.calls;
    const fellThrough = warnCalls.find(
      (a) => typeof a[0] === "string" && a[0].includes("deterministic merge fallback failed")
    );
    expect(fellThrough).toBeDefined();
  });
});
