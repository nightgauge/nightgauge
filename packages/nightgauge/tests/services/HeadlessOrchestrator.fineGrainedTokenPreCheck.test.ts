/**
 * HeadlessOrchestrator.fineGrainedTokenPreCheck.test.ts
 *
 * The GitHub auth pre-check used to grep `gh auth status` for the word `repo`.
 * Only classic OAuth tokens advertise scopes; fine-grained PATs (github_pat_…)
 * and GitHub App tokens print no scopes line, so a token with full
 * Contents/Issues/Pull-requests write on the repository was refused at
 * pipeline-start with "lacks required `repo` scope". Observed on the
 * clean-install gate's fine-grained CLEAN_INSTALL_GH_TOKEN (run 33692630440).
 *
 * Now: a token that advertises scopes must list `repo`; a token that does not
 * is judged by `gh repo view --json viewerPermission` on the target repository.
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
let execBehavior: (cmd: string) => Promise<{ stdout: string; stderr: string }>;

vi.mock("child_process", async () => {
  const actual = await vi.importActual<typeof import("child_process")>("child_process");
  const kCustom = Symbol.for("nodejs.util.promisify.custom");

  const issueJson = '{"labels":[],"state":"OPEN","title":"Active issue #79"}';

  const execMock: any = vi.fn();
  execMock[kCustom] = (cmd: string) => execBehavior(cmd);

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
  } as unknown as PipelineStateService;
}

const FINE_GRAINED_STATUS =
  "github.com\n  ✓ Logged in to github.com account octocat (GH_TOKEN)\n" +
  "  - Active account: true\n  - Git operations protocol: https\n" +
  "  - Token: github_pat_************\n";
const CLASSIC_STATUS = (scopes: string) =>
  "github.com\n  ✓ Logged in to github.com account octocat (keyring)\n" +
  "  - Active account: true\n  - Git operations protocol: ssh\n" +
  "  - Token: gho_************\n  - Token scopes: " +
  scopes +
  "\n";

function statusThen(status: string, permission: string | Error) {
  return (cmd: string) => {
    if (cmd.startsWith("gh auth status")) return Promise.resolve({ stdout: status, stderr: "" });
    if (cmd.startsWith("gh repo view")) {
      return permission instanceof Error
        ? Promise.reject(permission)
        : Promise.resolve({ stdout: `${permission}\n`, stderr: "" });
    }
    // Anything after the pre-check is out of scope here: fail it fast with a
    // sentinel so a run that PASSES the pre-check still resolves, instead of
    // hanging on unmocked git/gh calls and leaking into the next test.
    return Promise.reject(Object.assign(new Error("SENTINEL_AFTER_PRECHECK"), { stderr: "" }));
  };
}

function authFailedMarker(onStderr: ReturnType<typeof vi.fn>): string | undefined {
  const call = onStderr.mock.calls.find(
    ([stage, data]) =>
      stage === "pipeline-start" && typeof data === "string" && data.includes("github-auth-failed")
  );
  return call ? (call[1] as string) : undefined;
}

describe("HeadlessOrchestrator GitHub auth pre-check — tokens that advertise no scopes", () => {
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

  // preCheckAuth is exercised directly for the accepting cases: past the
  // pre-check, runPipeline() needs the whole stage machinery, which this
  // file does not mock, and a run that hangs there leaks into the next test.
  function preCheck(orchestrator: HeadlessOrchestrator) {
    return (
      orchestrator as unknown as { preCheckAuth: () => Promise<Record<string, unknown>> }
    ).preCheckAuth();
  }

  it("accepts a fine-grained token that can push (viewerPermission=ADMIN)", async () => {
    execBehavior = statusThen(FINE_GRAINED_STATUS, "ADMIN");
    const orchestrator = new HeadlessOrchestrator(makePipelineStateMock(), mockLogger, {
      contextFileWaitMs: 0,
    });
    const result = await preCheck(orchestrator);
    expect(result.isAuthenticated).toBe(true);
    expect(result.hasRequiredScopes).toBe(true);
    expect(result.errorMessage).toBeUndefined();
  });

  it("accepts a fine-grained token with WRITE", async () => {
    execBehavior = statusThen(FINE_GRAINED_STATUS, "WRITE");
    const orchestrator = new HeadlessOrchestrator(makePipelineStateMock(), mockLogger, {
      contextFileWaitMs: 0,
    });
    const result = await preCheck(orchestrator);
    expect(result.hasRequiredScopes).toBe(true);
  });

  it("refuses a fine-grained token that can only read (viewerPermission=READ)", async () => {
    execBehavior = statusThen(FINE_GRAINED_STATUS, "READ");
    const orchestrator = new HeadlessOrchestrator(makePipelineStateMock(), mockLogger, {
      contextFileWaitMs: 0,
    });
    const onStderr = vi.fn();
    const result = await orchestrator.runPipeline(79, { onStderr });
    expect(result.success).toBe(false);
    expect(result.failedStage).toBe("pipeline-start");
    expect(authFailedMarker(onStderr)).toContain("cannot push to this repository");
    expect(authFailedMarker(onStderr)).toContain("viewerPermission=READ");
  });

  it("reports the probe's own failure when the permission cannot be read", async () => {
    execBehavior = statusThen(
      FINE_GRAINED_STATUS,
      Object.assign(new Error("Command failed: gh repo view"), {
        stderr:
          "none of the git remotes configured for this repository point to a known GitHub host",
      })
    );
    const orchestrator = new HeadlessOrchestrator(makePipelineStateMock(), mockLogger, {
      contextFileWaitMs: 0,
    });
    const onStderr = vi.fn();
    const result = await orchestrator.runPipeline(79, { onStderr });
    expect(result.failedStage).toBe("pipeline-start");
    expect(authFailedMarker(onStderr)).toContain("viewerPermission=unknown");
    expect(authFailedMarker(onStderr)).toContain("none of the git remotes");
    expect(authFailedMarker(onStderr)).not.toContain("gh auth login");
  });

  it("still requires `repo` from a classic token that advertises scopes", async () => {
    execBehavior = statusThen(CLASSIC_STATUS("'gist', 'read:org'"), "ADMIN");
    const orchestrator = new HeadlessOrchestrator(makePipelineStateMock(), mockLogger, {
      contextFileWaitMs: 0,
    });
    const onStderr = vi.fn();
    const result = await orchestrator.runPipeline(79, { onStderr });
    expect(result.failedStage).toBe("pipeline-start");
    expect(authFailedMarker(onStderr)).toContain("lacks required `repo` scope");
  });

  it("accepts a classic token whose scopes include `repo` without probing the repository", async () => {
    const probe = vi.fn();
    execBehavior = (cmd: string) => {
      if (cmd.startsWith("gh repo view")) probe();
      return statusThen(CLASSIC_STATUS("'gist', 'read:org', 'repo'"), "READ")(cmd);
    };
    const orchestrator = new HeadlessOrchestrator(makePipelineStateMock(), mockLogger, {
      contextFileWaitMs: 0,
    });
    const result = await preCheck(orchestrator);
    expect(result.hasRequiredScopes).toBe(true);
    expect(probe).not.toHaveBeenCalled();
  });
});
