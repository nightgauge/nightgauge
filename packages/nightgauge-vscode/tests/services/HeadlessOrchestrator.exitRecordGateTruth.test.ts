/**
 * HeadlessOrchestrator.exitRecordGateTruth.test.ts
 *
 * Issue #125 — the forensic corpus must never disagree with history.
 *
 * A stage whose skill exits 0 but whose post-condition gate FAILS the stage was
 * written to `.nightgauge/pipeline/exit-records/<day>.jsonl` as `success=true`,
 * because `recordStageExitDiagnostic()` was called with the SkillRunner result
 * (the skill's self-reported process exit code) BEFORE any gate ran. History
 * recorded the truth (`V2StageDetail.status="failed"`); the exit record kept the
 * pre-gate view forever. Gate-caught failures therefore landed in the HEALTHY
 * baseline of every ratio-based health analysis — under-counting exactly the
 * failures gates exist to catch.
 *
 * The invariant these tests pin:
 *
 *   for every stage:  exitRecord.success === (V2StageDetail.status !== "failed")
 *
 * History's `status` is modelled the way Go actually derives it in
 * `internal/state/history.go:BuildV2Record`:
 *   - a stage in `RuntimeState.StageErrors` → `status:"failed"`. TS reaches that
 *     map exclusively via `PipelineStateService.failStage()` →
 *     `pipeline.notifyStageTransition{status:"failed"}` → `SetStageError`.
 *   - a stage in `CompletedStages` → `status:"complete"`.
 * So spying `failStage` + reading `PipelineRunResult.completedStages` reproduces
 * the authoritative history status without booting the Go binary.
 *
 * @see docs/STAGE_GATES.md — the `skill-said-success` failure mode
 * @see docs/STAGE_EXIT_DIAGNOSTIC.md — the record this writes
 */
import { describe, it, expect, beforeEach, vi } from "vitest";
import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { PipelineStateService } from "../../src/services/PipelineStateService";
import type { Logger } from "../../src/utils/logger";
import type { SkillRunResult } from "../../src/utils/skillRunner";
import { runStageSkillHeadless } from "../../src/utils/skillRunner";

const ISSUE = 125;

const recordStageExit = vi.fn().mockResolvedValue({ recorded: true });

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      diagnosticsRecordStageExit: recordStageExit,
      call: vi.fn().mockResolvedValue({}),
      on: vi.fn(() => ({ dispose: vi.fn() })),
    }),
  },
}));

// Skip the live-adapter auth preflight (no CLI auth in the test env).
vi.mock("../../src/utils/incrediConfig", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../src/utils/incrediConfig")>()),
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

// Keep the deterministic pr-stage runners out of the picture — this test is
// about the LLM path's gates, not the #300 fast path.
vi.mock("../../src/services/BinaryResolver", () => ({
  BinaryResolver: { fromVSCode: () => ({ resolve: async () => null }) },
}));

/** `validation_status` the mocked `validate-{N}.json` reports. */
const validateVerdict = { value: "failed" as "failed" | "passed" };

vi.mock("child_process", async () => {
  const actual = await vi.importActual<typeof import("child_process")>("child_process");
  const kCustom = Symbol.for("nodejs.util.promisify.custom");

  const authStatus =
    "Logged in to github.com account testuser (keyring)\n" +
    "  Token: gho_fake\n  Token scopes: 'gist', 'read:org', 'repo', 'workflow'";
  const issueJson = '{"labels":[],"state":"OPEN","title":"Issue #125"}';

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

vi.mock("fs", async () => {
  const actual = await vi.importActual<typeof import("fs")>("fs");
  return {
    ...actual,
    existsSync: vi.fn().mockReturnValue(true),
    readFileSync: vi.fn().mockImplementation((p: string) => {
      if (typeof p === "string" && p.includes(`validate-${ISSUE}.json`)) {
        // The real-run shape: the skill exits 0 and writes its own FAILED
        // verdict into the context file. `verifyPostValidateState` is the gate
        // that turns that into a stage failure.
        return JSON.stringify({
          schema_version: "1.0",
          issue_number: ISSUE,
          validation_status: validateVerdict.value,
          errorCategory: "no-implementation",
        });
      }
      return "{}";
    }),
    writeFileSync: vi.fn(),
    mkdirSync: vi.fn(),
    appendFileSync: vi.fn(),
  };
});

/** Positional argument layout of `IpcClient.diagnosticsRecordStageExit`. */
const ARG = { stage: 2, success: 3, errorText: 9 } as const;

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
    notifyPipelineComplete: vi.fn().mockResolvedValue(undefined),
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

/** Every skill reports a clean exit-0 unless `failAt` names it. */
function mockSkills(failAt?: string) {
  vi.mocked(runStageSkillHeadless).mockImplementation((stage, _issue, callbacks) => {
    Promise.resolve().then(() => {
      void callbacks?.onComplete?.(
        stage === failAt
          ? ({ success: false, exitCode: 1, error: "build failed" } as unknown as SkillRunResult)
          : ({ success: true, exitCode: 0 } as SkillRunResult)
      );
    });
    return { kill: vi.fn(), process: null } as any;
  });
}

/** `stage -> exit-record success` from the captured IPC payloads. */
function exitRecordSuccessByStage(): Map<string, boolean> {
  const m = new Map<string, boolean>();
  for (const call of recordStageExit.mock.calls) {
    m.set(call[ARG.stage] as string, call[ARG.success] as boolean);
  }
  return m;
}

/**
 * `stage -> V2StageDetail.status`, derived exactly as Go's `BuildV2Record`
 * does: `StageErrors` (reached only through `failStage`) wins over
 * `CompletedStages`.
 */
function historyStatusByStage(
  state: PipelineStateService,
  completedStages: readonly string[]
): Map<string, "complete" | "failed"> {
  const m = new Map<string, "complete" | "failed">();
  for (const s of completedStages) m.set(s, "complete");
  for (const call of vi.mocked(state.failStage).mock.calls) {
    m.set(call[0] as string, "failed");
  }
  return m;
}

describe("HeadlessOrchestrator — exit records must record the POST-GATE outcome (#125)", () => {
  let mockLogger: Logger;

  beforeEach(() => {
    vi.clearAllMocks();
    recordStageExit.mockClear();
    validateVerdict.value = "failed";
    mockLogger = makeLogger();
  });

  it("records success=false for a stage that exits 0 and then fails its post-condition gate", async () => {
    mockSkills();
    const state = createMockStateService();
    const orch = new HeadlessOrchestrator(state, mockLogger, { contextFileWaitMs: 0 } as never);

    const result = await orch.runPipeline(ISSUE);

    // The gate did its job: the run failed at feature-validate.
    expect(result.success).toBe(false);
    expect(result.failedStage).toBe("feature-validate");

    // ...and the forensic record agrees. Pre-#125 this was `true`.
    const records = exitRecordSuccessByStage();
    expect(records.has("feature-validate")).toBe(true);
    expect(records.get("feature-validate")).toBe(false);
  });

  it("carries the gate's reason into the record so a retro needs no log archaeology", async () => {
    mockSkills();
    const state = createMockStateService();
    const orch = new HeadlessOrchestrator(state, mockLogger, { contextFileWaitMs: 0 } as never);

    await orch.runPipeline(ISSUE);

    const call = recordStageExit.mock.calls.find((c) => c[ARG.stage] === "feature-validate");
    expect(call).toBeDefined();
    // The `[validation-failed]` marker is what Go's ClassifyTerminalKind keys on
    // to book `validation_failed` — the same terminal kind the run record shows.
    expect(String(call![ARG.errorText] ?? "")).toContain("validation-failed");
  });

  it("INVARIANT: exit-record success never disagrees with history V2StageDetail.status", async () => {
    mockSkills();
    const state = createMockStateService();
    const orch = new HeadlessOrchestrator(state, mockLogger, { contextFileWaitMs: 0 } as never);

    const result = await orch.runPipeline(ISSUE);

    const records = exitRecordSuccessByStage();
    const history = historyStatusByStage(state, result.completedStages);

    const disagreements: string[] = [];
    for (const [stage, recorded] of records) {
      const status = history.get(stage);
      if (status === undefined) continue; // stage never reached history
      const historySaysSuccess = status !== "failed";
      if (recorded !== historySaysSuccess) {
        disagreements.push(
          `${stage}: exit-record success=${recorded} but history status="${status}"`
        );
      }
    }

    expect(disagreements).toEqual([]);
    // Guard against a vacuous pass — the failing stage must be in both corpora.
    expect(records.has("feature-validate")).toBe(true);
    expect(history.get("feature-validate")).toBe("failed");
  });

  it("does not regress the skill-exit-code failure path (success=false, no gate involved)", async () => {
    validateVerdict.value = "passed";
    mockSkills("feature-dev");
    const state = createMockStateService();
    const orch = new HeadlessOrchestrator(state, mockLogger, { contextFileWaitMs: 0 } as never);

    const result = await orch.runPipeline(ISSUE);

    expect(result.failedStage).toBe("feature-dev");
    const records = exitRecordSuccessByStage();
    expect(records.get("feature-dev")).toBe(false);
  });

  it("stages that pass their gates still record success=true (the healthy baseline stays intact)", async () => {
    mockSkills();
    const state = createMockStateService();
    const orch = new HeadlessOrchestrator(state, mockLogger, { contextFileWaitMs: 0 } as never);

    await orch.runPipeline(ISSUE);

    // Same run as above: only feature-validate trips a gate. Everything before
    // it must still anchor "normal" — the fix must not blanket-flip successes.
    const records = exitRecordSuccessByStage();
    expect(records.get("feature-planning")).toBe(true);
    expect(records.get("feature-dev")).toBe(true);
  });
});
