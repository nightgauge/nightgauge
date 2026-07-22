/**
 * HeadlessOrchestrator.githubQuotaLow.test.ts
 *
 * Verifies the transient GitHub-quota deferral path (#3896): when the
 * pipeline-start preflight finds the GitHub API rate-limit bucket below
 * headroom, runPipeline() must NOT treat it like a permanent auth/scope
 * failure. It must instead:
 *   - emit a `[pipeline-start-failure] github-quota-low` marker on the
 *     per-issue stderr stream, and
 *   - return an error whose text carries the `[github-quota-low]` token so the
 *     Go ClassifyTerminalKind fallback routes it to the transient
 *     (issue-stays-Ready, no lifetime-cap increment) recovery path.
 *
 * Contrast: the legacy auth-failed path emits `github-auth-failed` and burns
 * the issue. A 1-minute quota dip must never do that.
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

// Auth status: logged in WITH repo scope, so the preflight passes auth+scope
// and reaches the rate-limit headroom check. The issue is OPEN so we don't
// short-circuit on the issue-closed path.
vi.mock("child_process", async () => {
  const actual = await vi.importActual<typeof import("child_process")>("child_process");
  const kCustom = Symbol.for("nodejs.util.promisify.custom");

  const authStatus =
    "Logged in to github.com account testuser (keyring)\n" +
    "  Token: gho_fake\n  Token scopes: 'gist', 'read:org', 'repo', 'workflow'";
  const issueJson = '{"labels":[],"state":"OPEN","title":"Active issue #77"}';

  const execMock: any = vi.fn();
  execMock[kCustom] = () => Promise.resolve({ stdout: authStatus, stderr: "" });

  const execFileMock: any = vi.fn();
  execFileMock[kCustom] = () => Promise.resolve({ stdout: issueJson, stderr: "" });

  return {
    ...actual,
    exec: execMock,
    execFile: execFileMock,
    execSync: vi.fn().mockReturnValue(authStatus),
    execFileSync: vi.fn().mockReturnValue(issueJson),
  };
});

// IpcClient.githubRateLimit returns a near-empty bucket → below MIN headroom.
const githubRateLimitMock = vi.fn().mockResolvedValue({
  remaining: 8,
  limit: 5000,
  resetAt: Math.floor(Date.now() / 1000) + 60,
});
vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      githubRateLimit: githubRateLimitMock,
    }),
  },
}));

function makePipelineStateMock(): PipelineStateService {
  return {
    getState: vi.fn().mockResolvedValue({
      schema_version: "1.0",
      issue_number: 77,
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

describe("HeadlessOrchestrator GitHub-quota-low deferral (#3896)", () => {
  let mockLogger: Logger;

  beforeEach(() => {
    vi.clearAllMocks();
    githubRateLimitMock.mockResolvedValue({
      remaining: 8,
      limit: 5000,
      resetAt: Math.floor(Date.now() / 1000) + 60,
    });
    mockLogger = {
      info: vi.fn(),
      warn: vi.fn(),
      error: vi.fn(),
      debug: vi.fn(),
    } as unknown as Logger;
  });

  it("emits github-quota-low marker and a transient-classifiable error (not github-auth-failed)", async () => {
    const orchestrator = new HeadlessOrchestrator(makePipelineStateMock(), mockLogger, {
      contextFileWaitMs: 0,
    });
    const onStderr = vi.fn();

    const result = await orchestrator.runPipeline(77, { onStderr });

    expect(result.success).toBe(false);
    expect(result.failedStage).toBe("pipeline-start");

    // The error text must carry the [github-quota-low] token so the Go
    // ClassifyTerminalKind fallback routes it transient (failureDetail = error.message).
    expect(result.error?.message).toContain("[github-quota-low]");

    const calls = onStderr.mock.calls.filter(([stage]) => stage === "pipeline-start");
    const markerCall = calls.find(
      ([, data]) =>
        typeof data === "string" && data.includes("[pipeline-start-failure] github-quota-low")
    );
    expect(markerCall).toBeDefined();
    expect(markerCall![1]).toContain("resetInSec=");

    // It must NOT have taken the permanent auth-failed path.
    const authFailed = calls.find(
      ([, data]) => typeof data === "string" && data.includes("github-auth-failed")
    );
    expect(authFailed).toBeUndefined();
  });
});
