/**
 * Sign Out command — clears all stored platform auth state.
 *
 * Thin wrapper that delegates entirely to OAuthDeviceFlowService.
 *
 * @see Issue #1464 - Implement OAuth Device Flow login command and UI
 */

import * as vscode from "vscode";
import type { OAuthDeviceFlowService } from "../services/OAuthDeviceFlowService";
import type { TrialStateStore } from "../platform/TrialState";
import type { Logger } from "../utils/logger";

export function registerSignOutCommand(
  oauthService: OAuthDeviceFlowService,
  logger: Logger,
  trialStore: TrialStateStore | null = null
): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.signOut", async () => {
    try {
      await oauthService.signOut();
      // The trial is account-bound — drop the local countdown record too.
      await trialStore?.clear();
      vscode.window.showInformationMessage("Nightgauge: Signed out.");
    } catch (err) {
      const error = err instanceof Error ? err : new Error(String(err));
      logger.error("Sign-out failed", error);
      vscode.window.showErrorMessage(`Nightgauge: Sign-out failed — ${error.message}`);
    }
  });
}
