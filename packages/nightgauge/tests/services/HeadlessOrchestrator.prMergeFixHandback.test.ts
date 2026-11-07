/**
 * HeadlessOrchestrator.prMergeFixHandback.test.ts
 *
 * The pr-merge fix hand-back on the extension path (#2833, mirroring the Go
 * scheduler's #2504): a skill that pushed a CI fix writes
 * `pr-merge-fix-{N}.json` and exits 0; the orchestrator must consume it and
 * re-enter the deterministic runner instead of reading the exit-0 session as
 * a merge. Harness copied from HeadlessOrchestrator.deterministicPrStage.test.ts.
 */

import { describe, it, expect, beforeEach, vi } from "vitest";
import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { PipelineStateService } from "../../src/services/PipelineStateService";
import type { Logger } from "../../src/utils/logger";
import type { SkillRunResult } from "../../src/utils/skillRunner";
import { runStageSkillHeadless } from "../../src/utils/skillRunner";
import { fakeCloneLayout } from "../helpers/cloneLayout";
import {
  clearStalePrMergeFixMarker,
  consumePrMergeFixMarker,
} from "../../src/services/prMergeFixMarker";

const { prCiConfig, markerQueue } = vi.hoisted(() => ({
  prCiConfig: { value: { autoFixMaxAttempts: 2 } as Record<string, unknown> },
  // What each consume of the marker yields, in order; absent → null.
  markerQueue: { value: [] as Array<Record<string, unknown> | null> },
}));

// The marker's file handling is unit-tested in prMergeFixMarker.test.ts; this
// harness mocks fs wholesale, so the module is driven from a queue here.
vi.mock("../../src/services/prMergeFixMarker", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../src/services/prMergeFixMarker")>()),
  clearStalePrMergeFixMarker: vi.fn().mockResolvedValue(undefined),
  consumePrMergeFixMarker: vi.fn(async () => markerQueue.value.shift() ?? null),
}));

// Skip the live-adapter auth preflight (no CLI auth in the test env).
vi.mock("../../src/utils/nightgaugeConfig", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../src/utils/nightgaugeConfig")>()),
  getSkipAuthPreflight: () => true,
  getPrCICheckConfig: () => ({ ...prCiConfig.value }),
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
  BinaryResolver: { fromVSCode: () => ({ resolve: async () => binaryPath.value }) },
}));

// Mutable state shared between the hoisted mock factory and the tests.
const {
  prStageCalls,
  prStageCreate,
  prStageMerge,
  gatePrMergePassed,
  prStagePhaseLines,
  gitHeadLeaked,
  prStageBudget,
  prStageRecover,
  prStageMergeQueue,
  prForgeState,
  binaryPath,
} = vi.hoisted(() => ({
  // #2878: the forge's answer to the resume's PR-state check.
  prForgeState: { value: "OPEN" },
  binaryPath: { value: "/fake/nightgauge" as string | null },
  // #2841: what `pr-stage recover` answers, one entry per call; a null entry
  // makes the verb fail.
  prStageRecover: { value: [] as Array<Record<string, unknown> | null> },
  // #2841: per-call pr-stage merge answers, ahead of prStageMerge.
  prStageMergeQueue: { value: [] as Array<Record<string, unknown>> },
  // #2730: what `pr-stage budget` answers; null makes the verb fail.
  prStageBudget: {
    value: {
      queue_wait_ms: 5_400_000,
      cli_timeout_ms: 6_000_000,
      client_timeout_ms: 6_060_000,
      recovery_client_timeout_ms: 7_000_000,
    } as Record<string, number> | null,
  },
  // Issue 1675: when true, the branch's published head commits AGENTS.md
  // with the managed steering block (the fake `git` below serves it).
  gitHeadLeaked: { value: false },
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

    if (cmd === "git" && gitHeadLeaked.value) {
      const g = a[0] === "-C" ? a.slice(2) : a;
      const ok = (stdout: string) => Promise.resolve({ stdout, stderr: "" });
      if (g[0] === "symbolic-ref") return ok("fix/300-x\n");
      if (g[0] === "config" && g[2]?.endsWith(".remote")) return ok("origin\n");
      if (g[0] === "config" && g[2]?.endsWith(".merge")) return ok("refs/heads/fix/300-x\n");
      if (g[0] === "fetch") return ok("");
      if (g[0] === "rev-parse") return ok("abc123def4567890\n");
      if (g[0] === "ls-tree") return ok("AGENTS.md\0README.md\0");
      if (g[0] === "show") {
        return ok(
          "# Rules\n\n<!-- BEGIN NIGHTGAUGE MANAGED STEERING -->\ngenerated\n<!-- END NIGHTGAUGE MANAGED STEERING -->\n"
        );
      }
      // merge-base / log / push: nothing publishable from this fake clone.
      return Promise.reject(new Error(`git ${g.join(" ")}: not in this fixture`));
    }

    if (isBinary && a[0] === "pr-stage" && a[1] === "budget") {
      if (prStageBudget.value === null) return Promise.reject(new Error("no budget verb"));
      return Promise.resolve({ stdout: JSON.stringify(prStageBudget.value), stderr: "" });
    }

    if (isBinary && a[0] === "pr-stage" && a[1] === "recover") {
      prStageCalls.value.push({ verb: "recover", args: a });
      const answer = prStageRecover.value.shift();
      if (!answer) return Promise.reject(new Error("recover verb failed"));
      return Promise.resolve({ stdout: JSON.stringify(answer), stderr: "" });
    }

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

    // #2841: a rewind replays feature-dev and feature-validate; #2878: a retired
    // run replays everything from issue-pickup.
    if (
      isBinary &&
      a[0] === "gate" &&
      ["issue-pickup", "feature-planning", "feature-dev", "feature-validate"].includes(a[2])
    ) {
      return Promise.resolve({
        stdout: JSON.stringify({
          stage: a[2],
          gate_name: a[2],
          passed: true,
          reason: "ok",
          evidence: [],
        }),
        stderr: "",
      });
    }

    // #2878: the hook withholds Ready on a no-op that left the issue open.
    if (isBinary && a[0] === "hook") {
      return Promise.resolve({
        stdout: JSON.stringify({ readyWithheld: a.includes("--already-merged") }),
        stderr: "",
      });
    }

    if (isBinary && a[0] === "epic") {
      return Promise.resolve({ stdout: "{}", stderr: "" });
    }

    if (a[0] === "pr") {
      // The resume's forge check (#2878).
      if (a.includes("-q") && a.includes(".state")) {
        return Promise.resolve({ stdout: prForgeState.value, stderr: "" });
      }
      // Pre-merge branch-behind guard reads mergeStateStatus; CLEAN → proceed.
      if (a.includes("-q") && a.includes(".mergeStateStatus")) {
        return Promise.resolve({ stdout: "CLEAN", stderr: "" });
      }
      return Promise.resolve({
        stdout: JSON.stringify({ state: "MERGED", statusCheckRollup: [] }),
        stderr: "",
      });
    }

    // resolvePostMergeNwo
    if (a[0] === "repo" && a.includes('.owner.login + "/" + .name')) {
      return Promise.resolve({ stdout: "TestOrg/test-repo", stderr: "" });
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
      const payload =
        verb === "create"
          ? prStageCreate.value
          : (prStageMergeQueue.value.shift() ?? prStageMerge.value);
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

const failedChecks = {
  stage: "pr-merge",
  path: "punt",
  pr_number: 999,
  pr_state: "OPEN",
  reason: "failed-ci-checks: lint",
  rate_limited: false,
  duration_ms: 3,
};
const merged = {
  stage: "pr-merge",
  path: "merged",
  pr_number: 999,
  pr_state: "MERGED",
  reason: "clean-mergeable: merged",
  rate_limited: false,
  duration_ms: 6,
};
const fixPushed = { result: "fix-pushed", pr_number: 999, head_sha: "abc123", attempt: 1 };

describe("HeadlessOrchestrator pr-merge fix hand-back (#2833)", () => {
  let mockLogger: Logger;

  beforeEach(() => {
    vi.clearAllMocks();
    fakeCloneLayout(WORKTREE);
    prStageCalls.value = [];
    prStageRecover.value = [];
    prStageMergeQueue.value = [];
    prStagePhaseLines.value = [];
    gatePrMergePassed.value = true;
    prForgeState.value = "OPEN";
    binaryPath.value = "/fake/nightgauge";
    prCiConfig.value = { autoFixMaxAttempts: 2 };
    markerQueue.value = [];
    prStageMerge.value = merged;
    mockLogger = {
      info: vi.fn((m) => process.env.DBG && console.log("I", m)),
      warn: vi.fn((m) => process.env.DBG && console.log("W", m)),
      error: vi.fn((m) => process.env.DBG && console.log("E", m)),
      debug: vi.fn(),
    } as unknown as Logger;
    mockSkillSuccess();
  });

  async function run() {
    const state = createMockStateService("pr-merge");
    const orchestrator = new HeadlessOrchestrator(state, mockLogger, { contextFileWaitMs: 0 });
    orchestrator.setWorktreeOverride(WORKTREE);
    const result = await orchestrator.runPipeline(300);
    const skillRuns = vi
      .mocked(runStageSkillHeadless)
      .mock.calls.filter((c) => c[0] === "pr-merge").length;
    const mergeCalls = prStageCalls.value.filter((c) => c.verb === "merge").length;
    return { result, state, skillRuns, mergeCalls };
  }

  it("a pushed fix re-enters the runner, which merges", async () => {
    prStageMergeQueue.value = [failedChecks, merged];
    markerQueue.value = [fixPushed];
    const { result, skillRuns, mergeCalls } = await run();
    expect(skillRuns).toBe(1);
    expect(mergeCalls).toBe(2);
    expect(result.success).toBe(true);
    // The stale marker is cleared before the session, at the clone's state dir.
    expect(vi.mocked(clearStalePrMergeFixMarker)).toHaveBeenCalledWith(
      expect.stringContaining("/.git/nightgauge/pipeline/pr-merge-fix-300.json")
    );
    expect(vi.mocked(consumePrMergeFixMarker).mock.calls[0][0]).toBe(
      vi.mocked(clearStalePrMergeFixMarker).mock.calls[0][0]
    );
  });

  it("no usable marker: the skill result stands and the runner is not re-entered", async () => {
    prStageMergeQueue.value = [failedChecks];
    const { result, skillRuns, mergeCalls } = await run();
    expect(skillRuns).toBe(1);
    expect(mergeCalls).toBe(1);
    expect(result.success).toBe(true);
  });

  it("failed checks again re-runs the skill, then fails auto-fix-exhausted", async () => {
    prStageMergeQueue.value = [failedChecks, failedChecks, failedChecks];
    markerQueue.value = [fixPushed, fixPushed];
    const { result, state, skillRuns, mergeCalls } = await run();
    expect(skillRuns).toBe(2);
    expect(mergeCalls).toBe(3);
    expect(result.success).toBe(false);
    expect(vi.mocked(state.failStage)).toHaveBeenCalledWith(
      "pr-merge",
      expect.stringContaining("auto-fix-exhausted: failed-ci-checks: lint (after 2 of 2")
    );
  });

  it("a punt that is not failed checks fails at once", async () => {
    prStageMergeQueue.value = [
      failedChecks,
      { ...failedChecks, reason: "dirty-merge-state: BLOCKED" },
    ];
    markerQueue.value = [fixPushed];
    const { result, state, skillRuns } = await run();
    expect(skillRuns).toBe(1);
    expect(result.success).toBe(false);
    expect(vi.mocked(state.failStage)).toHaveBeenCalledWith(
      "pr-merge",
      expect.stringContaining("auto-fix-exhausted: dirty-merge-state: BLOCKED (after 1 of 2")
    );
  });

  it("a rate limit after the fix defers without another session", async () => {
    prStageMergeQueue.value = [
      failedChecks,
      { ...failedChecks, reason: "rate-limited", rate_limited: true },
    ];
    markerQueue.value = [fixPushed];
    const { result, state, skillRuns } = await run();
    expect(skillRuns).toBe(1);
    expect(result.success).toBe(false);
    expect(vi.mocked(state.failStage)).toHaveBeenCalledWith(
      "pr-merge",
      expect.stringContaining("github-quota-low")
    );
  });
});
