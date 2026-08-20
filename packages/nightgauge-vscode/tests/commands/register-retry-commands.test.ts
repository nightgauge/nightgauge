import { beforeEach, describe, expect, it, vi } from "vitest";
import type { RecoveryRequiredPayload } from "@nightgauge/sdk";

const mocks = vi.hoisted(() => ({
  registerRetryStage: vi.fn(() => ({ dispose: vi.fn() })),
  registerRetryFromPhase: vi.fn(() => ({ dispose: vi.fn() })),
  dialogShow: vi.fn(),
  dialogWaitForNextAction: vi.fn(),
  dialogDispose: vi.fn(),
  showErrorMessage: vi.fn(),
}));

vi.mock("vscode", () => ({
  window: { showErrorMessage: mocks.showErrorMessage },
}));

vi.mock("../../src/commands/retryStage", () => ({
  registerRetryStageCommand: mocks.registerRetryStage,
}));

vi.mock("../../src/commands/retryFromPhase", () => ({
  registerRetryFromPhaseCommand: mocks.registerRetryFromPhase,
}));

vi.mock("../../src/views/recovery", () => ({
  RecoveryDialog: class {
    show = mocks.dialogShow;
    waitForNextAction = mocks.dialogWaitForNextAction;
    dispose = mocks.dialogDispose;
  },
}));

import { registerRetryCommands } from "../../src/commands/register-retry-commands";
import type { RecoveryPresenter } from "../../src/orchestrator/recovery/RecoveryCoordinator";

const payload: RecoveryRequiredPayload = {
  issueNumber: 793,
  triggeringStage: "feature-dev",
  errorKind: "RUN_STATE_MISSING",
  errorDetail: "missing state",
  runState: "none",
  availableActions: ["open-run-state-directory", "cancel"],
};

describe("registerRetryCommands", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("passes one real coordinator presenter to both shipped retry commands", async () => {
    mocks.dialogShow.mockResolvedValue({ action: "cancel" });
    const orchestrator = {
      runRecoveryAction: vi.fn(),
      getRecoveryShape: vi.fn(),
    };
    const disposables = registerRetryCommands({
      context: { extensionUri: { fsPath: "/extension" } } as never,
      orchestrator: orchestrator as never,
      stateService: null,
      logger: { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn() } as never,
      statusBar: {} as never,
      outputWindow: {} as never,
    });

    const stagePresenter = mocks.registerRetryStage.mock.calls[0]?.[5] as RecoveryPresenter;
    const phasePresenter = mocks.registerRetryFromPhase.mock.calls[0]?.[5] as RecoveryPresenter;
    expect(stagePresenter).toBeTypeOf("function");
    expect(phasePresenter).toBe(stagePresenter);
    expect(disposables).toHaveLength(3);

    stagePresenter(payload);

    await vi.waitFor(() => expect(mocks.dialogShow).toHaveBeenCalledWith(payload));
  });

  it("registers both commands without a presenter when orchestration is unavailable", () => {
    const disposables = registerRetryCommands({
      context: { extensionUri: { fsPath: "/extension" } } as never,
      orchestrator: null,
      stateService: null,
      logger: {} as never,
      statusBar: {} as never,
      outputWindow: {} as never,
    });

    expect(mocks.registerRetryStage.mock.calls[0]?.[5]).toBeUndefined();
    expect(mocks.registerRetryFromPhase.mock.calls[0]?.[5]).toBeUndefined();
    expect(disposables).toHaveLength(2);
  });
});
