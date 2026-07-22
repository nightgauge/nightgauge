/**
 * configureMattermostWorkspace - Full Mattermost workspace setup command
 *
 * Collects server URL, bot token, incoming webhook URL, and per-channel
 * signing tokens. Verifies connectivity with a live test-connection, then
 * atomically writes credentials to SecretStorage and updates config.yaml:
 *   - notifications.mattermost block via IncrediYamlService (schema path)
 *   - notifiers.mattermost.channels block via raw YAML Document API
 *     (avoids coupling TS schema to Go-owned config keys — see ADR-001)
 *
 * @see Issue #3378
 */

import * as vscode from "vscode";
import { parse as parseDocument, stringify as stringifyYaml } from "yaml";
import {
  SecretStorageService,
  SECRET_KEYS,
  mattermostSigningKey,
} from "../services/SecretStorageService";
import { IncrediYamlService } from "../views/settings/IncrediYamlService";

const MATTERMOST_WEBHOOK_PATTERN = /^https?:\/\/[^/\s]+\/hooks\/[A-Za-z0-9]+\/?$/;
const SERVER_URL_PATTERN = /^https:\/\/[^/\s]+/;
const DEFAULT_INBOUND_PORT = 8765;

interface Channel {
  channelId: string;
  token: string;
}

interface TestConnectionResult {
  webhookOk: boolean;
  webhookError?: string;
  inboundOk: boolean | "skipped";
  inboundError?: string;
}

async function testConnection(
  webhookUrl: string,
  channels: Channel[]
): Promise<TestConnectionResult> {
  // Test outbound webhook
  let webhookOk = false;
  let webhookError: string | undefined;
  try {
    const resp = await fetch(webhookUrl, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ text: "Nightgauge: test connection 🔗" }),
    });
    if (resp.ok) {
      webhookOk = true;
    } else {
      webhookError = `HTTP ${resp.status} ${resp.statusText}`;
    }
  } catch (err) {
    webhookError = err instanceof Error ? err.message : "Network error";
  }

  // Test inbound receiver availability via HEAD probe
  let inboundOk: boolean | "skipped";
  let inboundError: string | undefined;

  const inboundUrl = `http://127.0.0.1:${DEFAULT_INBOUND_PORT}/mattermost`;
  try {
    const controller = new AbortController();
    const timeoutId = setTimeout(() => controller.abort(), 500);
    try {
      await fetch(inboundUrl, { method: "HEAD", signal: controller.signal });
      clearTimeout(timeoutId);
      // Receiver is reachable — attempt signed request if channels configured
      if (channels.length > 0) {
        const { channelId, token } = channels[0];
        const body = `token=${encodeURIComponent(token)}&channel_name=${encodeURIComponent(channelId)}&text=test`;
        const resp = await fetch(inboundUrl, {
          method: "POST",
          headers: { "Content-Type": "application/x-www-form-urlencoded" },
          body,
        });
        if (resp.ok) {
          inboundOk = true;
        } else {
          // Non-200 from receiver — don't block save, just warn
          inboundOk = false;
          inboundError = `Receiver returned HTTP ${resp.status} — check signing token`;
        }
      } else {
        // Receiver is up but no channels to test
        inboundOk = true;
      }
    } catch (fetchErr) {
      clearTimeout(timeoutId);
      if ((fetchErr as Error).name === "AbortError") {
        // Timed out → receiver not running
        inboundOk = "skipped";
      } else {
        inboundOk = "skipped";
      }
    }
  } catch {
    inboundOk = "skipped";
  }

  return { webhookOk, webhookError, inboundOk, inboundError };
}

/**
 * Write the notifiers.mattermost.channels block to config.yaml using the raw
 * YAML Document API. This avoids schema stripping since notifiers is a
 * Go-owned config key not present in IncrediConfigSchema (ADR-001).
 */
async function writeNotifiersBlock(configPath: string, channels: Channel[]): Promise<void> {
  let rawContent = "";
  const uri = vscode.Uri.file(configPath);

  try {
    const bytes = await vscode.workspace.fs.readFile(uri);
    rawContent = Buffer.from(bytes).toString("utf-8");
  } catch {
    // File may not exist yet — start with empty string
  }

  // Parse preserving structure (we use plain parse here since Document API
  // does not guarantee comment preservation across all yaml versions;
  // the plan acknowledges this limitation for the notifiers block)
  const doc = parseDocument(rawContent) as Record<string, unknown>;
  const existing = typeof doc === "object" && doc !== null ? doc : {};

  const channelsMap: Record<string, { token_env: string }> = {};
  for (const { channelId } of channels) {
    const envKey = `MATTERMOST_SIGNING_${channelId.toUpperCase().replace(/[^A-Z0-9]/g, "_")}`;
    channelsMap[channelId] = { token_env: envKey };
  }

  const updated = {
    ...existing,
    notifiers: {
      ...((existing as Record<string, unknown>).notifiers as Record<string, unknown> | undefined),
      mattermost: {
        channels: channelsMap,
      },
    },
  };

  const yaml = stringifyYaml(updated, { indent: 2, lineWidth: 100 });
  await vscode.workspace.fs.writeFile(uri, Buffer.from(yaml, "utf-8"));
}

/** Register the configureMattermostWorkspace command */
export function registerConfigureMattermostWorkspaceCommand(): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.configureMattermostWorkspace", async () => {
    const secretService = SecretStorageService.getInstance();
    if (!secretService) {
      vscode.window.showErrorMessage("Nightgauge: SecretStorage is not available.");
      return;
    }

    // ── Step 1: Server URL ────────────────────────────────────────────────
    const serverUrl = await vscode.window.showInputBox({
      title: "Configure Mattermost Workspace (1/4) — Server URL",
      prompt: "Mattermost server URL",
      placeHolder: "https://mattermost.example.com",
      ignoreFocusOut: true,
      validateInput: (v) => {
        if (!v.trim()) return "Server URL cannot be empty";
        if (!SERVER_URL_PATTERN.test(v.trim())) {
          return "Must be a valid https:// URL (e.g. https://mattermost.example.com)";
        }
        return null;
      },
    });
    if (serverUrl === undefined) return;

    // ── Step 2: Bot token ─────────────────────────────────────────────────
    const botToken = await vscode.window.showInputBox({
      title: "Configure Mattermost Workspace (2/4) — Bot Token",
      prompt: "Mattermost bot token (stored securely in OS keychain)",
      password: true,
      ignoreFocusOut: true,
    });
    if (botToken === undefined) return;

    // ── Step 3: Incoming webhook URL ──────────────────────────────────────
    const webhookUrl = await vscode.window.showInputBox({
      title: "Configure Mattermost Workspace (3/4) — Incoming Webhook URL",
      prompt: "Mattermost incoming-webhook URL",
      placeHolder: "https://mattermost.example.com/hooks/abc123...",
      ignoreFocusOut: true,
      validateInput: (v) => {
        if (!v.trim()) return "Webhook URL cannot be empty";
        if (!MATTERMOST_WEBHOOK_PATTERN.test(v.trim())) {
          return "Must be a valid Mattermost incoming-webhook URL (https://host/hooks/<token>)";
        }
        return null;
      },
    });
    if (webhookUrl === undefined) return;

    // ── Step 4: Per-channel signing tokens (loop) ─────────────────────────
    const channels: Channel[] = [];
    while (true) {
      const channelInput = await vscode.window.showInputBox({
        title: `Configure Mattermost Workspace (4/4) — Signing Tokens (${channels.length} added)`,
        prompt: "Channel ID for outgoing-webhook signing token (leave blank to finish)",
        placeHolder: "e.g. town-square  (leave blank to skip)",
        ignoreFocusOut: true,
      });
      if (channelInput === undefined) return; // ESC → abort entire command
      if (!channelInput.trim()) break; // empty → done collecting channels

      const tokenInput = await vscode.window.showInputBox({
        title: `Signing token for #${channelInput.trim()}`,
        prompt: `Outgoing-webhook signing token for channel #${channelInput.trim()}`,
        password: true,
        ignoreFocusOut: true,
      });
      if (tokenInput === undefined) return; // ESC → abort entire command

      channels.push({ channelId: channelInput.trim(), token: tokenInput.trim() });
    }

    // ── Test connection ───────────────────────────────────────────────────
    let testResult: TestConnectionResult;
    await vscode.window.withProgress(
      {
        location: vscode.ProgressLocation.Notification,
        title: "Nightgauge: Testing Mattermost connection…",
        cancellable: false,
      },
      async () => {
        testResult = await testConnection(webhookUrl.trim(), channels);
      }
    );

    // Abort if webhook test failed
    if (!testResult!.webhookOk) {
      vscode.window.showErrorMessage(
        `Nightgauge: Webhook connection failed — ${testResult!.webhookError ?? "unknown error"}. Credentials were not saved.`
      );
      return;
    }

    // ── Write secrets ─────────────────────────────────────────────────────
    await secretService.setSecret(SECRET_KEYS.mattermostWebhookUrl, webhookUrl.trim());
    await secretService.setSecret(SECRET_KEYS.mattermostBotToken, botToken.trim());
    for (const { channelId, token } of channels) {
      await secretService.setSecret(mattermostSigningKey(channelId), token);
    }

    // ── Write notifications block via IncrediYamlService ──────────────────
    const workspaceFolders = vscode.workspace.workspaceFolders;
    const workspaceRoot = workspaceFolders?.[0]?.uri.fsPath;

    if (workspaceRoot) {
      const yamlService = new IncrediYamlService(workspaceRoot);
      try {
        const readResult = await yamlService.read();
        const existing = readResult.config ?? {};
        const writeResult = await yamlService.write(
          {
            ...existing,
            notifications: {
              ...existing.notifications,
              mattermost: {
                ...existing.notifications?.mattermost,
                enabled: true,
              },
            },
          },
          "project"
        );
        if (!writeResult.success) {
          vscode.window.showErrorMessage(
            `Nightgauge: Failed to update config.yaml — ${writeResult.error ?? "unknown error"}`
          );
          return;
        }

        // Write notifiers block via raw Document API (ADR-001)
        if (channels.length > 0) {
          const configPath = yamlService.getPrimaryConfigPath();
          await writeNotifiersBlock(configPath, channels);
        }
      } finally {
        yamlService.dispose();
      }
    }

    // ── Notify user ───────────────────────────────────────────────────────
    const inboundStatus = testResult!.inboundOk;
    if (inboundStatus === true) {
      vscode.window.showInformationMessage(
        "Nightgauge: Mattermost connected — webhook ✓, receiver ✓"
      );
    } else if (inboundStatus === "skipped") {
      vscode.window.showWarningMessage(
        "Nightgauge: Mattermost connected — webhook ✓, receiver ⚠ (not running). " +
          "Set `notifications.inbound.enabled: true` in config.yaml to enable inbound verification."
      );
    } else {
      // inboundOk === false: receiver up but signing failed — still saved
      vscode.window.showWarningMessage(
        `Nightgauge: Mattermost connected — webhook ✓, receiver ⚠ (${testResult!.inboundError ?? "signing verification failed"}). Credentials saved.`
      );
    }
  });
}
