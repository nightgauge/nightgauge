/**
 * Production-handler tests for the Retry From Phase command.
 *
 * @see Issue #793
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import * as vscode from "vscode";
import { SchemaVersionMismatch, type PipelineStage, WorktreeMissing } from "@nightgauge/sdk";
import { registerRetryFromPhaseCommand } from "../../src/commands/retryFromPhase";
import type {
  HeadlessOrchestrator,
  PipelineCallbacks,
} from "../../src/services/HeadlessOrchestrator";
import type { PipelineStateService } from "../../src/services/PipelineStateService";
import type { Logger } from "../../src/utils/logger";
import type { StatusBarManager } from "../../src/utils/statusBar";
import type { OutputWindow } from "../../src/views";
import type { PhaseTreeItem } from "../../src/views/items/PhaseTreeItem";
import type { RecoveryPresenter } from "../../src/orchestrator/recovery/RecoveryCoordinator";
import { createPhaseTracker } from "../../src/utils/phaseTracker";
import { createStreamOutputHandler } from "../../src/utils/streamOutputHandler";

const mocks = vi.hoisted(() => ({
  showErrorMessage: vi.fn(),
  showWarningMessage: vi.fn(),
  showInformationMessage: vi.fn(),
  registerCommand: vi.fn(),
  executeCommand: vi.fn(),
  getConfiguration: vi.fn(),
  configGet: vi.fn(),
  createPhaseTracker: vi.fn(),
  createStreamOutputHandler: vi.fn(),
  getNextStage: vi.fn(),
  getStageLabel: vi.fn(),
}));

vi.mock("vscode", () => ({
  window: {
    showErrorMessage: mocks.showErrorMessage,
    showWarningMessage: mocks.showWarningMessage,
    showInformationMessage: mocks.showInformationMessage,
  },
  commands: {
    registerCommand: mocks.registerCommand,
    executeCommand: mocks.executeCommand,
  },
  workspace: {
    getConfiguration: mocks.getConfiguration,
  },
}));

vi.mock("../../src/utils/phaseTracker", () => ({
  createPhaseTracker: mocks.createPhaseTracker,
}));

vi.mock("../../src/utils/streamOutputHandler", () => ({
  createStreamOutputHandler: mocks.createStreamOutputHandler,
}));

vi.mock("../../src/utils/skillRunner", () => ({
  getNextStage: mocks.getNextStage,
  getStageLabel: mocks.getStageLabel,
}));

type RetryFromPhaseHandler = (item?: PhaseTreeItem) => Promise<void>;

function pipelineState(issueNumber = 42) {
  return {
    issue_number: issueNumber,
    stages: {
      "feature-dev": {
        status: "error",
        phases: [
          { name: "implementation", status: "complete" },
          { name: "quality-review", status: "error" },
        ],
      },
      "feature-validate": {
        status: "pending",
        phases: [],
      },
    },
  };
}

function createOrchestrator(): HeadlessOrchestrator {
  return {
    getIsRunning: vi.fn().mockReturnValue(false),
    runStage: vi.fn().mockResolvedValue({
      success: true,
      stage: "feature-dev",
      durationMs: 1000,
    }),
    getRecoveryShape: vi.fn().mockReturnValue(null),
  } as unknown as HeadlessOrchestrator;
}

function createStateService(): PipelineStateService {
  return {
    getState: vi.fn().mockResolvedValue(pipelineState()),
    isPaused: vi.fn().mockResolvedValue(false),
    getExecutionMode: vi.fn().mockResolvedValue("automatic"),
    resumePipeline: vi.fn().mockResolvedValue(undefined),
    setExecutionMode: vi.fn().mockResolvedValue(undefined),
    pausePipeline: vi.fn().mockResolvedValue(undefined),
  } as unknown as PipelineStateService;
}

function createLogger(): Logger {
  return {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  } as unknown as Logger;
}

function createStatusBar(): StatusBarManager {
  return {
    showRunning: vi.fn(),
    showComplete: vi.fn(),
    showError: vi.fn(),
  } as unknown as StatusBarManager;
}

function phaseItem(
  phaseName = "quality-review",
  stage: PipelineStage | undefined = "feature-dev"
): PhaseTreeItem {
  return { phaseName, stage } as unknown as PhaseTreeItem;
}

function firstCallbacks(orchestrator: HeadlessOrchestrator): PipelineCallbacks {
  const callbacks = vi.mocked(orchestrator.runStage).mock.calls[0]?.[2];
  if (!callbacks) {
    throw new Error("runStage callbacks were not supplied");
  }
  return callbacks;
}

describe("registerRetryFromPhaseCommand", () => {
  let orchestrator: HeadlessOrchestrator;
  let stateService: PipelineStateService;
  let logger: Logger;
  let statusBar: StatusBarManager;
  let outputWindow: OutputWindow;
  let presentRecovery: RecoveryPresenter;
  let handler: RetryFromPhaseHandler;

  const register = (
    orchestratorOverride: HeadlessOrchestrator | null = orchestrator,
    stateServiceOverride: PipelineStateService | null = stateService,
    outputOverride: OutputWindow | null = outputWindow,
    recoveryOverride: RecoveryPresenter | undefined = presentRecovery
  ): RetryFromPhaseHandler => {
    handler = undefined as unknown as RetryFromPhaseHandler;
    registerRetryFromPhaseCommand(
      orchestratorOverride,
      stateServiceOverride,
      logger,
      statusBar,
      outputOverride,
      recoveryOverride
    );
    if (!handler) {
      throw new Error("Retry From Phase command handler was not registered");
    }
    return handler;
  };

  beforeEach(() => {
    vi.clearAllMocks();
    orchestrator = createOrchestrator();
    stateService = createStateService();
    logger = createLogger();
    statusBar = createStatusBar();
    outputWindow = { appendLine: vi.fn() } as unknown as OutputWindow;
    presentRecovery = vi.fn();

    mocks.registerCommand.mockImplementation(
      (_command: string, registered: RetryFromPhaseHandler) => {
        handler = registered;
        return { dispose: vi.fn() };
      }
    );
    mocks.getConfiguration.mockReturnValue({ get: mocks.configGet });
    mocks.configGet.mockImplementation((key: string, fallback: unknown) =>
      key === "autoContinue" ? false : fallback
    );
    mocks.getNextStage.mockReturnValue("feature-validate" as PipelineStage);
    mocks.getStageLabel.mockImplementation((stage: string) => stage);
    mocks.createPhaseTracker.mockReturnValue({
      onPhaseDetected: vi.fn(),
      completeStagePhases: vi.fn(),
      completeAllStages: vi.fn(),
    });
    mocks.createStreamOutputHandler.mockReturnValue({
      onStdout: vi.fn(),
      onStderr: vi.fn(),
      flushStage: vi.fn(),
    });

    register();
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("finds the owning stage and passes skipToPhase to production runStage", async () => {
    await handler(phaseItem());

    expect(mocks.registerCommand).toHaveBeenCalledWith(
      "nightgauge.retryFromPhase",
      expect.any(Function)
    );
    expect(orchestrator.runStage).toHaveBeenCalledWith(
      "feature-dev",
      42,
      expect.any(Object),
      "quality-review"
    );
    expect(statusBar.showRunning).toHaveBeenCalledWith("feature-dev");
    expect(statusBar.showComplete).toHaveBeenCalledWith("feature-dev");
  });

  it("uses the clicked item's stage when phase names repeat", async () => {
    const state = pipelineState();
    vi.mocked(stateService.getState).mockResolvedValue({
      ...state,
      stages: {
        "feature-planning": {
          status: "complete",
          phases: [{ name: "quality-review", status: "complete" }],
        },
        ...state.stages,
      },
    } as never);

    await handler(phaseItem("quality-review", "feature-dev"));

    expect(orchestrator.runStage).toHaveBeenCalledWith(
      "feature-dev",
      42,
      expect.any(Object),
      "quality-review"
    );
    expect(orchestrator.runStage).not.toHaveBeenCalledWith(
      "feature-planning",
      expect.anything(),
      expect.anything(),
      expect.anything()
    );

    vi.mocked(orchestrator.runStage).mockClear();
    await handler(phaseItem("quality-review", "feature-planning"));
    expect(orchestrator.runStage).toHaveBeenCalledWith(
      "feature-planning",
      42,
      expect.any(Object),
      "quality-review"
    );
    expect(orchestrator.runStage).not.toHaveBeenCalledWith(
      "feature-dev",
      expect.anything(),
      expect.anything(),
      expect.anything()
    );
  });

  it("rejects unavailable services, active runs, and invalid invocations", async () => {
    await register(null)(phaseItem());
    expect(mocks.showErrorMessage).toHaveBeenCalledWith(
      "Nightgauge orchestrator not initialized. Check extension logs for details."
    );

    handler = register();
    vi.mocked(orchestrator.getIsRunning).mockReturnValue(true);
    await handler(phaseItem());
    expect(mocks.showWarningMessage).toHaveBeenCalledWith(
      "Pipeline is already running. Stop it first or wait for completion."
    );

    vi.mocked(orchestrator.getIsRunning).mockReturnValue(false);
    await handler();
    expect(mocks.showErrorMessage).toHaveBeenCalledWith(
      "Retry From Phase must be invoked from a phase tree item."
    );

    await handler({ phaseName: "quality-review" } as PhaseTreeItem);
    expect(mocks.showErrorMessage).toHaveBeenCalledWith(
      'Could not determine the owning stage for phase "quality-review".'
    );

    await register(orchestrator, null)(phaseItem());
    expect(mocks.showErrorMessage).toHaveBeenCalledWith(
      "Pipeline state service not available. Cannot determine stage for phase."
    );
    expect(orchestrator.runStage).not.toHaveBeenCalled();
  });

  it("rejects missing state, unknown phases, and missing issue identity", async () => {
    vi.mocked(stateService.getState).mockResolvedValueOnce(null);
    await handler(phaseItem());
    expect(mocks.showErrorMessage).toHaveBeenCalledWith(
      "No pipeline state found. Cannot determine stage for phase."
    );

    vi.mocked(stateService.getState).mockResolvedValueOnce(pipelineState());
    await handler(phaseItem("not-a-real-phase"));
    expect(mocks.showErrorMessage).toHaveBeenCalledWith(
      'Could not find phase "not-a-real-phase" in stage "feature-dev".'
    );

    vi.mocked(stateService.getState).mockResolvedValueOnce(pipelineState(0));
    await handler(phaseItem());
    expect(mocks.showErrorMessage).toHaveBeenCalledWith("No issue number found in pipeline state.");
    expect(orchestrator.runStage).not.toHaveBeenCalled();
  });

  it("contains rejected state reads and shows a path-safe error", async () => {
    vi.mocked(stateService.getState).mockRejectedValue(
      new Error("Unable to read /Users/alice/private/state.json")
    );

    await expect(handler(phaseItem())).resolves.toBeUndefined();

    expect(orchestrator.runStage).not.toHaveBeenCalled();
    expect(statusBar.showError).toHaveBeenCalledWith("Unable to read pipeline state");
    expect(mocks.showErrorMessage).toHaveBeenCalledWith(
      "Unable to read pipeline state. Check extension logs for details."
    );
    expect(mocks.showErrorMessage).not.toHaveBeenCalledWith(expect.stringContaining("/Users/"));
    expect(logger.error).toHaveBeenCalledWith(
      "Retry from phase state error",
      expect.objectContaining({ error: expect.stringContaining("state.json") })
    );
  });

  it("wires stream and phase lifecycle callbacks", async () => {
    await handler(phaseItem());
    const callbacks = firstCallbacks(orchestrator);
    const tracker = vi.mocked(createPhaseTracker).mock.results[0]?.value;
    const stream = vi.mocked(createStreamOutputHandler).mock.results[0]?.value;

    callbacks.onStageStart?.("feature-dev");
    callbacks.onStdout?.("feature-dev", "stdout");
    callbacks.onStderr?.("feature-dev", "stderr");
    callbacks.onStageError?.("feature-dev", new Error("stream failed"));
    callbacks.onStageComplete?.("feature-dev", {
      success: true,
      stage: "feature-dev",
      durationMs: 10,
    });

    expect(createPhaseTracker).toHaveBeenCalledWith(stateService);
    expect(createStreamOutputHandler).toHaveBeenCalledWith(outputWindow, {
      onPhaseDetected: tracker.onPhaseDetected,
    });
    expect(stream.onStdout).toHaveBeenCalledWith("feature-dev", "stdout");
    expect(stream.onStderr).toHaveBeenCalledWith("feature-dev", "stderr");
    expect(stream.flushStage).toHaveBeenCalledWith("feature-dev");
    expect(tracker.completeStagePhases).toHaveBeenCalledWith("feature-dev");
    expect(logger.debug).toHaveBeenCalledWith(
      "Retry-from-phase stage started",
      expect.objectContaining({ phase: "quality-review", issueNumber: 42 })
    );
    expect(logger.error).toHaveBeenCalledWith(
      "Retry-from-phase stage error",
      expect.objectContaining({ error: "stream failed" })
    );
  });

  it("honors backward-transition confirmation", async () => {
    await handler(phaseItem());
    const confirm = firstCallbacks(orchestrator).onBackwardTransitionConfirm;
    mocks.showWarningMessage.mockResolvedValueOnce("Continue");

    await expect(confirm?.("feature-dev", "Retry backward?")).resolves.toBe(true);
    expect(mocks.showWarningMessage).toHaveBeenCalledWith(
      "Retry backward?",
      { modal: true },
      "Continue"
    );

    mocks.showWarningMessage.mockResolvedValueOnce(undefined);
    await expect(confirm?.("feature-dev", "Retry backward?")).resolves.toBe(false);
    mocks.showWarningMessage.mockResolvedValueOnce("Cancel");
    await expect(confirm?.("feature-dev", "Retry backward?")).resolves.toBe(false);
  });

  it("auto-continues after phase retry in automatic mode", async () => {
    vi.useFakeTimers();
    mocks.configGet.mockImplementation((key: string, fallback: unknown) => {
      if (key === "autoContinue") return true;
      if (key === "autoContinueDelay") return 20;
      return fallback;
    });

    await handler(phaseItem());
    expect(vscode.commands.executeCommand).not.toHaveBeenCalled();

    await vi.advanceTimersByTimeAsync(19);
    expect(vscode.commands.executeCommand).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(1);

    expect(stateService.isPaused).toHaveBeenCalledOnce();
    expect(stateService.getExecutionMode).toHaveBeenCalledOnce();
    expect(vscode.commands.executeCommand).toHaveBeenCalledWith(
      "nightgauge.runStage",
      "feature-validate"
    );
  });

  it("pauses after a manual continuation is declined", async () => {
    vi.useFakeTimers();
    mocks.configGet.mockImplementation((key: string, fallback: unknown) => {
      if (key === "autoContinue") return true;
      if (key === "autoContinueDelay") return 20;
      return fallback;
    });
    vi.mocked(stateService.getExecutionMode).mockResolvedValue("manual");
    mocks.showInformationMessage.mockResolvedValue(undefined);

    await handler(phaseItem());
    await vi.advanceTimersByTimeAsync(20);

    expect(stateService.pausePipeline).toHaveBeenCalledOnce();
    expect(mocks.showInformationMessage).toHaveBeenCalledWith(
      'Pipeline paused. Run "feature-validate" to continue.'
    );
    expect(vscode.commands.executeCommand).not.toHaveBeenCalled();
  });

  it("reports resolved failures and rejected dispatches", async () => {
    vi.mocked(orchestrator.runStage).mockResolvedValueOnce({
      success: false,
      stage: "feature-dev",
      durationMs: 20,
      error: new Error("phase failed"),
    });
    await handler(phaseItem());
    expect(statusBar.showError).toHaveBeenCalledWith("phase failed");
    expect(mocks.showErrorMessage).toHaveBeenCalledWith(
      'Stage "feature-dev" failed: Error: phase failed'
    );

    vi.mocked(orchestrator.runStage).mockRejectedValueOnce(new Error("dispatch failed"));
    await handler(phaseItem());
    expect(statusBar.showError).toHaveBeenCalledWith("dispatch failed");
    expect(mocks.showErrorMessage).toHaveBeenCalledWith("Retry from phase error: dispatch failed");
  });

  it("suppresses path-bearing recovery errors in resolved and rejected runs", async () => {
    const recoveryError = new WorktreeMissing(
      "/Users/alice/private/release-worktree",
      "fix/private-branch"
    );
    const payload = {
      issueNumber: 42,
      triggeringStage: "feature-dev",
      errorKind: "WORKTREE_MISSING" as const,
      errorDetail: recoveryError.message,
      runState: "aborted" as const,
      availableActions: ["open-run-state-directory" as const, "cancel" as const],
    };
    vi.mocked(orchestrator.runStage).mockImplementationOnce(async (_stage, _issue, callbacks) => {
      callbacks?.onRecoveryRequired?.(payload);
      return {
        success: false,
        stage: "feature-dev",
        durationMs: 20,
        error: recoveryError,
      };
    });

    await handler(phaseItem());

    expect(statusBar.showError).toHaveBeenCalledWith("Recovery required");
    expect(mocks.showErrorMessage).not.toHaveBeenCalled();
    expect(presentRecovery).toHaveBeenCalledOnce();
    expect(presentRecovery).toHaveBeenCalledWith(payload);
    expect(logger.warn).toHaveBeenCalledWith(
      "Retry from phase failed",
      expect.objectContaining({ recoveryShaped: true })
    );

    mocks.showErrorMessage.mockClear();
    vi.mocked(statusBar.showError).mockClear();
    vi.mocked(orchestrator.runStage).mockRejectedValueOnce(recoveryError);
    await handler(phaseItem());
    expect(statusBar.showError).toHaveBeenCalledOnce();
    expect(statusBar.showError).toHaveBeenLastCalledWith("Recovery required");
    expect(statusBar.showError).not.toHaveBeenCalledWith(expect.stringContaining("/Users/"));
    expect(mocks.showErrorMessage).toHaveBeenCalledWith(
      "Retry from phase requires recovery. Check extension logs for details."
    );
    expect(mocks.showErrorMessage).not.toHaveBeenCalledWith(expect.stringContaining("/Users/"));
    expect(logger.error).toHaveBeenCalledWith(
      "Retry from phase error",
      expect.objectContaining({ recoveryShaped: true })
    );
  });

  it("derives and presents recovery from a returned production error", async () => {
    const recoveryError = new WorktreeMissing("/private/worktree", "fix/793");
    const payload = {
      issueNumber: 42,
      triggeringStage: "feature-dev",
      errorKind: "WORKTREE_MISSING" as const,
      errorDetail: recoveryError.message,
      runState: "aborted" as const,
      availableActions: ["open-run-state-directory" as const, "cancel" as const],
    };
    vi.mocked(orchestrator.getRecoveryShape).mockReturnValue(payload);
    vi.mocked(orchestrator.runStage).mockResolvedValueOnce({
      success: false,
      stage: "feature-dev",
      durationMs: 20,
      error: recoveryError,
    });

    await handler(phaseItem());

    expect(orchestrator.getRecoveryShape).toHaveBeenCalledWith(recoveryError, 42, "feature-dev");
    expect(presentRecovery).toHaveBeenCalledOnce();
    expect(presentRecovery).toHaveBeenCalledWith(payload);
    expect(mocks.showErrorMessage).not.toHaveBeenCalled();
  });

  it("derives recovery when production runStage rejects before emitting a callback", async () => {
    const recoveryError = new WorktreeMissing("/private/worktree", "fix/793");
    const payload = {
      issueNumber: 42,
      triggeringStage: "feature-dev",
      errorKind: "WORKTREE_MISSING" as const,
      errorDetail: recoveryError.message,
      runState: "aborted" as const,
      availableActions: ["open-run-state-directory" as const, "cancel" as const],
    };
    vi.mocked(orchestrator.getRecoveryShape).mockReturnValue(payload);
    vi.mocked(orchestrator.runStage).mockRejectedValueOnce(recoveryError);

    await handler(phaseItem());

    expect(orchestrator.getRecoveryShape).toHaveBeenCalledWith(recoveryError, 42, "feature-dev");
    expect(presentRecovery).toHaveBeenCalledOnce();
    expect(mocks.showErrorMessage).not.toHaveBeenCalled();
  });

  it("suppresses path-bearing schema-version errors in resolved and rejected runs", async () => {
    const recoveryError = new SchemaVersionMismatch("/Users/alice/private/context.json", "2.0", 1);
    const payload = {
      issueNumber: 42,
      triggeringStage: "feature-dev",
      errorKind: "SCHEMA_VERSION_MISMATCH" as const,
      errorDetail: recoveryError.message,
      runState: "aborted" as const,
      availableActions: ["cancel" as const],
    };
    vi.mocked(orchestrator.runStage).mockImplementationOnce(async (_stage, _issue, callbacks) => {
      callbacks?.onRecoveryRequired?.(payload);
      return {
        success: false,
        stage: "feature-dev",
        durationMs: 20,
        error: recoveryError,
      };
    });

    await handler(phaseItem());

    expect(statusBar.showError).toHaveBeenCalledWith("Recovery required");
    expect(mocks.showErrorMessage).not.toHaveBeenCalled();
    expect(presentRecovery).toHaveBeenCalledWith(payload);

    mocks.showErrorMessage.mockClear();
    vi.mocked(statusBar.showError).mockClear();
    vi.mocked(orchestrator.runStage).mockRejectedValueOnce(recoveryError);
    await handler(phaseItem());
    expect(statusBar.showError).toHaveBeenCalledOnce();
    expect(statusBar.showError).toHaveBeenLastCalledWith("Recovery required");
    expect(statusBar.showError).not.toHaveBeenCalledWith(expect.stringContaining("/Users/"));
    expect(mocks.showErrorMessage).toHaveBeenCalledWith(
      "Retry from phase requires recovery. Check extension logs for details."
    );
    expect(mocks.showErrorMessage).not.toHaveBeenCalledWith(expect.stringContaining("/Users/"));
  });
});
