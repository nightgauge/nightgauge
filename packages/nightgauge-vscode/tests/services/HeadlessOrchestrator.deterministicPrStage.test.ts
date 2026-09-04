/**
 * HeadlessOrchestrator.deterministicPrStage.test.ts
 *
 * Deterministic-FIRST pr-create / pr-merge on the TS (VSCode-dogfood) execution
 * path (Issue #300). Verifies that runPipeline invokes the Go `pr-stage` runners
 * BEFORE the LLM skill and reacts to their JSON contract exactly like the Go
 * scheduler:
 *   - created / merged → LLM skill is NOT spawned (~$0 stage), the run completes.
 *   - punt            → the LLM skill IS spawned (fallthrough, no regression).
 *   - rate-limited    → DEFER: LLM skill is NOT spawned, the run fails transient.
 *   - worktree        → the runner is invoked with --workdir = the worktree.
 *
 * This is the money fix: pre-#300 the dogfood path always ran pr-create + pr-merge
 * through the LLM, paying $5–8/run.
 *
 * @see Issue #300 - wire the deterministic-first hooks into HeadlessOrchestrator
 */

import { describe, it, expect, beforeEach, vi } from "vitest";
import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { PipelineStateService } from "../../src/services/PipelineStateService";
import type { Logger } from "../../src/utils/logger";
import type { SkillRunResult } from "../../src/utils/skillRunner";
import { runStageSkillHeadless } from "../../src/utils/skillRunner";

// Skip the live-adapter auth preflight (no CLI auth in the test env).
vi.mock("../../src/utils/nightgaugeConfig", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../src/utils/nightgaugeConfig")>()),
  getSkipAuthPreflight: () => true,
}));

// Isolate the rate-limit breaker: keep the classifier real, stub the trip so it
// neither touches IPC nor leaks global state between tests.
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
  resolveModel: vi.fn().mockReturnValue({ model: "claude-sonnet-4-6", source: "default" }),
}));

// BinaryResolver returns a fake path so runDeterministicPrStage dispatches to the
// child_process stub below.
vi.mock("../../src/services/BinaryResolver", () => ({
  BinaryResolver: { fromVSCode: () => ({ resolve: async () => "/fake/nightgauge" }) },
}));

// Mutable state shared between the hoisted mock factory and the tests.
const { prStageCalls, prStageCreate, prStageMerge, gatePrMergePassed, prStagePhaseLines } =
  vi.hoisted(() => ({
    prStageCalls: { value: [] as Array<{ verb: string; args: string[] }> },
    // Default: both stages succeed deterministically.
    prStageCreate: {
      value: {
        stage: "pr-create",
        path: "created",
        pr_number: 999,
        pr_url: "https://github.com/TestOrg/test-repo/pull/999",
        reason: "rich-context",
        rate_limited: false,
        duration_ms: 4,
      } as Record<string, unknown>,
    },
    prStageMerge: {
      value: {
        stage: "pr-merge",
        path: "merged",
        pr_number: 999,
        pr_state: "MERGED",
        reason: "clean-mergeable: merged",
        rate_limited: false,
        duration_ms: 6,
      } as Record<string, unknown>,
    },
    gatePrMergePassed: { value: true },
    // #1397 — the sentinel-prefixed phase transitions the fake pr-stage binary
    // writes to stderr while it "runs".
    prStagePhaseLines: { value: [] as string[] },
  }));

vi.mock("child_process", async () => {
  const actual = await vi.importActual<typeof import("child_process")>("child_process");
  const kCustom = Symbol.for("nodejs.util.promisify.custom");

  const authStatus =
    "Logged in to github.com account testuser (keyring)\n" +
    "  Token: gho_fake\n  Token scopes: 'gist', 'read:org', 'repo', 'workflow'";
  // Issue is OPEN at pipeline-start (so the run isn't short-circuited as
  // already-closed); the reconcile `-q .state` branch below returns CLOSED so
  // reconcileCompletionSideEffects is satisfied post-merge.
  const issueJson = '{"labels":[],"state":"OPEN","title":"Test issue #300"}';

  const execMock: any = vi.fn();
  execMock[kCustom] = () => Promise.resolve({ stdout: authStatus, stderr: "" });

  const execFileMock: any = vi.fn();
  execFileMock[kCustom] = (cmd: string, args: string[]) => {
    const a = args ?? [];
    const isBinary = typeof cmd === "string" && cmd.includes("nightgauge");

    if (isBinary && a[0] === "pr-stage") {
      const verb = a[1]; // "create" | "merge"
      prStageCalls.value.push({ verb, args: a });
      const payload = verb === "create" ? prStageCreate.value : prStageMerge.value;
      return Promise.resolve({ stdout: JSON.stringify(payload), stderr: "" });
    }

    if (isBinary && a[0] === "gate" && a[2] === "pr-merge") {
      const passed = gatePrMergePassed.value;
      const stdout = JSON.stringify({
        stage: "pr-merge",
        gate_name: "pr-merge",
        passed,
        reason: passed ? "PR is MERGED" : "PR #999 is not MERGED (state=OPEN)",
        evidence: [passed ? "state=MERGED" : "state=OPEN"],
      });
      if (passed) return Promise.resolve({ stdout, stderr: "" });
      const err: any = new Error("gate failed");
      err.code = 2;
      err.stdout = stdout;
      return Promise.reject(err);
    }

    if (isBinary && a[0] === "gate" && a[2] === "pr-create") {
      return Promise.resolve({
        stdout: JSON.stringify({
          stage: "pr-create",
          gate_name: "pr-create",
          passed: true,
          reason: "PR exists (OPEN)",
          evidence: ["pr=999"],
        }),
        stderr: "",
      });
    }

    if (isBinary && a[0] === "epic") {
      return Promise.resolve({ stdout: "{}", stderr: "" });
    }

    if (a[0] === "pr") {
      // Pre-merge branch-behind guard reads mergeStateStatus; CLEAN → proceed.
      if (a.includes("-q") && a.includes(".mergeStateStatus")) {
        return Promise.resolve({ stdout: "CLEAN", stderr: "" });
      }
      return Promise.resolve({
        stdout: JSON.stringify({ state: "MERGED", statusCheckRollup: [] }),
        stderr: "",
      });
    }

    // resolveRunRepoSlug: gh repo view --json nameWithOwner -q .nameWithOwner
    if (a[0] === "repo" && a.includes(".nameWithOwner")) {
      return Promise.resolve({ stdout: "TestOrg/test-repo", stderr: "" });
    }

    // reconcileCompletionSideEffects: gh issue view -q .state → CLOSED
    if (a.includes("-q") && a.includes(".state")) {
      return Promise.resolve({ stdout: "CLOSED", stderr: "" });
    }

    return Promise.resolve({ stdout: issueJson, stderr: "" });
  };

  // #1397: the deterministic pr-stage call moved from execFile to spawn, so the
  // extension can consume the CLI's phase transitions AS THEY HAPPEN rather
  // than only when the process exits. pr-merge waits out CI on a 30s x 30
  // budget, so an exit-only reader shows 0/14 for up to fifteen minutes.
  const { EventEmitter } = await vi.importActual<typeof import("events")>("events");
  const { Readable } = await vi.importActual<typeof import("stream")>("stream");

  const spawnMock: any = vi.fn((cmd: string, args: string[]) => {
    const a = args ?? [];
    const child: any = new EventEmitter();
    child.stdout = new Readable({ read() {} });
    child.stderr = new Readable({ read() {} });
    child.kill = vi.fn();

    const isBinary = typeof cmd === "string" && cmd.includes("nightgauge");
    const isPrStage = isBinary && a[0] === "pr-stage";
    if (isPrStage) {
      const verb = a[1]; // "create" | "merge"
      prStageCalls.value.push({ verb, args: a });
      const payload = verb === "create" ? prStageCreate.value : prStageMerge.value;
      setImmediate(() => {
        // stderr first, as the real binary does: the transitions are reported
        // during the run, the result only at the end.
        for (const line of prStagePhaseLines.value) child.stderr.push(`${line}\n`);
        child.stdout.push(JSON.stringify(payload));
        child.stdout.push(null);
        child.stderr.push(null);
        child.emit("close", 0);
      });
    } else {
      setImmediate(() => {
        child.stdout.push(null);
        child.stderr.push(null);
        child.emit("close", 0);
      });
    }
    return child;
  });

  return {
    ...actual,
    exec: execMock,
    execFile: execFileMock,
    spawn: spawnMock,
    execSync: vi.fn().mockReturnValue(authStatus),
    execFileSync: vi.fn().mockReturnValue(issueJson),
  };
});

// A valid PRContextSchema pr-{N}.json so validateStageContextOutput passes on
// the deterministic pr-create path (mirrors what the Go runner writes, #300).
const validPrContext = JSON.stringify({
  schema_version: "1.0",
  issue_number: 300,
  pr_number: 999,
  pr_url: "https://github.com/TestOrg/test-repo/pull/999",
  title: "feat(#300): deterministic-first",
  base_branch: "main",
  status: "open",
  reviewers: [],
  preflight_results: {
    json_validation: "skipped",
    yaml_validation: "skipped",
    version_consistency: "skipped",
    security_scan: "skipped",
    coverage_check: "skipped",
    scope_drift_check: "skipped",
  },
  created_at: "2026-07-19T00:00:00Z",
});

vi.mock("fs", async () => {
  const actual = await vi.importActual<typeof import("fs")>("fs");
  return {
    ...actual,
    existsSync: vi.fn().mockReturnValue(true),
    readFileSync: vi.fn().mockImplementation((p: string) => {
      if (typeof p === "string" && p.includes("pr-")) return validPrContext;
      return "{}";
    }),
    writeFileSync: vi.fn(),
    mkdirSync: vi.fn(),
  };
});

function makeState(runningStage: "pr-create" | "pr-merge") {
  const complete = { status: "complete", auto_retry_count: 0 };
  const running = { status: "running", auto_retry_count: 0 };
  const pending = { status: "pending", auto_retry_count: 0 };
  return {
    schema_version: "1.0",
    issue_number: 300,
    stages: {
      "pipeline-start": complete,
      "issue-pickup": complete,
      "feature-planning": complete,
      "feature-dev": complete,
      "feature-validate": complete,
      "pr-create": runningStage === "pr-create" ? running : complete,
      "pr-merge": runningStage === "pr-merge" ? running : pending,
      "pipeline-finish": complete,
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

function createMockStateService(runningStage: "pr-create" | "pr-merge"): PipelineStateService {
  return {
    getState: vi.fn().mockResolvedValue(makeState(runningStage)),
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
    skipPhase: vi.fn().mockResolvedValue(undefined),
  } as unknown as PipelineStateService;
}

/** Skill mock that always reports success (used only on the punt fallthrough). */
function mockSkillSuccess() {
  vi.mocked(runStageSkillHeadless).mockImplementation((_stage, _issue, callbacks) => {
    Promise.resolve().then(() => {
      void callbacks?.onComplete?.({ success: true, exitCode: 0 } as SkillRunResult);
    });
    return { kill: vi.fn(), process: null } as any;
  });
}

const WORKTREE = "/tmp/nightgauge-worktrees/issue-300";

describe("HeadlessOrchestrator deterministic-first pr-stage (Issue #300)", () => {
  let mockLogger: Logger;

  beforeEach(() => {
    vi.clearAllMocks();
    prStageCalls.value = [];
    prStagePhaseLines.value = [];
    gatePrMergePassed.value = true;
    // Reset to the deterministic-success defaults each test.
    prStageCreate.value = {
      stage: "pr-create",
      path: "created",
      pr_number: 999,
      pr_url: "https://github.com/TestOrg/test-repo/pull/999",
      reason: "rich-context",
      rate_limited: false,
      duration_ms: 4,
    };
    prStageMerge.value = {
      stage: "pr-merge",
      path: "merged",
      pr_number: 999,
      pr_state: "MERGED",
      reason: "clean-mergeable: merged",
      rate_limited: false,
      duration_ms: 6,
    };
    mockLogger = {
      info: vi.fn(),
      warn: vi.fn(),
      error: vi.fn(),
      debug: vi.fn(),
    } as unknown as Logger;
  });

  it("pr-merge: deterministic 'merged' skips the LLM skill and passes --workdir = worktree", async () => {
    mockSkillSuccess(); // must NOT be invoked
    const state = createMockStateService("pr-merge");
    const orchestrator = new HeadlessOrchestrator(state, mockLogger, { contextFileWaitMs: 0 });
    orchestrator.setWorktreeOverride(WORKTREE);

    const runPromise = orchestrator.runPipeline(300);
    const result = await runPromise;

    // The LLM skill was never spawned for pr-merge (the ~$0 win).
    const skillStages = vi.mocked(runStageSkillHeadless).mock.calls.map((c) => c[0]);
    expect(skillStages).not.toContain("pr-merge");

    // The deterministic runner was invoked in the worktree (#288 context-locality).
    const mergeCall = prStageCalls.value.find((c) => c.verb === "merge");
    expect(mergeCall).toBeDefined();
    expect(mergeCall!.args).toContain("--workdir");
    expect(mergeCall!.args[mergeCall!.args.indexOf("--workdir") + 1]).toBe(WORKTREE);

    expect(result.success).toBe(true);
  });

  // ── Phase reporting on this route (#1397) ───────────────────────────────
  //
  // #1247 gave the deterministic runners real phase reporting, but only the Go
  // scheduler attached a reporter. This route runs the SAME runners in a
  // separate process, so it reported nothing and showed 0/14 for the whole
  // stage — two routes to one stage, one instrumented and one not, and the
  // uninstrumented one is the route an ordinary VS Code user takes.

  const phaseLine = (t: Record<string, unknown>): string =>
    "@@nightgauge-phase@@" +
    JSON.stringify({
      stage: "pr-merge",
      name: "ci-gate",
      index: 3,
      total: 14,
      status: "running",
      ...t,
    });

  it("pr-merge: streamed phase transitions are recorded as they arrive (#1397)", async () => {
    prStagePhaseLines.value = [
      phaseLine({ name: "read-pr-context", index: 0, status: "running" }),
      phaseLine({ name: "read-pr-context", index: 0, status: "complete" }),
      phaseLine({ name: "ci-gate", index: 3, status: "running" }),
      phaseLine({ name: "ci-gate", index: 3, status: "complete" }),
      phaseLine({ name: "output-summary", index: 12, status: "skipped" }),
    ];
    mockSkillSuccess();
    const state = createMockStateService("pr-merge");
    const orchestrator = new HeadlessOrchestrator(state, mockLogger, { contextFileWaitMs: 0 });
    orchestrator.setWorktreeOverride(WORKTREE);

    await orchestrator.runPipeline(300);

    // Every transition reached the state service with its registry position —
    // the tree renders "n/14" from index/total.
    expect(vi.mocked(state.startPhase).mock.calls).toEqual(
      expect.arrayContaining([
        ["pr-merge", "read-pr-context", 14, 0],
        ["pr-merge", "ci-gate", 14, 3],
      ])
    );
    expect(vi.mocked(state.completePhase).mock.calls).toEqual(
      expect.arrayContaining([
        ["pr-merge", "read-pr-context", 14],
        ["pr-merge", "ci-gate", 14],
      ])
    );
    expect(vi.mocked(state.skipPhase).mock.calls).toEqual(
      expect.arrayContaining([["pr-merge", "output-summary", 14, 12]])
    );
  });

  it("pr-merge: falls back to the result array when nothing streamed (#1397)", async () => {
    // An older binary streams nothing but still returns the array. The durable
    // record must still be written — and exactly once, which is why the array
    // is applied ONLY when the stream was silent.
    prStagePhaseLines.value = [];
    prStageMerge.value = {
      ...prStageMerge.value,
      phases: [
        { stage: "pr-merge", name: "merge", index: 9, total: 14, status: "running" },
        { stage: "pr-merge", name: "merge", index: 9, total: 14, status: "complete" },
      ],
    };
    mockSkillSuccess();
    const state = createMockStateService("pr-merge");
    const orchestrator = new HeadlessOrchestrator(state, mockLogger, { contextFileWaitMs: 0 });
    orchestrator.setWorktreeOverride(WORKTREE);

    await orchestrator.runPipeline(300);

    expect(vi.mocked(state.startPhase).mock.calls).toEqual([["pr-merge", "merge", 14, 9]]);
    expect(vi.mocked(state.completePhase).mock.calls).toEqual([["pr-merge", "merge", 14]]);
  });

  it("pr-merge: a streamed run does NOT also replay the array — no double records (#1397)", async () => {
    // The array and the stream carry the SAME transitions. Applying both would
    // record every phase twice, which is a worse record than none.
    prStagePhaseLines.value = [phaseLine({ name: "merge", index: 9, status: "running" })];
    prStageMerge.value = {
      ...prStageMerge.value,
      phases: [{ stage: "pr-merge", name: "merge", index: 9, total: 14, status: "running" }],
    };
    mockSkillSuccess();
    const state = createMockStateService("pr-merge");
    const orchestrator = new HeadlessOrchestrator(state, mockLogger, { contextFileWaitMs: 0 });
    orchestrator.setWorktreeOverride(WORKTREE);

    await orchestrator.runPipeline(300);

    expect(vi.mocked(state.startPhase).mock.calls).toEqual([["pr-merge", "merge", 14, 9]]);
  });

  it("pr-merge: a malformed phase line never fails the stage (#1397)", async () => {
    // This is a progress channel. A bad line must not take down the merge that
    // was reporting progress.
    prStagePhaseLines.value = [
      "@@nightgauge-phase@@{not json",
      "2026/09/04 gh: ordinary log output",
      phaseLine({ name: "merge", index: 9, status: "running" }),
    ];
    mockSkillSuccess();
    const state = createMockStateService("pr-merge");
    const orchestrator = new HeadlessOrchestrator(state, mockLogger, { contextFileWaitMs: 0 });
    orchestrator.setWorktreeOverride(WORKTREE);

    const result = await orchestrator.runPipeline(300);

    expect(result.success).toBe(true);
    expect(vi.mocked(state.startPhase).mock.calls).toEqual([["pr-merge", "merge", 14, 9]]);
  });

  it("pr-merge: 'punt' records the reason and falls through to the LLM skill", async () => {
    prStageMerge.value = {
      stage: "pr-merge",
      path: "punt",
      pr_number: 999,
      pr_state: "OPEN",
      reason: "dirty-merge-state: BLOCKED",
      rate_limited: false,
      duration_ms: 3,
    };
    mockSkillSuccess(); // MUST be invoked on the fallthrough
    const state = createMockStateService("pr-merge");
    const orchestrator = new HeadlessOrchestrator(state, mockLogger, { contextFileWaitMs: 0 });
    orchestrator.setWorktreeOverride(WORKTREE);

    const runPromise = orchestrator.runPipeline(300);
    const result = await runPromise;

    // Deterministic path was attempted, punted, and the LLM skill then ran.
    expect(prStageCalls.value.some((c) => c.verb === "merge")).toBe(true);
    const skillStages = vi.mocked(runStageSkillHeadless).mock.calls.map((c) => c[0]);
    expect(skillStages).toContain("pr-merge");

    // The punt reason was logged (observability preserved on the fallthrough).
    const info = vi.mocked(mockLogger.info).mock.calls;
    const puntLog = info.find(
      (args) => typeof args[0] === "string" && args[0].includes("deterministic path punted")
    );
    expect(puntLog).toBeDefined();
    expect(result.success).toBe(true);
  });

  it("pr-merge: 'rate_limited' DEFERS — the LLM skill is NOT run (#3976)", async () => {
    prStageMerge.value = {
      stage: "pr-merge",
      path: "punt",
      pr_number: 999,
      pr_state: "OPEN",
      reason: "rate-limited",
      rate_limited: true,
      duration_ms: 2,
    };
    mockSkillSuccess(); // must NOT be invoked
    const state = createMockStateService("pr-merge");
    const orchestrator = new HeadlessOrchestrator(state, mockLogger, { contextFileWaitMs: 0 });
    orchestrator.setWorktreeOverride(WORKTREE);

    const runPromise = orchestrator.runPipeline(300);
    const result = await runPromise;

    const skillStages = vi.mocked(runStageSkillHeadless).mock.calls.map((c) => c[0]);
    expect(skillStages).not.toContain("pr-merge");
    expect(result.success).toBe(false);
    // The stage failed with a transient rate-limit marker (routes to cooldown,
    // NOT a burned issue).
    expect(vi.mocked(state.failStage)).toHaveBeenCalledWith(
      "pr-merge",
      expect.stringContaining("github-quota-low")
    );
  });

  it("pr-create: deterministic 'created' skips the LLM skill and passes --repo + --workdir", async () => {
    mockSkillSuccess(); // must NOT be invoked for pr-create OR pr-merge
    const state = createMockStateService("pr-create");
    const orchestrator = new HeadlessOrchestrator(state, mockLogger, { contextFileWaitMs: 0 });
    orchestrator.setWorktreeOverride(WORKTREE);

    const runPromise = orchestrator.runPipeline(300);
    const result = await runPromise;

    // Neither pr-create nor pr-merge spawned the LLM skill (both deterministic).
    const skillStages = vi.mocked(runStageSkillHeadless).mock.calls.map((c) => c[0]);
    expect(skillStages).not.toContain("pr-create");
    expect(skillStages).not.toContain("pr-merge");

    // The create runner was invoked with the resolved repo slug and worktree.
    const createCall = prStageCalls.value.find((c) => c.verb === "create");
    expect(createCall).toBeDefined();
    expect(createCall!.args).toContain("--repo");
    expect(createCall!.args[createCall!.args.indexOf("--repo") + 1]).toBe("TestOrg/test-repo");
    expect(createCall!.args[createCall!.args.indexOf("--workdir") + 1]).toBe(WORKTREE);

    expect(result.success).toBe(true);
  });

  // #309: the observability regression. The per-stage execution-path decision the
  // deterministic-first hook records must reach pipeline.notifyComplete, which the
  // Go handler replays onto the authoritative history stage record. Assert the
  // wire payload directly (firePipelineComplete → notifyPipelineComplete): a
  // deterministic pr-merge threads execution_path="deterministic", no punt reason.
  it("pr-merge: deterministic path threads execution_path='deterministic' into notifyComplete (#309)", async () => {
    mockSkillSuccess();
    const state = createMockStateService("pr-merge");
    const orchestrator = new HeadlessOrchestrator(state, mockLogger, { contextFileWaitMs: 0 });
    orchestrator.setWorktreeOverride(WORKTREE);

    const runPromise = orchestrator.runPipeline(300);
    await runPromise;

    expect(vi.mocked(state.notifyPipelineComplete)).toHaveBeenCalledTimes(1);
    const payload = vi.mocked(state.notifyPipelineComplete).mock.calls[0][0];
    expect(payload.stageExecutionPaths?.["pr-merge"]).toBe("deterministic");
    // A deterministic success never records a punt reason.
    expect(payload.stagePuntReasons?.["pr-merge"]).toBeUndefined();
  });

  // #309: a punted pr-create must thread execution_path="llm" + the punt reason,
  // so history answers WHY the expensive LLM path ran — the exact platform#209
  // blind spot this issue is about.
  it("pr-create: punt threads execution_path='llm' + punt_reason into notifyComplete (#309)", async () => {
    prStageCreate.value = {
      stage: "pr-create",
      path: "punt",
      pr_number: 0,
      reason: "missing-validate-context",
      rate_limited: false,
      duration_ms: 3,
    };
    mockSkillSuccess(); // the LLM skill runs on the fallthrough
    const state = createMockStateService("pr-create");
    const orchestrator = new HeadlessOrchestrator(state, mockLogger, { contextFileWaitMs: 0 });
    orchestrator.setWorktreeOverride(WORKTREE);

    const runPromise = orchestrator.runPipeline(300);
    await runPromise;

    expect(vi.mocked(state.notifyPipelineComplete)).toHaveBeenCalledTimes(1);
    const payload = vi.mocked(state.notifyPipelineComplete).mock.calls[0][0];
    expect(payload.stageExecutionPaths?.["pr-create"]).toBe("llm");
    expect(payload.stagePuntReasons?.["pr-create"]).toBe("missing-validate-context");
    // pr-merge still ran deterministically after the pr-create fallthrough.
    expect(payload.stageExecutionPaths?.["pr-merge"]).toBe("deterministic");
  });
});
