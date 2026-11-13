/**
 * HeadlessOrchestrator.acReconcileRegen.test.ts
 *
 * Issue #3230 — drives the extension pipeline loop (runPipeline) through the
 * REAL acReconcileRegen module, so the pre-planning ac-reconcile rebuild (#3228)
 * is proven at loop level: the binary verbs run before feature-planning
 * dispatches, and a failing `preflight ac-reconcile` fails the stage without
 * dispatching it. Harness copied from HeadlessOrchestrator.contextHandoff.test.ts.
 */

import { describe, it, expect, beforeEach, vi } from "vitest";

// tests/setup.ts stubs this module for every other suite; that stub stays.
// This suite needs the real module so the loop runs the binary verbs.
vi.unmock("../../src/services/acReconcileRegen");

const h = vi.hoisted(() => ({
  calls: [] as string[][],
  order: [] as string[],
  failPreflight: false,
}));

// Without a resolved binary the real module fails with "binary not found",
// which would prove nothing about the verbs.
vi.mock("../../src/services/BinaryResolver", () => ({
  BinaryResolver: { fromVSCode: () => ({ resolve: async () => "/fake/nightgauge" }) },
}));

import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { PipelineStateService } from "../../src/services/PipelineStateService";
import type { Logger } from "../../src/utils/logger";
import type { SkillRunResult } from "../../src/utils/skillRunner";
import { runStageSkillHeadless } from "../../src/utils/skillRunner";
import { existsSync, readFileSync } from "fs";

// Mock skillRunner (imported by HeadlessOrchestrator)
vi.mock("../../src/utils/skillRunner", () => ({
  hasActiveProcess: vi.fn().mockReturnValue(false),
  killAllActiveProcesses: vi.fn(),
  getActiveInteractiveProcess: vi.fn().mockReturnValue(null),
  runStageSkillHeadless: vi.fn(),
  getNextStage: vi.fn(),
  getStageLabel: vi.fn((stage: string) => stage),
  resolveModel: vi.fn().mockReturnValue({ model: "claude-sonnet-4-6", source: "default" }),
}));

// Mock fs — control existsSync and readFileSync per test
vi.mock("fs", async () => {
  const actual = await vi.importActual<typeof import("fs")>("fs");
  return {
    ...actual,
    existsSync: vi.fn().mockReturnValue(true),
    readFileSync: vi.fn().mockReturnValue("{}"),
  };
});

vi.mock("child_process", async () => {
  const actual = await vi.importActual<typeof import("child_process")>("child_process");
  const kCustom = Symbol.for("nodejs.util.promisify.custom");
  const authStatus =
    "Logged in to github.com account testuser (keyring)\n" +
    "  Token: gho_fake\n  Token scopes: 'gist', 'read:org', 'repo', 'workflow'";
  const issueJson = '{"labels":[],"state":"OPEN","title":"Test issue #42"}';

  const execMock: any = vi.fn();
  execMock[kCustom] = () => Promise.resolve({ stdout: authStatus, stderr: "" });

  const execFileMock: any = vi.fn();
  execFileMock[kCustom] = (_cmd: string, args: string[] = []) => {
    h.calls.push(args);
    if (args[0] === "forge" && args.includes("--vouched")) {
      h.order.push("forge-vouched");
      return Promise.resolve({ stdout: '{"body":"- [ ] A\\n"}', stderr: "" });
    }
    if (args[0] === "preflight" && args[1] === "ac-reconcile") {
      h.order.push("preflight-ac-reconcile");
      if (h.failPreflight) {
        return Promise.reject(
          new Error("preflight ac-reconcile: exit status 1: issue body unreadable")
        );
      }
      return Promise.resolve({ stdout: "", stderr: "" });
    }
    if (args[0] === "gate" && args[1] === "verify") {
      return Promise.resolve({
        stdout: JSON.stringify({ passed: true, reason: "ok", gate_name: args[2] }),
        stderr: "",
      });
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
 * State with all stages pre-completed except feature-planning.
 * The pipeline loop will skip all pre-completed stages and only
 * run feature-planning.
 */
function makeStateWithoutFeaturePlanning() {
  return {
    schema_version: "1.0",
    issue_number: 42,
    stages: {
      "pipeline-start": { status: "complete", auto_retry_count: 0 },
      "issue-pickup": { status: "complete", auto_retry_count: 0 },
      // feature-planning intentionally absent — will be run
      // feature-dev is "skipped" so earlyExitHandled=true, which prevents
      // reconcileCompletionSideEffects from running (it calls live gh CLI).
      "feature-dev": { status: "skipped", auto_retry_count: 0 },
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

function createMockStateService(): PipelineStateService {
  return {
    getState: vi.fn().mockResolvedValue(makeStateWithoutFeaturePlanning()),
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
    notifyPipelineComplete: vi.fn().mockResolvedValue(undefined),
    finalizeRun: vi.fn(),
  } as unknown as PipelineStateService;
}

describe("HeadlessOrchestrator pre-planning ac-reconcile regeneration (#3230)", () => {
  let mockLogger: Logger;

  beforeEach(() => {
    vi.clearAllMocks();
    h.calls.length = 0;
    h.order.length = 0;
    h.failPreflight = false;
    mockLogger = {
      info: vi.fn(),
      warn: vi.fn(),
      error: vi.fn(),
      debug: vi.fn(),
    } as unknown as Logger;
    vi.mocked(existsSync).mockReturnValue(true);
    vi.mocked(readFileSync).mockReturnValue("{}");
    vi.spyOn(
      HeadlessOrchestrator.prototype as unknown as {
        forgePrState(n: number): Promise<string | null>;
      },
      "forgePrState"
    ).mockResolvedValue("OPEN");
    vi.spyOn(
      HeadlessOrchestrator.prototype as unknown as { readMergedPrNumber(n: number): number },
      "readMergedPrNumber"
    ).mockReturnValue(999);
    vi.mocked(runStageSkillHeadless).mockImplementation((stage, _issueNumber, callbacks) => {
      h.order.push(`dispatch:${stage}`);
      Promise.resolve().then(() => {
        void callbacks?.onComplete?.({ success: true, exitCode: 0 } as SkillRunResult);
      });
      return { kill: vi.fn(), process: null } as any;
    });
  });

  it("rebuilds the report with the binary before dispatching feature-planning", async () => {
    const orchestrator = new HeadlessOrchestrator(createMockStateService(), mockLogger, {
      contextFileWaitMs: 0,
    });

    await orchestrator.runPipeline(42);

    const forge = h.calls.find((a) => a[0] === "forge" && a.includes("--vouched"));
    expect(forge?.slice(0, 4)).toEqual(["forge", "issue", "view", "42"]);
    const reconcile = h.calls.find((a) => a[0] === "preflight" && a[1] === "ac-reconcile");
    expect(reconcile?.[2]).toBe("42");
    expect(reconcile).toContain("--notify-empty");
    expect(reconcile?.[reconcile.indexOf("--out") + 1]).toMatch(/ac-reconcile-42\.json$/);

    expect(
      vi.mocked(runStageSkillHeadless).mock.calls.some((c) => c[0] === "feature-planning")
    ).toBe(true);
    const iForge = h.order.indexOf("forge-vouched");
    const iReconcile = h.order.indexOf("preflight-ac-reconcile");
    const iDispatch = h.order.indexOf("dispatch:feature-planning");
    expect(iForge).toBeGreaterThanOrEqual(0);
    expect(iForge).toBeLessThan(iReconcile);
    expect(iReconcile).toBeLessThan(iDispatch);
  });

  it("fails feature-planning without dispatching it when the reconcile exits non-zero", async () => {
    h.failPreflight = true;
    const state = createMockStateService();
    const orchestrator = new HeadlessOrchestrator(state, mockLogger, { contextFileWaitMs: 0 });

    const result = await orchestrator.runPipeline(42);

    expect(h.order).toContain("preflight-ac-reconcile");
    expect(
      vi.mocked(runStageSkillHeadless).mock.calls.some((c) => c[0] === "feature-planning")
    ).toBe(false);
    expect(result.success).toBe(false);
    expect(result.failedStage).toBe("feature-planning");
    expect(result.error?.message).toContain("ac-reconcile regeneration failed");
    expect(result.error?.message).toContain("issue body unreadable");
    // The loop's break records nothing through failStage; the run's terminal
    // funnel carries the reason to Go as failureDetail (#1329).
    expect(state.failStage).not.toHaveBeenCalledWith("feature-planning", expect.anything());
    expect(state.notifyPipelineComplete).toHaveBeenCalledWith(
      expect.objectContaining({
        success: false,
        failureDetail: expect.stringContaining("issue body unreadable"),
      })
    );
  });
});
