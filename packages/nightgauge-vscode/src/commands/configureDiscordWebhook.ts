/**
 * configureDiscordWebhook - Store or remove the Discord webhook URL
 *
 * The URL is stored in VSCode SecretStorage (OS keychain) — never in any file.
 * Users run this command once after installing the extension.
 */

import * as vscode from "vscode";
import { SecretStorageService, SECRET_KEYS } from "../services/SecretStorageService";

/** Register the configureDiscordWebhook command */
export function registerConfigureDiscordWebhookCommand(): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.configureDiscordNotifications", async () => {
    const secretService = SecretStorageService.getInstance();
    if (!secretService) {
      vscode.window.showErrorMessage("Nightgauge: SecretStorage is not available.");
      return;
    }

    const existing = await secretService.getSecret(SECRET_KEYS.discordWebhookUrl);

    // If already configured, let the user update or remove it
    const action = existing
      ? await vscode.window.showQuickPick(["Update webhook URL", "Remove webhook URL"], {
          title: "Discord Notifications",
          placeHolder: "Webhook is already configured",
        })
      : "Update webhook URL";

    if (!action) return; // dismissed

    if (action === "Remove webhook URL") {
      await secretService.deleteSecret(SECRET_KEYS.discordWebhookUrl);
      vscode.window.showInformationMessage("Nightgauge: Discord webhook URL removed.");
      return;
    }

    const url = await vscode.window.showInputBox({
      title: "Configure Discord Notifications",
      prompt: "Paste your Discord webhook URL. It will be stored securely in the OS keychain.",
      placeHolder: "https://discord.com/api/webhooks/...",
      value: existing ?? "",
      password: false,
      ignoreFocusOut: true,
      validateInput: (value) => {
        if (!value.trim()) return "URL cannot be empty";
        if (!/discord\.com\/api\/webhooks\/\d+\/[\w-]+/.test(value)) {
          return "Must be a valid Discord webhook URL";
        }
        return null;
      },
    });

    if (!url) return; // dismissed or cleared

    await secretService.setSecret(SECRET_KEYS.discordWebhookUrl, url.trim());

    // Ensure Discord notifications are enabled in config if not already set
    vscode.window.showInformationMessage(
      "Nightgauge: Discord webhook configured. " +
        "Make sure `notifications.discord.enabled: true` is set in .nightgauge/config.yaml."
    );
  });
}
