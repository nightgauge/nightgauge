/**
 * HeadlessOrchestrator.failedStageBacktrack.test.ts
 *
 * Issue #1142 — the backtrack engine was unreachable on a failed stage.
 *
 * `runPipelineInner` ran the backtrack engine (`readFeedbackSignals` →
 * `evaluateBacktrack` → `executeBacktrack`) ~640 lines BELOW the post-validate
 * gate that halts the run. `readFeedbackSignals` filters for exactly
 * `severity === "blocking"`, and a blocking signal is by definition emitted when
 * the stage could not succeed — precisely when the gate's unconditional `break`
 * fires first. So `PLAN_REVISION_NEEDED → feature-planning` was written into
 * `validate-{N}.json` by the skill and then dropped on the floor, and
 * `feedback-{N}.json` (whose only writer is `executeBacktrack`, and whose only
 * reader is the Go retry engine) had no producer at all.
 *
 * WHY THE OLD TESTS DID NOT CATCH IT: every existing backtrack test stubs
 * `readFeedbackSignals` or hand-writes `feedback-{N}.json`, so none of them ever
 * traverses the post-validate gate — the missing link between the two was
 * untested by construction. These tests therefore drive a REAL
 * `validate-{N}.json` fixture through `runPipeline()` and stub NOTHING in the
 * backtrack engine: the reader, the budget/oscillation guards and the writer are
 * all the production code.
 *
 * RED-PROOF: restoring the unconditional `break` (deleting the
 * `evaluateFailedStageFeedback` consult at the post-validate gate, leaving every
 * method it calls compiling and in place) turns the rewind tests AND the two
 * `blocked` tests red — feature-planning runs once instead of twice, no
 * `feedback-1142.json` is written, and the run books a generic failure instead
 * of `blocked` — while the halt tests stay green.
 *
 * THE FORK: a blocking signal is not automatically a rewind request. One that
 * declares work outside the issue's own scope must end the run as the existing
 * `blocked` outcome, because no re-plan can make that work possible. The
 * discriminator is the signal's structured fields (`signal_type`, and an
 * `evidence` marker) — never the free-text `rationale`.
 *
 * @see Issue #1142
 * @see docs/FEEDBACK_LOOPS.md
 */
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { PipelineStateService } from "../../src/services/PipelineStateService";
import type { Logger } from "../../src/utils/logger";
import type { SkillRunResult } from "../../src/utils/skillRunner";
import { runStageSkillHeadless } from "../../src/utils/skillRunner";

const ISSUE = 1142;

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      diagnosticsRecordStageExit: vi.fn().mockResolvedValue({ recorded: true }),
      call: vi.fn().mockResolvedValue({}),
      on: vi.fn(() => ({ dispose: vi.fn() })),
    }),
  },
}));

// Skip the live-adapter auth preflight (no CLI auth in the test env).
vi.mock("../../src/utils/nightgaugeConfig", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../src/utils/nightgaugeConfig")>()),
  getSkipAuthPreflight: () => true,
}));

vi.mock("../../src/utils/rateLimitCircuitBreaker", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../src/utils/rateLimitCircuitBreaker")>()),
  tripBreakerIfRateLimited: vi.fn().mockResolvedValue(true),
}));

vi.mock("../../src/utils/skillRunner", () => ({
  hasActiveProcess: vi.fn().mockReturnValue(false),
  killAllActiveProcesses: vi.fn(),
  getActiveInteractiveProcess: vi.fn().mockReturnValue(null),
  runStageSkillHeadless: vi.fn(),
  getNextStage: vi.fn(),
  getStageLabel: vi.fn((stage: string) => stage),
  resolveModel: vi.fn().mockReturnValue({ model: "sonnet", source: "default" }),
}));

// No binary: keeps the deterministic pr-stage fast path and the
// `gate verify` re-derivation out of the picture. This test is about the
// TS post-validate gate's halt decision.
vi.mock("../../src/services/BinaryResolver", () => ({
  BinaryResolver: { fromVSCode: () => ({ resolve: async () => null }) },
}));

vi.mock("child_process", async () => {
  const actual = await vi.importActual<typeof import("child_process")>("child_process");
  const kCustom = Symbol.for("nodejs.util.promisify.custom");

  const authStatus =
    "Logged in to github.com account testuser (keyring)\n" +
    "  Token: gho_fake\n  Token scopes: 'gist', 'read:org', 'repo', 'workflow'";
  const issueJson = `{"labels":[],"state":"OPEN","title":"Issue #${1142}"}`;

  const execMock: any = vi.fn();
  execMock[kCustom] = () => Promise.resolve({ stdout: authStatus, stderr: "" });

  const execFileMock: any = vi.fn();
  execFileMock[kCustom] = (_cmd: string, args: string[]) => {
    const a = args ?? [];
    if (a[0] === "repo" && a.includes(".nameWithOwner")) {
      return Promise.resolve({ stdout: "TestOrg/test-repo", stderr: "" });
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

/**
 * The REAL `validate-{N}.json` the mocked filesystem serves — the exact shape the
 * feature-validate skill writes on a hard-gate failure: exit 0, a `failed`
 * verdict, and the `feedback` array asking for a rewind.
 */
const { validateContext, writes } = vi.hoisted(() => ({
  validateContext: { value: "{}" },
  writes: [] as Array<{ path: string; data: string }>,
}));

vi.mock("fs", async () => {
  const actual = await vi.importActual<typeof import("fs")>("fs");
  return {
    ...actual,
    existsSync: vi.fn().mockReturnValue(true),
    readFileSync: vi.fn().mockImplementation((p: string) => {
      if (typeof p === "string" && p.includes(`validate-${1142}.json`)) {
        return validateContext.value;
      }
      return "{}";
    }),
    writeFileSync: vi.fn().mockImplementation((p: string, data: unknown) => {
      writes.push({ path: String(p), data: typeof data === "string" ? data : String(data) });
    }),
    mkdirSync: vi.fn(),
    appendFileSync: vi.fn(),
  };
});

interface FeedbackFixture {
  signal_type: string;
  emitted_by_stage: string;
  severity: string;
  rationale: string;
  evidence: string[];
  backtrack_target_stage?: string | null;
}

/** A real failed-validate deliverable, optionally carrying feedback signals. */
function failedValidateContext(feedback: FeedbackFixture[]): string {
  return JSON.stringify({
    schema_version: "1.0",
    issue_number: ISSUE,
    stage: "feature-validate",
    validation_status: "failed",
    errorCategory: "tests-failed",
    ...(feedback.length > 0 ? { feedback } : {}),
  });
}

/** Plan-fixable: a different plan for THIS issue would satisfy the criteria. */
const PLAN_REVISION_NEEDED: FeedbackFixture = {
  signal_type: "PLAN_REVISION_NEEDED",
  emitted_by_stage: "feature-validate",
  severity: "blocking",
  rationale: "The plan's data model cannot satisfy the acceptance criteria.",
  evidence: ["tests/models_test.ts:41 — no field can carry the reconciled state"],
  backtrack_target_stage: "feature-planning",
};

/**
 * NOT plan-fixable: the same signal type, but the stage declared an
 * out-of-scope blocker in `evidence`. This is the run that motivated #1142 —
 * one acceptance criterion depended on six other open issues in other repos, so
 * re-planning could only burn another lap and land in the same place.
 */
const PLAN_REVISION_BLOCKED_EXTERNALLY: FeedbackFixture = {
  ...PLAN_REVISION_NEEDED,
  rationale: "AC 3 requires the shared board API, which is not implemented yet.",
  evidence: ["blocked-on: the shared board API in a different repository"],
};

function makeLogger(): Logger {
  return {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  } as unknown as Logger;
}

function createMockStateService(): PipelineStateService {
  const stage = { status: "pending", auto_retry_count: 0 };
  return {
    getState: vi.fn().mockResolvedValue({
      schema_version: "1.0",
      issue_number: ISSUE,
      stages: {
        "pipeline-start": stage,
        "issue-pickup": stage,
        "feature-planning": stage,
        "feature-dev": stage,
        "feature-validate": stage,
        "pr-create": stage,
        "pr-merge": stage,
        "pipeline-finish": stage,
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
    notifyPipelineComplete: vi.fn().mockResolvedValue(undefined),
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

/** Every skill reports a clean exit-0 — the failure lives in the deliverable. */
function mockSkills(): void {
  vi.mocked(runStageSkillHeadless).mockImplementation((stage, _issue, callbacks) => {
    Promise.resolve().then(() => {
      void callbacks?.onComplete?.({ success: true, exitCode: 0 } as SkillRunResult);
    });
    return { kill: vi.fn(), process: null } as any;
  });
}

/** Stages the orchestrator actually dispatched, in order. */
function dispatchedStages(): string[] {
  return vi.mocked(runStageSkillHeadless).mock.calls.map((c) => String(c[0]));
}

function countOf(stage: string): number {
  return dispatchedStages().filter((s) => s === stage).length;
}

describe("HeadlessOrchestrator — backtrack must be reachable on a FAILED stage (#1142)", () => {
  let logger: Logger;

  beforeEach(() => {
    vi.clearAllMocks();
    writes.length = 0;
    delete process.env.NIGHTGAUGE_PIPELINE_MAX_BACKTRACKS;
    logger = makeLogger();
    mockSkills();
  });

  afterEach(() => {
    delete process.env.NIGHTGAUGE_PIPELINE_MAX_BACKTRACKS;
  });

  it("rewinds to the blocking signal's backtrack_target_stage instead of halting", async () => {
    validateContext.value = failedValidateContext([PLAN_REVISION_NEEDED]);
    const state = createMockStateService();
    const orch = new HeadlessOrchestrator(state, logger, { contextFileWaitMs: 0 } as never);

    await orch.runPipeline(ISSUE);

    // The whole point: feature-planning ran a SECOND time because the failed
    // validate asked for it. Pre-#1142 the unconditional break made this 1.
    expect(countOf("feature-planning")).toBe(2);
    expect(countOf("feature-validate")).toBe(2);

    // The rewind was a real backtrack, recorded through the existing engine —
    // not a bespoke retry.
    const backtracks = vi.mocked(state.recordBacktrack).mock.calls;
    expect(backtracks).toHaveLength(1);
    expect(backtracks[0][0]).toMatchObject({
      from_stage: "feature-validate",
      to_stage: "feature-planning",
      signal_type: "PLAN_REVISION_NEEDED",
      attempt_number: 1,
    });
  });

  it("writes feedback-{N}.json on the failed-stage rewind — the Go retry engine's only producer", async () => {
    validateContext.value = failedValidateContext([PLAN_REVISION_NEEDED]);
    const orch = new HeadlessOrchestrator(createMockStateService(), logger, {
      contextFileWaitMs: 0,
    } as never);

    await orch.runPipeline(ISSUE);

    const feedbackWrite = writes.find((w) => w.path.includes(`feedback-${ISSUE}.json`));
    expect(feedbackWrite).toBeDefined();
    const written = JSON.parse(feedbackWrite!.data);
    expect(written.issue_number).toBe(ISSUE);
    expect(written.signals).toHaveLength(1);
    expect(written.signals[0].signal_type).toBe("PLAN_REVISION_NEEDED");
    expect(written.signals[0].backtrack_target_stage).toBe("feature-planning");
  });

  it("still halts when the failed validate carries NO feedback (the halt is the default)", async () => {
    validateContext.value = failedValidateContext([]);
    const state = createMockStateService();
    const orch = new HeadlessOrchestrator(state, logger, { contextFileWaitMs: 0 } as never);

    const result = await orch.runPipeline(ISSUE);

    expect(result.success).toBe(false);
    expect(result.failedStage).toBe("feature-validate");
    expect(result.error?.message).toContain("[validation-failed]");
    expect(countOf("feature-planning")).toBe(1);
    expect(state.recordBacktrack).not.toHaveBeenCalled();
    expect(vi.mocked(state.failStage).mock.calls.map((c) => c[0])).toContain("feature-validate");
    expect(writes.some((w) => w.path.includes(`feedback-${ISSUE}.json`))).toBe(false);
  });

  it("still halts on non-blocking feedback — a warning is not a rewind request", async () => {
    validateContext.value = failedValidateContext([
      { ...PLAN_REVISION_NEEDED, severity: "warning" },
    ]);
    const state = createMockStateService();
    const orch = new HeadlessOrchestrator(state, logger, { contextFileWaitMs: 0 } as never);

    const result = await orch.runPipeline(ISSUE);

    expect(result.failedStage).toBe("feature-validate");
    expect(result.error?.message).toContain("[validation-failed]");
    expect(countOf("feature-planning")).toBe(1);
    expect(state.recordBacktrack).not.toHaveBeenCalled();
  });

  it("still halts on a blocking signal with no backtrack_target_stage — nowhere to rewind to", async () => {
    validateContext.value = failedValidateContext([
      { ...PLAN_REVISION_NEEDED, backtrack_target_stage: null },
    ]);
    const state = createMockStateService();
    const orch = new HeadlessOrchestrator(state, logger, { contextFileWaitMs: 0 } as never);

    const result = await orch.runPipeline(ISSUE);

    expect(result.failedStage).toBe("feature-validate");
    expect(countOf("feature-planning")).toBe(1);
    expect(state.recordBacktrack).not.toHaveBeenCalled();
  });

  it("halts with the ORIGINAL validation error when the backtrack budget is exhausted", async () => {
    // The real guard, through the real config resolver: 0 backtracks allowed.
    process.env.NIGHTGAUGE_PIPELINE_MAX_BACKTRACKS = "0";
    validateContext.value = failedValidateContext([PLAN_REVISION_NEEDED]);
    const state = createMockStateService();
    const orch = new HeadlessOrchestrator(state, logger, { contextFileWaitMs: 0 } as never);

    const result = await orch.runPipeline(ISSUE);

    expect(result.success).toBe(false);
    expect(result.failedStage).toBe("feature-validate");
    // The original verdict error survives — the blocked backtrack must not
    // replace it with a backtrack-specific message.
    expect(result.error?.message).toContain("[validation-failed]");
    expect(result.error?.message).toContain("tests-failed");
    expect(countOf("feature-planning")).toBe(1);
    expect(state.recordBacktrack).not.toHaveBeenCalled();
    expect(writes.some((w) => w.path.includes(`feedback-${ISSUE}.json`))).toBe(false);
  });

  // =====================================================================
  // The fork: a blocking signal is not automatically a rewind request.
  // A signal declaring work OUTSIDE this issue's scope must terminate the run
  // as `blocked` — re-planning cannot make that work possible, so a rewind
  // would burn a whole planning+dev+validate lap to reach the same verdict.
  // =====================================================================

  it("books `blocked` instead of rewinding when the signal declares an out-of-scope blocker", async () => {
    validateContext.value = failedValidateContext([PLAN_REVISION_BLOCKED_EXTERNALLY]);
    const state = createMockStateService();
    const orch = new HeadlessOrchestrator(state, logger, { contextFileWaitMs: 0 } as never);

    const result = await orch.runPipeline(ISSUE);

    // No rewind: the same signal_type that rewinds above must NOT rewind here.
    expect(countOf("feature-planning")).toBe(1);
    expect(state.recordBacktrack).not.toHaveBeenCalled();
    expect(writes.some((w) => w.path.includes(`feedback-${ISSUE}.json`))).toBe(false);

    // ...and the run ends BLOCKED, not as an anonymous failure.
    expect(result.success).toBe(false);
    expect(result.outcomeType).toBe("blocked");
    expect(vi.mocked(state.setOutcomeType)).toHaveBeenCalledWith("blocked");
    expect(result.blocked?.blocker).toContain("out-of-scope");
    expect(result.blocked?.remediation).toContain("shared board API");
    expect(result.failedStage).toBe("feature-validate");
  });

  it("books `blocked` for a blocking signal type no re-plan can clear", async () => {
    // ACCEPTANCE_CRITERIA_AMBIGUOUS needs a human to settle the criterion —
    // planning cannot invent the answer, so this is a terminal blocked run.
    validateContext.value = failedValidateContext([
      {
        signal_type: "ACCEPTANCE_CRITERIA_AMBIGUOUS",
        emitted_by_stage: "feature-validate",
        severity: "blocking",
        rationale: "AC 2 does not say which of the two totals is authoritative.",
        evidence: ["issue body AC 2"],
        backtrack_target_stage: "feature-planning",
      },
    ]);
    const state = createMockStateService();
    const orch = new HeadlessOrchestrator(state, logger, { contextFileWaitMs: 0 } as never);

    const result = await orch.runPipeline(ISSUE);

    expect(countOf("feature-planning")).toBe(1);
    expect(state.recordBacktrack).not.toHaveBeenCalled();
    expect(result.outcomeType).toBe("blocked");
    expect(result.blocked?.blocker).toContain("ACCEPTANCE_CRITERIA_AMBIGUOUS");
  });

  it("a rewindable signal does NOT book blocked — the fork discriminates, it does not blanket-block", async () => {
    validateContext.value = failedValidateContext([PLAN_REVISION_NEEDED]);
    const state = createMockStateService();
    const orch = new HeadlessOrchestrator(state, logger, { contextFileWaitMs: 0 } as never);

    const result = await orch.runPipeline(ISSUE);

    expect(result.outcomeType).not.toBe("blocked");
    expect(result.blocked).toBeUndefined();
    expect(state.recordBacktrack).toHaveBeenCalledTimes(1);
  });

  it("the oscillation guard terminates the loop: one rewind, then the halt", async () => {
    // max_backtracks defaults to 1, so the second failed validate is refused and
    // the run ends at feature-validate rather than looping forever.
    validateContext.value = failedValidateContext([PLAN_REVISION_NEEDED]);
    const state = createMockStateService();
    const orch = new HeadlessOrchestrator(state, logger, { contextFileWaitMs: 0 } as never);

    const result = await orch.runPipeline(ISSUE);

    expect(result.success).toBe(false);
    expect(result.failedStage).toBe("feature-validate");
    expect(result.error?.message).toContain("[validation-failed]");
    expect(vi.mocked(state.recordBacktrack)).toHaveBeenCalledTimes(1);
  });
});
