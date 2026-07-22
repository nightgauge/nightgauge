/**
 * configureMattermostWebhook - Store or remove the Mattermost webhook URL
 *
 * The URL is stored in VSCode SecretStorage (OS keychain) — never in any file.
 * Users run this command once after installing the extension.
 *
 * @see Issue #3373
 */

import * as vscode from "vscode";
import { SecretStorageService, SECRET_KEYS } from "../services/SecretStorageService";

const MATTERMOST_WEBHOOK_PATTERN = /^https?:\/\/[^/\s]+\/hooks\/[A-Za-z0-9]+\/?$/;

/** Register the configureMattermostWebhook command */
export function registerConfigureMattermostWebhookCommand(): vscode.Disposable {
  return vscode.commands.registerCommand(
    "nightgauge.configureMattermostNotifications",
    async () => {
      const secretService = SecretStorageService.getInstance();
      if (!secretService) {
        vscode.window.showErrorMessage("Nightgauge: SecretStorage is not available.");
        return;
      }

      const existing = await secretService.getSecret(SECRET_KEYS.mattermostWebhookUrl);

      const action = existing
        ? await vscode.window.showQuickPick(["Update webhook URL", "Remove webhook URL"], {
            title: "Mattermost Notifications",
            placeHolder: "Webhook is already configured",
          })
        : "Update webhook URL";

      if (!action) return;

      if (action === "Remove webhook URL") {
        await secretService.deleteSecret(SECRET_KEYS.mattermostWebhookUrl);
        vscode.window.showInformationMessage("Nightgauge: Mattermost webhook URL removed.");
        return;
      }

      const url = await vscode.window.showInputBox({
        title: "Configure Mattermost Notifications",
        prompt:
          "Paste your Mattermost incoming-webhook URL. It will be stored securely in the OS keychain.",
        placeHolder: "https://mattermost.example.com/hooks/abc123def456...",
        value: existing ?? "",
        password: false,
        ignoreFocusOut: true,
        validateInput: (value) => {
          if (!value.trim()) return "URL cannot be empty";
          if (!MATTERMOST_WEBHOOK_PATTERN.test(value.trim())) {
            return "Must be a valid Mattermost incoming-webhook URL (https://host/hooks/<token>)";
          }
          return null;
        },
      });

      if (!url) return;

      await secretService.setSecret(SECRET_KEYS.mattermostWebhookUrl, url.trim());

      vscode.window.showInformationMessage(
        "Nightgauge: Mattermost webhook configured. " +
          "Make sure `notifications.mattermost.enabled: true` is set in .nightgauge/config.yaml."
      );
    }
  );
}
