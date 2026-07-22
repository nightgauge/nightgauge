/**
 * Sign In command — shows a quick pick to choose between GitHub and Device Flow auth.
 *
 * Delegates to GitHubAuthService (vscode.authentication) or OAuthDeviceFlowService
 * (RFC 8628 browser + code) based on user selection.
 *
 * @see Issue #1467 - Add GitHub Sign-in as alternative auth path
 * @see Issue #1464 - Implement OAuth Device Flow login command and UI
 */

import * as vscode from "vscode";
import type { OAuthDeviceFlowService } from "../services/OAuthDeviceFlowService";
import { AUTH_METHOD, type GitHubAuthService } from "../services/GitHubAuthService";
import type { Logger } from "../utils/logger";

interface AuthMethodItem extends vscode.QuickPickItem {
  method: (typeof AUTH_METHOD)[keyof typeof AUTH_METHOD];
}

const AUTH_METHOD_ITEMS: AuthMethodItem[] = [
  {
    label: "$(mark-github) GitHub",
    description: "Sign in using your GitHub account (recommended)",
    method: AUTH_METHOD.GITHUB,
  },
  {
    label: "$(device-desktop) Device Flow",
    description: "Sign in with a browser code (works without GitHub in VSCode)",
    method: AUTH_METHOD.DEVICE_FLOW,
  },
];

export function registerSignInCommand(
  oauthService: OAuthDeviceFlowService,
  gitHubAuthService: GitHubAuthService,
  logger: Logger
): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.signIn", async () => {
    const selection = await vscode.window.showQuickPick(AUTH_METHOD_ITEMS, {
      placeHolder: "Choose a sign-in method",
      title: "Nightgauge: Sign In",
    });

    if (!selection) return; // User dismissed

    try {
      if (selection.method === AUTH_METHOD.GITHUB) {
        await gitHubAuthService.signInWithGitHub();
      } else {
        await oauthService.startDeviceFlow();
      }
    } catch (err) {
      const error = err instanceof Error ? err : new Error(String(err));
      logger.error("Sign-in failed", error);
      vscode.window.showErrorMessage(`Nightgauge: Sign-in failed — ${error.message}. Try again.`);
    }
  });
}
