/**
 * Link This Machine command — run the device-key pass on demand (#1883).
 *
 * The one-click fix the unlinked-machine notice offers, and a command-palette
 * entry. It reuses DeviceKeyService, so the precedence ladder holds: an
 * explicit key is never replaced.
 *
 * @see Issue #1883
 */

import * as vscode from "vscode";
import {
  ACTION,
  COMMAND,
  LINK_MACHINE_COMMAND,
  messageForDeviceKeyError,
  type DeviceKeyService,
} from "../platform/DeviceKeyService";

/**
 * `nightgauge.linkMachine` (#1883): run one device-key pass now, ignoring the
 * once-per-session guard. The precedence ladder still holds, so an explicit
 * key is never replaced.
 */
export function registerLinkMachineCommand(
  getService: () => DeviceKeyService | null
): vscode.Disposable {
  return vscode.commands.registerCommand(LINK_MACHINE_COMMAND, async () => {
    const service = getService();
    if (!service) {
      void vscode.window.showInformationMessage(
        "Nightgauge: Turn on platform features (platform.enabled) and sign in to link this machine to your account."
      );
      return;
    }
    const outcome = await service.ensure("link-machine", { force: true });
    if (outcome.kind === "no-session") {
      const action = await vscode.window.showInformationMessage(
        "Nightgauge: Sign in to link this machine to your account.",
        ACTION.signIn
      );
      if (action === ACTION.signIn) {
        await vscode.commands.executeCommand(COMMAND.signIn);
      }
    } else if (outcome.kind === "explicit-key") {
      void vscode.window.showInformationMessage(
        `Nightgauge: This machine already has a license key (${outcome.source}); it was not replaced.`
      );
    } else if (outcome.kind === "failed" && messageForDeviceKeyError(outcome.code) === null) {
      void vscode.window.showWarningMessage(
        outcome.code === "RATE_LIMITED"
          ? "Nightgauge: Too many link requests — try again in a minute."
          : "Nightgauge: Couldn't reach the Nightgauge platform to link this machine. Try again later."
      );
    }
  });
}
