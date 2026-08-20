import * as vscode from "vscode";
import type { RecoveryRequiredPayload } from "@nightgauge/sdk";
import type { HeadlessOrchestrator } from "../../services/HeadlessOrchestrator";
import type { Logger } from "../../utils/logger";
import type { RecoveryResult } from "../../views/recovery";

interface RecoveryDialogLike extends vscode.Disposable {
  show(payload: RecoveryRequiredPayload): Promise<RecoveryResult>;
  waitForNextAction(): Promise<RecoveryResult>;
}

export type RecoveryPresenter = (payload: RecoveryRequiredPayload) => void;

/** Coordinates Recovery Dialog actions with the orchestrator's recovery API. */
export class RecoveryCoordinator implements vscode.Disposable {
  private pending: Promise<void> = Promise.resolve();
  private disposed = false;

  constructor(
    private readonly orchestrator: HeadlessOrchestrator,
    private readonly dialog: RecoveryDialogLike,
    private readonly logger: Logger
  ) {}

  readonly present: RecoveryPresenter = (payload) => {
    if (this.disposed) return;
    this.pending = this.pending
      .then(() => (this.disposed ? undefined : this.run(payload)))
      .catch((error: unknown) => {
        this.logger.error("Recovery Dialog failed", {
          error: error instanceof Error ? error.message : String(error),
        });
        vscode.window.showErrorMessage(
          "Recovery could not be presented. Check extension logs for details."
        );
        this.dialog.dispose();
      });
  };

  private async run(payload: RecoveryRequiredPayload): Promise<void> {
    let result = await this.dialog.show(payload);

    while (!this.disposed && result.action !== "cancel") {
      if (!payload.availableActions.includes(result.action)) {
        this.logger.warn("Rejected unavailable recovery action", {
          action: result.action,
          issueNumber: payload.issueNumber,
          availableActions: payload.availableActions,
        });
        vscode.window.showErrorMessage(
          "That recovery action is no longer available. Retry the command to refresh recovery options."
        );
        this.dialog.dispose();
        return;
      }

      const actionResult = await this.orchestrator.runRecoveryAction(result.action);

      if (actionResult.success) {
        result = await this.dialog.waitForNextAction();
        continue;
      }

      this.logger.error("Recovery action failed", {
        action: result.action,
        issueNumber: payload.issueNumber,
        error: actionResult.error?.message ?? "Unknown error",
      });
      vscode.window.showErrorMessage("Recovery action failed. Check extension logs for details.");
      this.dialog.dispose();
      return;
    }

    this.dialog.dispose();
  }

  dispose(): void {
    this.disposed = true;
    this.dialog.dispose();
  }
}
