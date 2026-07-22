/**
 * showNotifierSettings - Command to open the Notifier Settings webview panel (#3379).
 *
 * Opens a singleton webview for managing Discord and Mattermost notifier instances.
 */

import * as vscode from "vscode";
import { ConfigBridge } from "../services/ConfigBridge";
import { IncrediYamlService } from "../views/settings/IncrediYamlService";
import { NotifierSettingsPanel } from "../views/notifier/NotifierSettingsPanel";
import { getWorkspaceRoot } from "../config/settings";

export function registerShowNotifierSettingsCommand(
  context: vscode.ExtensionContext
): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.showNotifierSettings", () => {
    const workspaceRoot = getWorkspaceRoot();
    if (!workspaceRoot) {
      vscode.window.showErrorMessage(
        "Nightgauge: No workspace folder open — cannot open Notifier Settings."
      );
      return;
    }

    const configBridge = ConfigBridge.getInstance();
    const yamlService = new IncrediYamlService(workspaceRoot);

    NotifierSettingsPanel.show(context, configBridge, yamlService);
  });
}
