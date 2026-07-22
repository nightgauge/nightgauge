/**
 * HeadlessOrchestrator.githubNetworkOutage.test.ts
 *
 * Verifies the transient network-outage deferral path (#4002): `gh auth
 * status` exits non-zero BOTH when not authenticated AND when api.github.com
 * is unreachable. When the failure carries a connectivity signature (gh's own
 * "error connecting to api.github.com" diagnostic), runPipeline() must NOT
 * take the permanent github-auth-failed path ("Run `gh auth login`"). It must
 * instead:
 *   - emit a `[pipeline-start-failure] github-network-outage` marker on the
 *     per-issue stderr stream, and
 *   - return an error whose text carries the `[github-network-outage]` token
 *     so the Go ClassifyTerminalKind fallback routes it to the transient
 *     (short global cooldown, issue-stays-Ready, no lifetime-cap increment)
 *     recovery path.
 *
 * Guard: a REAL auth failure (non-zero exit with no connectivity signature)
 * must still take the github-auth-failed path — never silently retried.
 */

import { describe, it, expect, beforeEach, vi } from "vitest";
import { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";
import type { PipelineStateService } from "../../src/services/PipelineStateService";
import type { Logger } from "../../src/utils/logger";

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

// Controls what the promisified `exec` (used for `gh auth status 2>&1`) does.
// Each test sets this to simulate a connectivity failure vs a real auth
// failure. execFile (issue pre-check) always resolves an OPEN issue so we
// reach the auth pre-check.
let execBehavior: () => Promise<{ stdout: string; stderr: string }>;

vi.mock("child_process", async () => {
  const actual = await vi.importActual<typeof import("child_process")>("child_process");
  const kCustom = Symbol.for("nodejs.util.promisify.custom");

  const issueJson = '{"labels":[],"state":"OPEN","title":"Active issue #79"}';

  const execMock: any = vi.fn();
  execMock[kCustom] = () => execBehavior();

  const execFileMock: any = vi.fn();
  execFileMock[kCustom] = () => Promise.resolve({ stdout: issueJson, stderr: "" });

  return {
    ...actual,
    exec: execMock,
    execFile: execFileMock,
    execSync: vi.fn().mockReturnValue(""),
    execFileSync: vi.fn().mockReturnValue(issueJson),
  };
});

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      githubRateLimit: vi.fn().mockResolvedValue({
        remaining: 5000,
        limit: 5000,
        resetAt: Math.floor(Date.now() / 1000) + 3600,
      }),
    }),
  },
}));

function makePipelineStateMock(): PipelineStateService {
  return {
    getState: vi.fn().mockResolvedValue({
      schema_version: "1.0",
      issue_number: 79,
      stages: {},
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

describe("HeadlessOrchestrator GitHub network-outage deferral (#4002)", () => {
  let mockLogger: Logger;

  beforeEach(() => {
    vi.clearAllMocks();
    mockLogger = {
      info: vi.fn(),
      warn: vi.fn(),
      error: vi.fn(),
      debug: vi.fn(),
    } as unknown as Logger;
  });

  it("emits github-network-outage marker and a transient-classifiable error (not github-auth-failed)", async () => {
    // gh's exact diagnostic when DNS / connectivity is down (2>&1 redirect
    // lands it on the error's stdout).
    execBehavior = () =>
      Promise.reject(
        Object.assign(new Error("Command failed: gh auth status 2>&1"), {
          stdout:
            "github.com\n  X error connecting to api.github.com\n" +
            "  check your internet connection or https://githubstatus.com\n",
          stderr: "",
        })
      );

    const orchestrator = new HeadlessOrchestrator(makePipelineStateMock(), mockLogger, {
      contextFileWaitMs: 0,
    });
    const onStderr = vi.fn();

    const result = await orchestrator.runPipeline(79, { onStderr });

    expect(result.success).toBe(false);
    expect(result.failedStage).toBe("pipeline-start");

    // The error text must carry the [github-network-outage] token so the Go
    // ClassifyTerminalKind fallback routes it transient.
    expect(result.error?.message).toContain("[github-network-outage]");
    // And must NOT page the operator to re-authenticate.
    expect(result.error?.message).not.toContain("gh auth login");

    const calls = onStderr.mock.calls.filter(([stage]) => stage === "pipeline-start");
    const markerCall = calls.find(
      ([, data]) =>
        typeof data === "string" && data.includes("[pipeline-start-failure] github-network-outage")
    );
    expect(markerCall).toBeDefined();
    expect(markerCall![1]).toContain("retryInSec=");

    const authFailed = calls.find(
      ([, data]) => typeof data === "string" && data.includes("github-auth-failed")
    );
    expect(authFailed).toBeUndefined();
  });

  it("still takes the github-auth-failed path for a real auth failure (no connectivity signature)", async () => {
    execBehavior = () =>
      Promise.reject(
        Object.assign(new Error("Command failed: gh auth status 2>&1"), {
          stdout: "You are not logged into any GitHub hosts.\n",
          stderr: "",
        })
      );

    const orchestrator = new HeadlessOrchestrator(makePipelineStateMock(), mockLogger, {
      contextFileWaitMs: 0,
    });
    const onStderr = vi.fn();

    const result = await orchestrator.runPipeline(79, { onStderr });

    expect(result.success).toBe(false);
    expect(result.failedStage).toBe("pipeline-start");
    expect(result.error?.message).toContain("gh auth login");
    expect(result.error?.message).not.toContain("[github-network-outage]");
  });
});
