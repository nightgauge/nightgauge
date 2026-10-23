/**
 * Production-handler tests for the Retry Stage command.
 *
 * These tests intentionally capture the handler registered by
 * registerRetryStageCommand. A local simulator previously duplicated an older
 * version of the command and could stay green while the production handler had
 * no coverage.
 *
 * @see Issue #793
 */

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import * as vscode from "vscode";
import { SchemaVersionMismatch, type PipelineStage, WorktreeMissing } from "@nightgauge/sdk";
import { registerRetryStageCommand } from "../../src/commands/retryStage";
import type {
  HeadlessOrchestrator,
  PipelineCallbacks,
} from "../../src/services/HeadlessOrchestrator";
import type { PipelineStateService } from "../../src/services/PipelineStateService";
import type { Logger } from "../../src/utils/logger";
import type { StatusBarManager } from "../../src/utils/statusBar";
import type { OutputWindow, StageTreeItem } from "../../src/views";
import type { RecoveryPresenter } from "../../src/orchestrator/recovery/RecoveryCoordinator";
import { MAX_STAGE_RETRIES } from "../../src/utils/stageTransitionValidator";
import { createPhaseTracker } from "../../src/utils/phaseTracker";
import { createStreamOutputHandler } from "../../src/utils/streamOutputHandler";
import { getNextStage } from "../../src/utils/skillRunner";

const mocks = vi.hoisted(() => ({
  showErrorMessage: vi.fn(),
  showWarningMessage: vi.fn(),
  showInformationMessage: vi.fn(),
  showQuickPick: vi.fn(),
  showInputBox: vi.fn(),
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
    showQuickPick: mocks.showQuickPick,
    showInputBox: mocks.showInputBox,
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

type RetryStageHandler = (item?: StageTreeItem) => Promise<void>;

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
    getState: vi.fn().mockResolvedValue({ issue_number: 42 }),
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

function createStageItem(overrides: Partial<StageTreeItem> = {}): StageTreeItem {
  return {
    stage: "feature-dev" as PipelineStage,
    clearError: vi.fn(),
    isRetryable: vi.fn().mockReturnValue(true),
    getRetryCount: vi.fn().mockReturnValue(1),
    ...overrides,
  } as unknown as StageTreeItem;
}

function firstCallbacks(orchestrator: HeadlessOrchestrator): PipelineCallbacks {
  const callbacks = vi.mocked(orchestrator.runStage).mock.calls[0]?.[2];
  if (!callbacks) {
    throw new Error("runStage callbacks were not supplied");
  }
  return callbacks;
}

describe("registerRetryStageCommand", () => {
  let orchestrator: HeadlessOrchestrator;
  let stateService: PipelineStateService;
  let logger: Logger;
  let statusBar: StatusBarManager;
  let outputWindow: OutputWindow;
  let presentRecovery: RecoveryPresenter;
  let handler: RetryStageHandler;

  const register = (
    orchestratorOverride: HeadlessOrchestrator | null = orchestrator,
    stateServiceOverride: PipelineStateService | null = stateService,
    outputOverride: OutputWindow | null = outputWindow,
    recoveryOverride: RecoveryPresenter | undefined = presentRecovery
  ): RetryStageHandler => {
    handler = undefined as unknown as RetryStageHandler;
    registerRetryStageCommand(
      orchestratorOverride,
      stateServiceOverride,
      logger,
      statusBar,
      outputOverride,
      recoveryOverride
    );
    if (!handler) {
      throw new Error("Retry Stage command handler was not registered");
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

    mocks.registerCommand.mockImplementation((_command: string, registered: RetryStageHandler) => {
      handler = registered;
      return { dispose: vi.fn() };
    });
    mocks.getConfiguration.mockReturnValue({ get: mocks.configGet });
    mocks.configGet.mockImplementation((key: string, fallback: unknown) =>
      key === "autoContinue" ? false : fallback
    );
    mocks.getNextStage.mockReturnValue("feature-validate");
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

  it("registers and invokes the production command handler", async () => {
    const item = createStageItem();

    await handler(item);

    expect(mocks.registerCommand).toHaveBeenCalledWith(
      "nightgauge.retryStage",
      expect.any(Function)
    );
    expect(orchestrator.runStage).toHaveBeenCalledWith("feature-dev", 42, expect.any(Object));
    expect(stateService.getState).toHaveBeenCalledOnce();
    expect(mocks.showInputBox).not.toHaveBeenCalled();
    expect(statusBar.showComplete).toHaveBeenCalledWith("feature-dev");
  });

  it("falls back to validated issue input when state lookup fails", async () => {
    vi.mocked(stateService.getState).mockRejectedValue(new Error("state unavailable"));
    mocks.showInputBox.mockResolvedValue("99");

    await handler(createStageItem());

    expect(logger.warn).toHaveBeenCalledWith(
      "Failed to get issue number from state service",
      expect.objectContaining({ error: expect.any(Error) })
    );
    expect(orchestrator.runStage).toHaveBeenCalledWith("feature-dev", 99, expect.any(Object));
    const options = mocks.showInputBox.mock.calls[0]?.[0];
    expect(options.validateInput("0")).toBe("Please enter a valid positive issue number");
    expect(options.validateInput("12")).toBeNull();
  });

  it("treats empty issue input as cancellation", async () => {
    vi.mocked(stateService.getState).mockResolvedValue(null);
    mocks.showInputBox.mockResolvedValue("");

    await handler(createStageItem());

    expect(orchestrator.runStage).not.toHaveBeenCalled();
    expect(mocks.showErrorMessage).not.toHaveBeenCalled();
  });

  it.each([" ", "+12", "-1", "12.5", "12junk", "0", "9007199254740992"])(
    "does not dispatch malformed issue input %j",
    async (input) => {
      vi.mocked(stateService.getState).mockResolvedValue(null);
      mocks.showInputBox.mockResolvedValue(input);

      await handler(createStageItem());

      const options = mocks.showInputBox.mock.calls[0]?.[0];
      expect(options.validateInput(input)).toBe("Please enter a valid positive issue number");
      expect(orchestrator.runStage).not.toHaveBeenCalled();
      expect(mocks.showErrorMessage).toHaveBeenCalledWith(
        "Please enter a valid positive issue number"
      );
    }
  );

  it("supports command-palette stage selection and cancellation", async () => {
    mocks.showQuickPick.mockResolvedValueOnce({
      label: "Feature Validation",
      value: "feature-validate",
    });
    await handler();
    expect(orchestrator.runStage).toHaveBeenCalledWith("feature-validate", 42, expect.any(Object));

    vi.mocked(orchestrator.runStage).mockClear();
    mocks.showQuickPick.mockResolvedValueOnce(undefined);
    await handler();
    expect(orchestrator.runStage).not.toHaveBeenCalled();
  });

  it("blocks unavailable, active, and non-retryable executions", async () => {
    const noOrchestrator = register(null);
    await noOrchestrator();
    expect(mocks.showErrorMessage).toHaveBeenCalledWith(
      "Nightgauge orchestrator not initialized. Check extension logs for details."
    );

    handler = register();
    vi.mocked(orchestrator.getIsRunning).mockReturnValue(true);
    await handler(createStageItem());
    expect(mocks.showWarningMessage).toHaveBeenCalledWith(
      "Pipeline is already running. Stop it first or wait for completion."
    );

    vi.mocked(orchestrator.getIsRunning).mockReturnValue(false);
    const item = createStageItem({
      isRetryable: vi.fn().mockReturnValue(false),
      getRetryCount: vi.fn().mockReturnValue(MAX_STAGE_RETRIES),
    });
    await handler(item);
    expect(mocks.showErrorMessage).toHaveBeenCalledWith(
      expect.stringContaining(`Maximum retries (${MAX_STAGE_RETRIES}) exceeded`)
    );
    expect(orchestrator.runStage).not.toHaveBeenCalled();
  });

  it("clears the tree error and marks running before dispatch", async () => {
    const item = createStageItem();

    await handler(item);

    const clearError = vi.mocked(item.clearError);
    const runStage = vi.mocked(orchestrator.runStage);
    expect(clearError).toHaveBeenCalledOnce();
    expect(statusBar.showRunning).toHaveBeenCalledWith("feature-dev");
    expect(clearError.mock.invocationCallOrder[0]).toBeLessThan(
      runStage.mock.invocationCallOrder[0]
    );
  });

  it("wires stream and phase callbacks into the real runStage dispatch", async () => {
    await handler(createStageItem());
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
    expect(logger.debug).toHaveBeenCalledWith("Retry stage started", {
      stage: "feature-dev",
      issueNumber: 42,
    });
    expect(logger.error).toHaveBeenCalledWith(
      "Retry stage error",
      expect.objectContaining({ error: "stream failed" })
    );
  });

  it("honors backward-transition confirmation", async () => {
    await handler(createStageItem());
    const confirm = firstCallbacks(orchestrator).onBackwardTransitionConfirm;
    mocks.showWarningMessage.mockResolvedValueOnce("Continue");

    await expect(confirm?.("feature-dev", "Go backward?")).resolves.toBe(true);
    expect(mocks.showWarningMessage).toHaveBeenCalledWith(
      "Go backward?",
      { modal: true },
      "Continue"
    );

    mocks.showWarningMessage.mockResolvedValueOnce(undefined);
    await expect(confirm?.("feature-dev", "Go backward?")).resolves.toBe(false);
    mocks.showWarningMessage.mockResolvedValueOnce("Cancel");
    await expect(confirm?.("feature-dev", "Go backward?")).resolves.toBe(false);
  });

  it("auto-continues to the next stage in automatic mode", async () => {
    vi.useFakeTimers();
    mocks.configGet.mockImplementation((key: string, fallback: unknown) => {
      if (key === "autoContinue") return true;
      if (key === "autoContinueDelay") return 25;
      return fallback;
    });

    await handler(createStageItem());
    expect(getNextStage).toHaveBeenCalledWith("feature-dev");
    expect(stateService.isPaused).toHaveBeenCalledOnce();
    expect(stateService.getExecutionMode).toHaveBeenCalledOnce();
    expect(vscode.commands.executeCommand).not.toHaveBeenCalled();

    await vi.advanceTimersByTimeAsync(24);
    expect(vscode.commands.executeCommand).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(1);
    expect(vscode.commands.executeCommand).toHaveBeenCalledWith(
      "nightgauge.runStage",
      "feature-validate"
    );
  });

  it("applies the manual Yes to All continuation choice", async () => {
    vi.useFakeTimers();
    mocks.configGet.mockImplementation((key: string, fallback: unknown) => {
      if (key === "autoContinue") return true;
      if (key === "autoContinueDelay") return 10;
      return fallback;
    });
    vi.mocked(stateService.getExecutionMode).mockResolvedValue("manual");
    mocks.showInformationMessage.mockResolvedValue("Yes to All");

    await handler(createStageItem());
    await vi.advanceTimersByTimeAsync(10);

    expect(stateService.setExecutionMode).toHaveBeenCalledWith("automatic");
    expect(stateService.resumePipeline).toHaveBeenCalledOnce();
    expect(vscode.commands.executeCommand).toHaveBeenCalledWith(
      "nightgauge.runStage",
      "feature-validate"
    );
  });

  it("reports ordinary failures from resolved and rejected stage runs", async () => {
    vi.mocked(orchestrator.runStage).mockResolvedValueOnce({
      success: false,
      stage: "feature-dev",
      durationMs: 20,
      error: new Error("stage failed"),
    });
    await handler(createStageItem());
    expect(statusBar.showError).toHaveBeenCalledWith("stage failed");
    expect(mocks.showErrorMessage).toHaveBeenCalledWith(
      'Stage "feature-dev" failed: Error: stage failed'
    );

    vi.mocked(orchestrator.runStage).mockRejectedValueOnce(new Error("dispatch failed"));
    await handler(createStageItem());
    expect(statusBar.showError).toHaveBeenCalledWith("dispatch failed");
    expect(mocks.showErrorMessage).toHaveBeenCalledWith("Stage retry error: dispatch failed");
  });

  it("suppresses duplicate toasts for recovery-shaped errors", async () => {
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

    await handler(createStageItem());

    expect(statusBar.showError).toHaveBeenCalledWith("Recovery required");
    expect(mocks.showErrorMessage).not.toHaveBeenCalled();
    expect(presentRecovery).toHaveBeenCalledOnce();
    expect(presentRecovery).toHaveBeenCalledWith(payload);
    expect(logger.warn).toHaveBeenCalledWith(
      "Stage retry failed",
      expect.objectContaining({ recoveryShaped: true })
    );

    mocks.showErrorMessage.mockClear();
    vi.mocked(statusBar.showError).mockClear();
    vi.mocked(orchestrator.runStage).mockRejectedValueOnce(recoveryError);
    await handler(createStageItem());
    expect(statusBar.showError).toHaveBeenCalledOnce();
    expect(statusBar.showError).toHaveBeenLastCalledWith("Recovery required");
    expect(statusBar.showError).not.toHaveBeenCalledWith(expect.stringContaining("/Users/"));
    expect(mocks.showErrorMessage).toHaveBeenCalledWith(
      "Stage retry requires recovery. Check extension logs for details."
    );
    expect(mocks.showErrorMessage).not.toHaveBeenCalledWith(expect.stringContaining("/Users/"));
    expect(logger.error).toHaveBeenCalledWith(
      "Stage retry error",
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

    await handler(createStageItem());

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

    await handler(createStageItem());

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

    await handler(createStageItem());

    expect(statusBar.showError).toHaveBeenCalledWith("Recovery required");
    expect(mocks.showErrorMessage).not.toHaveBeenCalled();
    expect(presentRecovery).toHaveBeenCalledWith(payload);

    mocks.showErrorMessage.mockClear();
    vi.mocked(statusBar.showError).mockClear();
    vi.mocked(orchestrator.runStage).mockRejectedValueOnce(recoveryError);
    await handler(createStageItem());
    expect(statusBar.showError).toHaveBeenCalledOnce();
    expect(statusBar.showError).toHaveBeenLastCalledWith("Recovery required");
    expect(statusBar.showError).not.toHaveBeenCalledWith(expect.stringContaining("/Users/"));
    expect(mocks.showErrorMessage).toHaveBeenCalledWith(
      "Stage retry requires recovery. Check extension logs for details."
    );
    expect(mocks.showErrorMessage).not.toHaveBeenCalledWith(expect.stringContaining("/Users/"));
  });
});
