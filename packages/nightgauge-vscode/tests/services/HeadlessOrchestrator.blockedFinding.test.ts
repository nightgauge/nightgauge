/**
 * HeadlessOrchestrator.blockedFinding.test.ts
 *
 * Issue #1147 — the `blocked` terminal was honest but inert.
 *
 * #1142 taught the post-validate gate to consult a failed stage's feedback and
 * fork: a signal declaring work OUTSIDE the issue's scope terminates the run as
 * the first-class `blocked` outcome instead of an anonymous validation failure.
 * That is where it stopped. Nothing was written down, so the next dispatch of
 * the same issue spent planning + dev + validate rediscovering the identical
 * wall, and a human had to read a session log to find out why anything stopped.
 *
 * These tests drive the DURABLE half end to end, through `runPipeline()`, with
 * a real `validate-{N}.json` fixture and nothing in the finding path stubbed:
 *
 *   1. a blocked termination writes the finding artifact, comments on the
 *      issue, and raises the Action Center card, and
 *   2. the NEXT dispatch of that issue reads the artifact at pickup and DEFERS
 *      — no subagent, no `hook check-deps` forge call, zero tokens.
 *
 * (2) is the acceptance criterion that makes (1) worth doing, and it is tested
 * against the same production reader the orchestrator uses rather than a stub,
 * because "we wrote a file" and "the next run acts on that file" are two claims
 * and only the second is the feature.
 *
 * RED-PROOFS (behavioural neuters — every one leaves the code compiling):
 *
 *   A. In `recordOutOfScopeBlockedFinding`, replace the body with
 *      `return false` (methods it called stay in place, exported and typed).
 *      → the four "records the finding" tests go red: no `blocked-findings`
 *        write, no `gh issue comment`, no `attentionRaise`, and
 *        `result.blocked.outOfScopeFinding` is false. OBSERVED.
 *   B. In the `issue-pickup` branch, change the finding consult to
 *      `const recordedFinding = null;` (leaving `readBlockedFinding` imported
 *      and used by nothing).
 *      → both re-dispatch tests go red: the run no longer defers, dispatches
 *        `issue-pickup` to an LLM subagent, and books no `deferred` outcome.
 *        OBSERVED.
 *   C. Move the finding consult to AFTER `generateDeterministicContext`.
 *      → the zero-forge-cost test goes red: `generateDeterministicContext` is
 *        called once instead of never, so the defer is no longer free.
 *        OBSERVED.
 *
 * @see Issue #1147
 * @see Issue #1142 — the fork this makes durable
 */
import { describe, it, expect, beforeEach, vi } from "vitest";
import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { PipelineStateService } from "../../src/services/PipelineStateService";
import type { Logger } from "../../src/utils/logger";
import type { SkillRunResult } from "../../src/utils/skillRunner";
import { runStageSkillHeadless } from "../../src/utils/skillRunner";

const ISSUE = 1147;

const { attentionRaise } = vi.hoisted(() => ({
  attentionRaise: vi.fn().mockResolvedValue({ outcome: "created", id: "dr_test" }),
}));

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      diagnosticsRecordStageExit: vi.fn().mockResolvedValue({ recorded: true }),
      attentionRaise,
      call: vi.fn().mockResolvedValue({}),
      on: vi.fn(() => ({ dispose: vi.fn() })),
    }),
  },
}));

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

// No binary: keeps the deterministic pr-stage fast path and the `gate verify`
// re-derivation out of the picture, as the #1142 sibling test does.
vi.mock("../../src/services/BinaryResolver", () => ({
  BinaryResolver: { fromVSCode: () => ({ resolve: async () => null }) },
}));

/** Every shell command the run issued, so the `gh issue comment` can be found. */
const { shellCommands } = vi.hoisted(() => ({ shellCommands: [] as string[] }));

vi.mock("child_process", async () => {
  const actual = await vi.importActual<typeof import("child_process")>("child_process");
  const kCustom = Symbol.for("nodejs.util.promisify.custom");

  const authStatus =
    "Logged in to github.com account testuser (keyring)\n" +
    "  Token: gho_fake\n  Token scopes: 'gist', 'read:org', 'repo', 'workflow'";
  const issueJson = `{"labels":[],"state":"OPEN","title":"Issue #${1147}"}`;

  const execMock: any = vi.fn();
  execMock[kCustom] = (cmd: string) => {
    shellCommands.push(String(cmd));
    return Promise.resolve({ stdout: authStatus, stderr: "" });
  };

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

const { validateContext, findingOnDisk, writes } = vi.hoisted(() => ({
  validateContext: { value: "{}" },
  /** null = no recorded finding for the issue; otherwise the file's contents. */
  findingOnDisk: { value: null as string | null },
  writes: [] as Array<{ path: string; data: string }>,
}));

vi.mock("fs", async () => {
  const actual = await vi.importActual<typeof import("fs")>("fs");
  const isFindingPath = (p: unknown) =>
    typeof p === "string" && p.includes("blocked-findings") && p.includes(`${1147}.json`);
  return {
    ...actual,
    existsSync: vi.fn().mockImplementation((p: string) => {
      if (isFindingPath(p)) {
        return findingOnDisk.value !== null;
      }
      return true;
    }),
    readFileSync: vi.fn().mockImplementation((p: string) => {
      if (isFindingPath(p)) {
        return findingOnDisk.value ?? "{}";
      }
      if (typeof p === "string" && p.includes(`validate-${1147}.json`)) {
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

/**
 * A real failed-validate deliverable carrying the out-of-scope signal — the
 * exact fixture #1142's fork test uses, so the two tests exercise one condition.
 */
function failedValidateWithOutOfScopeSignal(): string {
  return JSON.stringify({
    schema_version: "1.0",
    issue_number: ISSUE,
    stage: "feature-validate",
    validation_status: "failed",
    errorCategory: "tests-failed",
    feedback: [
      {
        signal_type: "PLAN_REVISION_NEEDED",
        emitted_by_stage: "feature-validate",
        severity: "blocking",
        rationale: "AC 3 requires the shared board API, which is not implemented yet.",
        evidence: ["blocked-on: the shared board API in a different repository"],
        backtrack_target_stage: "feature-planning",
      },
    ],
  });
}

function makeLogger(): Logger {
  return {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  } as unknown as Logger;
}

function stageMap(pickupPending: boolean) {
  const done = { status: "complete", auto_retry_count: 0 };
  const skipped = { status: "skipped", auto_retry_count: 0 };
  const pending = { status: "pending", auto_retry_count: 0 };
  if (!pickupPending) {
    return {
      "pipeline-start": pending,
      "issue-pickup": pending,
      "feature-planning": pending,
      "feature-dev": pending,
      "feature-validate": pending,
      "pr-create": pending,
      "pr-merge": pending,
      "pipeline-finish": pending,
    };
  }
  // Everything but issue-pickup already settled, so the loop lands straight on
  // the stage the finding consult guards.
  return {
    "pipeline-start": done,
    "feature-planning": skipped,
    "feature-dev": skipped,
    "feature-validate": skipped,
    "pr-create": skipped,
    "pr-merge": skipped,
    "pipeline-finish": done,
  };
}

function createMockStateService(pickupPending = false): PipelineStateService {
  return {
    getState: vi.fn().mockResolvedValue({
      schema_version: "1.0",
      issue_number: ISSUE,
      stages: stageMap(pickupPending),
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

function findingWrite() {
  return writes.find((w) => w.path.includes("blocked-findings"));
}

describe("HeadlessOrchestrator — a blocked terminal leaves a durable finding (#1147)", () => {
  let logger: Logger;

  beforeEach(() => {
    vi.clearAllMocks();
    writes.length = 0;
    shellCommands.length = 0;
    findingOnDisk.value = null;
    validateContext.value = failedValidateWithOutOfScopeSignal();
    logger = makeLogger();
    mockSkills();
    attentionRaise.mockResolvedValue({ outcome: "created", id: "dr_test" });
  });

  it("persists the finding as a durable artifact outside the archived context tier", async () => {
    const orch = new HeadlessOrchestrator(createMockStateService(), logger, {
      contextFileWaitMs: 0,
    } as never);

    await orch.runPipeline(ISSUE);

    const write = findingWrite();
    expect(write).toBeDefined();
    // A DIRECTORY under pipeline/, not a flat `blocked-<issue>.json`:
    // runstate.ArchiveRun sweeps every flat `*-<issue>.json` under
    // `.nightgauge/pipeline/` into history/<runId>/ at run end and skips
    // directories, so a flat finding would be archived by the run that wrote it.
    expect(write!.path).toContain(`.nightgauge/pipeline/blocked-findings/${ISSUE}.json`);

    const written = JSON.parse(write!.data);
    expect(written.issue_number).toBe(ISSUE);
    expect(written.stage).toBe("feature-validate");
    expect(written.signal_type).toBe("PLAN_REVISION_NEEDED");
    // The rationale and the marker survive VERBATIM — a human turns this into
    // real blockedBy edges, and a summarised marker would make them wrong.
    expect(written.rationale).toContain("shared board API");
    expect(written.evidence).toEqual([
      "blocked-on: the shared board API in a different repository",
    ]);
    expect(written.reason).toContain("out-of-scope blocker");
  });

  it("posts the finding on the issue, so the reason is visible without a session log", async () => {
    const orch = new HeadlessOrchestrator(createMockStateService(), logger, {
      contextFileWaitMs: 0,
    } as never);

    await orch.runPipeline(ISSUE);

    const comment = shellCommands.find((c) => c.startsWith(`gh issue comment ${ISSUE}`));
    expect(comment).toBeDefined();
    expect(comment).toContain("Blocked");
    expect(comment).toContain("shared board API");
    expect(comment).toContain("blocked-on: the shared board API in a different repository");
    // And it says plainly that nothing was written to the dependency graph.
    expect(comment).toContain("blockedBy");
    expect(comment).toContain("add-blocked-by");
  });

  it("raises the out-of-scope-blocker card — the decision request a human resolves", async () => {
    const orch = new HeadlessOrchestrator(createMockStateService(), logger, {
      contextFileWaitMs: 0,
    } as never);

    await orch.runPipeline(ISSUE);

    expect(attentionRaise).toHaveBeenCalledTimes(1);
    const call = attentionRaise.mock.calls[0];
    expect(call[0]).toBe("out-of-scope-blocker");
    expect(call[1]).toBe("TestOrg/test-repo");
    expect(call[2]).toBe(ISSUE);
    // No prose crosses the wire: the raise carries the producer, the repo, the
    // issue, a run id and the stage — never the rationale or the evidence.
    expect(JSON.stringify(call)).not.toContain("shared board API");
  });

  it("marks the terminal state so the slot manager suppresses its generic failure comment", async () => {
    const state = createMockStateService();
    const orch = new HeadlessOrchestrator(state, logger, { contextFileWaitMs: 0 } as never);

    const result = await orch.runPipeline(ISSUE);

    expect(result.outcomeType).toBe("blocked");
    expect(result.blocked?.outOfScopeFinding).toBe(true);
    expect(result.blocked?.blocker).toContain("out-of-scope");
  });

  it("does NOT claim the finding landed when the artifact write failed", async () => {
    // The flag gates another service's comment. Setting it on a failed write
    // would trade a generic failure comment for no comment at all.
    const fs = await import("fs");
    vi.mocked(fs.writeFileSync).mockImplementation((p: any) => {
      // Only the finding write fails; a `mockImplementationOnce` would be
      // consumed by whichever bookkeeping write the run happens to make first.
      if (String(p).includes("blocked-findings")) {
        throw new Error("EACCES");
      }
    });
    const orch = new HeadlessOrchestrator(createMockStateService(), logger, {
      contextFileWaitMs: 0,
    } as never);

    const result = await orch.runPipeline(ISSUE);

    expect(result.outcomeType).toBe("blocked");
    expect(result.blocked?.outOfScopeFinding).toBe(false);
  });
});

describe("HeadlessOrchestrator — a recorded finding defers the re-dispatch for free (#1147)", () => {
  let logger: Logger;

  function recordedFinding(): string {
    return JSON.stringify({
      schema_version: "1.0",
      issue_number: ISSUE,
      stage: "feature-validate",
      signal_type: "PLAN_REVISION_NEEDED",
      reason: "the signal declares an out-of-scope blocker (blocked-on: …)",
      rationale: "AC 3 requires the shared board API, which is not implemented yet.",
      evidence: ["blocked-on: the shared board API in a different repository"],
      run_id: "run-1147",
      recorded_at: "2026-08-29T00:00:00.000Z",
    });
  }

  function orchestratorWithRecordedFinding() {
    const state = createMockStateService(true);
    const orch = new HeadlessOrchestrator(state, logger, { contextFileWaitMs: 0 } as never);
    const assembler = (
      orch as unknown as {
        contextAssembler: { generateDeterministicContext: ReturnType<typeof vi.fn> };
      }
    ).contextAssembler;
    // The REAL deterministic generator would make a `hook check-deps` forge
    // call. Spying (rather than removing it) is what lets the zero-cost test
    // below assert it was never reached.
    assembler.generateDeterministicContext = vi
      .fn()
      .mockResolvedValue({ generated: false, blockedBy: [] });
    return { orch, state, assembler };
  }

  beforeEach(() => {
    vi.clearAllMocks();
    writes.length = 0;
    shellCommands.length = 0;
    findingOnDisk.value = recordedFinding();
    validateContext.value = "{}";
    logger = makeLogger();
    mockSkills();
  });

  it("terminates as a DEFERRAL, not a failure — the shape #189/#305 established", async () => {
    const { orch, state } = orchestratorWithRecordedFinding();

    const result = await orch.runPipeline(ISSUE);

    expect(result.deferred).toBe(true);
    expect(result.success).toBe(false);
    expect(result.failedStage).toBeUndefined();
    expect(result.error).toBeUndefined();
    expect(result.outcomeType).toBe("deferred");
    expect(result.deferredStages).toContain("issue-pickup");
    expect(state.deferStage).toHaveBeenCalledWith("issue-pickup");
    expect(state.setOutcomeType).toHaveBeenCalledWith("deferred");
    // Never a failure: failStage would surface as subagent_crash, pause
    // autonomous, and pollute failure-rate telemetry for a run that spent
    // nothing and in which nothing crashed.
    expect(state.failStage).not.toHaveBeenCalled();
  });

  it("costs ZERO — no subagent, and not even the deterministic path's forge call", async () => {
    const { orch, assembler } = orchestratorWithRecordedFinding();

    await orch.runPipeline(ISSUE);

    // The whole point of writing the finding down: the run that discovered this
    // wall paid planning + dev + validate for it. This one pays a file read.
    expect(runStageSkillHeadless).not.toHaveBeenCalled();
    // And the consult sits AHEAD of deterministic generation, whose
    // `hook check-deps` round trip is pointless once a stage has already
    // answered the question.
    expect(assembler.generateDeterministicContext).not.toHaveBeenCalled();
  });

  it("carries the [blocked-dependency] marker so the completion routes to the deferral path", async () => {
    const { orch } = orchestratorWithRecordedFinding();
    const stderr: string[] = [];

    await orch.runPipeline(ISSUE, {
      onStderr: (_stage: string, data: string) => stderr.push(data),
    } as never);

    const marker = stderr.find((l) => l.includes("[blocked-dependency]"));
    expect(marker).toBeDefined();
    expect(marker).toContain("out-of-scope blocked finding");
    // NOT a `[pipeline-*-failure]` line — that is what the slot manager and the
    // Go scheduler key off to route a completion to the FAILURE path.
    expect(stderr.some((l) => l.includes("pipeline-") && l.includes("-failure"))).toBe(false);
  });

  it("runs normally once the finding is cleared — the defer is a hold, not a verdict", async () => {
    // What resolving the Action Center card does (blocked.clearFinding deletes
    // the file). Without this the hold would be a permanent silent stall.
    findingOnDisk.value = null;
    const { orch, assembler } = orchestratorWithRecordedFinding();

    const result = await orch.runPipeline(ISSUE);

    expect(result.deferred).toBeUndefined();
    expect(assembler.generateDeterministicContext).toHaveBeenCalled();
  });

  // A hit defers a run, so the reader must recognise a finding POSITIVELY —
  // "it parsed and mentions this issue" is not enough. A pipeline context file
  // has `schema_version` and `issue_number` and is not a finding; accepting one
  // would stop the issue running for a reason nothing recorded.
  it.each([
    ["a different issue", { schema_version: "1.0", signal_type: "X", issue_number: 999 }],
    ["a foreign schema version", { schema_version: "1.3", signal_type: "X", issue_number: ISSUE }],
    [
      "no signal_type — e.g. a pipeline context file",
      { schema_version: "1.0", issue_number: ISSUE },
    ],
  ])("ignores a file that only looks like a finding: %s", async (_label, doc) => {
    findingOnDisk.value = JSON.stringify(doc);
    const { orch, assembler } = orchestratorWithRecordedFinding();

    const result = await orch.runPipeline(ISSUE);

    expect(result.deferred).toBeUndefined();
    expect(assembler.generateDeterministicContext).toHaveBeenCalled();
  });
});
