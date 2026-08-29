/**
 * configureSlackNotifications — store or remove the Slack bot token.
 *
 * The token is stored in VSCode SecretStorage (OS keychain) — never in any
 * file. Users run this command once after creating their Slack app.
 *
 * Unlike the Discord and Mattermost commands, this one also prompts for the
 * target channel: an incoming webhook has its channel baked into the URL, but a
 * bot token can post anywhere, so the channel is a separate setting. The channel
 * lives in config rather than the keychain — it is not a secret. This command
 * writes it to the machine tier itself; it used to prompt for the channel and
 * then discard it, telling the operator to add it to YAML by hand (#1115).
 *
 * @see Issue #1073
 * @see SlackService — consumes SECRET_KEYS.slackBotToken and notifications.slack.channel
 */

import * as vscode from "vscode";
import { SecretStorageService, SECRET_KEYS } from "../services/SecretStorageService";
import { isSlackBotToken } from "../services/notifications/SlackService";
import { reportNotifierSetup } from "./notifierSetupReport";

/**
 * A Slack channel id (`C…`/`G…`/`D…`) or a `#name`. The id is preferred and is
 * what the docs recommend: a channel rename silently breaks a `#name` lookup,
 * while the id is stable for the channel's lifetime.
 */
const SLACK_CHANNEL_PATTERN = /^(?:[CGD][A-Z0-9]{6,}|#[a-z0-9][a-z0-9._-]{0,79})$/;

/** Register the configureSlackNotifications command. */
export function registerConfigureSlackNotificationsCommand(): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.configureSlackNotifications", async () => {
    const secretService = SecretStorageService.getInstance();
    if (!secretService) {
      vscode.window.showErrorMessage("Nightgauge: SecretStorage is not available.");
      return;
    }

    const existing = await secretService.getSecret(SECRET_KEYS.slackBotToken);

    const action = existing
      ? await vscode.window.showQuickPick(["Update bot token", "Remove bot token"], {
          title: "Slack Notifications",
          placeHolder: "A bot token is already configured",
        })
      : "Update bot token";

    if (!action) return;

    if (action === "Remove bot token") {
      await secretService.deleteSecret(SECRET_KEYS.slackBotToken);
      vscode.window.showInformationMessage("Nightgauge: Slack bot token removed.");
      return;
    }

    const token = await vscode.window.showInputBox({
      title: "Configure Slack Notifications",
      prompt:
        "Paste your Slack app's Bot User OAuth Token (needs the chat:write scope). " +
        "It will be stored securely in the OS keychain.",
      placeHolder: "xoxb-…",
      // The token is a live credential: never echo it back into the box, and
      // never pre-fill it from the keychain.
      password: true,
      ignoreFocusOut: true,
      validateInput: (value) => {
        if (!value.trim()) return "Token cannot be empty";
        if (!isSlackBotToken(value)) {
          // The realistic paste-mistakes, named so the fix is obvious. Without
          // this, each surfaces as an opaque invalid_auth at the first run.
          const v = value.trim();
          if (v.startsWith("xoxp-")) return "That is a user token — use the Bot User OAuth Token";
          if (v.startsWith("xapp-"))
            return "That is an app-level token — use the Bot User OAuth Token";
          if (v.startsWith("http")) return "That is a webhook URL — use the Bot User OAuth Token";
          return "Must be a Slack bot token (starts with xoxb-)";
        }
        return null;
      },
    });

    if (!token) return;

    const channel = await vscode.window.showInputBox({
      title: "Slack Channel",
      prompt:
        "Channel to post pipeline status into. The channel ID is preferred — a rename breaks a #name.",
      placeHolder: "C0123456789",
      ignoreFocusOut: true,
      validateInput: (value) => {
        if (!value.trim()) return "Channel cannot be empty";
        if (!SLACK_CHANNEL_PATTERN.test(value.trim())) {
          return "Must be a channel ID (C…) or a #channel-name";
        }
        return null;
      },
    });

    if (!channel) return;

    await secretService.setSecret(SECRET_KEYS.slackBotToken, token.trim());

    // Persist the channel we just collected instead of discarding it and telling
    // the operator to type it again by hand (#1115). It goes to the machine
    // tier, which is where notifications.* belongs — the old message named the
    // project tier and would have produced a second, shadowing block.
    await reportNotifierSetup("slack", { channel });
  });
}
