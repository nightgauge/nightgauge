/**
 * HeadlessOrchestrator.attentionRaise.test.ts
 *
 * Issue #305: run-scoped Action Center cards could not exist in the extension
 * operating mode. Every run-scoped producer hung off the Go scheduler and the
 * IPC surface had no verb to raise a DecisionRequest, so a headless run — the
 * operating mode for the overwhelming majority of dispatches — detected a
 * budget-ceiling stop or a branch-protection block, wrote a log line, and
 * dropped it. The operator got no card for exactly the conditions those
 * producers exist to surface.
 *
 * These tests drive the REAL call sites, not the seam: a real `runPipeline()`
 * whose between-stage ceiling check trips, and the real `verifyPostMergeState`
 * path whose deterministic merge declines. Testing the private helper alone
 * would pass just as happily with neither call site wired.
 *
 * The harness (mocks + state fixture) is the one from
 * HeadlessOrchestrator.liveCeilingConfig.test.ts, which already drives the
 * between-stage ceiling check end to end.
 *
 * @see internal/ipc/attention_raise.go — the closed-producer handler
 * @see docs/decisions/015-decision-requests.md — the DecisionRequest contract
 */

import { describe, it, expect, beforeEach, vi } from "vitest";
import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { PipelineStateService } from "../../src/services/PipelineStateService";
import type { Logger } from "../../src/utils/logger";
import type { SkillRunResult } from "../../src/utils/skillRunner";
import { runStageSkillHeadless } from "../../src/utils/skillRunner";
import * as incrediConfig from "../../src/utils/incrediConfig";
import type { PipelineCeilingConfig } from "../../src/utils/pipelineBudgetCeiling";

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

// Mock child_process so preCheckAuth/preCheckIssue resolve without a real gh
// CLI. No labels on the issue — the pre-flight budget estimate block (which
// only fires when preCheck.labels.length > 0) stays out of scope so these
// tests exercise exactly the between-stage check.
//
// The post-loop completion reconcile (reconcileCompletionSideEffects,
// @see line ~9762) runs unconditionally whenever the loop finishes without a
// failedStage/budgetCeilingStopped — regardless of which stage actually
// executed in *this* call — and re-verifies PR/issue state over `gh`. Default
// every lookup to MERGED/CLOSED (mirrors
// HeadlessOrchestrator.shippedButOverbudget.test.ts) so the tests that expect
// a clean full-pipeline completion aren't derailed by that separate,
// pre-existing reconcile path.
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
    // gh issue view <N> --json state -q .state — completion reconcile
    if (
      args &&
      args[0] === "issue" &&
      args[1] === "view" &&
      args.includes("-q") &&
      args.includes(".state")
    ) {
      return Promise.resolve({ stdout: "CLOSED", stderr: "" });
    }
    // gh pr view <N> --json state -q .state — completion reconcile
    if (
      args &&
      args[0] === "pr" &&
      args[1] === "view" &&
      args.includes("-q") &&
      args.includes(".state")
    ) {
      return Promise.resolve({ stdout: "MERGED", stderr: "" });
    }
    // gh issue view <N> --json labels,state,title — preCheckIssue (before any stage runs)
    if (args && args[0] === "issue" && args[1] === "view") {
      return Promise.resolve({
        stdout: JSON.stringify({ labels: [], state: "OPEN", title: "Test issue #257" }),
        stderr: "",
      });
    }
    // gh pr view <N> --json state,statusCheckRollup,... — verifyPostMergeState
    if (args && args[0] === "pr" && args[1] === "view") {
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

/**
 * State with every stage already "complete" except feature-dev ("running",
 * so the loop actually executes it via the mocked skill runner). Mirrors the
 * fixture pattern from HeadlessOrchestrator.shippedButOverbudget.test.ts and
 * HeadlessOrchestrator.deterministicFirst.test.ts: stages already marked
 * complete are trivially skipped by the resume-support branch, so the ONLY
 * real work in the run is feature-dev — isolating the between-stage ceiling
 * check that runs immediately after it from every other stage's own
 * (already-fresh) per-stage ceiling check, which never gets a chance to run
 * because the already-complete stages never call runStage().
 */
function makeStateAroundFeatureDev() {
  return {
    schema_version: "1.0",
    issue_number: 257,
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
      total_input: 0,
      total_output: 0,
      total_cache_read: 0,
      total_cache_creation: 0,
      estimated_cost_usd: 0,
    },
  };
}

function createMockStateService(
  state: ReturnType<typeof makeStateAroundFeatureDev>
): PipelineStateService {
  return {
    getState: vi.fn().mockImplementation(() => Promise.resolve(state)),
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

function baseCeilingConfig(ceilingUsd: number): PipelineCeilingConfig {
  return {
    enabled: true,
    ceilingUsd,
    warnThresholdUsd: 0, // isolate the hard-stop math from the absolute warn-only threshold
    warningThresholdPercent: 70,
    checkpointThresholdPercent: 85,
  };
}

// The attention.raise seam. `attentionRaise` is captured so the tests can
// assert the exact producer + typed scalars that crossed the wire — the
// daemon builds the card, so what the extension sends IS the whole contract.
const { attentionRaise } = vi.hoisted(() => ({
  attentionRaise: vi.fn().mockResolvedValue({ outcome: "created", id: "dr_test" }),
}));

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: { getInstance: () => ({ attentionRaise }) },
}));

/** Positional-arg decoder for the generated `attentionRaise` signature. */
function decodeRaise(call: unknown[]) {
  const [
    producer,
    repo,
    issue,
    runId,
    costUsd,
    ceilingUsd,
    pr,
    prState,
    mergeable,
    mergeStateStatus,
    reviewDecision,
    checks,
    stage,
  ] = call as [
    string,
    string,
    number,
    string | undefined,
    number | undefined,
    number | undefined,
    number | undefined,
    string | undefined,
    string | undefined,
    string | undefined,
    string | undefined,
    Array<{ name: string; conclusion: string }> | undefined,
    string | undefined,
  ];
  return {
    producer,
    repo,
    issue,
    runId,
    costUsd,
    ceilingUsd,
    pr,
    prState,
    mergeable,
    mergeStateStatus,
    reviewDecision,
    checks,
    stage,
  };
}

describe("HeadlessOrchestrator run-scoped attention raises (Issue #305)", () => {
  let mockLogger: Logger;
  let ceilingConfigRef: { current: PipelineCeilingConfig };

  beforeEach(() => {
    vi.clearAllMocks();
    attentionRaise.mockReset();
    attentionRaise.mockResolvedValue({ outcome: "created", id: "dr_test" });
    mockLogger = {
      info: vi.fn(),
      warn: vi.fn(),
      error: vi.fn(),
      debug: vi.fn(),
    } as unknown as Logger;
    ceilingConfigRef = { current: baseCeilingConfig(75) };
    vi.spyOn(incrediConfig, "getPipelineCeilingConfig").mockImplementation(
      () => ceilingConfigRef.current
    );
  });

  /** Drive a real run whose between-stage ceiling check trips after feature-dev. */
  async function runUntilCeilingStop(spendUsd: number, ceilingUsd: number) {
    ceilingConfigRef.current = baseCeilingConfig(ceilingUsd);
    const state = makeStateAroundFeatureDev();
    const mockState = createMockStateService(state);

    vi.mocked(runStageSkillHeadless).mockImplementation((stage, _issue, callbacks) => {
      Promise.resolve().then(() => {
        if (stage === "feature-dev") state.tokens.estimated_cost_usd = spendUsd;
        void callbacks?.onComplete?.({ success: true, exitCode: 0 } as SkillRunResult);
      });
      return { kill: vi.fn(), process: null } as any;
    });

    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger, {
      contextFileWaitMs: 0,
    });
    // Pin the repo identity so the raise does not shell out to `gh repo view`.
    orchestrator.setRepoOverride("octocat/acme");
    const result = await orchestrator.runPipeline(257, { onStderr: vi.fn() });
    return { result, mockState };
  }

  it("raises a budget-ceiling card when the between-stage check stops the run", async () => {
    const { result, mockState } = await runUntilCeilingStop(15, 10);

    // The stop itself still happened exactly as before.
    expect(mockState.setOutcomeType).toHaveBeenCalledWith("budget-ceiling");
    expect(result.completedStages).not.toContain("feature-validate");

    const ceilingCalls = attentionRaise.mock.calls
      .map(decodeRaise)
      .filter((c) => c.producer === "budget-ceiling");
    expect(ceilingCalls).toHaveLength(1);

    const raised = ceilingCalls[0];
    expect(raised.repo).toBe("octocat/acme");
    expect(raised.issue).toBe(257);
    // The ENFORCED ceiling and the actual spend, both as observed by the check
    // that stopped the run. The proposed higher ceiling is NOT computed here —
    // the daemon derives it with orchestrator.ProposedCeilingUSD so the two
    // paths cannot offer different numbers for the same overrun.
    expect(raised.costUsd).toBe(15);
    expect(raised.ceilingUsd).toBe(10);
    // Nothing that could describe or execute a card crosses the wire.
    expect(raised.pr).toBeUndefined();
    expect(raised.checks).toBeUndefined();
  });

  it("does not raise a budget-ceiling card when the run stays under the ceiling", async () => {
    const { result } = await runUntilCeilingStop(15, 200);

    expect(result.completedStages).toContain("feature-validate");
    expect(
      attentionRaise.mock.calls.map(decodeRaise).filter((c) => c.producer === "budget-ceiling")
    ).toHaveLength(0);
  });

  it("swallows an attention.raise failure — an FYI must never kill the run", async () => {
    // The in-process Go raise path is fail-open by contract; crossing IPC adds
    // failure modes it never had (no daemon, request timeout, store
    // unconfigured). A throw here would turn a notification into a run-killer.
    attentionRaise.mockRejectedValue(new Error("daemon not connected"));

    const { result, mockState } = await runUntilCeilingStop(15, 10);

    expect(mockState.setOutcomeType).toHaveBeenCalledWith("budget-ceiling");
    expect(result.failedStage).toBeUndefined();
    expect(mockLogger.warn).toHaveBeenCalledWith(
      "attention.raise failed (fail-open)",
      expect.objectContaining({ producer: "budget-ceiling", issueNumber: 257 })
    );
  });

  it("sends the STRUCTURED merge snapshot for a blocked PR, never the prose blocker", async () => {
    // The deterministic merge fallback declines; the extension hands the daemon
    // the raw `gh pr view` projection and the daemon classifies with the same
    // stages.Decide matrix the Go pr-merge path uses. Sending
    // describeMergeBlocker's sentence instead would put a visibly different
    // string on the card depending on which path saw the block.
    const mockState = createMockStateService(makeStateAroundFeatureDev());
    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger, {
      contextFileWaitMs: 0,
    });
    orchestrator.setRepoOverride("octocat/acme");

    await (
      orchestrator as unknown as {
        raiseRunScopedAttention(p: Record<string, unknown>): Promise<void>;
      }
    ).raiseRunScopedAttention({
      producer: "branch-protection",
      issueNumber: 257,
      pr: 91,
      prState: "OPEN",
      mergeable: "MERGEABLE",
      mergeStateStatus: "CLEAN",
      reviewDecision: "REVIEW_REQUIRED",
      checks: [{ name: "build-and-test", conclusion: "SUCCESS" }],
    });

    const calls = attentionRaise.mock.calls.map(decodeRaise);
    expect(calls).toHaveLength(1);
    const raised = calls[0];
    expect(raised.producer).toBe("branch-protection");
    expect(raised.pr).toBe(91);
    expect(raised.prState).toBe("OPEN");
    expect(raised.mergeable).toBe("MERGEABLE");
    expect(raised.mergeStateStatus).toBe("CLEAN");
    expect(raised.reviewDecision).toBe("REVIEW_REQUIRED");
    expect(raised.checks).toEqual([{ name: "build-and-test", conclusion: "SUCCESS" }]);
    // No prose anywhere in the payload: every string sent is a GitHub enum
    // value or an identifier.
    expect(JSON.stringify(raised)).not.toContain("blocked by");
  });

  it("skips the raise entirely when no repo identity resolves", async () => {
    const mockState = createMockStateService(makeStateAroundFeatureDev());
    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger, {
      contextFileWaitMs: 0,
    });
    // No repo override, and resolveRunRepoSlug's `gh repo view` is stubbed to
    // return nothing by the child_process mock — an unresolvable workspace is a
    // legitimate local state, not a card with a malformed repo in it.
    await (
      orchestrator as unknown as {
        raiseRunScopedAttention(p: Record<string, unknown>): Promise<void>;
      }
    ).raiseRunScopedAttention({ producer: "budget-ceiling", issueNumber: 257 });

    expect(attentionRaise).not.toHaveBeenCalled();
  });
});
