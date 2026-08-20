import { beforeEach, describe, expect, it, vi } from "vitest";
import type { RecoveryRequiredPayload } from "@nightgauge/sdk";

const mocks = vi.hoisted(() => ({ showErrorMessage: vi.fn() }));

vi.mock("vscode", () => ({
  window: { showErrorMessage: mocks.showErrorMessage },
}));

import { RecoveryCoordinator } from "../../../src/orchestrator/recovery/RecoveryCoordinator";
import type { HeadlessOrchestrator } from "../../../src/services/HeadlessOrchestrator";
import type { Logger } from "../../../src/utils/logger";

const payload: RecoveryRequiredPayload = {
  issueNumber: 793,
  triggeringStage: "feature-dev",
  producingStage: "feature-planning",
  errorKind: "MISSING_INPUT_FILE",
  errorDetail: "missing planning context",
  runState: "aborted",
  availableActions: ["open-run-state-directory", "cancel"],
};

function makeDeps() {
  const orchestrator = {
    runRecoveryAction: vi.fn(),
  } as unknown as HeadlessOrchestrator;
  const dialog = {
    show: vi.fn(),
    waitForNextAction: vi.fn(),
    dispose: vi.fn(),
  };
  const logger = {
    info: vi.fn(),
    warn: vi.fn(),
    error: vi.fn(),
    debug: vi.fn(),
  } as unknown as Logger;
  return { orchestrator, dialog, logger };
}

describe("RecoveryCoordinator", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("closes cleanly when recovery is cancelled", async () => {
    const { orchestrator, dialog, logger } = makeDeps();
    dialog.show.mockResolvedValue({ action: "cancel" });
    const coordinator = new RecoveryCoordinator(orchestrator, dialog, logger);

    coordinator.present(payload);

    await vi.waitFor(() => expect(dialog.dispose).toHaveBeenCalledOnce());
    expect(orchestrator.runRecoveryAction).not.toHaveBeenCalled();
  });

  it("opens the state directory, re-enables the same dialog, and then cancels", async () => {
    const { orchestrator, dialog, logger } = makeDeps();
    dialog.show.mockResolvedValue({ action: "open-run-state-directory" });
    dialog.waitForNextAction.mockResolvedValue({ action: "cancel" });
    vi.mocked(orchestrator.runRecoveryAction).mockResolvedValue({ success: true });
    const coordinator = new RecoveryCoordinator(orchestrator, dialog, logger);

    coordinator.present(payload);

    await vi.waitFor(() => expect(dialog.waitForNextAction).toHaveBeenCalledOnce());
    expect(orchestrator.runRecoveryAction).toHaveBeenCalledWith("open-run-state-directory");
    expect(dialog.dispose).toHaveBeenCalledOnce();
  });

  it("rejects an action that is not in the current payload", async () => {
    const { orchestrator, dialog, logger } = makeDeps();
    dialog.show.mockResolvedValue({ action: "retired-state-changing-action" as never });
    const coordinator = new RecoveryCoordinator(orchestrator, dialog, logger);

    coordinator.present(payload);

    await vi.waitFor(() =>
      expect(mocks.showErrorMessage).toHaveBeenCalledWith(
        "That recovery action is no longer available. Retry the command to refresh recovery options."
      )
    );
    expect(orchestrator.runRecoveryAction).not.toHaveBeenCalled();
    expect(logger.warn).toHaveBeenCalledWith(
      "Rejected unavailable recovery action",
      expect.objectContaining({ action: "retired-state-changing-action" })
    );
  });

  it("shows a path-safe fallback when directory opening fails", async () => {
    const { orchestrator, dialog, logger } = makeDeps();
    dialog.show.mockResolvedValue({ action: "open-run-state-directory" });
    vi.mocked(orchestrator.runRecoveryAction).mockResolvedValue({
      success: false,
      error: new Error("failed at /Users/alice/private/run-state.json"),
    });
    const coordinator = new RecoveryCoordinator(orchestrator, dialog, logger);

    coordinator.present(payload);

    await vi.waitFor(() =>
      expect(mocks.showErrorMessage).toHaveBeenCalledWith(
        "Recovery action failed. Check extension logs for details."
      )
    );
    expect(mocks.showErrorMessage).not.toHaveBeenCalledWith(expect.stringContaining("/Users/"));
    expect(logger.error).toHaveBeenCalledWith(
      "Recovery action failed",
      expect.objectContaining({ error: expect.stringContaining("/Users/") })
    );
  });

  it("contains dialog failures behind a path-safe fallback", async () => {
    const { orchestrator, dialog, logger } = makeDeps();
    dialog.show.mockRejectedValue(new Error("webview failed at /Users/alice/private"));
    const coordinator = new RecoveryCoordinator(orchestrator, dialog, logger);

    coordinator.present(payload);

    await vi.waitFor(() =>
      expect(mocks.showErrorMessage).toHaveBeenCalledWith(
        "Recovery could not be presented. Check extension logs for details."
      )
    );
    expect(mocks.showErrorMessage).not.toHaveBeenCalledWith(expect.stringContaining("/Users/"));
    expect(dialog.dispose).toHaveBeenCalledOnce();
  });

  it("serializes multiple recovery presentations", async () => {
    const { orchestrator, dialog, logger } = makeDeps();
    let resolveFirst!: (result: { action: "cancel" }) => void;
    dialog.show
      .mockImplementationOnce(
        () =>
          new Promise((resolve) => {
            resolveFirst = resolve;
          })
      )
      .mockResolvedValueOnce({ action: "cancel" });
    const coordinator = new RecoveryCoordinator(orchestrator, dialog, logger);
    const second = { ...payload, issueNumber: 794 };

    coordinator.present(payload);
    coordinator.present(second);

    await vi.waitFor(() => expect(dialog.show).toHaveBeenCalledOnce());
    resolveFirst({ action: "cancel" });
    await vi.waitFor(() => expect(dialog.show).toHaveBeenCalledTimes(2));
    expect(dialog.show).toHaveBeenNthCalledWith(1, payload);
    expect(dialog.show).toHaveBeenNthCalledWith(2, second);
  });

  it("does not open queued recovery dialogs after disposal", async () => {
    const { orchestrator, dialog, logger } = makeDeps();
    let resolveFirst!: (result: { action: "cancel" }) => void;
    dialog.show.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          resolveFirst = resolve;
        })
    );
    const coordinator = new RecoveryCoordinator(orchestrator, dialog, logger);

    coordinator.present(payload);
    coordinator.present({ ...payload, issueNumber: 794 });
    await vi.waitFor(() => expect(dialog.show).toHaveBeenCalledOnce());
    coordinator.dispose();
    resolveFirst({ action: "cancel" });
    await vi.waitFor(() => expect(dialog.dispose).toHaveBeenCalledTimes(2));

    expect(dialog.show).toHaveBeenCalledOnce();
  });
});
