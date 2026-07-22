/**
 * Disable Auto-Accept command
 *
 * Quick override to disable auto-accept. Sets both auto_accept_stages and
 * auto_accept_permissions to false in .nightgauge/config.local.yaml —
 * the gitignored local tier. Local wins the tier merge, so this also
 * overrides a committed team-config `true` without dirtying the working
 * tree.
 */

import * as vscode from "vscode";
import { IncrediYamlService } from "../views/settings/IncrediYamlService";
import type { Logger } from "../utils/logger";

/**
 * Register the Disable Auto-Accept command
 */
export function registerDisableAutoAcceptCommand(logger: Logger): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.disableAutoAccept", async () => {
    logger.debug("Disabling auto-accept settings");

    const workspaceRoot = vscode.workspace.workspaceFolders?.[0]?.uri.fsPath;
    if (!workspaceRoot) {
      vscode.window.showErrorMessage(
        "No workspace folder open. Cannot update .nightgauge/config.yaml"
      );
      return;
    }

    try {
      const yamlService = new IncrediYamlService(workspaceRoot);
      const result = await yamlService.readLocal();

      if (!result.success) {
        vscode.window.showErrorMessage(
          `Failed to read .nightgauge/config.local.yaml: ${result.error}`
        );
        return;
      }

      // Update the local tier to disable auto-accept — an explicit false here
      // overrides any committed team-config true via the tier merge.
      const updatedConfig = {
        ...(result.config ?? {}),
        human_in_the_loop: {
          auto_accept_stages: false,
          auto_accept_permissions: false,
          trusted_stages: [],
        },
      };

      const writeResult = await yamlService.writeLocal(updatedConfig);
      if (!writeResult.success) {
        vscode.window.showErrorMessage(
          `Failed to write .nightgauge/config.local.yaml: ${writeResult.error}`
        );
        return;
      }

      logger.info("Auto-accept disabled");
      vscode.window.showInformationMessage(
        "✓ Auto-accept disabled in .nightgauge/config.local.yaml (local override tier)"
      );
    } catch (error) {
      logger.error("Failed to disable auto-accept", { error });
      vscode.window.showErrorMessage(
        `Failed to disable auto-accept: ${error instanceof Error ? error.message : String(error)}`
      );
    }
  });
}
