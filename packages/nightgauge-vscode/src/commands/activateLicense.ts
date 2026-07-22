/**
 * Activate License command — lets a user turn on a license key they received by
 * email (after purchase, or a free-trial key) without editing config files.
 *
 * Flow:
 *  1. Prompt for the key.
 *  2. Verify it against the platform via Go IPC. `platform.validateLicense`
 *     honors a passed key that differs from the session key (it routes to
 *     LicenseService.ValidateKey, which validates the arbitrary key WITHOUT
 *     touching the session cache), so we learn whether the entered key is good
 *     before persisting anything.
 *  3. On success, persist the key to SecretStorage under
 *     SECRET_KEYS.platformLicenseKey — the source of truth that
 *     IpcClientBase.resolveLicenseKey() reads and forwardPlatformEnv() injects
 *     into the Go IPC server as NIGHTGAUGE_LICENSE_KEY on the next spawn.
 *  4. The running Go server still holds the previous key, so the new license
 *     applies after a window reload, which we offer inline.
 *
 * @see Issue #1138 - Commercialization: in-extension license activation
 */

import * as vscode from "vscode";
import { IpcClient } from "../services/IpcClient";
import { SecretStorageService, SECRET_KEYS } from "../services/SecretStorageService";
import type { LicensePreflight } from "../platform/LicensePreflight";
import type { TrialStateStore } from "../platform/TrialState";
import type { Logger } from "../utils/logger";

export function registerActivateLicenseCommand(
  licensePreflight: LicensePreflight | null,
  logger: Logger,
  trialStore: TrialStateStore | null = null
): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.activateLicense", async () => {
    const entered = await vscode.window.showInputBox({
      title: "Activate Nightgauge License",
      prompt: "Paste the license key from your purchase confirmation or free-trial email.",
      placeHolder: "ib_live_...",
      ignoreFocusOut: true,
      validateInput: (value) => (value.trim().length === 0 ? "Enter a license key" : undefined),
    });
    // `undefined` = user cancelled (Esc); empty = nothing to do.
    if (entered === undefined) {
      return;
    }
    const key = entered.trim();
    if (key.length === 0) {
      return;
    }

    const ipcClient = IpcClient.getInstance();
    if (!ipcClient) {
      vscode.window.showErrorMessage("Nightgauge: backend not available — try again in a moment.");
      return;
    }

    let valid = false;
    let tier = "";
    try {
      await vscode.window.withProgress(
        {
          location: vscode.ProgressLocation.Notification,
          title: "Nightgauge: Verifying license…",
          cancellable: false,
        },
        async () => {
          const info = await ipcClient.platformValidateLicense(key);
          valid = info.valid === true;
          tier = String(info.tier ?? "");
        }
      );
    } catch (err) {
      const message = err instanceof Error ? err.message : String(err);
      logger.error(
        "[activateLicense] verification failed",
        err instanceof Error ? err : new Error(message)
      );
      vscode.window.showErrorMessage(
        `Nightgauge: Couldn't verify the license — ${message}. Check your connection and try again.`
      );
      return;
    }

    // A valid paid/trial license reports a non-community tier. `community` (or
    // an empty tier) means the platform did not accept the key as a paid one.
    if (!valid || tier === "" || tier === "community") {
      vscode.window.showErrorMessage(
        "Nightgauge: That license key was not accepted (invalid, expired, or revoked). Double-check the key and try again."
      );
      return;
    }

    const secrets = SecretStorageService.getInstance();
    if (!secrets) {
      vscode.window.showErrorMessage(
        "Nightgauge: secure storage is unavailable — could not save the license."
      );
      return;
    }
    await secrets.setSecret(SECRET_KEYS.platformLicenseKey, key);
    // Drop any cached community/old-tier result so the next validate re-checks.
    licensePreflight?.clearCache();
    // Activating an explicit key supersedes any in-progress trial — clear the
    // local trial record so the status bar stops showing a countdown.
    await trialStore?.clear();
    logger.info("[activateLicense] license stored to SecretStorage", { tier });

    const tierLabel = tier.charAt(0).toUpperCase() + tier.slice(1);
    const action = await vscode.window.showInformationMessage(
      `Nightgauge: ${tierLabel} license activated. Reload the window to apply it.`,
      "Reload Window"
    );
    if (action === "Reload Window") {
      await vscode.commands.executeCommand("workbench.action.reloadWindow");
    }
  });
}
