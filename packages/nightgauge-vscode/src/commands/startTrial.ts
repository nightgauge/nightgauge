/**
 * Start Free Trial command — issues a 14-day Pro trial for the signed-in user.
 *
 * The platform's POST /v1/license/trial is **JWT-only** (device-flow auth) — a
 * license key is not accepted — so this flow:
 *  1. Requires sign-in (prompts the device flow if needed).
 *  2. Reads the current device-flow access token and passes it to the Go IPC
 *     method `platform.startTrial`, which applies it as a PER-CALL bearer
 *     (the Go client's session bearer stays the license key for every other
 *     call). On a 401 it does one centralized refresh-and-retry.
 *  3. On success, persists the issued trial key to SecretStorage (same store as
 *     Activate License) and offers a window reload to apply it.
 *
 * The once-per-account 409 is surfaced as a friendly "not eligible" message with
 * Activate-License / pricing affordances.
 *
 * @see Issue #1138 - Commercialization: in-extension free trial
 */

import * as vscode from "vscode";
import { IpcClient } from "../services/IpcClient";
import { SecretStorageService, SECRET_KEYS } from "../services/SecretStorageService";
import type { LicensePreflight } from "../platform/LicensePreflight";
import type { SessionManager } from "../platform/SessionManager";
import type { IOnDemandTokenRefresher } from "../platform/TokenRefreshManager";
import type { TrialStateStore } from "../platform/TrialState";
import type { Logger } from "../utils/logger";

const PRICING_PAGE_URL = "https://nightgauge.dev/pricing";

/** A 401/auth rejection surfaced through the Go IPC layer. */
function isAuthError(err: unknown): boolean {
  const m = err instanceof Error ? err.message : String(err);
  return /\b401\b/.test(m) || /unauthorized|session has expired|sign in/i.test(m);
}

/** The once-per-account 409 (typed TrialError NOT_ELIGIBLE in Go). */
function isNotEligible(err: unknown): boolean {
  const m = err instanceof Error ? err.message : String(err);
  return /NOT_ELIGIBLE|not eligible|already has a license/i.test(m);
}

export function registerStartTrialCommand(
  sessionManager: SessionManager | null,
  licensePreflight: LicensePreflight | null,
  logger: Logger,
  tokenRefresher: IOnDemandTokenRefresher | null = null,
  trialStore: TrialStateStore | null = null
): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.startTrial", async () => {
    // 1. Require sign-in — the trial endpoint authenticates the user (JWT).
    const authed = sessionManager ? await sessionManager.isAuthenticated() : false;
    if (!authed) {
      const action = await vscode.window.showInformationMessage(
        "Nightgauge: Sign in to start your free 14-day Pro trial.",
        "Sign In"
      );
      if (action === "Sign In") {
        await vscode.commands.executeCommand("nightgauge.signIn");
      }
      return;
    }

    const token = sessionManager ? await sessionManager.getAccessToken() : null;
    if (!token) {
      vscode.window.showErrorMessage(
        "Nightgauge: Could not read your session — please sign in again."
      );
      return;
    }

    const ipcClient = IpcClient.getInstance();
    if (!ipcClient) {
      vscode.window.showErrorMessage("Nightgauge: backend not available — try again in a moment.");
      return;
    }

    let result: Awaited<ReturnType<typeof ipcClient.platformStartTrial>>;
    try {
      result = await vscode.window.withProgress(
        {
          location: vscode.ProgressLocation.Notification,
          title: "Nightgauge: Starting your free trial…",
          cancellable: false,
        },
        async () => {
          try {
            return await ipcClient.platformStartTrial(token);
          } catch (err) {
            // A stale access token yields a one-off 401; one centralized
            // refresh-and-retry recovers it before surfacing any error.
            if (tokenRefresher && isAuthError(err)) {
              const fresh = await tokenRefresher.forceRefresh();
              if (fresh) {
                return await ipcClient.platformStartTrial(fresh);
              }
            }
            throw err;
          }
        }
      );
    } catch (err) {
      if (isNotEligible(err)) {
        const action = await vscode.window.showInformationMessage(
          "Nightgauge: Your account already has a license, so it isn't eligible for a free trial. If you have a key, activate it; otherwise see pricing.",
          "Activate License",
          "View Pricing"
        );
        if (action === "Activate License") {
          await vscode.commands.executeCommand("nightgauge.activateLicense");
        } else if (action === "View Pricing") {
          await vscode.env.openExternal(vscode.Uri.parse(PRICING_PAGE_URL));
        }
        return;
      }
      if (isAuthError(err)) {
        const action = await vscode.window.showErrorMessage(
          "Nightgauge: Your session may have expired — sign in again to start your trial.",
          "Sign In"
        );
        if (action === "Sign In") {
          await vscode.commands.executeCommand("nightgauge.signIn");
        }
        return;
      }
      const message = err instanceof Error ? err.message : String(err);
      logger.error("[startTrial] failed", err instanceof Error ? err : new Error(message));
      vscode.window.showErrorMessage(
        `Nightgauge: Couldn't start your trial — ${message}. Try again.`
      );
      return;
    }

    // Persist the issued trial key (same store as Activate License) + reload.
    const secrets = SecretStorageService.getInstance();
    if (!secrets) {
      vscode.window.showErrorMessage(
        "Nightgauge: secure storage is unavailable — could not save the trial license."
      );
      return;
    }
    await secrets.setSecret(SECRET_KEYS.platformLicenseKey, result.licenseKey);
    licensePreflight?.clearCache();
    // Persist the trial record so the status bar can show a countdown (the
    // license `validate` response carries neither a trial flag nor the run
    // allowance — we only know them here, at issue time).
    await trialStore?.set({
      tier: result.tier,
      expiresAt: result.expiresAt,
      runAllowance: result.runAllowance,
      startedAt: new Date().toISOString(),
    });
    logger.info("[startTrial] trial activated", {
      tier: result.tier,
      runAllowance: result.runAllowance,
    });

    const action = await vscode.window.showInformationMessage(
      `Nightgauge: Free Pro trial activated — ${result.runAllowance} pipeline runs over 14 days. Reload the window to apply it.`,
      "Reload Window"
    );
    if (action === "Reload Window") {
      await vscode.commands.executeCommand("workbench.action.reloadWindow");
    }
  });
}
