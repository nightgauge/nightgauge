/**
 * retryFailedIssue.identityRelease.test.ts
 *
 * ADR-017 Decision 10 (#370) — WHAT A DISPATCH INSTALLS, THE DISPATCH
 * RELEASES.
 *
 * `retryFailedIssue` mints and installs a run identity, then drives the stage
 * through `HeadlessOrchestrator.runStage`. That wrapper's receive-or-mint sees
 * an identity already installed, so it neither mints nor releases; the
 * single-stage path never reaches `firePipelineComplete`, so
 * `notifyPipelineComplete`'s release never fires either. Without the command's
 * own keyed `finally` the id outlives the retry and the SECOND retry of the
 * same issue is refused "already running" for a run that is not running —
 * making `MAX_RETRIES = 3` unreachable past attempt one — while every later
 * `retryStage`/`retryFromPhase` books its transitions under the dead id.
 *
 * Driven against the REAL `PipelineStateService`, because the sibling suite
 * mocks `beginRun` with a no-op that can never refuse.
 *
 * @see docs/decisions/017-runtime-identity-keying.md — Decision 10, F23
 */

import { describe, it, expect, vi, beforeEach } from "vitest";
import type { PipelineStage } from "@nightgauge/sdk";

const {
  mockShowErrorMessage,
  mockShowWarningMessage,
  mockShowInformationMessage,
  mockRegisterCommand,
  mockGetFailedIssue,
  mockRemoveFromFailed,
  mockAddCompleted,
  mockAddFailed,
} = vi.hoisted(() => ({
  mockShowErrorMessage: vi.fn(),
  mockShowWarningMessage: vi.fn(),
  mockShowInformationMessage: vi.fn(),
  mockRegisterCommand: vi.fn(),
  mockGetFailedIssue: vi.fn(),
  mockRemoveFromFailed: vi.fn(),
  mockAddCompleted: vi.fn(),
  mockAddFailed: vi.fn(),
}));

vi.mock("vscode", () => ({
  EventEmitter: class {
    private _handlers: Array<(v: unknown) => void> = [];
    event = (cb: (v: unknown) => void) => {
      this._handlers.push(cb);
      return { dispose: () => {} };
    };
    fire(value: unknown) {
      for (const h of this._handlers) h(value);
    }
    dispose() {}
  },
  Disposable: class {
    dispose() {}
  },
  window: {
    showErrorMessage: mockShowErrorMessage,
    showWarningMessage: mockShowWarningMessage,
    showInformationMessage: mockShowInformationMessage,
    createOutputChannel: vi.fn(() => ({
      appendLine: vi.fn(),
      show: vi.fn(),
      clear: vi.fn(),
      dispose: vi.fn(),
    })),
  },
  commands: { registerCommand: mockRegisterCommand },
  workspace: { workspaceFolders: [{ uri: { fsPath: "/mock/workspace" } }] },
}));

vi.mock("node:fs/promises", () => ({
  access: vi.fn().mockResolvedValue(undefined),
}));

vi.mock("../../src/services/CompletedIssuesService", () => ({
  CompletedIssuesService: {
    getInstance: vi.fn(() => ({
      getFailedIssue: mockGetFailedIssue,
      removeFromFailed: mockRemoveFromFailed,
      addCompleted: mockAddCompleted,
      addFailed: mockAddFailed,
    })),
  },
}));

vi.mock("../../src/utils/configPathResolver", () => ({
  getRepoIdentity: vi.fn().mockResolvedValue({ owner: "nightgauge", repo: "nightgauge" }),
}));

vi.mock("../../src/services/IpcClient", () => ({
  IpcClient: {
    getInstance: () => ({
      on: vi.fn(() => ({ dispose: vi.fn() })),
      call: vi.fn(() => Promise.resolve({ status: "ok" })),
    }),
  },
}));

import { registerRetryFailedIssueCommand } from "../../src/commands/retryFailedIssue";
import { PipelineStateService } from "../../src/services/PipelineStateService";
import type { HeadlessOrchestrator } from "../../src/services/HeadlessOrchestrator";

const failedIssue = {
  issue_number: 100,
  title: "Fix login bug",
  branch: "fix/100-login-bug",
  failed_stage: "feature-dev",
  error: "Test failure",
  retry_count: 0,
  timestamp: "2026-01-01T00:00:00Z",
};

describe("retryFailedIssue — the retry releases the identity it installed", () => {
  let commandHandler: (arg?: number) => Promise<void>;
  let stateService: PipelineStateService;
  let runStage: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    vi.clearAllMocks();
    mockGetFailedIssue.mockReturnValue(failedIssue);

    PipelineStateService.resetInstance();
    stateService = PipelineStateService.createForWorktree("/mock/workspace", 100);

    runStage = vi.fn().mockResolvedValue({
      success: true,
      stage: "feature-dev" as PipelineStage,
      durationMs: 1,
    });

    mockRegisterCommand.mockImplementation(
      (_name: string, handler: (...args: unknown[]) => unknown) => {
        commandHandler = handler as (arg?: number) => Promise<void>;
        return { dispose: vi.fn() };
      }
    );

    registerRetryFailedIssueCommand(
      {
        workspaceState: { get: vi.fn(), update: vi.fn() },
      } as unknown as import("vscode").ExtensionContext,
      { runStage, getIsRunning: vi.fn().mockReturnValue(false) } as unknown as HeadlessOrchestrator,
      stateService
    );
  });

  it("accepts a SECOND retry of the same issue once the first one's stage settled", async () => {
    await commandHandler(100);
    expect(runStage).toHaveBeenCalledTimes(1);
    // Released on the way out — nothing is running, so nothing holds the id.
    expect(stateService.getRunId()).toBeNull();

    await commandHandler(100);

    expect(runStage).toHaveBeenCalledTimes(2);
    expect(stateService.getRunId()).toBeNull();
    expect(mockShowErrorMessage).not.toHaveBeenCalled();
  });

  it("releases on the FAILURE path too", async () => {
    runStage.mockResolvedValue({
      success: false,
      stage: "feature-dev" as PipelineStage,
      durationMs: 1,
      error: new Error("boom"),
    });

    await commandHandler(100);

    expect(mockAddFailed).toHaveBeenCalled();
    expect(stateService.getRunId()).toBeNull();
  });

  it("still REFUSES while the same issue is genuinely LIVE, and does not steal its id", async () => {
    // A live run holds the singleton — this is the F23 case the refusal
    // exists for, and it must survive the release fix.
    const { uuidV7 } = await import("@nightgauge/sdk");
    const live = uuidV7();
    stateService.beginRun(live, "nightgauge/nightgauge", 100);

    await commandHandler(100);

    expect(runStage).not.toHaveBeenCalled();
    expect(mockShowErrorMessage).toHaveBeenCalledWith(
      expect.stringMatching(/Failed to retry issue: Issue #100 is already running/)
    );
    // The keyed release makes the refusal path inert: the live run keeps its id.
    expect(stateService.getRunId()).toBe(live);
  });
});
