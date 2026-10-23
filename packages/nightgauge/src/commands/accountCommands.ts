import * as vscode from "vscode";
import type { TrialStateStore } from "../platform/TrialState";
import type { GitHubAuthService } from "../services/GitHubAuthService";
import type { OAuthDeviceFlowService } from "../services/OAuthDeviceFlowService";
import type { Logger } from "../utils/logger";
import { registerSignInCommand } from "./signIn";
import { registerSignOutCommand } from "./signOut";

/**
 * Register account commands as an invariant of extension activation.
 *
 * Command-palette visibility may be gated by nightgauge.cloudEnabled, but
 * contributed commands must never be left without handlers. These services
 * are available in empty windows and when platform.enabled disables automatic
 * platform communication.
 */
export function registerAccountCommands(
  oauthDeviceFlowService: OAuthDeviceFlowService,
  gitHubAuthService: GitHubAuthService,
  logger: Logger,
  trialStore: TrialStateStore
): vscode.Disposable[] {
  return [
    registerSignInCommand(oauthDeviceFlowService, gitHubAuthService, logger),
    registerSignOutCommand(oauthDeviceFlowService, logger, trialStore),
    vscode.commands.registerCommand("nightgauge.signInWithGitHub", async () => {
      await gitHubAuthService.signInWithGitHub();
    }),
  ];
}
