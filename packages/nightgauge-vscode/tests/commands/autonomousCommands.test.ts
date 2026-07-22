/**
 * Tests for autonomous mode commands
 *
 * Covers all six user-facing command handlers and the autonomous.dispatch
 * event listener registered by registerAutonomousCommands().
 *
 * @see src/commands/autonomousCommands.ts
 * @see Issue #2503 - Add tests for autonomous commands
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import * as vscode from "vscode";
import {
  registerAutonomousCommands,
  disposeAutonomousOutputChannel,
  resetWatchdogStateForTest,
} from "../../src/commands/autonomousCommands";
import { IpcClient } from "../../src/services/IpcClient";
import type { AutonomousStatusResult } from "../../src/services/IpcClientBase";
import type { Logger } from "../../src/utils/logger";
import type { StatusBarManager } from "../../src/utils/statusBar";

// ---------------------------------------------------------------------------
// Mocks
// ---------------------------------------------------------------------------

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: vi.fn(),
  },
  IpcClientBase: {
    activeCallSource: undefined,
  },
}));

vi.mock("../../src/utils/configPathResolver", () => ({
  getRepoIdentity: vi.fn(async () => ({
    owner: "nightgauge",
    repo: "nightgauge",
  })),
}));

/** Stable mock output channel — same object returned on every createOutputChannel call. */
const mockOutputChannel = {
  appendLine: vi.fn(),
  clear: vi.fn(),
  show: vi.fn(),
  dispose: vi.fn(),
};

vi.mock("vscode", () => ({
  commands: {
    registerCommand: vi.fn((_id: string, handler: any) => ({
      dispose: vi.fn(),
    })),
    executeCommand: vi.fn(),
  },
  window: {
    showWarningMessage: vi.fn(),
    showInformationMessage: vi.fn(),
    showErrorMessage: vi.fn(),
    // Always returns the same stable mockOutputChannel object
    createOutputChannel: vi.fn(() => mockOutputChannel),
    // statusBar.ts evaluates STATUS_COLORS at module load and
    // autonomousCommands.ts transitively imports its formatCooldown* helpers
    // (#3446), so ThemeColor + createStatusBarItem must be present even
    // though no test here drives the status-bar surface directly.
    createStatusBarItem: vi.fn(() => ({
      text: "",
      tooltip: "",
      backgroundColor: undefined,
      command: "",
      show: vi.fn(),
      hide: vi.fn(),
      dispose: vi.fn(),
    })),
  },
  workspace: {
    workspaceFolders: [{ uri: { fsPath: "/test/workspace" }, name: "nightgauge", index: 0 }],
  },
  ThemeColor: class ThemeColor {
    constructor(public id: string) {}
  },
  StatusBarAlignment: {
    Left: 1,
    Right: 2,
  },
}));

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

/** Extract a registered command handler by command ID. */
function getHandlerById(commandId: string): (...args: any[]) => Promise<void> {
  const calls = (vscode.commands.registerCommand as any).mock.calls;
  const match = calls.find((c: any[]) => c[0] === commandId);
  if (!match) throw new Error(`Command not registered: ${commandId}`);
  return match[1];
}

/** Build a realistic AutonomousStatusResult with sensible defaults. */
function createMockStatus(overrides?: Partial<AutonomousStatusResult>): AutonomousStatusResult {
  return {
    status: "running",
    startedAt: new Date(Date.now() - 60_000).toISOString(),
    lastScanAt: new Date().toISOString(),
    running: [],
    completed: [],
    failed: [],
    remaining: 5,
    tokensSpent: 100_000,
    tokensCeiling: 500_000,
    cyclesRun: 10,
    ...overrides,
  };
}

const createMockLogger = (): Logger =>
  ({
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  }) as unknown as Logger;

const createMockStatusBar = (): StatusBarManager =>
  ({
    showAutonomousRunning: vi.fn(),
    showAutonomousPaused: vi.fn(),
    showAutonomousComplete: vi.fn(),
    showAutonomousDisconnected: vi.fn(),
    showAutonomousCooldown: vi.fn(),
  }) as unknown as StatusBarManager;

const createMockQueueService = () => ({
  enqueue: vi.fn(() => Promise.resolve({ position: 1 })),
});

/** Get the stable mock output channel. */
function getMockChannel() {
  return mockOutputChannel;
}

// ---------------------------------------------------------------------------
// Suite
// ---------------------------------------------------------------------------

describe("registerAutonomousCommands", () => {
  let mockLogger: Logger;
  let mockStatusBar: StatusBarManager;
  let mockIpc: any;

  beforeEach(() => {
    vi.clearAllMocks();
    // Reset module-level output channel so it's lazily re-created each test
    disposeAutonomousOutputChannel();

    mockLogger = createMockLogger();
    mockStatusBar = createMockStatusBar();

    mockIpc = {
      on: vi.fn(() => ({ dispose: vi.fn() })),
      onDidChangeStatus: vi.fn(() => ({ dispose: vi.fn() })),
      autonomousStart: vi.fn(() => Promise.resolve(createMockStatus())),
      autonomousStatus: vi.fn(() => Promise.resolve(createMockStatus())),
      autonomousPause: vi.fn(() => Promise.resolve(createMockStatus({ status: "paused" }))),
      autonomousResume: vi.fn(() => Promise.resolve(createMockStatus())),
      autonomousStop: vi.fn(() =>
        Promise.resolve(createMockStatus({ status: "complete", remaining: 0 }))
      ),
      autonomousClearQuotaCooldown: vi.fn(() =>
        Promise.resolve({ cleared: true, previousUntil: "2026-05-11T03:31:00Z" })
      ),
    };

    (IpcClient.getInstance as any).mockReturnValue(mockIpc);
  });

  // ── Registration ──────────────────────────────────────────────────────

  describe("command registration", () => {
    it("returns an array of disposables (one per command + two event listeners)", () => {
      const disposables = registerAutonomousCommands(mockLogger, mockStatusBar, null);
      // 2 event listeners (autonomous.statusChanged + autonomous.dispatch)
      // + 9 command handlers = 11 disposables. #3251 added statusChanged.
      // #3446 added autonomousClearQuotaCooldown.
      expect(disposables).toHaveLength(11);
      disposables.forEach((d) => expect(d).toHaveProperty("dispose"));
    });

    it("registers all expected commands", () => {
      registerAutonomousCommands(mockLogger, mockStatusBar, null);
      const registered = (vscode.commands.registerCommand as any).mock.calls.map(
        (c: any[]) => c[0]
      );
      expect(registered).toContain("nightgauge.autonomousRun");
      expect(registered).toContain("nightgauge.autonomousDryRun");
      expect(registered).toContain("nightgauge.autonomousPause");
      expect(registered).toContain("nightgauge.autonomousResume");
      expect(registered).toContain("nightgauge.autonomousStop");
      expect(registered).toContain("nightgauge.autonomousStatus");
      expect(registered).toContain("nightgauge.autonomousSelectRepos");
      expect(registered).toContain("nightgauge.autonomousClearIssueFailures");
      expect(registered).toContain("nightgauge.autonomousClearQuotaCooldown");
    });

    it("subscribes to autonomous.dispatch IPC event", () => {
      registerAutonomousCommands(mockLogger, mockStatusBar, null);
      expect(mockIpc.on).toHaveBeenCalledWith("autonomous.dispatch", expect.any(Function));
    });

    it("subscribes to autonomous.statusChanged IPC event (#3251)", () => {
      registerAutonomousCommands(mockLogger, mockStatusBar, null);
      expect(mockIpc.on).toHaveBeenCalledWith("autonomous.statusChanged", expect.any(Function));
    });
  });

  // ── autonomous.statusChanged event handling (#3251) ───────────────────

  describe("autonomous.statusChanged event subscription", () => {
    /** Pull the registered statusChanged handler so we can fire events at it. */
    function getStatusChangedHandler(): (data: unknown) => void {
      const calls = (mockIpc.on as any).mock.calls;
      const match = calls.find((c: any[]) => c[0] === "autonomous.statusChanged");
      if (!match) throw new Error("autonomous.statusChanged not subscribed");
      return match[1];
    }

    beforeEach(() => {
      registerAutonomousCommands(mockLogger, mockStatusBar, null);
    });

    it("flips the badge to 'paused' when Go transitions to paused", () => {
      const handler = getStatusChangedHandler();
      handler({
        status: "paused",
        pauseReason: "haltQueueOnSlotFailure: #3239 failed at pr-merge",
        pauseTriggeredBy: "haltQueueOnSlotFailure",
        runningCount: 0,
        remaining: 5,
      });
      expect(mockStatusBar.showAutonomousPaused).toHaveBeenCalledTimes(1);
      expect(vscode.commands.executeCommand).toHaveBeenCalledWith(
        "setContext",
        "nightgauge.autonomousRunning",
        false
      );
    });

    it("flips the badge to 'paused' when Go transitions to safety_tripped", () => {
      const handler = getStatusChangedHandler();
      handler({ status: "safety_tripped", pauseReason: "circuit breaker", runningCount: 0 });
      expect(mockStatusBar.showAutonomousPaused).toHaveBeenCalledTimes(1);
    });

    it("flips the badge to 'running' when Go transitions to running", () => {
      const handler = getStatusChangedHandler();
      handler({ status: "running", runningCount: 2, remaining: 7 });
      expect(mockStatusBar.showAutonomousRunning).toHaveBeenCalledWith(2, 7);
      expect(vscode.commands.executeCommand).toHaveBeenCalledWith(
        "setContext",
        "nightgauge.autonomousRunning",
        true
      );
    });

    it("clears running context on terminal states (stopped/complete/budget_exhausted/crashed)", () => {
      const handler = getStatusChangedHandler();
      for (const status of ["stopped", "complete", "budget_exhausted", "crashed"]) {
        (vscode.commands.executeCommand as any).mockClear();
        handler({ status, runningCount: 0, remaining: 0 });
        expect(vscode.commands.executeCommand).toHaveBeenCalledWith(
          "setContext",
          "nightgauge.autonomousRunning",
          false
        );
      }
    });

    it("logs the pause reason and triggeredBy to the output channel", () => {
      const handler = getStatusChangedHandler();
      handler({
        status: "paused",
        pauseReason: "user requested via UI",
        pauseTriggeredBy: "user",
      });
      const logged = mockOutputChannel.appendLine.mock.calls.map((c: any[]) => c[0]).join("\n");
      expect(logged).toContain("paused");
      expect(logged).toContain("user requested via UI");
      expect(logged).toContain("user");
    });

    it("ignores malformed events without a string status", () => {
      const handler = getStatusChangedHandler();
      handler(undefined);
      handler({});
      handler({ status: 42 });
      expect(mockStatusBar.showAutonomousRunning).not.toHaveBeenCalled();
      expect(mockStatusBar.showAutonomousPaused).not.toHaveBeenCalled();
    });
  });

  // ── autonomousRun ─────────────────────────────────────────────────────

  describe("autonomousRun", () => {
    beforeEach(() => {
      registerAutonomousCommands(mockLogger, mockStatusBar, null);
      // Status check succeeds with non-safety-tripped status
      mockIpc.autonomousStatus.mockResolvedValue(createMockStatus({ status: "stopped" }));
    });

    it("starts autonomous mode when user confirms", async () => {
      vi.mocked(vscode.window.showWarningMessage).mockResolvedValue("Start" as any);
      mockIpc.autonomousStart.mockResolvedValue(
        createMockStatus({
          running: [
            {
              repo: "test",
              number: 1,
              title: "T",
              startedAt: new Date().toISOString(),
            },
          ],
        })
      );

      const handler = getHandlerById("nightgauge.autonomousRun");
      await handler();

      expect(mockIpc.autonomousStart).toHaveBeenCalled();
      expect(mockStatusBar.showAutonomousRunning).toHaveBeenCalled();
      expect(vscode.commands.executeCommand).toHaveBeenCalledWith(
        "setContext",
        "nightgauge.autonomousRunning",
        true
      );
    });

    it("redirects to dry run when user selects 'Dry Run First'", async () => {
      vi.mocked(vscode.window.showWarningMessage).mockResolvedValue("Dry Run First" as any);

      const handler = getHandlerById("nightgauge.autonomousRun");
      await handler();

      expect(vscode.commands.executeCommand).toHaveBeenCalledWith("nightgauge.autonomousDryRun");
      expect(mockIpc.autonomousStart).not.toHaveBeenCalled();
    });

    it("does nothing when user cancels the confirmation dialog", async () => {
      vi.mocked(vscode.window.showWarningMessage).mockResolvedValue(undefined as any);

      const handler = getHandlerById("nightgauge.autonomousRun");
      await handler();

      expect(mockIpc.autonomousStart).not.toHaveBeenCalled();
      expect(mockStatusBar.showAutonomousRunning).not.toHaveBeenCalled();
    });

    it("shows safety trip reason and prompts resume when status is safety_tripped", async () => {
      // First dialog: start confirmation
      vi.mocked(vscode.window.showWarningMessage)
        .mockResolvedValueOnce("Start" as any)
        // Second dialog: safety trip resume prompt
        .mockResolvedValueOnce("Resume" as any);

      mockIpc.autonomousStatus.mockResolvedValue(
        createMockStatus({
          status: "safety_tripped",
          safety: {
            tripReason: "Too many failures",
            consecutiveFailures: 5,
            tokensUsed: 0,
          },
        })
      );

      const handler = getHandlerById("nightgauge.autonomousRun");
      await handler();

      expect(vscode.window.showWarningMessage).toHaveBeenCalledTimes(2);
      expect(mockIpc.autonomousStart).toHaveBeenCalled();
    });

    it("returns without starting if user dismisses the safety-tripped resume prompt", async () => {
      vi.mocked(vscode.window.showWarningMessage)
        .mockResolvedValueOnce("Start" as any)
        .mockResolvedValueOnce(undefined as any);

      mockIpc.autonomousStatus.mockResolvedValue(
        createMockStatus({
          status: "safety_tripped",
          safety: {
            tripReason: "Circuit breaker",
            consecutiveFailures: 3,
            tokensUsed: 0,
          },
        })
      );

      const handler = getHandlerById("nightgauge.autonomousRun");
      await handler();

      expect(mockIpc.autonomousStart).not.toHaveBeenCalled();
    });

    it("shows error message and logs when IPC start fails", async () => {
      vi.mocked(vscode.window.showWarningMessage).mockResolvedValue("Start" as any);
      mockIpc.autonomousStart.mockRejectedValue(new Error("Connection refused"));

      const handler = getHandlerById("nightgauge.autonomousRun");
      await handler();

      expect(vscode.window.showErrorMessage).toHaveBeenCalledWith(
        expect.stringContaining("Connection refused")
      );
      expect(mockLogger.error).toHaveBeenCalled();
    });

    it("proceeds normally when status check fails (status check error is swallowed)", async () => {
      vi.mocked(vscode.window.showWarningMessage).mockResolvedValue("Start" as any);
      mockIpc.autonomousStatus.mockRejectedValue(new Error("IPC not running"));
      mockIpc.autonomousStart.mockResolvedValue(createMockStatus());

      const handler = getHandlerById("nightgauge.autonomousRun");
      await handler();

      expect(mockIpc.autonomousStart).toHaveBeenCalled();
    });
  });

  // ── autonomousDryRun ──────────────────────────────────────────────────

  describe("autonomousDryRun", () => {
    beforeEach(() => {
      registerAutonomousCommands(mockLogger, mockStatusBar, null);
    });

    it("clears the output channel and shows the dry run preview", async () => {
      mockIpc.autonomousStatus.mockResolvedValue(createMockStatus({ remaining: 3 }));

      const handler = getHandlerById("nightgauge.autonomousDryRun");
      await handler();

      const channel = getMockChannel();
      expect(channel.clear).toHaveBeenCalled();
      expect(channel.show).toHaveBeenCalled();
      expect(channel.appendLine).toHaveBeenCalledWith(expect.stringContaining("Dry Run Preview"));
    });

    it("shows remaining candidate count when candidates exist", async () => {
      mockIpc.autonomousStatus.mockResolvedValue(createMockStatus({ remaining: 7 }));

      const handler = getHandlerById("nightgauge.autonomousDryRun");
      await handler();

      const channel = getMockChannel();
      expect(channel.appendLine).toHaveBeenCalledWith(expect.stringContaining("7"));
    });

    it("shows 'no candidates' message when remaining is 0", async () => {
      mockIpc.autonomousStatus.mockResolvedValue(createMockStatus({ remaining: 0 }));

      const handler = getHandlerById("nightgauge.autonomousDryRun");
      await handler();

      const channel = getMockChannel();
      expect(channel.appendLine).toHaveBeenCalledWith(
        expect.stringContaining("No candidates found")
      );
    });

    it("shows budget info when tokensCeiling > 0", async () => {
      mockIpc.autonomousStatus.mockResolvedValue(
        createMockStatus({ tokensSpent: 250_000, tokensCeiling: 500_000 })
      );

      const handler = getHandlerById("nightgauge.autonomousDryRun");
      await handler();

      const channel = getMockChannel();
      const calls = channel.appendLine.mock.calls.map((c: any[]) => c[0]);
      expect(calls.some((l: string) => l.includes("50%"))).toBe(true);
    });

    it("shows error message and logs when IPC fails", async () => {
      mockIpc.autonomousStatus.mockRejectedValue(new Error("Timeout"));

      const handler = getHandlerById("nightgauge.autonomousDryRun");
      await handler();

      expect(vscode.window.showErrorMessage).toHaveBeenCalledWith(
        expect.stringContaining("Timeout")
      );
      expect(mockLogger.error).toHaveBeenCalled();
    });
  });

  // ── autonomousPause ───────────────────────────────────────────────────

  describe("autonomousPause", () => {
    beforeEach(() => {
      registerAutonomousCommands(mockLogger, mockStatusBar, null);
    });

    it("updates status bar to paused and logs when pause succeeds", async () => {
      vi.mocked(vscode.window.showInformationMessage).mockResolvedValue(undefined as any);

      const handler = getHandlerById("nightgauge.autonomousPause");
      await handler();

      expect(mockIpc.autonomousPause).toHaveBeenCalled();
      expect(mockStatusBar.showAutonomousPaused).toHaveBeenCalled();
      expect(mockLogger.info).toHaveBeenCalled();
    });

    it("executes autonomousResume command when user selects Resume from prompt", async () => {
      vi.mocked(vscode.window.showInformationMessage).mockResolvedValue("Resume" as any);

      const handler = getHandlerById("nightgauge.autonomousPause");
      await handler();

      expect(vscode.commands.executeCommand).toHaveBeenCalledWith("nightgauge.autonomousResume");
    });

    it("stays paused when user dismisses the resume prompt", async () => {
      vi.mocked(vscode.window.showInformationMessage).mockResolvedValue(undefined as any);

      const handler = getHandlerById("nightgauge.autonomousPause");
      await handler();

      const resumeCalls = (vscode.commands.executeCommand as any).mock.calls.filter(
        (c: any[]) => c[0] === "nightgauge.autonomousResume"
      );
      expect(resumeCalls).toHaveLength(0);
    });

    it("shows error message and logs when IPC pause fails", async () => {
      mockIpc.autonomousPause.mockRejectedValue(new Error("Pause failed"));

      const handler = getHandlerById("nightgauge.autonomousPause");
      await handler();

      expect(vscode.window.showErrorMessage).toHaveBeenCalledWith(
        expect.stringContaining("Pause failed")
      );
      expect(mockLogger.error).toHaveBeenCalled();
    });
  });

  // ── autonomousResume ──────────────────────────────────────────────────

  describe("autonomousResume", () => {
    beforeEach(() => {
      registerAutonomousCommands(mockLogger, mockStatusBar, null);
    });

    it("updates status bar and sets context key when resume succeeds", async () => {
      mockIpc.autonomousResume.mockResolvedValue(
        createMockStatus({
          running: [
            {
              repo: "my-repo",
              number: 5,
              title: "Fix bug",
              startedAt: new Date().toISOString(),
            },
          ],
          remaining: 3,
        })
      );

      const handler = getHandlerById("nightgauge.autonomousResume");
      await handler();

      expect(mockIpc.autonomousResume).toHaveBeenCalled();
      expect(mockStatusBar.showAutonomousRunning).toHaveBeenCalledWith(1, 3);
      expect(vscode.commands.executeCommand).toHaveBeenCalledWith(
        "setContext",
        "nightgauge.autonomousRunning",
        true
      );
      expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
        expect.stringContaining("resumed")
      );
      expect(mockLogger.info).toHaveBeenCalled();
    });

    it("shows error message and logs when IPC resume fails", async () => {
      mockIpc.autonomousResume.mockRejectedValue(new Error("Resume error"));

      const handler = getHandlerById("nightgauge.autonomousResume");
      await handler();

      expect(vscode.window.showErrorMessage).toHaveBeenCalledWith(
        expect.stringContaining("Resume error")
      );
      expect(mockLogger.error).toHaveBeenCalled();
    });
  });

  // ── workspace scoping via WorkspaceManager (#3766) ───────────────────

  describe("workspace scoping (#3766)", () => {
    function buildMockWorkspaceManager(repos: Array<{ owner: string; repo: string }>) {
      return {
        getAllRepositories: vi.fn(() =>
          repos.map((r) => ({
            name: r.repo,
            github: { owner: r.owner, repo: r.repo },
          }))
        ),
      };
    }

    beforeEach(() => {
      vi.mocked(vscode.window.showWarningMessage).mockResolvedValue("Start" as any);
      mockIpc.autonomousStatus.mockResolvedValue(createMockStatus({ status: "stopped" }));
      mockIpc.autonomousStart.mockResolvedValue(createMockStatus());
      mockIpc.autonomousResume.mockResolvedValue(createMockStatus());
    });

    it("autonomousRun uses WorkspaceManager repos when manifest repos are available", async () => {
      const mockWM = buildMockWorkspaceManager([
        { owner: "acme", repo: "acmeapp-infra" },
        { owner: "acme", repo: "acmeapp-platform" },
      ]);

      registerAutonomousCommands(mockLogger, mockStatusBar, null, null, undefined, mockWM as any);

      const handler = getHandlerById("nightgauge.autonomousRun");
      await handler();

      expect(mockIpc.autonomousStart).toHaveBeenCalledWith(
        expect.arrayContaining(["acme/acmeapp-infra", "acme/acmeapp-platform"])
      );
      // Folder-identity fallback (getRepoIdentity → nightgauge/nightgauge) must NOT appear
      const callArg = mockIpc.autonomousStart.mock.calls[0][0];
      if (Array.isArray(callArg)) {
        expect(callArg).not.toContain("nightgauge/nightgauge");
      }
    });

    it("autonomousRun falls back to folder identity when WorkspaceManager returns no repos", async () => {
      const mockWM = buildMockWorkspaceManager([]); // empty — no manifest repos

      registerAutonomousCommands(mockLogger, mockStatusBar, null, null, undefined, mockWM as any);

      const handler = getHandlerById("nightgauge.autonomousRun");
      await handler();

      // Falls back to getRepoIdentity (mocked to return nightgauge/nightgauge)
      expect(mockIpc.autonomousStart).toHaveBeenCalledWith(
        expect.arrayContaining(["nightgauge/nightgauge"])
      );
    });

    it("autonomousRun falls back to folder identity when no WorkspaceManager is provided", async () => {
      registerAutonomousCommands(mockLogger, mockStatusBar, null, null, undefined, null);

      const handler = getHandlerById("nightgauge.autonomousRun");
      await handler();

      expect(mockIpc.autonomousStart).toHaveBeenCalledWith(
        expect.arrayContaining(["nightgauge/nightgauge"])
      );
    });

    it("autonomousResume uses WorkspaceManager repos when manifest repos are available", async () => {
      const mockWM = buildMockWorkspaceManager([{ owner: "acme", repo: "acmeapp-infra" }]);

      registerAutonomousCommands(mockLogger, mockStatusBar, null, null, undefined, mockWM as any);

      const handler = getHandlerById("nightgauge.autonomousResume");
      await handler();

      expect(mockIpc.autonomousResume).toHaveBeenCalledWith(
        expect.arrayContaining(["acme/acmeapp-infra"])
      );
      const callArg = mockIpc.autonomousResume.mock.calls[0][0];
      if (Array.isArray(callArg)) {
        expect(callArg).not.toContain("nightgauge/nightgauge");
      }
    });

    it("autonomousResume falls back to folder identity when WorkspaceManager returns no repos", async () => {
      const mockWM = buildMockWorkspaceManager([]);

      registerAutonomousCommands(mockLogger, mockStatusBar, null, null, undefined, mockWM as any);

      const handler = getHandlerById("nightgauge.autonomousResume");
      await handler();

      expect(mockIpc.autonomousResume).toHaveBeenCalledWith(
        expect.arrayContaining(["nightgauge/nightgauge"])
      );
    });

    it("does not include foreign workspace repos from enabled_repos when WorkspaceManager scopes correctly", async () => {
      // WorkspaceManager returns only acmeapp repos (current workspace)
      const mockWM = buildMockWorkspaceManager([{ owner: "acme", repo: "acmeapp-infra" }]);

      registerAutonomousCommands(mockLogger, mockStatusBar, null, null, undefined, mockWM as any);

      const handler = getHandlerById("nightgauge.autonomousRun");
      await handler();

      const callArg = mockIpc.autonomousStart.mock.calls[0][0];
      // The call arg is workspaceRepos (before intersectWithEnabledRepos filters further).
      // With no enabledReposConfigService, the full workspace list is passed through.
      if (Array.isArray(callArg)) {
        // Foreign repos (nightgauge-*) must not appear in acmeapp workspace scope
        expect(callArg.every((r: string) => !r.includes("nightgauge"))).toBe(true);
      }
    });
  });

  // ── autonomousStop ────────────────────────────────────────────────────

  describe("autonomousStop", () => {
    beforeEach(() => {
      registerAutonomousCommands(mockLogger, mockStatusBar, null);
    });

    it("stops autonomous mode when user confirms", async () => {
      vi.mocked(vscode.window.showWarningMessage).mockResolvedValue("Stop" as any);
      mockIpc.autonomousStop.mockResolvedValue(
        createMockStatus({
          status: "complete",
          completed: [
            {
              repo: "r",
              number: 1,
              title: "T",
              completedAt: new Date().toISOString(),
            },
          ],
          failed: [],
          remaining: 0,
        })
      );

      const handler = getHandlerById("nightgauge.autonomousStop");
      await handler();

      expect(mockIpc.autonomousStop).toHaveBeenCalled();
      expect(mockStatusBar.showAutonomousComplete).toHaveBeenCalledWith(1);
      expect(vscode.commands.executeCommand).toHaveBeenCalledWith(
        "setContext",
        "nightgauge.autonomousRunning",
        false
      );
    });

    it("shows completion info message with counts after stopping", async () => {
      vi.mocked(vscode.window.showWarningMessage).mockResolvedValue("Stop" as any);
      mockIpc.autonomousStop.mockResolvedValue(
        createMockStatus({
          status: "complete",
          completed: [
            {
              repo: "r",
              number: 1,
              title: "A",
              completedAt: new Date().toISOString(),
            },
            {
              repo: "r",
              number: 2,
              title: "B",
              completedAt: new Date().toISOString(),
            },
          ],
          failed: [
            {
              repo: "r",
              number: 3,
              title: "C",
              failedAt: new Date().toISOString(),
            },
          ],
          remaining: 0,
        })
      );

      const handler = getHandlerById("nightgauge.autonomousStop");
      await handler();

      expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
        expect.stringContaining("2")
      );
    });

    it("does nothing when user cancels the stop confirmation", async () => {
      vi.mocked(vscode.window.showWarningMessage).mockResolvedValue(undefined as any);

      const handler = getHandlerById("nightgauge.autonomousStop");
      await handler();

      expect(mockIpc.autonomousStop).not.toHaveBeenCalled();
    });

    it("shows error message and logs when IPC stop fails", async () => {
      vi.mocked(vscode.window.showWarningMessage).mockResolvedValue("Stop" as any);
      mockIpc.autonomousStop.mockRejectedValue(new Error("Stop error"));

      const handler = getHandlerById("nightgauge.autonomousStop");
      await handler();

      expect(vscode.window.showErrorMessage).toHaveBeenCalledWith(
        expect.stringContaining("Stop error")
      );
      expect(mockLogger.error).toHaveBeenCalled();
    });
  });

  // ── autonomousStatus ──────────────────────────────────────────────────

  describe("autonomousStatus", () => {
    beforeEach(() => {
      registerAutonomousCommands(mockLogger, mockStatusBar, null);
    });

    it("clears and populates the output channel with status, then shows it", async () => {
      mockIpc.autonomousStatus.mockResolvedValue(createMockStatus());

      const handler = getHandlerById("nightgauge.autonomousStatus");
      await handler();

      const channel = getMockChannel();
      expect(channel.clear).toHaveBeenCalled();
      expect(channel.show).toHaveBeenCalled();
      expect(channel.appendLine).toHaveBeenCalledWith(expect.stringContaining("Status"));
    });

    it("sets context key to true and updates status bar when status is running", async () => {
      mockIpc.autonomousStatus.mockResolvedValue(
        createMockStatus({
          status: "running",
          running: [
            {
              repo: "r",
              number: 1,
              title: "T",
              startedAt: new Date().toISOString(),
            },
          ],
          remaining: 4,
        })
      );

      const handler = getHandlerById("nightgauge.autonomousStatus");
      await handler();

      expect(mockStatusBar.showAutonomousRunning).toHaveBeenCalledWith(1, 4);
      expect(vscode.commands.executeCommand).toHaveBeenCalledWith(
        "setContext",
        "nightgauge.autonomousRunning",
        true
      );
    });

    it("updates status bar to paused when status is paused", async () => {
      mockIpc.autonomousStatus.mockResolvedValue(createMockStatus({ status: "paused" }));

      const handler = getHandlerById("nightgauge.autonomousStatus");
      await handler();

      expect(mockStatusBar.showAutonomousPaused).toHaveBeenCalled();
    });

    it("shows safety trip reason and prompts resume when status is safety_tripped", async () => {
      vi.mocked(vscode.window.showWarningMessage).mockResolvedValue("Resume" as any);
      mockIpc.autonomousStatus.mockResolvedValue(
        createMockStatus({
          status: "safety_tripped",
          safety: {
            tripReason: "Health gate",
            consecutiveFailures: 4,
            tokensUsed: 0,
          },
        })
      );

      const handler = getHandlerById("nightgauge.autonomousStatus");
      await handler();

      expect(vscode.window.showWarningMessage).toHaveBeenCalledWith(
        expect.stringContaining("Health gate"),
        "Resume",
        "Dismiss"
      );
      expect(vscode.commands.executeCommand).toHaveBeenCalledWith("nightgauge.autonomousResume");
    });

    it("does not execute resume when user dismisses the safety-tripped prompt", async () => {
      vi.mocked(vscode.window.showWarningMessage).mockResolvedValue("Dismiss" as any);
      mockIpc.autonomousStatus.mockResolvedValue(createMockStatus({ status: "safety_tripped" }));

      const handler = getHandlerById("nightgauge.autonomousStatus");
      await handler();

      const resumeCalls = (vscode.commands.executeCommand as any).mock.calls.filter(
        (c: any[]) => c[0] === "nightgauge.autonomousResume"
      );
      expect(resumeCalls).toHaveLength(0);
    });

    it("sets context key to false and shows completed count when status is complete", async () => {
      mockIpc.autonomousStatus.mockResolvedValue(
        createMockStatus({
          status: "complete",
          completed: [
            {
              repo: "r",
              number: 1,
              title: "T",
              completedAt: new Date().toISOString(),
            },
          ],
          remaining: 0,
        })
      );

      const handler = getHandlerById("nightgauge.autonomousStatus");
      await handler();

      expect(mockStatusBar.showAutonomousComplete).toHaveBeenCalledWith(1);
      expect(vscode.commands.executeCommand).toHaveBeenCalledWith(
        "setContext",
        "nightgauge.autonomousRunning",
        false
      );
    });

    it("sets context key false for stopped/budget_exhausted default case", async () => {
      mockIpc.autonomousStatus.mockResolvedValue(
        createMockStatus({ status: "budget_exhausted", remaining: 0 })
      );

      const handler = getHandlerById("nightgauge.autonomousStatus");
      await handler();

      expect(vscode.commands.executeCommand).toHaveBeenCalledWith(
        "setContext",
        "nightgauge.autonomousRunning",
        false
      );
    });

    it("shows error message and logs when IPC fails", async () => {
      mockIpc.autonomousStatus.mockRejectedValue(new Error("IPC error"));

      const handler = getHandlerById("nightgauge.autonomousStatus");
      await handler();

      expect(vscode.window.showErrorMessage).toHaveBeenCalledWith(
        expect.stringContaining("IPC error")
      );
      expect(mockLogger.error).toHaveBeenCalled();
    });
  });

  // ── autonomous.dispatch event listener ────────────────────────────────

  describe("autonomous.dispatch event listener", () => {
    let dispatchHandler: ((raw: unknown) => void) | null;

    beforeEach(() => {
      dispatchHandler = null;
      mockIpc.on.mockImplementation((event: string, handler: any) => {
        if (event === "autonomous.dispatch") {
          dispatchHandler = handler;
        }
        return { dispose: vi.fn() };
      });
    });

    it("enqueues the issue via queue service when dispatch event received", async () => {
      const mockQueueService = createMockQueueService();
      registerAutonomousCommands(mockLogger, mockStatusBar, mockQueueService);

      dispatchHandler?.({
        owner: "nightgauge",
        repo: "nightgauge",
        issueNumber: 42,
        title: "Test Issue",
      });

      // Let the async enqueue promise settle
      await Promise.resolve();
      await Promise.resolve();

      expect(mockQueueService.enqueue).toHaveBeenCalledWith(42, "Test Issue", [], undefined, {
        repoOverride: { owner: "nightgauge", repo: "nightgauge" },
      });
    });

    it("logs the dispatch event to the output channel", async () => {
      const mockQueueService = createMockQueueService();
      registerAutonomousCommands(mockLogger, mockStatusBar, mockQueueService);

      dispatchHandler?.({
        owner: "nightgauge",
        repo: "nightgauge",
        issueNumber: 7,
        title: "My Issue",
      });

      await Promise.resolve();

      const channel = getMockChannel();
      expect(channel.appendLine).toHaveBeenCalledWith(expect.stringContaining("7"));
    });

    it("logs info after successful enqueue", async () => {
      const mockQueueService = createMockQueueService();
      mockQueueService.enqueue.mockResolvedValue({ position: 3 });
      registerAutonomousCommands(mockLogger, mockStatusBar, mockQueueService);

      dispatchHandler?.({
        owner: "nightgauge",
        repo: "nightgauge",
        issueNumber: 99,
        title: "Issue 99",
      });

      // Let promise chain settle
      await new Promise((r) => setTimeout(r, 0));

      expect(mockLogger.info).toHaveBeenCalledWith(
        "Autonomous dispatch enqueued",
        expect.objectContaining({ issueNumber: 99 })
      );
    });

    it("logs warning when queue service is null", async () => {
      registerAutonomousCommands(mockLogger, mockStatusBar, null);

      dispatchHandler?.({
        owner: "nightgauge",
        repo: "nightgauge",
        issueNumber: 10,
        title: "No Queue",
      });

      await Promise.resolve();

      expect(mockLogger.warn).toHaveBeenCalledWith(expect.stringContaining("no queue service"));
    });

    it("logs error when enqueue promise rejects", async () => {
      const mockQueueService = createMockQueueService();
      mockQueueService.enqueue.mockRejectedValue(new Error("Queue full"));
      registerAutonomousCommands(mockLogger, mockStatusBar, mockQueueService);

      dispatchHandler?.({
        owner: "nightgauge",
        repo: "nightgauge",
        issueNumber: 5,
        title: "Overflow Issue",
      });

      await new Promise((r) => setTimeout(r, 0));

      expect(mockLogger.error).toHaveBeenCalledWith(
        "Autonomous dispatch failed",
        expect.objectContaining({ issueNumber: 5 })
      );
    });

    it("passes malformed data through to queue service without validation", async () => {
      const mockQueueService = createMockQueueService();
      registerAutonomousCommands(mockLogger, mockStatusBar, mockQueueService);

      // Malformed: missing owner/repo fields
      dispatchHandler?.({ issueNumber: 1, title: "Bad Data" });

      await new Promise((r) => setTimeout(r, 0));

      // Queue service still called — no pre-validation
      expect(mockQueueService.enqueue).toHaveBeenCalled();
    });
  });

  // ── disposeAutonomousOutputChannel ────────────────────────────────────

  describe("disposeAutonomousOutputChannel", () => {
    it("disposes the output channel if it was created", async () => {
      // Trigger lazy channel creation by running a command
      registerAutonomousCommands(mockLogger, mockStatusBar, null);
      mockIpc.autonomousStatus.mockResolvedValue(createMockStatus());
      const handler = getHandlerById("nightgauge.autonomousStatus");
      await handler();

      const channel = getMockChannel();
      expect(channel).toBeDefined();

      disposeAutonomousOutputChannel();
      expect(channel.dispose).toHaveBeenCalled();
    });

    it("is a no-op when no output channel was created", () => {
      // Should not throw
      expect(() => disposeAutonomousOutputChannel()).not.toThrow();
    });
  });
});

// ---------------------------------------------------------------------------
// Helper function unit tests (formatStatus, formatElapsed via output channel)
// ---------------------------------------------------------------------------

describe("formatStatus (via autonomousStatus command output)", () => {
  let mockLogger: Logger;
  let mockStatusBar: StatusBarManager;
  let mockIpc: any;

  beforeEach(() => {
    vi.clearAllMocks();
    disposeAutonomousOutputChannel();

    mockLogger = createMockLogger();
    mockStatusBar = createMockStatusBar();

    mockIpc = {
      on: vi.fn(() => ({ dispose: vi.fn() })),
      onDidChangeStatus: vi.fn(() => ({ dispose: vi.fn() })),
      autonomousStart: vi.fn(),
      autonomousStatus: vi.fn(),
      autonomousPause: vi.fn(),
      autonomousResume: vi.fn(),
      autonomousStop: vi.fn(),
    };
    (IpcClient.getInstance as any).mockReturnValue(mockIpc);
  });

  async function runStatusWith(status: AutonomousStatusResult): Promise<string[]> {
    registerAutonomousCommands(mockLogger, mockStatusBar, null);
    mockIpc.autonomousStatus.mockResolvedValue(status);
    vi.mocked(vscode.window.showWarningMessage).mockResolvedValue("Dismiss" as any);

    const handler = getHandlerById("nightgauge.autonomousStatus");
    await handler();

    const channel = getMockChannel();
    return channel.appendLine.mock.calls.map((c: any[]) => c[0]);
  }

  it("includes safety trip reason and consecutive failures when safety_tripped", async () => {
    const lines = await runStatusWith(
      createMockStatus({
        status: "safety_tripped",
        safety: {
          tripReason: "Rate limit exceeded",
          consecutiveFailures: 7,
          tokensUsed: 50_000,
        },
      })
    );
    const combined = lines.join("\n");
    expect(combined).toContain("Rate limit exceeded");
    expect(combined).toContain("7");
  });

  it("includes budget percentage when tokensCeiling > 0", async () => {
    const lines = await runStatusWith(
      createMockStatus({
        tokensSpent: 300_000,
        tokensCeiling: 600_000,
      })
    );
    const combined = lines.join("\n");
    expect(combined).toContain("50%");
  });

  it("shows 'no ceiling' budget when tokensCeiling is 0 and tokens > 0", async () => {
    const lines = await runStatusWith(
      createMockStatus({
        tokensSpent: 12_345,
        tokensCeiling: 0,
      })
    );
    const combined = lines.join("\n");
    expect(combined).toContain("no ceiling");
  });

  it("lists running items with repo and number", async () => {
    const lines = await runStatusWith(
      createMockStatus({
        status: "running",
        running: [
          {
            repo: "my-repo",
            number: 42,
            title: "Fix bug",
            startedAt: new Date(Date.now() - 120_000).toISOString(),
          },
        ],
      })
    );
    const combined = lines.join("\n");
    expect(combined).toContain("my-repo#42");
  });

  it("lists completed items", async () => {
    const lines = await runStatusWith(
      createMockStatus({
        completed: [
          {
            repo: "r",
            number: 1,
            title: "Done",
            completedAt: new Date().toISOString(),
          },
        ],
      })
    );
    const combined = lines.join("\n");
    expect(combined).toContain("r#1");
  });

  it("lists failed items with reason", async () => {
    const lines = await runStatusWith(
      createMockStatus({
        failed: [
          {
            repo: "r",
            number: 2,
            title: "Fail",
            failedAt: new Date().toISOString(),
            reason: "Build error",
          },
        ],
      })
    );
    const combined = lines.join("\n");
    expect(combined).toContain("Build error");
  });

  it("lists remaining count when remaining > 0", async () => {
    const lines = await runStatusWith(createMockStatus({ remaining: 12 }));
    const combined = lines.join("\n");
    expect(combined).toContain("12");
  });

  // ─── Session-vs-history separation (Issue: autonomous status display) ────

  it("separates 'this session' from lifetime history by startedAt timestamp", async () => {
    const now = Date.now();
    const sessionStart = new Date(now - 60_000).toISOString(); // 1 min ago

    const lines = await runStatusWith(
      createMockStatus({
        startedAt: sessionStart,
        completed: [
          // One before session start (historical)
          {
            repo: "r",
            number: 100,
            title: "Old",
            completedAt: new Date(now - 2 * 86_400_000).toISOString(), // 2 days ago
          },
          // One after session start (this session)
          {
            repo: "r",
            number: 200,
            title: "Fresh",
            completedAt: new Date(now - 30_000).toISOString(),
          },
        ],
        failed: [],
      })
    );
    const combined = lines.join("\n");
    // This-session block should only reference #200.
    expect(combined).toMatch(/This session \(1 completed, 0 failed\)/);
    expect(combined).toContain("r#200: Fresh");
    // History block should account for the old one.
    expect(combined).toMatch(/History \(previous sessions\)/);
    expect(combined).toMatch(/1 completed/);
  });

  it("shows 'nothing completed or failed yet' placeholder when session is empty", async () => {
    const lines = await runStatusWith(
      createMockStatus({
        startedAt: new Date(Date.now() - 1_000).toISOString(),
        completed: [],
        failed: [],
      })
    );
    const combined = lines.join("\n");
    expect(combined).toContain("This session — nothing completed or failed yet");
  });

  it("deduplicates repeat failures with count and last-failure age", async () => {
    const now = Date.now();
    const sessionStart = new Date(now - 1_000).toISOString();
    const makeFail = (n: number, ageMs: number) => ({
      repo: "r",
      number: n,
      title: "",
      failedAt: new Date(now - ageMs).toISOString(),
      reason: "pipeline failure",
    });

    const lines = await runStatusWith(
      createMockStatus({
        startedAt: sessionStart,
        failed: [
          makeFail(2530, 3 * 86_400_000),
          makeFail(2530, 2.5 * 86_400_000),
          makeFail(2530, 2 * 86_400_000),
          makeFail(2595, 4 * 86_400_000), // single failure, should NOT appear in repeat list
        ],
      })
    );
    const combined = lines.join("\n");
    expect(combined).toMatch(/Repeat failures/);
    expect(combined).toMatch(/r#2530 × 3 failures/);
    // Single-failure issues must not appear in the repeat failures section.
    const repeatIndex = combined.indexOf("Repeat failures");
    expect(combined.slice(repeatIndex)).not.toContain("#2595");
  });

  it("omits Repeat failures section when every historical failure is unique", async () => {
    const now = Date.now();
    const lines = await runStatusWith(
      createMockStatus({
        startedAt: new Date(now - 1_000).toISOString(),
        failed: [
          {
            repo: "r",
            number: 1,
            title: "",
            failedAt: new Date(now - 86_400_000).toISOString(),
            reason: "pipeline failure",
          },
        ],
      })
    );
    const combined = lines.join("\n");
    expect(combined).not.toContain("Repeat failures");
    // Summary line must still count the one unique failure.
    expect(combined).toMatch(/1 failure on 1 unique issue/);
  });

  it("renders blank-title issues as '{repo}#{number}' without dangling colon", async () => {
    const now = Date.now();
    const lines = await runStatusWith(
      createMockStatus({
        startedAt: new Date(now - 1_000).toISOString(),
        failed: [
          {
            repo: "r",
            number: 42,
            title: "",
            failedAt: new Date(now - 500).toISOString(),
            reason: "pipeline failure",
          },
        ],
      })
    );
    const combined = lines.join("\n");
    expect(combined).toContain("r#42");
    // Regression guard: the old format produced "r#42:  — pipeline failure".
    expect(combined).not.toContain("r#42: ");
  });

  it("reports elapsed time in days for multi-day gaps", async () => {
    const now = Date.now();
    const lines = await runStatusWith(
      createMockStatus({
        startedAt: new Date(now - 1_000).toISOString(),
        failed: [
          {
            repo: "r",
            number: 1,
            title: "",
            failedAt: new Date(now - 3 * 86_400_000).toISOString(),
            reason: "pipeline failure",
          },
        ],
      })
    );
    const combined = lines.join("\n");
    // History summary should surface the 3-day age (d unit) instead of ~72h.
    expect(combined).toMatch(/3d/);
  });

  it("writes the new 'Session started ... · N total cycles' header", async () => {
    const lines = await runStatusWith(
      createMockStatus({
        startedAt: new Date(Date.now() - 65_000).toISOString(),
        cyclesRun: 3356,
      })
    );
    const combined = lines.join("\n");
    expect(combined).toMatch(/Session started 1m ago · 3,356 total cycles/);
  });
});

describe("formatElapsed (via running items in status output)", () => {
  let mockLogger: Logger;
  let mockStatusBar: StatusBarManager;
  let mockIpc: any;

  beforeEach(() => {
    vi.clearAllMocks();
    disposeAutonomousOutputChannel();

    mockLogger = createMockLogger();
    mockStatusBar = createMockStatusBar();

    mockIpc = {
      on: vi.fn(() => ({ dispose: vi.fn() })),
      onDidChangeStatus: vi.fn(() => ({ dispose: vi.fn() })),
      autonomousStart: vi.fn(),
      autonomousStatus: vi.fn(),
      autonomousPause: vi.fn(),
      autonomousResume: vi.fn(),
      autonomousStop: vi.fn(),
    };
    (IpcClient.getInstance as any).mockReturnValue(mockIpc);
  });

  async function getOutputForRunning(startedAtOffset: number): Promise<string> {
    registerAutonomousCommands(mockLogger, mockStatusBar, null);
    const startedAt = new Date(Date.now() - startedAtOffset).toISOString();
    mockIpc.autonomousStatus.mockResolvedValue(
      createMockStatus({
        status: "running",
        running: [{ repo: "r", number: 1, title: "T", startedAt }],
      })
    );

    const handler = getHandlerById("nightgauge.autonomousStatus");
    await handler();

    const channel = getMockChannel();
    return channel.appendLine.mock.calls.map((c: any[]) => c[0]).join("\n");
  }

  it("shows seconds format for < 60 seconds elapsed", async () => {
    const output = await getOutputForRunning(30_000); // 30 seconds ago
    expect(output).toMatch(/\d+s/);
  });

  it("shows minutes format for 1-59 minutes elapsed", async () => {
    const output = await getOutputForRunning(5 * 60_000); // 5 minutes ago
    expect(output).toMatch(/\d+m/);
  });

  it("shows hours format for >= 60 minutes elapsed", async () => {
    const output = await getOutputForRunning(90 * 60_000); // 90 minutes ago
    expect(output).toMatch(/1h 30m/);
  });

  it("shows whole hours with no minutes component when minutes = 0", async () => {
    const output = await getOutputForRunning(120 * 60_000); // 120 minutes ago
    expect(output).toMatch(/2h(?! \d+m)/);
  });
});

// ---------------------------------------------------------------------------
// formatStatus — backend_disconnected state
// ---------------------------------------------------------------------------

describe("formatStatus — backend_disconnected", () => {
  let mockLogger: Logger;
  let mockStatusBar: StatusBarManager;
  let mockIpc: any;

  beforeEach(() => {
    vi.clearAllMocks();
    disposeAutonomousOutputChannel();

    mockLogger = createMockLogger();
    mockStatusBar = createMockStatusBar();

    mockIpc = {
      on: vi.fn(() => ({ dispose: vi.fn() })),
      onDidChangeStatus: vi.fn(() => ({ dispose: vi.fn() })),
      autonomousStart: vi.fn(),
      autonomousStatus: vi.fn(),
      autonomousPause: vi.fn(),
      autonomousResume: vi.fn(),
      autonomousStop: vi.fn(),
    };
    (IpcClient.getInstance as any).mockReturnValue(mockIpc);
  });

  async function runStatusWith(status: AutonomousStatusResult): Promise<string[]> {
    registerAutonomousCommands(mockLogger, mockStatusBar, null);
    mockIpc.autonomousStatus.mockResolvedValue(status);
    vi.mocked(vscode.window.showWarningMessage).mockResolvedValue("Dismiss" as any);
    const handler = getHandlerById("nightgauge.autonomousStatus");
    await handler();
    const channel = getMockChannel();
    return channel.appendLine.mock.calls.map((c: any[]) => c[0]);
  }

  it("shows BACKEND DISCONNECTED heading for backend_disconnected status", async () => {
    const lines = await runStatusWith(createMockStatus({ status: "backend_disconnected" }));
    const combined = lines.join("\n");
    expect(combined).toContain("BACKEND DISCONNECTED");
  });

  it("does not include running/remaining/history sections for backend_disconnected", async () => {
    const lines = await runStatusWith(
      createMockStatus({
        status: "backend_disconnected",
        running: [{ repo: "r", number: 1, title: "T", startedAt: new Date().toISOString() }],
      })
    );
    const combined = lines.join("\n");
    // Short-circuit: only disconnected message, no running section
    expect(combined).not.toContain("Running (");
    expect(combined).not.toContain("This session");
  });

  it("mentions autonomous-exits.jsonl crash log path", async () => {
    const lines = await runStatusWith(createMockStatus({ status: "backend_disconnected" }));
    const combined = lines.join("\n");
    expect(combined).toContain("autonomous-exits.jsonl");
  });
});

// ---------------------------------------------------------------------------
// Liveness probe — handleBackendDisconnected triggered via onDidChangeStatus
// ---------------------------------------------------------------------------

describe("liveness probe — socket disconnect triggers handleBackendDisconnected", () => {
  let mockLogger: Logger;
  let mockStatusBar: StatusBarManager;
  let mockIpc: any;
  let capturedStatusListener: ((connected: boolean) => void) | null;

  beforeEach(() => {
    vi.clearAllMocks();
    vi.useFakeTimers();
    disposeAutonomousOutputChannel();
    // Reset module-level timer and liveness state so tests don't bleed into each other.
    resetWatchdogStateForTest();

    capturedStatusListener = null;
    mockLogger = createMockLogger();
    mockStatusBar = createMockStatusBar();

    mockIpc = {
      on: vi.fn(() => ({ dispose: vi.fn() })),
      onDidChangeStatus: vi.fn((cb: (connected: boolean) => void) => {
        capturedStatusListener = cb;
        return { dispose: vi.fn() };
      }),
      autonomousStart: vi.fn(() => Promise.resolve(createMockStatus())),
      autonomousStatus: vi.fn(() => Promise.resolve(createMockStatus())),
      autonomousPause: vi.fn(() => Promise.resolve(createMockStatus({ status: "paused" }))),
      autonomousResume: vi.fn(() => Promise.resolve(createMockStatus())),
      autonomousStop: vi.fn(() =>
        Promise.resolve(createMockStatus({ status: "complete", remaining: 0 }))
      ),
      autonomousClearIssueFailures: vi.fn(() => Promise.resolve({ cleared: 0 })),
    };

    (IpcClient.getInstance as any).mockReturnValue(mockIpc);
  });

  afterEach(() => {
    vi.clearAllTimers();
    vi.useRealTimers();
    resetWatchdogStateForTest();
  });

  it("subscribes to onDidChangeStatus when autonomous mode starts", async () => {
    vi.mocked(vscode.window.showWarningMessage).mockResolvedValue("Start" as any);
    mockIpc.autonomousStatus.mockResolvedValue(createMockStatus({ status: "stopped" }));

    registerAutonomousCommands(mockLogger, mockStatusBar, null);
    const handler = getHandlerById("nightgauge.autonomousRun");
    await handler();

    expect(mockIpc.onDidChangeStatus).toHaveBeenCalled();
  });

  it("calls showAutonomousDisconnected when socket fires connected=false", async () => {
    vi.mocked(vscode.window.showWarningMessage)
      .mockResolvedValueOnce("Start" as any)
      // Second call is the disconnect notification — dismiss it
      .mockResolvedValueOnce("Dismiss" as any);
    mockIpc.autonomousStatus.mockResolvedValue(createMockStatus({ status: "stopped" }));

    registerAutonomousCommands(mockLogger, mockStatusBar, null);
    const handler = getHandlerById("nightgauge.autonomousRun");
    await handler();

    // Simulate the backend process dying
    capturedStatusListener?.(false);
    // handleBackendDisconnected runs synchronously up to its first await.
    // showAutonomousDisconnected is called before that point.
    await Promise.resolve();

    expect(mockStatusBar.showAutonomousDisconnected).toHaveBeenCalled();
  });

  it("sets autonomousRunning context to false on disconnect", async () => {
    vi.mocked(vscode.window.showWarningMessage)
      .mockResolvedValueOnce("Start" as any)
      .mockResolvedValueOnce("Dismiss" as any);
    mockIpc.autonomousStatus.mockResolvedValue(createMockStatus({ status: "stopped" }));

    registerAutonomousCommands(mockLogger, mockStatusBar, null);
    const handler = getHandlerById("nightgauge.autonomousRun");
    await handler();

    capturedStatusListener?.(false);
    await Promise.resolve();

    expect(vscode.commands.executeCommand).toHaveBeenCalledWith(
      "setContext",
      "nightgauge.autonomousRunning",
      false
    );
  });

  it("shows warning message with Restart action on disconnect", async () => {
    vi.mocked(vscode.window.showWarningMessage)
      .mockResolvedValueOnce("Start" as any)
      .mockResolvedValueOnce("Dismiss" as any);
    mockIpc.autonomousStatus.mockResolvedValue(createMockStatus({ status: "stopped" }));

    registerAutonomousCommands(mockLogger, mockStatusBar, null);
    const handler = getHandlerById("nightgauge.autonomousRun");
    await handler();

    capturedStatusListener?.(false);
    await Promise.resolve();

    expect(vscode.window.showWarningMessage).toHaveBeenCalledWith(
      expect.stringContaining("backend lost connection"),
      "Restart",
      "Dismiss"
    );
  });

  it("does not fire handleBackendDisconnected twice for same disconnect event", async () => {
    vi.mocked(vscode.window.showWarningMessage)
      .mockResolvedValueOnce("Start" as any)
      .mockResolvedValue("Dismiss" as any);
    mockIpc.autonomousStatus.mockResolvedValue(createMockStatus({ status: "stopped" }));

    registerAutonomousCommands(mockLogger, mockStatusBar, null);
    const handler = getHandlerById("nightgauge.autonomousRun");
    await handler();

    // Fire disconnect twice
    capturedStatusListener?.(false);
    capturedStatusListener?.(false);
    await Promise.resolve();

    // showAutonomousDisconnected is idempotent — only called once
    expect(mockStatusBar.showAutonomousDisconnected).toHaveBeenCalledTimes(1);
  });

  it("does not trigger disconnect when connected=true fires", async () => {
    vi.mocked(vscode.window.showWarningMessage).mockResolvedValueOnce("Start" as any);
    mockIpc.autonomousStatus.mockResolvedValue(createMockStatus({ status: "stopped" }));

    registerAutonomousCommands(mockLogger, mockStatusBar, null);
    const handler = getHandlerById("nightgauge.autonomousRun");
    await handler();

    // connected=true should not trigger disconnect handling
    capturedStatusListener?.(true);
    await Promise.resolve();

    expect(mockStatusBar.showAutonomousDisconnected).not.toHaveBeenCalled();
  });
});

// ── Issue #3446: Quota-cooldown visibility, prompt-on-start, manual clear ──

import {
  maybeLogCooldownTick,
  _resetCooldownLogTickStateForTests,
  parseFutureCooldown,
} from "../../src/commands/autonomousCommands";

describe("autonomousRun prompt-on-cooldown (#3446)", () => {
  let mockLogger: Logger;
  let mockStatusBar: StatusBarManager;
  let mockIpc: any;

  beforeEach(() => {
    vi.clearAllMocks();
    disposeAutonomousOutputChannel();
    mockLogger = createMockLogger();
    mockStatusBar = createMockStatusBar();
    mockIpc = {
      on: vi.fn(() => ({ dispose: vi.fn() })),
      onDidChangeStatus: vi.fn(() => ({ dispose: vi.fn() })),
      autonomousStart: vi.fn(() => Promise.resolve(createMockStatus())),
      autonomousStatus: vi.fn(() => Promise.resolve(createMockStatus())),
      autonomousPause: vi.fn(() => Promise.resolve(createMockStatus({ status: "paused" }))),
      autonomousResume: vi.fn(() => Promise.resolve(createMockStatus())),
      autonomousStop: vi.fn(() =>
        Promise.resolve(createMockStatus({ status: "complete", remaining: 0 }))
      ),
      autonomousClearQuotaCooldown: vi.fn(() =>
        Promise.resolve({ cleared: true, previousUntil: "2026-05-11T03:31:00Z" })
      ),
    };
    (IpcClient.getInstance as any).mockReturnValue(mockIpc);
    registerAutonomousCommands(mockLogger, mockStatusBar, null);
  });

  it("prompts the user when cooldown is active and they click Start", async () => {
    vi.mocked(vscode.window.showWarningMessage)
      .mockResolvedValueOnce("Start" as any) // initial start confirmation
      .mockResolvedValueOnce("Wait (start anyway)" as any); // cooldown prompt
    const future = new Date(Date.now() + 60 * 60_000).toISOString();
    mockIpc.autonomousStatus.mockResolvedValue(
      createMockStatus({ status: "stopped", quotaCooldownUntil: future })
    );

    const handler = getHandlerById("nightgauge.autonomousRun");
    await handler();

    expect(vscode.window.showWarningMessage).toHaveBeenCalledTimes(2);
    const secondCall = vi.mocked(vscode.window.showWarningMessage).mock.calls[1] as any[];
    expect(secondCall[0]).toBe("Autonomous quota cooldown active");
    // "Wait" path does NOT clear the cooldown — scheduler idles itself.
    expect(mockIpc.autonomousClearQuotaCooldown).not.toHaveBeenCalled();
    expect(mockIpc.autonomousStart).toHaveBeenCalled();
  });

  it("clears the cooldown when user clicks 'Override cooldown and start'", async () => {
    vi.mocked(vscode.window.showWarningMessage)
      .mockResolvedValueOnce("Start" as any)
      .mockResolvedValueOnce("Override cooldown and start" as any);
    const future = new Date(Date.now() + 60 * 60_000).toISOString();
    mockIpc.autonomousStatus.mockResolvedValue(
      createMockStatus({ status: "stopped", quotaCooldownUntil: future })
    );

    const handler = getHandlerById("nightgauge.autonomousRun");
    await handler();

    expect(mockIpc.autonomousClearQuotaCooldown).toHaveBeenCalledTimes(1);
    expect(mockIpc.autonomousStart).toHaveBeenCalled();
  });

  it("does NOT prompt when no cooldown is active", async () => {
    vi.mocked(vscode.window.showWarningMessage).mockResolvedValue("Start" as any);
    mockIpc.autonomousStatus.mockResolvedValue(
      createMockStatus({ status: "stopped" /* no quotaCooldownUntil */ })
    );

    const handler = getHandlerById("nightgauge.autonomousRun");
    await handler();

    // Only the initial "Start autonomous mode?" dialog should fire.
    expect(vscode.window.showWarningMessage).toHaveBeenCalledTimes(1);
    expect(mockIpc.autonomousStart).toHaveBeenCalled();
  });

  it("does NOT prompt when cooldown deadline is already in the past", async () => {
    vi.mocked(vscode.window.showWarningMessage).mockResolvedValue("Start" as any);
    const past = new Date(Date.now() - 60_000).toISOString();
    mockIpc.autonomousStatus.mockResolvedValue(
      createMockStatus({ status: "stopped", quotaCooldownUntil: past })
    );

    const handler = getHandlerById("nightgauge.autonomousRun");
    await handler();

    expect(vscode.window.showWarningMessage).toHaveBeenCalledTimes(1);
    expect(mockIpc.autonomousStart).toHaveBeenCalled();
  });

  it("aborts when user dismisses the cooldown prompt", async () => {
    vi.mocked(vscode.window.showWarningMessage)
      .mockResolvedValueOnce("Start" as any)
      .mockResolvedValueOnce(undefined as any); // Cancel
    const future = new Date(Date.now() + 60 * 60_000).toISOString();
    mockIpc.autonomousStatus.mockResolvedValue(
      createMockStatus({ status: "stopped", quotaCooldownUntil: future })
    );

    const handler = getHandlerById("nightgauge.autonomousRun");
    await handler();

    expect(mockIpc.autonomousStart).not.toHaveBeenCalled();
  });
});

describe("autonomousClearQuotaCooldown command (#3446)", () => {
  let mockLogger: Logger;
  let mockStatusBar: StatusBarManager;
  let mockIpc: any;

  beforeEach(() => {
    vi.clearAllMocks();
    disposeAutonomousOutputChannel();
    mockLogger = createMockLogger();
    mockStatusBar = createMockStatusBar();
    mockIpc = {
      on: vi.fn(() => ({ dispose: vi.fn() })),
      onDidChangeStatus: vi.fn(() => ({ dispose: vi.fn() })),
      autonomousStart: vi.fn(() => Promise.resolve(createMockStatus())),
      autonomousStatus: vi.fn(() =>
        Promise.resolve(
          createMockStatus({ quotaCooldownUntil: new Date(Date.now() + 3_600_000).toISOString() })
        )
      ),
      autonomousPause: vi.fn(() => Promise.resolve(createMockStatus({ status: "paused" }))),
      autonomousResume: vi.fn(() => Promise.resolve(createMockStatus())),
      autonomousStop: vi.fn(() => Promise.resolve(createMockStatus({ status: "complete" }))),
      autonomousClearQuotaCooldown: vi.fn(() =>
        Promise.resolve({ cleared: true, previousUntil: "2026-05-11T03:31:00Z" })
      ),
    };
    (IpcClient.getInstance as any).mockReturnValue(mockIpc);
    registerAutonomousCommands(mockLogger, mockStatusBar, null);
  });

  it("calls IPC clearQuotaCooldown when user confirms warning", async () => {
    vi.mocked(vscode.window.showWarningMessage).mockResolvedValue("Clear Cooldown" as any);
    const handler = getHandlerById("nightgauge.autonomousClearQuotaCooldown");
    await handler();
    expect(mockIpc.autonomousClearQuotaCooldown).toHaveBeenCalledTimes(1);
    expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
      expect.stringMatching(/Quota cooldown cleared/)
    );
  });

  it("does not call IPC when user dismisses the warning", async () => {
    vi.mocked(vscode.window.showWarningMessage).mockResolvedValue(undefined as any);
    const handler = getHandlerById("nightgauge.autonomousClearQuotaCooldown");
    await handler();
    expect(mockIpc.autonomousClearQuotaCooldown).not.toHaveBeenCalled();
  });

  it("reports no-op clear when scheduler reports cleared=false", async () => {
    vi.mocked(vscode.window.showWarningMessage).mockResolvedValue("Clear Cooldown" as any);
    mockIpc.autonomousClearQuotaCooldown.mockResolvedValue({ cleared: false });
    const handler = getHandlerById("nightgauge.autonomousClearQuotaCooldown");
    await handler();
    expect(vscode.window.showInformationMessage).toHaveBeenCalledWith(
      expect.stringMatching(/No active quota cooldown/)
    );
  });
});

describe("maybeLogCooldownTick (#3446)", () => {
  beforeEach(() => {
    _resetCooldownLogTickStateForTests();
  });

  function makeChannel() {
    return {
      appendLine: vi.fn(),
      clear: vi.fn(),
      show: vi.fn(),
      dispose: vi.fn(),
    } as unknown as vscode.OutputChannel;
  }

  it("logs once per unique lastScanAt when quota-cooldown rejection is active", () => {
    const channel = makeChannel();
    const logger = createMockLogger();
    const until = new Date(Date.now() + 3_600_000).toISOString();
    const status = {
      status: "running",
      lastScanAt: "2026-05-11T02:36:07Z",
      quotaCooldownUntil: until,
      quotaCooldownReason: "rate-limit-quota-exhausted",
      lastRejectionReasons: { "quota-cooldown": 1 },
      remaining: 0,
    };

    expect(maybeLogCooldownTick(status, logger, channel)).toBe(true);
    // Second call with the same scan timestamp must not log again.
    expect(maybeLogCooldownTick(status, logger, channel)).toBe(false);
    expect((channel.appendLine as any).mock.calls).toHaveLength(1);
    expect((channel.appendLine as any).mock.calls[0][0]).toMatch(
      /\[cooldown\] Dispatch suppressed/
    );
  });

  it("re-logs when lastScanAt advances", () => {
    const channel = makeChannel();
    const logger = createMockLogger();
    const until = new Date(Date.now() + 3_600_000).toISOString();
    const base = {
      status: "running",
      quotaCooldownUntil: until,
      quotaCooldownReason: "rate-limit-quota-exhausted",
      lastRejectionReasons: { "quota-cooldown": 1 },
      remaining: 0,
    };
    maybeLogCooldownTick({ ...base, lastScanAt: "2026-05-11T02:36:07Z" }, logger, channel);
    maybeLogCooldownTick({ ...base, lastScanAt: "2026-05-11T02:36:37Z" }, logger, channel);
    expect((channel.appendLine as any).mock.calls).toHaveLength(2);
  });

  it("does NOT log when cooldown is absent / not the reject reason", () => {
    const channel = makeChannel();
    const logger = createMockLogger();
    expect(
      maybeLogCooldownTick(
        {
          status: "running",
          lastScanAt: "x",
          lastRejectionReasons: { "blocked-by-open-dep": 4 },
          remaining: 0,
        },
        logger,
        channel
      )
    ).toBe(false);
    expect(channel.appendLine).not.toHaveBeenCalled();
  });
});

describe("parseFutureCooldown (#3446)", () => {
  it("returns Date for parseable future ISO-8601", () => {
    const future = new Date(Date.now() + 60_000);
    const got = parseFutureCooldown(future.toISOString());
    expect(got).toBeInstanceOf(Date);
    expect(got?.getTime()).toBe(future.getTime());
  });

  it("returns null for past ISO-8601", () => {
    const past = new Date(Date.now() - 60_000);
    expect(parseFutureCooldown(past.toISOString())).toBeNull();
  });

  it("returns null for empty / malformed input", () => {
    expect(parseFutureCooldown(undefined)).toBeNull();
    expect(parseFutureCooldown("")).toBeNull();
    expect(parseFutureCooldown("not-a-date")).toBeNull();
  });
});
