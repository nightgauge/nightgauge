/**
 * HeadlessOrchestrator.epicSweep.test.ts
 *
 * Tests for the epic completion sweep guard added in Issue #2872.
 *
 * When rate-limited, `nightgauge epic check-completion --sweep --json`
 * returns {"skipped":true,...} instead of an array. Without the Array.isArray
 * guard this throws `TypeError: results.filter is not a function`.
 *
 * Test suite structure:
 *   - Integration tests: verify no-throw through the real orchestrator
 *   - Guard unit tests: verify Array.isArray logic covers all three response shapes
 *
 * @see Issue #2872 - fix(epic-completion): TypeError: results.filter is not a function
 */

import { describe, it, expect, beforeEach, vi } from "vitest";
import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { PipelineStateService } from "../../src/services/PipelineStateService";
import type { Logger } from "../../src/utils/logger";
import type { SkillRunResult } from "../../src/utils/skillRunner";
import { runStageSkillHeadless } from "../../src/utils/skillRunner";

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
  BinaryResolver: {
    fromVSCode: vi.fn().mockReturnValue({
      resolve: vi.fn().mockResolvedValue("/usr/local/bin/nightgauge"),
    }),
  },
}));

vi.mock("fs", async () => {
  const actual = await vi.importActual<typeof import("fs")>("fs");
  return {
    ...actual,
    existsSync: vi.fn().mockReturnValue(false),
    readFileSync: vi.fn().mockReturnValue("{}"),
  };
});

const { execFileSyncMock } = vi.hoisted(() => ({
  execFileSyncMock: vi.fn(),
}));

// #2884: HeadlessOrchestrator now uses promisify(exec)/promisify(execFile)
// for hot-path subprocess calls. Mock both sync (for cold paths still using
// it) and async (via nodejs.util.promisify.custom). The async execFile mock
// delegates to the same execFileSyncMock so existing per-test expectations
// keep working without per-test rewiring.
vi.mock("child_process", async () => {
  const actual = await vi.importActual<typeof import("child_process")>("child_process");
  const kCustom = Symbol.for("nodejs.util.promisify.custom");

  const authStatus =
    "Logged in to github.com account testuser (keyring)\n" +
    "  Token: gho_fake\n  Token scopes: 'gist', 'read:org', 'repo', 'workflow'";

  const execMock: any = vi.fn();
  execMock[kCustom] = () => Promise.resolve({ stdout: authStatus, stderr: "" });

  const execFileMock: any = vi.fn();
  execFileMock[kCustom] = (cmd: string, args: string[], opts?: unknown) => {
    try {
      const stdout = execFileSyncMock(cmd, args, opts) ?? "";
      return Promise.resolve({ stdout: String(stdout), stderr: "" });
    } catch (err) {
      return Promise.reject(err);
    }
  };

  return {
    ...actual,
    exec: execMock,
    execFile: execFileMock,
    execSync: vi.fn().mockReturnValue(authStatus),
    execFileSync: execFileSyncMock,
  };
});

function makeStateWithPrMergePending() {
  return {
    schema_version: "1.0",
    issue_number: 42,
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

function makeAllCompleteState() {
  return {
    ...makeStateWithPrMergePending(),
    stages: {
      ...makeStateWithPrMergePending().stages,
      "pr-merge": { status: "complete", auto_retry_count: 0 },
    },
  };
}

function createMockStateService(): PipelineStateService {
  const getStateMock = vi.fn();
  getStateMock
    .mockResolvedValueOnce(makeStateWithPrMergePending())
    .mockResolvedValue(makeAllCompleteState());
  return {
    getState: getStateMock,
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

describe("HeadlessOrchestrator epic sweep — integration (Issue #2872)", () => {
  let mockLogger: Logger;

  beforeEach(() => {
    vi.clearAllMocks();
    mockLogger = {
      info: vi.fn(),
      warn: vi.fn(),
      error: vi.fn(),
      debug: vi.fn(),
    } as unknown as Logger;

    // pr-merge completes successfully so reconcileCompletionSideEffects runs
    vi.mocked(runStageSkillHeadless).mockImplementation((_stage, _issueNumber, callbacks) => {
      Promise.resolve().then(() => {
        void callbacks?.onComplete?.({ success: true, exitCode: 0 } as SkillRunResult);
      });
      return { kill: vi.fn(), process: null } as any;
    });

    // Default execFileSync: handle all gh/git calls gracefully
    execFileSyncMock.mockImplementation((_bin: string, args: string[]) => {
      const argsStr = Array.isArray(args) ? args.join(" ") : "";
      if (argsStr.includes("check-completion") && argsStr.includes("--sweep")) {
        return JSON.stringify({ skipped: true, reason: "rate_limit_low" });
      }
      if (argsStr.includes("auth") && argsStr.includes("token")) return "gho_fake_token";
      if (argsStr.includes("issue") && argsStr.includes("view") && argsStr.includes(".state")) {
        return "CLOSED";
      }
      if (argsStr.includes("pr") && argsStr.includes("list")) return "99";
      if (argsStr.includes("repo") && argsStr.includes("view")) return "testowner/testrepo";
      if (argsStr.includes("graphql") || argsStr.includes("api")) {
        return JSON.stringify({
          data: { repository: { issue: { subIssues: { nodes: [] } } } },
        });
      }
      return JSON.stringify({ labels: [], state: "OPEN", title: "Test issue #42" });
    });
  });

  it("does not throw TypeError when binary returns rate-limited object", async () => {
    // Default mock already returns {skipped:true,...} for the sweep call
    const mockState = createMockStateService();
    const orchestrator = new HeadlessOrchestrator(mockState, mockLogger, {
      contextFileWaitMs: 0,
    });

    let threw = false;
    try {
      await orchestrator.runPipeline(42);
    } catch {
      threw = true;
    }

    expect(threw).toBe(false);

    // Verify no TypeError about filter was logged
    const warnCalls = vi.mocked(mockLogger.warn).mock.calls;
    const filterTypeError = warnCalls.find(
      (call) =>
        typeof call[1] === "object" &&
        call[1] !== null &&
        String((call[1] as Record<string, unknown>).err).includes("filter is not a function")
    );
    expect(filterTypeError).toBeUndefined();
  });
});

// ============================================================
// Guard unit tests — directly exercise the Array.isArray fix
// ============================================================

describe("epic sweep Array.isArray guard (Issue #2872)", () => {
  /**
   * The exact guard logic extracted from HeadlessOrchestrator.ts:
   *   const parsed = JSON.parse(output);
   *   const results = Array.isArray(parsed) ? (parsed as Array<{complete?:boolean}>) : [];
   *   epicSweepClosed = results.filter((r) => r.complete).length;
   */
  function applyGuard(output: string): number {
    const parsed = JSON.parse(output);
    const results = Array.isArray(parsed) ? (parsed as Array<{ complete?: boolean }>) : [];
    return results.filter((r) => r.complete).length;
  }

  it("returns 0 and does not throw when output is a rate-limited object", () => {
    const output = JSON.stringify({ skipped: true, reason: "rate_limit_low" });
    expect(() => applyGuard(output)).not.toThrow();
    expect(applyGuard(output)).toBe(0);
  });

  it("returns 0 when output is an empty array", () => {
    expect(applyGuard("[]")).toBe(0);
  });

  it("counts only complete:true entries in a normal array response", () => {
    const output = JSON.stringify([
      { epicNumber: 100, complete: true },
      { epicNumber: 101, complete: false },
      { epicNumber: 102, complete: true },
    ]);
    expect(applyGuard(output)).toBe(2);
  });
});
