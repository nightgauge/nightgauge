/**
 * NotifierSettingsPanel — singleton webview for notifier instance management (#3379).
 *
 * Shows Discord and Mattermost notifier instances sourced from:
 *   1. `notifiers[]` array in effective config (multi-instance routing, Issue #3374)
 *   2. Legacy `notifications.discord` / `notifications.mattermost` blocks when
 *      a webhook URL exists in SecretStorage but no `notifiers[]` entry exists.
 *
 * Follows the TelemetrySettingsPanel singleton pattern.
 */

import * as vscode from "vscode";
import type { ConfigBridge } from "../../services/ConfigBridge";
import { IncrediYamlService } from "../settings/IncrediYamlService";
import { SecretStorageService, SECRET_KEYS } from "../../services/SecretStorageService";
import {
  NotifierStatusTracker,
  type NotifierStatus,
} from "../../services/notifications/NotifierStatusTracker";
import { redactSecrets } from "../../services/notifications/transport";
import {
  NotifierSettingsMessageHandler,
  type NotifierExtensionToWebViewMessage,
} from "./NotifierSettingsMessageHandler";
import type { NotifierInstanceRow } from "./NotifierInstancesSection";
import type { NotifierRoutingRule } from "../../config/schema";

/** 15-minute window within which a last-success is considered "connected". */
const CONNECTED_WINDOW_MS = 15 * 60 * 1000;

export class NotifierSettingsPanel implements vscode.Disposable {
  private static currentPanel: NotifierSettingsPanel | undefined;

  private panel: vscode.WebviewPanel | undefined;
  private readonly disposables: vscode.Disposable[] = [];
  private readonly messageHandler: NotifierSettingsMessageHandler;

  private constructor(
    private readonly context: vscode.ExtensionContext,
    private readonly configBridge: ConfigBridge,
    private readonly yamlService: IncrediYamlService
  ) {
    this.messageHandler = new NotifierSettingsMessageHandler({
      onGetState: () => this.loadNotifiers(),
      onNotifierAdd: async (notifierType) => {
        const cmd =
          notifierType === "discord"
            ? "nightgauge.configureDiscordNotifications"
            : "nightgauge.configureMattermostNotifications";
        await vscode.commands.executeCommand(cmd);
        await this.loadNotifiers();
      },
      onNotifierAction: async (action, id) => {
        if (action === "test") {
          await this.handleTest(id);
        } else {
          await this.handleRemove(id);
        }
      },
    });

    const configListener = this.configBridge.onConfigChanged(() => {
      void this.loadNotifiers();
    });
    this.disposables.push(configListener);
  }

  static show(
    context: vscode.ExtensionContext,
    configBridge: ConfigBridge,
    yamlService: IncrediYamlService
  ): NotifierSettingsPanel {
    if (!NotifierSettingsPanel.currentPanel) {
      NotifierSettingsPanel.currentPanel = new NotifierSettingsPanel(
        context,
        configBridge,
        yamlService
      );
    }
    NotifierSettingsPanel.currentPanel.reveal();
    return NotifierSettingsPanel.currentPanel;
  }

  static get current(): NotifierSettingsPanel | undefined {
    return NotifierSettingsPanel.currentPanel;
  }

  private reveal(): void {
    if (this.panel) {
      this.panel.reveal(vscode.ViewColumn.One);
      void this.loadNotifiers();
      return;
    }

    this.panel = vscode.window.createWebviewPanel(
      "incrediNotifierSettings",
      "Nightgauge: Notifier Settings",
      vscode.ViewColumn.One,
      { enableScripts: true, retainContextWhenHidden: true }
    );

    this.panel.webview.html = this.renderHtml();
    this.panel.webview.onDidReceiveMessage(
      this.messageHandler.handleMessage,
      undefined,
      this.disposables
    );
    this.panel.onDidDispose(() => this.handlePanelClosed(), undefined, this.disposables);
    void this.loadNotifiers();
  }

  // ─── Data loading ─────────────────────────────────────────────────────────────

  private async loadNotifiers(): Promise<void> {
    if (!this.panel) return;

    const config = this.configBridge.getEffectiveConfig()?.config;
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const notifiersConfig: NotifierRoutingRule[] = (config?.notifiers ?? []) as any;

    const secretService = SecretStorageService.getInstance();
    const tracker = NotifierStatusTracker.getInstance();

    const rows: NotifierInstanceRow[] = [];
    const seenTypes = new Set<string>();

    for (const rule of notifiersConfig) {
      seenTypes.add(rule.type);
      const webhookKey =
        rule.webhook_secret_key ??
        (rule.type === "discord"
          ? SECRET_KEYS.discordWebhookUrl
          : SECRET_KEYS.mattermostWebhookUrl);
      const webhookUrl = secretService ? await secretService.getSecret(webhookKey) : undefined;

      const notifierStatus = tracker?.getStatus(rule.id);
      rows.push({
        id: rule.id,
        type: rule.type,
        channel: rule.channel,
        status: computeStatus(notifierStatus),
        lastEventSentAt: notifierStatus?.lastSuccessAt?.toISOString(),
        lastError: notifierStatus?.lastError,
        webhookRedacted: webhookUrl ? redactWebhookUrl(webhookUrl) : undefined,
      });
    }

    // Legacy single-instance fallback rows
    if (!seenTypes.has("discord")) {
      const discordUrl = secretService
        ? await secretService.getSecret(SECRET_KEYS.discordWebhookUrl)
        : undefined;
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const discordEnabled = (config?.notifications as any)?.discord?.enabled;
      if (discordUrl || discordEnabled) {
        const notifierStatus = tracker?.getStatus("discord");
        rows.push({
          id: "discord",
          type: "discord",
          status: computeStatus(notifierStatus),
          lastEventSentAt: notifierStatus?.lastSuccessAt?.toISOString(),
          lastError: notifierStatus?.lastError,
          webhookRedacted: discordUrl ? redactWebhookUrl(discordUrl) : undefined,
        });
      }
    }

    if (!seenTypes.has("mattermost")) {
      const mattermostUrl = secretService
        ? await secretService.getSecret(SECRET_KEYS.mattermostWebhookUrl)
        : undefined;
      // eslint-disable-next-line @typescript-eslint/no-explicit-any
      const mattermostEnabled = (config?.notifications as any)?.mattermost?.enabled;
      if (mattermostUrl || mattermostEnabled) {
        const notifierStatus = tracker?.getStatus("mattermost");
        rows.push({
          id: "mattermost",
          type: "mattermost",
          status: computeStatus(notifierStatus),
          lastEventSentAt: notifierStatus?.lastSuccessAt?.toISOString(),
          lastError: notifierStatus?.lastError,
          webhookRedacted: mattermostUrl ? redactWebhookUrl(mattermostUrl) : undefined,
        });
      }
    }

    await this.postMessage({ type: "update", notifiers: rows });
  }

  // ─── Action handlers ──────────────────────────────────────────────────────────

  private async handleTest(id: string): Promise<void> {
    const config = this.configBridge.getEffectiveConfig()?.config;
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const notifierRule = ((config?.notifiers ?? []) as any[]).find((r) => r.id === id);
    const type: string = notifierRule?.type ?? id;

    const secretService = SecretStorageService.getInstance();
    const webhookKey =
      notifierRule?.webhook_secret_key ??
      (type === "discord" ? SECRET_KEYS.discordWebhookUrl : SECRET_KEYS.mattermostWebhookUrl);
    const webhookUrl = secretService ? await secretService.getSecret(webhookKey) : undefined;

    if (!webhookUrl) {
      await this.postMessage({
        type: "test-result",
        id,
        ok: false,
        error: "No webhook URL configured. Use Add Discord / Add Mattermost to set one up.",
      });
      return;
    }

    let result: { ok: boolean; error?: string };
    try {
      result = await sendTestWebhook(type, webhookUrl);
    } catch (err) {
      result = { ok: false, error: redactSecrets(String(err)) };
    }

    await this.postMessage({ type: "test-result", id, ok: result.ok, error: result.error });
  }

  private async handleRemove(id: string): Promise<void> {
    const confirmed = await vscode.window.showWarningMessage(
      `Remove notifier "${id}"? This will delete the stored webhook URL and cannot be undone.`,
      { modal: true },
      "Remove"
    );
    if (confirmed !== "Remove") return;

    const config = this.configBridge.getEffectiveConfig()?.config;
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const notifierRule = ((config?.notifiers ?? []) as any[]).find((r) => r.id === id);

    if (notifierRule) {
      // Multi-instance: remove from notifiers[] and delete the webhook key
      await this.removeFromNotifiersConfig(id);
      const secretService = SecretStorageService.getInstance();
      if (secretService && notifierRule.webhook_secret_key) {
        await secretService.deleteSecret(notifierRule.webhook_secret_key);
      }
    } else {
      // Legacy entry: delete the SecretStorage key
      const secretService = SecretStorageService.getInstance();
      if (secretService) {
        const key =
          id === "discord" ? SECRET_KEYS.discordWebhookUrl : SECRET_KEYS.mattermostWebhookUrl;
        await secretService.deleteSecret(key);
      }
    }

    await this.loadNotifiers();
  }

  private async removeFromNotifiersConfig(id: string): Promise<void> {
    const result = await this.yamlService.read();
    if (!result.success || !result.config) return;

    const notifiers = result.config.notifiers ?? [];
    // eslint-disable-next-line @typescript-eslint/no-explicit-any
    const updated = (notifiers as any[]).filter((r) => r.id !== id);
    result.config.notifiers = updated as typeof notifiers;
    await this.yamlService.write(result.config, "project");
  }

  // ─── Messaging ────────────────────────────────────────────────────────────────

  private async postMessage(msg: NotifierExtensionToWebViewMessage): Promise<void> {
    if (!this.panel) return;
    await this.panel.webview.postMessage(msg);
  }

  // ─── Lifecycle ────────────────────────────────────────────────────────────────

  private handlePanelClosed(): void {
    this.panel = undefined;
    NotifierSettingsPanel.currentPanel = undefined;
    while (this.disposables.length) {
      const d = this.disposables.pop();
      d?.dispose();
    }
  }

  dispose(): void {
    this.handlePanelClosed();
    this.panel?.dispose();
  }

  // ─── HTML rendering ───────────────────────────────────────────────────────────

  private renderHtml(): string {
    return /* html */ `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1.0" />
  <title>Notifier Settings</title>
  <style>
    body {
      font-family: var(--vscode-font-family);
      color: var(--vscode-foreground);
      background: var(--vscode-editor-background);
      padding: 1.5rem;
      max-width: 900px;
    }
    h1 { font-size: 1.4rem; margin-top: 0; }
    .meta { color: var(--vscode-descriptionForeground); font-size: 0.9rem; margin-bottom: 1.5rem; }
    /* Toolbar */
    .notifier-toolbar { margin-bottom: 12px; display: flex; gap: 8px; }
    .notifier-add-btn {
      display: inline-flex; align-items: center; gap: 6px;
      padding: 5px 12px;
      background: var(--vscode-button-background);
      color: var(--vscode-button-foreground);
      border: none; border-radius: 2px; cursor: pointer; font-size: 12px;
    }
    .notifier-add-btn:hover { background: var(--vscode-button-hoverBackground); }
    /* Table */
    .notifier-table { width: 100%; border-collapse: collapse; font-size: 12px; }
    .notifier-table th {
      text-align: left; padding: 6px 8px;
      border-bottom: 1px solid var(--vscode-panel-border);
      color: var(--vscode-descriptionForeground); font-weight: 600;
    }
    .notifier-table td {
      padding: 6px 8px;
      border-bottom: 1px solid var(--vscode-panel-border);
      vertical-align: middle;
    }
    .notifier-empty-row {
      text-align: center; color: var(--vscode-descriptionForeground); padding: 16px !important;
    }
    /* Badges */
    .notifier-badge {
      display: inline-block; padding: 1px 6px;
      border-radius: 10px; font-size: 10px; font-weight: 600;
    }
    .badge-discord { background: #5865f2; color: #fff; }
    .badge-mattermost { background: #0058cc; color: #fff; }
    /* Status pills */
    .notifier-status {
      display: inline-block; padding: 1px 6px;
      border-radius: 10px; font-size: 10px; font-weight: 600;
    }
    .status-connected { background: #57f287; color: #000; }
    .status-errored { background: #ed4245; color: #fff; }
    .status-disabled { background: var(--vscode-disabledForeground); color: var(--vscode-editor-background); }
    .status-unknown { background: var(--vscode-badge-background); color: var(--vscode-badge-foreground); }
    /* Action buttons */
    .notifier-cell-actions { white-space: nowrap; }
    .notifier-action-btn {
      background: none; border: none; cursor: pointer;
      padding: 2px 6px; color: var(--vscode-foreground);
      opacity: 0.7; border-radius: 2px; font-size: 11px;
    }
    .notifier-action-btn:hover { opacity: 1; background: var(--vscode-toolbar-hoverBackground); }
    .notifier-action-btn:disabled { opacity: 0.3; cursor: default; }
    .notifier-action-delete:hover { color: var(--vscode-errorForeground); }
    /* Test result banner */
    #test-banner {
      display: none; padding: 8px 12px; margin-bottom: 12px;
      border-radius: 2px; font-size: 12px;
    }
    #test-banner.ok { background: #57f28722; border: 1px solid #57f287; }
    #test-banner.fail { background: #ed424522; border: 1px solid #ed4245; }
  </style>
</head>
<body>
  <h1>Notifier Settings</h1>
  <p class="meta">Manage Discord and Mattermost pipeline notification webhooks.</p>

  <div id="test-banner"></div>

  <div class="notifier-toolbar">
    <button class="notifier-add-btn" id="btn-add-discord">+ Add Discord</button>
    <button class="notifier-add-btn" id="btn-add-mattermost">+ Add Mattermost</button>
  </div>

  <table class="notifier-table">
    <thead>
      <tr>
        <th>ID</th>
        <th>Type</th>
        <th>Channel</th>
        <th>Status</th>
        <th>Last Event</th>
        <th>Webhook</th>
        <th>Actions</th>
      </tr>
    </thead>
    <tbody id="notifier-tbody">
      <tr><td colspan="7" class="notifier-empty-row">Loading…</td></tr>
    </tbody>
  </table>

  <script>
    const vscode = acquireVsCodeApi();

    // ─── Helpers ────────────────────────────────────────────────────────────────

    function escHtml(str) {
      return String(str)
        .replace(/&/g, '&amp;').replace(/</g, '&lt;')
        .replace(/>/g, '&gt;').replace(/"/g, '&quot;');
    }

    function typeBadge(type) {
      const label = type === 'discord' ? 'Discord' : 'Mattermost';
      const cls = type === 'discord' ? 'badge-discord' : 'badge-mattermost';
      return '<span class="notifier-badge ' + cls + '">' + label + '</span>';
    }

    function statusPill(status) {
      const labels = { connected: 'Connected', errored: 'Errored', disabled: 'Disabled', unknown: 'Unknown' };
      const classes = { connected: 'status-connected', errored: 'status-errored', disabled: 'status-disabled', unknown: 'status-unknown' };
      return '<span class="notifier-status ' + (classes[status] || 'status-unknown') + '">' + (labels[status] || status) + '</span>';
    }

    function formatDate(iso) {
      if (!iso) return 'Never';
      try { return new Date(iso).toLocaleString(); } catch { return iso; }
    }

    // ─── Render ─────────────────────────────────────────────────────────────────

    function renderRows(notifiers) {
      const tbody = document.getElementById('notifier-tbody');
      if (!notifiers || notifiers.length === 0) {
        tbody.innerHTML = '<tr><td colspan="7" class="notifier-empty-row">No notifiers configured. Click <strong>Add Discord</strong> or <strong>Add Mattermost</strong> to get started.</td></tr>';
        return;
      }
      tbody.innerHTML = notifiers.map(function(n) {
        const errorAttr = n.lastError ? ' title="' + escHtml(n.lastError) + '"' : '';
        return '<tr>' +
          '<td><code>' + escHtml(n.id) + '</code></td>' +
          '<td>' + typeBadge(n.type) + '</td>' +
          '<td>' + escHtml(n.channel || '—') + '</td>' +
          '<td' + errorAttr + '>' + statusPill(n.status) + '</td>' +
          '<td>' + escHtml(formatDate(n.lastEventSentAt)) + '</td>' +
          '<td><code>' + escHtml(n.webhookRedacted || '—') + '</code></td>' +
          '<td class="notifier-cell-actions">' +
            '<button class="notifier-action-btn" data-action="test" data-id="' + escHtml(n.id) + '" title="Send test message">Test</button>' +
            '<button class="notifier-action-btn notifier-action-delete" data-action="remove" data-id="' + escHtml(n.id) + '" title="Remove">Remove</button>' +
          '</td>' +
        '</tr>';
      }).join('');
    }

    // ─── Event handlers ──────────────────────────────────────────────────────────

    document.getElementById('btn-add-discord').addEventListener('click', function() {
      vscode.postMessage({ type: 'notifier-add', notifierType: 'discord' });
    });

    document.getElementById('btn-add-mattermost').addEventListener('click', function() {
      vscode.postMessage({ type: 'notifier-add', notifierType: 'mattermost' });
    });

    document.getElementById('notifier-tbody').addEventListener('click', function(e) {
      const btn = e.target.closest('[data-action]');
      if (!btn) return;
      const action = btn.getAttribute('data-action');
      const id = btn.getAttribute('data-id');
      if (action && id) {
        vscode.postMessage({ type: 'notifier-action', action: action, id: id });
      }
    });

    // ─── Message handler ─────────────────────────────────────────────────────────

    window.addEventListener('message', function(event) {
      const msg = event.data;
      if (!msg) return;

      if (msg.type === 'update') {
        renderRows(msg.notifiers);
      } else if (msg.type === 'test-result') {
        const banner = document.getElementById('test-banner');
        if (msg.ok) {
          banner.textContent = 'Test message sent successfully for "' + msg.id + '".';
          banner.className = 'ok';
        } else {
          banner.textContent = 'Test failed for "' + msg.id + '": ' + (msg.error || 'Unknown error');
          banner.className = 'fail';
        }
        banner.style.display = 'block';
        setTimeout(function() { banner.style.display = 'none'; }, 6000);
      } else if (msg.type === 'error') {
        const banner = document.getElementById('test-banner');
        banner.textContent = msg.message;
        banner.className = 'fail';
        banner.style.display = 'block';
      }
    });

    // Initial state request
    vscode.postMessage({ type: 'getState' });
  </script>
</body>
</html>`;
  }
}

// ─── Helpers ─────────────────────────────────────────────────────────────────

function computeStatus(status: NotifierStatus | undefined): NotifierInstanceRow["status"] {
  if (!status) return "unknown";
  if (status.lastSuccessAt && Date.now() - status.lastSuccessAt.getTime() < CONNECTED_WINDOW_MS) {
    return "connected";
  }
  if (status.lastErrorAt) return "errored";
  return "unknown";
}

function redactWebhookUrl(url: string): string {
  if (url.length <= 8) return "••••••••";
  return "••••" + url.slice(-8);
}

async function sendTestWebhook(
  type: string,
  webhookUrl: string
): Promise<{ ok: boolean; error?: string }> {
  const body =
    type === "discord"
      ? JSON.stringify({ content: "🔔 Nightgauge: Test notification" })
      : JSON.stringify({ text: "🔔 Nightgauge: Test notification" });

  const res = await fetch(webhookUrl, {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body,
  });

  if (!res.ok) {
    const text = await res.text().catch(() => "");
    return {
      ok: false,
      error: redactSecrets(`HTTP ${res.status}${text ? `: ${text.slice(0, 200)}` : ""}`),
    };
  }
  return { ok: true };
}
