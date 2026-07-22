/**
 * Show Platform Status command
 *
 * Consolidated account hub: when authenticated, shows a quick pick with
 * subscription info, account management, and sign out options.
 * When not authenticated, shows connection details or directs to sign-in.
 *
 * @see Issue #1461 - Platform connection status indicator
 * @see Issue #1469 - Add auth status indicators to sidebar and status bar
 */

import * as vscode from "vscode";
import type { PlatformStatusBarItem } from "../platform/PlatformStatusBarItem";
import type { LicensePreflight } from "../platform/LicensePreflight";

type AuthAction =
  "sign-out" | "switch-account" | "view-account" | "manage-subscription" | "upgrade";

interface AuthActionItem extends vscode.QuickPickItem {
  action: AuthAction;
}

/**
 * Register the Show Platform Status command
 *
 * Triggered when the user clicks the platform status bar item.
 * - When authenticated: shows account hub quick pick (subscription, manage, sign out)
 * - Otherwise: shows connection details in an info/warning/error message
 */
export function registerShowPlatformStatusCommand(
  platformStatusBarItem: PlatformStatusBarItem,
  licensePreflight?: LicensePreflight | null
): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.showPlatformStatus", async () => {
    // When authenticated, show account hub quick pick
    if (platformStatusBarItem.isAuthenticated()) {
      const items: AuthActionItem[] = [];

      // Try to get subscription info for the header
      if (licensePreflight) {
        try {
          const result = await licensePreflight.validate();
          const tierLabel =
            result.tier === "community"
              ? "Free Plan"
              : result.tier.charAt(0).toUpperCase() + result.tier.slice(1);

          if (result.tier === "community") {
            items.push({
              label: `$(star-empty) ${tierLabel}`,
              description: "Community tier",
              action: "upgrade",
            });
          } else {
            const expiryNote = result.expiresAt
              ? ` · Renews ${formatShortDate(result.expiresAt)}`
              : "";
            items.push({
              label: `$(verified) ${tierLabel}`,
              description: `Active subscription${expiryNote}`,
              action: "manage-subscription",
            });
          }

          // Separator
          items.push({
            label: "",
            kind: vscode.QuickPickItemKind.Separator,
            action: "view-account",
          });
        } catch {
          // Preflight failed — skip subscription line
        }
      }

      items.push(
        {
          label: "$(link-external) View Account",
          description: "Open account settings in browser",
          action: "view-account",
        },
        {
          label: "$(account) Switch Account",
          description: "Sign in with a different account",
          action: "switch-account",
        },
        {
          label: "$(sign-out) Sign Out",
          description: "Sign out of Nightgauge platform",
          action: "sign-out",
        }
      );

      const selection = await vscode.window.showQuickPick(items, {
        placeHolder: "Nightgauge Account",
        title: "Account",
      });

      if (!selection) return;

      switch (selection.action) {
        case "sign-out":
          await vscode.commands.executeCommand("nightgauge.signOut");
          break;
        case "switch-account":
          await vscode.commands.executeCommand("nightgauge.signOut");
          await vscode.commands.executeCommand("nightgauge.signIn");
          break;
        case "view-account":
          await vscode.env.openExternal(vscode.Uri.parse("https://nightgauge.dev/account"));
          break;
        case "manage-subscription":
          await vscode.env.openExternal(vscode.Uri.parse("https://nightgauge.dev/account"));
          break;
        case "upgrade":
          await vscode.env.openExternal(vscode.Uri.parse("https://nightgauge.dev/upgrade"));
          break;
      }
      return;
    }

    // Not authenticated — show connection details
    const state = platformStatusBarItem.getDisplayState();
    const details = platformStatusBarItem.getConnectionDetails();

    if (state === "connected") {
      vscode.window.showInformationMessage(details);
    } else if (state === "degraded") {
      vscode.window.showWarningMessage(details);
    } else if (state === "offline") {
      vscode.window.showErrorMessage(details);
    } else {
      // disabled
      vscode.window.showInformationMessage(details);
    }
  });
}

function formatShortDate(isoDate: string): string {
  const date = new Date(isoDate);
  return date.toLocaleDateString(undefined, {
    month: "short",
    day: "numeric",
    year: "numeric",
  });
}
