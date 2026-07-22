/**
 * Manage Subscription command — opens the Stripe Customer Portal for billing changes.
 *
 * Fetches a short-lived portal session URL via Go IPC and opens it in the
 * default browser. Handles unauthenticated users by prompting sign-in first.
 * After the portal session is returned to VSCode, clears the license preflight
 * cache so the updated subscription tier is picked up on the next pipeline run.
 *
 * Community tier users see an "Upgrade" action that opens the pricing page
 * directly (no portal session needed — they have no active subscription).
 *
 * @see Issue #1478 - Implement upgrade/downgrade flows via Stripe Customer Portal
 * @see Issue #2091 - Migrated from PlatformApiClient HTTP to Go IPC
 */

import * as vscode from "vscode";
import { IpcClient } from "../services/IpcClient";
import type { LicensePreflight } from "../platform/LicensePreflight";
import type { SessionManager } from "../platform/SessionManager";
import type { IOnDemandTokenRefresher } from "../platform/TokenRefreshManager";
import type { Logger } from "../utils/logger";

const PRICING_PAGE_URL = "https://nightgauge.dev/pricing";

/**
 * Whether an IPC error looks like an auth rejection (401/403). The Go IPC layer
 * surfaces platform auth failures as "IPC error UNAUTHORIZED/FORBIDDEN: …" or
 * "unexpected status 401/403", so match both the status codes and the words.
 */
function isAuthError(err: unknown): boolean {
  const message = err instanceof Error ? err.message : String(err);
  return /\b40[13]\b/.test(message) || /unauthorized|forbidden|auth/i.test(message);
}

export function registerManageSubscriptionCommand(
  sessionManager: SessionManager | null,
  licensePreflight: LicensePreflight | null,
  logger: Logger,
  // Centralized refresher (#3754) — a stale access token can cause a one-off
  // 401 on the portal call that a single refresh-and-retry recovers from,
  // instead of falsely telling the user their session expired.
  tokenRefresher: IOnDemandTokenRefresher | null = null
): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.manageSubscription", async () => {
    // Check authentication first
    const isAuthenticated = sessionManager ? await sessionManager.isAuthenticated() : false;

    if (!isAuthenticated) {
      const action = await vscode.window.showInformationMessage(
        "Nightgauge: You must be signed in to manage your subscription.",
        "Sign In"
      );
      if (action === "Sign In") {
        await vscode.commands.executeCommand("nightgauge.signIn");
      }
      return;
    }

    // Check current tier — community users go to pricing page
    if (licensePreflight) {
      const preflight = await licensePreflight.validate();
      if (preflight.tier === "community") {
        const action = await vscode.window.showInformationMessage(
          "Nightgauge: Upgrade to Pro, Team, or Enterprise to unlock batch pipelines, concurrent runs, and more.",
          "View Pricing"
        );
        if (action === "View Pricing") {
          await vscode.env.openExternal(vscode.Uri.parse(PRICING_PAGE_URL));
        }
        return;
      }
    }

    const ipcClient = IpcClient.getInstance();
    if (!ipcClient) {
      vscode.window.showErrorMessage(
        "Nightgauge: IPC backend not available. Check your connection and try again."
      );
      return;
    }

    const openPortal = async (): Promise<void> => {
      const session = await ipcClient.platformCreatePortalSession();
      await vscode.env.openExternal(vscode.Uri.parse(session.url));
    };

    try {
      await vscode.window.withProgress(
        {
          location: vscode.ProgressLocation.Notification,
          title: "Nightgauge: Opening subscription portal…",
          cancellable: false,
        },
        async () => {
          try {
            await openPortal();
          } catch (err) {
            // A stale access token yields a one-off 401; attempt a single
            // centralized refresh-and-retry before surfacing any error (#3754).
            if (tokenRefresher && isAuthError(err)) {
              const refreshed = await tokenRefresher.forceRefresh();
              if (refreshed) {
                await openPortal();
                return;
              }
            }
            throw err;
          }
        }
      );

      // Clear license cache so updated subscription is picked up on next run
      if (licensePreflight) {
        licensePreflight.clearCache();
        logger.info("[manageSubscription] License cache cleared after portal session");
      }
    } catch (err) {
      const message = err instanceof Error ? err.message : "Unknown error occurred";

      if (isAuthError(err)) {
        // forceRefresh already shows the terminal "session expired" prompt and
        // signs out on an auth-fatal failure. Only prompt here when the session
        // is still alive (transient refresh failure, or no refresher wired) so
        // we don't double-prompt the user (#3754).
        const stillAuthenticated = sessionManager ? await sessionManager.isAuthenticated() : true;
        if (stillAuthenticated) {
          const action = await vscode.window.showErrorMessage(
            "Nightgauge: Could not open the subscription portal — your session may have expired. Please sign in again.",
            "Sign In"
          );
          if (action === "Sign In") {
            await vscode.commands.executeCommand("nightgauge.signIn");
          }
        }
        return;
      }

      logger.error(
        "[manageSubscription] Failed to open portal",
        err instanceof Error ? err : new Error(String(err))
      );
      vscode.window.showErrorMessage(
        `Nightgauge: Could not open subscription portal — ${message}. Try again.`
      );
    }
  });
}
