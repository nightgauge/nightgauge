/**
 * HeadlessOrchestrator.liveCeilingConfig.test.ts
 *
 * Issue #257: the run-level pipeline budget ceiling was cached from
 * `getPipelineCeilingConfig()` once at the top of `runPipeline()`. A live
 * edit of `.nightgauge/config.yaml` (`pipeline.token_budget_ceiling.ceiling_usd`)
 * mid-run reached the per-stage ceiling instances (constructed fresh per
 * `executeStage`/`runStage()` call) but NOT the between-stage check in the
 * `runPipeline()` stage loop, which kept enforcing the stale value it read
 * at run start for the rest of the run — in either direction (an operator
 * raising the ceiling to let a run continue, or lowering it to stop runaway
 * spend, was silently ignored by this specific check).
 *
 * These tests drive a real `runPipeline()` call with `feature-dev` as the
 * only stage that actually executes (every other stage is pre-seeded as
 * "complete" in the mock state, matching the established resume-support
 * fixture pattern used across this suite) and flip the mocked
 * `getPipelineCeilingConfig()` return value from inside feature-dev's
 * `onComplete` — simulating a config-file edit that lands while the stage
 * is running, just before the between-stage check evaluates it.
 *
 * @see pipelineBudgetCeiling.ts
 * @see Issue #1047 - Configurable token budget ceiling
 * @see Issue #253  - setOverrideCeiling escalation override
 * @see Issue #257  - run-level ceiling must re-resolve config live
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

describe("HeadlessOrchestrator live pipeline-ceiling config re-resolution (Issue #257)", () => {
  let mockLogger: Logger;
  let ceilingConfigRef: { current: PipelineCeilingConfig };

  beforeEach(() => {
    vi.clearAllMocks();
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

  it("honors a mid-run ceiling RAISE at the very next between-stage check", async () => {
    // Stale-config bug reproduction: at run start the ceiling is $10 — low
    // enough that the $15 feature-dev cost would trip a hard stop if the
    // between-stage check kept using this snapshot for the rest of the run.
    ceilingConfigRef.current = baseCeilingConfig(10);
    const state = makeStateAroundFeatureDev();
    const mockState = createMockStateService(state);

    vi.mocked(runStageSkillHeadless).mockImplementation((stage, _issue, callbacks) => {
      Promise.resolve().then(() => {
        if (stage === "feature-dev") {
          // Simulate the operator's live config.yaml edit landing while
          // feature-dev was running, plus feature-dev's actual spend.
          state.tokens.estimated_cost_usd = 15;
          ceilingConfigRef.current = baseCeilingConfig(200);
        }
        void callbacks?.onComplete?.({ success: true, exitCode: 0 } as SkillRunResult);
      });
      return { kill: vi.fn(), process: null } as any;
    });

    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger, {
      contextFileWaitMs: 0,
    });
    const onStderr = vi.fn();

    const result = await orchestrator.runPipeline(257, { onStderr });

    // The between-stage check must have re-read the raised $200 ceiling —
    // no stop message was ever emitted for this run.
    const stopMessages = onStderr.mock.calls.filter(
      ([, data]) => typeof data === "string" && data.includes("PIPELINE BUDGET CEILING")
    );
    expect(stopMessages).toHaveLength(0);
    expect(mockState.setOutcomeType).not.toHaveBeenCalledWith("budget-ceiling");

    // Every stage is accounted for (feature-dev genuinely ran; the rest were
    // pre-seeded complete) — the run reached the end instead of stopping
    // right after feature-dev on the stale $10 ceiling.
    expect(result.completedStages).toContain("feature-dev");
    expect(result.completedStages).toContain("feature-validate");
    expect(result.completedStages).toContain("pipeline-finish");
    expect(result.failedStage).toBeUndefined();
    expect(result.success).toBe(true);
  });

  it("honors a mid-run ceiling LOWER at the very next between-stage check", async () => {
    // Inverse of the raise case: at run start the ceiling is a generous
    // $200 — comfortably above the $15 feature-dev cost. If the between-stage
    // check reused this stale snapshot, the run would sail on to
    // feature-validate. It must not: the operator lowered the ceiling to $10
    // mid-run and the very next check has to stop on it.
    ceilingConfigRef.current = baseCeilingConfig(200);
    const state = makeStateAroundFeatureDev();
    const mockState = createMockStateService(state);

    vi.mocked(runStageSkillHeadless).mockImplementation((stage, _issue, callbacks) => {
      Promise.resolve().then(() => {
        if (stage === "feature-dev") {
          state.tokens.estimated_cost_usd = 15;
          ceilingConfigRef.current = baseCeilingConfig(10);
        }
        void callbacks?.onComplete?.({ success: true, exitCode: 0 } as SkillRunResult);
      });
      return { kill: vi.fn(), process: null } as any;
    });

    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger, {
      contextFileWaitMs: 0,
    });
    const onStderr = vi.fn();

    const result = await orchestrator.runPipeline(257, { onStderr });

    // The between-stage check re-read the lowered $10 ceiling and stopped —
    // using the freshly-read values, not the stale $200 snapshot.
    const stopCall = onStderr.mock.calls.find(
      ([, data]) => typeof data === "string" && data.includes("PIPELINE BUDGET CEILING")
    );
    expect(stopCall).toBeDefined();
    expect(stopCall![1]).toContain("$15.00");
    expect(stopCall![1]).toContain("$10.00");
    expect(mockState.setOutcomeType).toHaveBeenCalledWith("budget-ceiling");

    // Controlled stop, not a failure: feature-validate never ran because the
    // loop broke right after feature-dev.
    expect(result.completedStages).toContain("feature-dev");
    expect(result.completedStages).not.toContain("feature-validate");
    expect(result.failedStage).toBeUndefined();
    expect(result.success).toBe(false);
  });

  it("layers the #253 escalation override on top of a freshly re-read base ceiling", async () => {
    // Regression guard for the interaction between #253 and #257: the
    // confirmed "Increase Ceiling & Continue" override must survive even
    // when the between-stage check is re-reading a live config edit that
    // landed in between (e.g. the maintainer independently lowered the base
    // ceiling right after the operator's escalation was confirmed).
    ceilingConfigRef.current = baseCeilingConfig(75);
    const state = makeStateAroundFeatureDev();
    const mockState = createMockStateService(state);

    vi.mocked(runStageSkillHeadless).mockImplementation((stage, _issue, callbacks) => {
      Promise.resolve().then(() => {
        if (stage === "feature-dev") {
          state.tokens.estimated_cost_usd = 107.02;
          // Base ceiling drops to $50 — well below the escalation override
          // of $150 set below, which must still win.
          ceilingConfigRef.current = baseCeilingConfig(50);
        }
        void callbacks?.onComplete?.({ success: true, exitCode: 0 } as SkillRunResult);
      });
      return { kill: vi.fn(), process: null } as any;
    });

    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger, {
      contextFileWaitMs: 0,
    });
    // Simulate a previously-confirmed "Increase Ceiling & Continue" (#253) —
    // set directly rather than driving the full interactive escalation UI.
    (orchestrator as unknown as { ceilingOverrideUsd: number | null }).ceilingOverrideUsd = 150;
    const onStderr = vi.fn();

    const result = await orchestrator.runPipeline(257, { onStderr });

    const stopMessages = onStderr.mock.calls.filter(
      ([, data]) => typeof data === "string" && data.includes("PIPELINE BUDGET CEILING")
    );
    expect(stopMessages).toHaveLength(0);
    expect(mockState.setOutcomeType).not.toHaveBeenCalledWith("budget-ceiling");
    expect(result.completedStages).toContain("feature-validate");
    expect(result.success).toBe(true);
  });
});
