/**
 * PipelineBridge.test.ts — Unit tests for the PipelineBridge service.
 *
 * Tests the community skill fallback notification behavior added in Issue #1474.
 *
 * Coverage:
 *  - Shows warning notification when skillFallbackUsed=true
 *  - Does NOT show warning when skillFallbackUsed=false (or omitted)
 *  - Notification fires without blocking stage execution
 *  - "Reconnect" action handler calls offlineManager.start() when present
 *  - "Reconnect" action handler is a no-op when offlineManager is null
 *
 * @see Issue #1474 — Community Skill Fallback for Offline and Free Tier
 * @see src/services/PipelineBridge.ts — implementation under test
 */

import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";

// ─── Mocks ──────────────────────────────────────────────────────────────────

const mockShowWarningMessage = vi.fn();
const mockIpcCall = vi.fn();

vi.mock("vscode", () => ({
  window: {
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
    createStatusBarItem: vi.fn(() => ({
      text: "",
      tooltip: "",
      command: undefined,
      show: vi.fn(),
      hide: vi.fn(),
      dispose: vi.fn(),
    })),
    showWarningMessage: (...args: unknown[]) => mockShowWarningMessage(...args),
  },
  StatusBarAlignment: {
    Left: 1,
    Right: 2,
  },
  workspace: {
    workspaceFolders: [{ uri: { fsPath: "/mock/workspace" } }],
  },
}));

// Mock SkillRunner so no real Claude CLI is invoked
vi.mock("../../src/services/SkillRunner", () => ({
  SkillRunner: vi.fn(function () {
    return {
      runStage: vi.fn().mockResolvedValue({
        success: true,
        exitCode: 0,
        inputTokens: 100,
        outputTokens: 50,
      }),
      abort: vi.fn(),
      isRunning: false,
    };
  }),
}));

// Import after mocks
import { PipelineBridge } from "../../src/services/PipelineBridge";
import { Logger } from "../../src/utils/logger";

// ─── Helpers ────────────────────────────────────────────────────────────────

/** Minimal IpcClient stub with controllable event dispatch. */
function makeIpcClient() {
  const handlers = new Map<string, (data: unknown) => void>();
  return {
    on: vi.fn((event: string, handler: (data: unknown) => void) => {
      handlers.set(event, handler);
      return { dispose: vi.fn() };
    }),
    call: mockIpcCall.mockResolvedValue({}),
    emit: (event: string, data: unknown) => {
      handlers.get(event)?.(data);
    },
    _handlers: handlers,
  };
}

/** Minimal OfflineManager stub. */
function makeOfflineManager() {
  return {
    start: vi.fn(),
    register: vi.fn(),
    getStrategy: vi.fn(),
    state: "online" as const,
    dispose: vi.fn(),
  };
}

function createLogger(): Logger {
  return new Logger("PipelineBridge-Test");
}

type FakeIpcClient = ReturnType<typeof makeIpcClient>;

/** Build base IPC runStage params. */
function makeRunStageParams(overrides: Record<string, unknown> = {}): Record<string, unknown> {
  return {
    stage: "feature-dev",
    issueNumber: 42,
    model: "claude-sonnet-4-20250514",
    timeoutMs: 60_000,
    worktreeDir: "/mock/worktree",
    repo: "nightgauge/test",
    ...overrides,
  };
}

// ─── Tests ───────────────────────────────────────────────────────────────────

describe("PipelineBridge — skillFallbackUsed notification", () => {
  let ipc: FakeIpcClient;
  let logger: Logger;
  let offlineManager: ReturnType<typeof makeOfflineManager>;

  beforeEach(() => {
    vi.clearAllMocks();
    ipc = makeIpcClient();
    logger = createLogger();
    offlineManager = makeOfflineManager();
    mockShowWarningMessage.mockResolvedValue(undefined);
  });

  // ── Notification shown ─────────────────────────────────────────────────

  it("shows warning notification when skillFallbackUsed=true", async () => {
    new PipelineBridge(ipc as any, logger, null, null, offlineManager as any);

    const params = makeRunStageParams({ skillFallbackUsed: true });
    ipc.emit("pipeline.runStage", params);

    // Allow microtasks to flush
    await vi.waitFor(() => {
      expect(mockShowWarningMessage).toHaveBeenCalledWith(
        "Using community skill — platform unavailable",
        "Reconnect"
      );
    });
  });

  it("does NOT show warning when skillFallbackUsed=false", async () => {
    new PipelineBridge(ipc as any, logger, null, null, offlineManager as any);

    const params = makeRunStageParams({ skillFallbackUsed: false });
    ipc.emit("pipeline.runStage", params);

    await vi.waitFor(() => {
      expect(mockIpcCall).toHaveBeenCalledWith("pipeline.stageResult", expect.any(Object));
    });

    expect(mockShowWarningMessage).not.toHaveBeenCalled();
  });

  it("does NOT show warning when skillFallbackUsed is omitted", async () => {
    new PipelineBridge(ipc as any, logger, null, null, offlineManager as any);

    const params = makeRunStageParams(); // no skillFallbackUsed field
    ipc.emit("pipeline.runStage", params);

    await vi.waitFor(() => {
      expect(mockIpcCall).toHaveBeenCalledWith("pipeline.stageResult", expect.any(Object));
    });

    expect(mockShowWarningMessage).not.toHaveBeenCalled();
  });

  // ── Non-blocking ───────────────────────────────────────────────────────

  it("stage execution proceeds without waiting for notification dismissal", async () => {
    // showWarningMessage never resolves (simulates user ignoring the prompt)
    mockShowWarningMessage.mockReturnValue(new Promise(() => {}));

    new PipelineBridge(ipc as any, logger, null, null, offlineManager as any);

    const params = makeRunStageParams({ skillFallbackUsed: true });
    ipc.emit("pipeline.runStage", params);

    // pipeline.stageResult must still be sent even though notification is pending
    await vi.waitFor(() => {
      expect(mockIpcCall).toHaveBeenCalledWith(
        "pipeline.stageResult",
        expect.objectContaining({ issueNumber: 42, success: true })
      );
    });
  });

  // ── Reconnect action handler ───────────────────────────────────────────

  it("calls offlineManager.start() when user clicks Reconnect", async () => {
    mockShowWarningMessage.mockResolvedValue("Reconnect");

    new PipelineBridge(ipc as any, logger, null, null, offlineManager as any);

    const params = makeRunStageParams({ skillFallbackUsed: true });
    ipc.emit("pipeline.runStage", params);

    await vi.waitFor(() => {
      expect(offlineManager.start).toHaveBeenCalledTimes(1);
    });
  });

  it("does NOT call offlineManager.start() when user dismisses without clicking Reconnect", async () => {
    mockShowWarningMessage.mockResolvedValue(undefined); // dismissed

    new PipelineBridge(ipc as any, logger, null, null, offlineManager as any);

    const params = makeRunStageParams({ skillFallbackUsed: true });
    ipc.emit("pipeline.runStage", params);

    await vi.waitFor(() => {
      expect(mockShowWarningMessage).toHaveBeenCalled();
    });

    // Give the .then() a chance to run
    await Promise.resolve();

    expect(offlineManager.start).not.toHaveBeenCalled();
  });

  it("is a no-op when offlineManager is null and user clicks Reconnect", async () => {
    mockShowWarningMessage.mockResolvedValue("Reconnect");

    // No offlineManager injected
    new PipelineBridge(ipc as any, logger, null, null, null);

    const params = makeRunStageParams({ skillFallbackUsed: true });
    ipc.emit("pipeline.runStage", params);

    // Should not throw — optional chaining handles null offlineManager
    await vi.waitFor(() => {
      expect(mockShowWarningMessage).toHaveBeenCalled();
    });
  });
});

describe("PipelineBridge — error classification in stageResult (Issue #2573)", () => {
  let ipc: FakeIpcClient;
  let logger: Logger;

  beforeEach(() => {
    vi.clearAllMocks();
    ipc = makeIpcClient();
    logger = createLogger();
  });

  it("includes errorCategory and retryAfterMs in pipeline.stageResult when present", async () => {
    // Mock SkillRunner to return a result with error classification
    const { SkillRunner: MockSkillRunner } = await import("../../src/services/SkillRunner");
    vi.mocked(MockSkillRunner).mockImplementation(function () {
      return {
        runStage: vi.fn().mockResolvedValue({
          success: false,
          exitCode: 1,
          inputTokens: 100,
          outputTokens: 50,
          cacheReadTokens: 0,
          cacheCreationTokens: 0,
          costUsd: 0.01,
          durationMs: 5000,
          errorCategory: "rate_limit",
          retryAfterMs: 300000,
        }),
        abort: vi.fn(),
        isRunning: false,
      } as any;
    });

    new PipelineBridge(ipc as any, logger);

    const params = makeRunStageParams();
    ipc.emit("pipeline.runStage", params);

    await vi.waitFor(() => {
      expect(mockIpcCall).toHaveBeenCalledWith(
        "pipeline.stageResult",
        expect.objectContaining({
          errorCategory: "rate_limit",
          retryAfterMs: 300000,
        })
      );
    });
  });

  it("finalizes the last running phase when the stage completes (no orphan spinner)", async () => {
    // The phase tracker only completes phase N when N+1 starts. The terminal
    // phase of a stage therefore needs an explicit close at stage end, or the
    // tree view spinner outlives the stage transition. Regression coverage for
    // the Go-driven path: the legacy HeadlessOrchestrator path already calls
    // completeStagePhases in onStageComplete; PipelineBridge must too.
    const { SkillRunner: MockSkillRunner } = await import("../../src/services/SkillRunner");
    vi.mocked(MockSkillRunner).mockImplementation(function () {
      return {
        runStage: vi.fn(async (_params: unknown, callbacks: any) => {
          // Simulate the skill emitting a single phase marker mid-stage.
          callbacks.onStdout?.(
            "feature-dev",
            '<!-- phase:start name="write-dev-context" index=16 total=17 stage="feature-dev" -->\n'
          );
          // Yield so the stream handler/phase tracker process the marker
          // before the runStage promise resolves and we hit the
          // post-stage completeStagePhases call.
          await Promise.resolve();
          return {
            success: true,
            exitCode: 0,
            inputTokens: 100,
            outputTokens: 50,
            cacheReadTokens: 0,
            cacheCreationTokens: 0,
            costUsd: 0.01,
            durationMs: 5000,
          };
        }),
        abort: vi.fn(),
        isRunning: false,
      } as any;
    });

    const stateService = {
      startPhase: vi.fn().mockResolvedValue(undefined),
      completePhase: vi.fn().mockResolvedValue(undefined),
      skipPhase: vi.fn().mockResolvedValue(undefined),
    };

    const outputWindow = {
      show: vi.fn(),
      setIssueNumber: vi.fn(),
      updateStageStatus: vi.fn(),
      appendLine: vi.fn(),
      addStallWarning: vi.fn(),
      removeStallWarnings: vi.fn(),
    };

    new PipelineBridge(ipc as any, logger, stateService as any, null, null, outputWindow as any);

    const params = makeRunStageParams({ stage: "feature-dev" });
    ipc.emit("pipeline.runStage", params);

    // Wait for the terminal pipeline.stageResult to confirm the stage finished.
    await vi.waitFor(() => {
      expect(mockIpcCall).toHaveBeenCalledWith(
        "pipeline.stageResult",
        expect.objectContaining({ stage: "feature-dev", success: true })
      );
    });

    // The phase tracker enqueues mutations on a per-stage promise chain.
    // Drain microtasks until completePhase fires (or fail loudly).
    await vi.waitFor(() => {
      expect(stateService.completePhase).toHaveBeenCalledWith(
        "feature-dev",
        "write-dev-context",
        expect.any(Number)
      );
    });
  });

  it("does not include errorCategory when stage succeeds", async () => {
    // Reset SkillRunner mock to return success
    const { SkillRunner: MockSkillRunner } = await import("../../src/services/SkillRunner");
    vi.mocked(MockSkillRunner).mockImplementation(function () {
      return {
        runStage: vi.fn().mockResolvedValue({
          success: true,
          exitCode: 0,
          inputTokens: 100,
          outputTokens: 50,
          cacheReadTokens: 0,
          cacheCreationTokens: 0,
          costUsd: 0.01,
          durationMs: 5000,
        }),
        abort: vi.fn(),
        isRunning: false,
      } as any;
    });

    new PipelineBridge(ipc as any, logger);

    const params = makeRunStageParams();
    ipc.emit("pipeline.runStage", params);

    await vi.waitFor(() => {
      expect(mockIpcCall).toHaveBeenCalledWith(
        "pipeline.stageResult",
        expect.objectContaining({ success: true })
      );
    });

    // errorCategory should be undefined on success
    const stageResultCall = mockIpcCall.mock.calls.find(
      (c: unknown[]) => c[0] === "pipeline.stageResult"
    );
    expect(stageResultCall?.[1]?.errorCategory).toBeUndefined();
  });
});

// Issue #3619 — 5xx backoff in PipelineBridge.handleRunStage()
// Verifies that transient Anthropic 5xx errors trigger exponential backoff
// retries inside the TS execution layer before forwarding the final result
// to Go. This mirrors the HeadlessOrchestrator path so the Go scheduler
// never sees intermediate failures.
describe("PipelineBridge — 5xx backoff (Issue #3619)", () => {
  let ipc: FakeIpcClient;
  let logger: Logger;

  beforeEach(() => {
    vi.clearAllMocks();
    vi.useFakeTimers();
    ipc = makeIpcClient();
    logger = createLogger();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("retries on 5xx up to max_auto_attempts with backoff before forwarding failure", async () => {
    const { SkillRunner: MockSkillRunner } = await import("../../src/services/SkillRunner");
    let callCount = 0;
    vi.mocked(MockSkillRunner).mockImplementation(function () {
      return {
        runStage: vi.fn(async () => {
          callCount++;
          if (callCount <= 2) {
            return {
              success: false,
              exitCode: 1,
              inputTokens: 100,
              outputTokens: 50,
              cacheReadTokens: 0,
              cacheCreationTokens: 0,
              costUsd: 0.01,
              errorText: "API Error: 500 Internal Server Error",
            };
          }
          return {
            success: true,
            exitCode: 0,
            inputTokens: 100,
            outputTokens: 50,
            cacheReadTokens: 0,
            cacheCreationTokens: 0,
            costUsd: 0.01,
          };
        }),
        abort: vi.fn(),
        isRunning: false,
      } as any;
    });

    new PipelineBridge(ipc as any, logger);
    const params = makeRunStageParams();
    ipc.emit("pipeline.runStage", params);

    // Drain two backoff delays (5s each with default config)
    await vi.runAllTimersAsync();

    await vi.waitFor(() => {
      expect(mockIpcCall).toHaveBeenCalledWith(
        "pipeline.stageResult",
        expect.objectContaining({ success: true })
      );
    });

    // pipeline.stageResult must be called exactly once — Go never sees the intermediate failures
    expect(
      mockIpcCall.mock.calls.filter((c: unknown[]) => c[0] === "pipeline.stageResult")
    ).toHaveLength(1);
    // SkillRunner was called 3 times total (2 failures + 1 success)
    expect(callCount).toBe(3);
  });

  it("forwards failure immediately when max_auto_attempts exhausted", async () => {
    const { SkillRunner: MockSkillRunner } = await import("../../src/services/SkillRunner");
    let callCount = 0;
    vi.mocked(MockSkillRunner).mockImplementation(function () {
      return {
        runStage: vi.fn(async () => {
          callCount++;
          return {
            success: false,
            exitCode: 1,
            inputTokens: 100,
            outputTokens: 50,
            cacheReadTokens: 0,
            cacheCreationTokens: 0,
            costUsd: 0.01,
            errorText: "API Error: 502 Bad Gateway",
          };
        }),
        abort: vi.fn(),
        isRunning: false,
      } as any;
    });

    new PipelineBridge(ipc as any, logger);
    const params = makeRunStageParams();
    ipc.emit("pipeline.runStage", params);

    await vi.runAllTimersAsync();

    await vi.waitFor(() => {
      expect(mockIpcCall).toHaveBeenCalledWith(
        "pipeline.stageResult",
        expect.objectContaining({ success: false })
      );
    });

    // One final result forwarded to Go
    expect(
      mockIpcCall.mock.calls.filter((c: unknown[]) => c[0] === "pipeline.stageResult")
    ).toHaveLength(1);
    // initial attempt + max_auto_attempts (3) retries = 4 total calls
    expect(callCount).toBe(4);
  });

  it("does NOT retry non-5xx failures — pass through immediately", async () => {
    const { SkillRunner: MockSkillRunner } = await import("../../src/services/SkillRunner");
    let callCount = 0;
    vi.mocked(MockSkillRunner).mockImplementation(function () {
      return {
        runStage: vi.fn(async () => {
          callCount++;
          return {
            success: false,
            exitCode: 1,
            inputTokens: 100,
            outputTokens: 50,
            cacheReadTokens: 0,
            cacheCreationTokens: 0,
            costUsd: 0.01,
            errorText: "Validation failed: missing acceptance criteria",
          };
        }),
        abort: vi.fn(),
        isRunning: false,
      } as any;
    });

    new PipelineBridge(ipc as any, logger);
    const params = makeRunStageParams();
    ipc.emit("pipeline.runStage", params);

    await vi.waitFor(() => {
      expect(mockIpcCall).toHaveBeenCalledWith(
        "pipeline.stageResult",
        expect.objectContaining({ success: false })
      );
    });

    // No backoff — called exactly once
    expect(callCount).toBe(1);
  });
});

// Issue #3605 — verify the stage-exit diagnostic fields populated by the
// SkillRunner are forwarded verbatim into the pipeline.stageResult IPC call
// so the Go scheduler can persist them in the daily JSONL exit-record.
describe("PipelineBridge — stage-exit diagnostic forwarding (Issue #3605)", () => {
  let ipc: FakeIpcClient;
  let logger: Logger;

  beforeEach(() => {
    vi.clearAllMocks();
    ipc = makeIpcClient();
    logger = createLogger();
  });

  it("forwards signal / signalSource / idleMsAtExit / lastBashCommand / stopHookErrored / stderrTail verbatim", async () => {
    const { SkillRunner: MockSkillRunner } = await import("../../src/services/SkillRunner");
    vi.mocked(MockSkillRunner).mockImplementation(function () {
      return {
        runStage: vi.fn().mockResolvedValue({
          success: false,
          exitCode: 137,
          inputTokens: 1000,
          outputTokens: 200,
          cacheReadTokens: 5000,
          cacheCreationTokens: 80,
          costUsd: 0.42,
          durationMs: 397_123,
          errorText: "[stall-killed] feature-dev terminated",
          // ── Diagnostic fields under test
          sessionId: "abc-123",
          signal: "SIGKILL",
          signalSource: "stall-kill",
          elapsedMs: 397_123,
          idleMsAtExit: 4_521,
          lastBashCommand: "nightgauge project move-status 3591 in-progress",
          lastBashExit: 1,
          stopHookErrored: true,
          stderrTail: "[skillRunner] Stage exceeded stall idle threshold (20m).",
        }),
        abort: vi.fn(),
        isRunning: false,
      } as any;
    });

    new PipelineBridge(ipc as any, logger);

    const params = makeRunStageParams();
    ipc.emit("pipeline.runStage", params);

    await vi.waitFor(() => {
      expect(mockIpcCall).toHaveBeenCalledWith(
        "pipeline.stageResult",
        expect.objectContaining({
          sessionId: "abc-123",
          signal: "SIGKILL",
          signalSource: "stall-kill",
          elapsedMs: 397_123,
          idleMsAtExit: 4_521,
          lastBashCommand: "nightgauge project move-status 3591 in-progress",
          lastBashExit: 1,
          stopHookErrored: true,
          stderrTail: "[skillRunner] Stage exceeded stall idle threshold (20m).",
          cacheCreationTokens: 80,
        })
      );
    });
  });

  it("omits diagnostic fields when SkillRunner leaves them undefined (healthy run)", async () => {
    const { SkillRunner: MockSkillRunner } = await import("../../src/services/SkillRunner");
    vi.mocked(MockSkillRunner).mockImplementation(function () {
      return {
        runStage: vi.fn().mockResolvedValue({
          success: true,
          exitCode: 0,
          inputTokens: 1000,
          outputTokens: 200,
          cacheReadTokens: 5000,
          cacheCreationTokens: 0,
          costUsd: 0.12,
          durationMs: 5_000,
          // No signal / signalSource / lastBashCommand etc. — healthy run.
        }),
        abort: vi.fn(),
        isRunning: false,
      } as any;
    });

    new PipelineBridge(ipc as any, logger);

    const params = makeRunStageParams();
    ipc.emit("pipeline.runStage", params);

    await vi.waitFor(() => {
      expect(mockIpcCall).toHaveBeenCalledWith(
        "pipeline.stageResult",
        expect.objectContaining({ success: true })
      );
    });
    const stageResultCall = mockIpcCall.mock.calls.find(
      (c: unknown[]) => c[0] === "pipeline.stageResult"
    );
    const payload = stageResultCall?.[1] as Record<string, unknown> | undefined;
    expect(payload?.signal).toBeUndefined();
    expect(payload?.signalSource).toBeUndefined();
    expect(payload?.lastBashCommand).toBeUndefined();
    expect(payload?.stopHookErrored).toBeUndefined();
    expect(payload?.stderrTail).toBeUndefined();
  });
});

// ─────────────────────────────────────────────────────────────────────────
// License status forwarding (Issue #4156)
// ─────────────────────────────────────────────────────────────────────────

describe("PipelineBridge — license status forwarding (Issue #4156)", () => {
  let ipc: FakeIpcClient;
  let logger: Logger;

  beforeEach(() => {
    vi.clearAllMocks();
    ipc = makeIpcClient();
    logger = createLogger();
  });

  /** Minimal LicensePreflight stub returning a fixed validate() result. */
  function makeLicensePreflight(result: Record<string, unknown>) {
    return {
      clearCache: vi.fn(),
      validate: vi.fn().mockResolvedValue(result),
    };
  }

  async function emitValidateLicenseAndAwaitResult(issueNumber = 42) {
    ipc.emit("pipeline.validateLicense", { issueNumber });
    await vi.waitFor(() => {
      expect(mockIpcCall).toHaveBeenCalledWith("pipeline.licenseResult", expect.anything());
    });
    const call = mockIpcCall.mock.calls.find((c: unknown[]) => c[0] === "pipeline.licenseResult");
    return call?.[1] as Record<string, unknown> | undefined;
  }

  it("forwards a confirmed status (active) verbatim", async () => {
    const licensePreflight = makeLicensePreflight({
      allowed: true,
      tier: "pro",
      cacheUntil: "2099-01-01T00:00:00Z",
      status: "active",
      offline: false,
    });
    new PipelineBridge(ipc as any, logger, null, licensePreflight as any);

    const payload = await emitValidateLicenseAndAwaitResult();
    expect(payload?.status).toBe("active");
    expect(payload?.allowed).toBe(true);
  });

  it("forwards a confirmed revoked status so Go can cache it", async () => {
    const licensePreflight = makeLicensePreflight({
      allowed: false,
      tier: "community",
      cacheUntil: "2099-01-01T00:00:00Z",
      status: "revoked",
      reason: "Your license has been revoked. Contact support for assistance.",
      actionUrl: "https://github.com/nightgauge/nightgauge/issues",
      offline: false,
    });
    new PipelineBridge(ipc as any, logger, null, licensePreflight as any);

    const payload = await emitValidateLicenseAndAwaitResult();
    expect(payload?.status).toBe("revoked");
    expect(payload?.allowed).toBe(false);
  });

  it("withholds status when the result is offline/degraded, even though the UI-facing status says community", async () => {
    // LicensePreflight.communityResult() reports status:"community" for
    // rendering purposes even on a network failure — PipelineBridge must NOT
    // forward that as a confirmed status, or it would overwrite Go's cached
    // last-confirmed-revoked/suspended status with a spurious "clean" one.
    const licensePreflight = makeLicensePreflight({
      allowed: true,
      tier: "community",
      cacheUntil: "2099-01-01T00:00:00Z",
      status: "community",
      offline: true,
    });
    new PipelineBridge(ipc as any, logger, null, licensePreflight as any);

    const payload = await emitValidateLicenseAndAwaitResult();
    expect(payload?.status).toBe("");
  });

  it("sends status=community when no LicensePreflight is configured (CLI mode)", async () => {
    new PipelineBridge(ipc as any, logger, null, null);

    const payload = await emitValidateLicenseAndAwaitResult();
    expect(payload?.status).toBe("community");
    expect(payload?.allowed).toBe(true);
    expect(payload?.tier).toBe("community");
  });
});
