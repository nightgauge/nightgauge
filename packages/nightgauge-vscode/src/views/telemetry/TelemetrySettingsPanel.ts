/**
 * TelemetrySettingsPanel — singleton webview for fine-grained telemetry control (#3327).
 *
 * Mirrors the singleton pattern of `views/settings/SettingsPanel`. Reads and
 * writes flow exclusively through `TelemetryConsentService` so the
 * VSCode-config-as-source-of-truth invariant (ADR-001) is preserved.
 */

import * as vscode from "vscode";
import { TelemetryConsentService } from "../../services/TelemetryConsentService.js";
import { ALL_STREAMS, type TelemetryStream } from "../../services/telemetry/types.js";
import {
  TelemetrySettingsMessageHandler,
  type TelemetryPanelState,
} from "./TelemetrySettingsMessageHandler.js";

const PRIVACY_DOC_RELATIVE_PATH = "docs/TELEMETRY_PRIVACY.md";

export class TelemetrySettingsPanel implements vscode.Disposable {
  private static currentPanel: TelemetrySettingsPanel | undefined;

  private panel: vscode.WebviewPanel | undefined;
  private readonly disposables: vscode.Disposable[] = [];
  private readonly messageHandler: TelemetrySettingsMessageHandler;

  private constructor(
    private readonly context: vscode.ExtensionContext,
    private readonly consentService: TelemetryConsentService
  ) {
    this.messageHandler = new TelemetrySettingsMessageHandler({
      onGetState: () => this.postState(),
      onSetEnabled: async (value) => {
        await this.consentService.setEnabled(value);
        await this.postState();
      },
      onToggleStream: async (stream, enabled) => {
        const current = new Set(this.consentService.getStreams());
        if (enabled) {
          current.add(stream);
        } else {
          current.delete(stream);
        }
        await this.consentService.setStreams(Array.from(current));
        await this.postState();
      },
      onSetUploadInterval: async (minutes) => {
        await this.consentService.setUploadIntervalMinutes(minutes);
        await this.postState();
      },
      onOpenPrivacyDoc: async () => {
        await this.openPrivacyDoc();
      },
    });

    // External edits to the settings (User settings.json) should reflect in
    // the panel.
    const cfgListener = vscode.workspace.onDidChangeConfiguration((e) => {
      if (e.affectsConfiguration("nightgauge.telemetry")) {
        void this.postState();
      }
    });
    this.disposables.push(cfgListener);
  }

  static show(
    context: vscode.ExtensionContext,
    consentService: TelemetryConsentService
  ): TelemetrySettingsPanel {
    if (!TelemetrySettingsPanel.currentPanel) {
      TelemetrySettingsPanel.currentPanel = new TelemetrySettingsPanel(context, consentService);
    }
    TelemetrySettingsPanel.currentPanel.reveal();
    return TelemetrySettingsPanel.currentPanel;
  }

  static get current(): TelemetrySettingsPanel | undefined {
    return TelemetrySettingsPanel.currentPanel;
  }

  private reveal(): void {
    if (this.panel) {
      this.panel.reveal(vscode.ViewColumn.One);
      // Re-push state so the rendered HTML reflects current settings.
      void this.postState();
      return;
    }

    this.panel = vscode.window.createWebviewPanel(
      "incrediTelemetrySettings",
      "Nightgauge: Telemetry Settings",
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
    void this.postState();
  }

  private buildState(): TelemetryPanelState {
    const lastUploadAtMs = this.consentService.getLastUploadAt();
    return {
      enabled: this.consentService.isEnabled(),
      streams: this.consentService.getStreams(),
      uploadIntervalMinutes: this.consentService.getUploadIntervalMinutes(),
      lastUploadAtMs,
      lastUploadDisplay: formatLastUpload(lastUploadAtMs),
      privacyDocPath: PRIVACY_DOC_RELATIVE_PATH,
    };
  }

  private async postState(): Promise<void> {
    if (!this.panel) return;
    await this.panel.webview.postMessage({ type: "state", state: this.buildState() });
  }

  private async openPrivacyDoc(): Promise<void> {
    const uri = this.resolvePrivacyDocUri();
    if (!uri) {
      void vscode.window.showWarningMessage(
        "Privacy document not found. See docs/TELEMETRY_PRIVACY.md in the Nightgauge repo."
      );
      return;
    }
    try {
      await vscode.commands.executeCommand("markdown.showPreview", uri);
    } catch {
      // Markdown preview unavailable — fall back to opening the file.
      await vscode.commands.executeCommand("vscode.open", uri);
    }
  }

  private resolvePrivacyDocUri(): vscode.Uri | undefined {
    const ws = vscode.workspace.workspaceFolders?.[0];
    if (ws) {
      const candidate = vscode.Uri.joinPath(ws.uri, PRIVACY_DOC_RELATIVE_PATH);
      // We optimistically return the workspace URI — the markdown preview
      // command surfaces a clear error if the file doesn't exist there, and
      // most users running the extension in the repo will have it.
      return candidate;
    }
    if (this.context.extensionUri) {
      return vscode.Uri.joinPath(this.context.extensionUri, PRIVACY_DOC_RELATIVE_PATH);
    }
    return undefined;
  }

  private handlePanelClosed(): void {
    this.panel = undefined;
    TelemetrySettingsPanel.currentPanel = undefined;
    while (this.disposables.length) {
      const d = this.disposables.pop();
      d?.dispose();
    }
  }

  /**
   * Render the static webview HTML. The `state` message hydrates form values
   * after first paint so we keep the markup minimal and framework-free.
   */
  private renderHtml(): string {
    const streamRows = ALL_STREAMS.map(
      (stream) => `
      <label class="stream-row">
        <input type="checkbox" data-stream="${stream}" />
        <span>${streamLabel(stream)}</span>
      </label>`
    ).join("\n");

    return /* html */ `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8" />
  <meta name="viewport" content="width=device-width, initial-scale=1.0" />
  <title>Telemetry Settings</title>
  <style>
    body {
      font-family: var(--vscode-font-family);
      color: var(--vscode-foreground);
      background: var(--vscode-editor-background);
      padding: 1.5rem;
      max-width: 720px;
    }
    h1 { font-size: 1.4rem; margin-top: 0; }
    .section { margin-top: 1.5rem; padding-top: 1rem; border-top: 1px solid var(--vscode-widget-border); }
    .stream-row { display: flex; align-items: center; gap: 0.5rem; margin: 0.25rem 0; }
    .stream-row[data-disabled="true"] { opacity: 0.5; }
    .row { display: flex; align-items: center; gap: 0.5rem; margin: 0.5rem 0; }
    input[type="number"] {
      background: var(--vscode-input-background);
      color: var(--vscode-input-foreground);
      border: 1px solid var(--vscode-input-border);
      padding: 0.25rem 0.5rem;
      width: 6rem;
    }
    button.link {
      background: none; border: none; padding: 0;
      color: var(--vscode-textLink-foreground);
      text-decoration: underline; cursor: pointer; font: inherit;
    }
    .meta { color: var(--vscode-descriptionForeground); font-size: 0.9rem; }
  </style>
</head>
<body>
  <h1>Telemetry Settings</h1>
  <p class="meta">
    Telemetry is opt-in. Anonymous usage data is never sent until you enable
    it. <button class="link" id="privacyLink" type="button">Read the Privacy Document</button>.
  </p>

  <div class="section">
    <label class="row">
      <input type="checkbox" id="masterEnabled" />
      <strong>Send anonymous usage data</strong>
    </label>
    <div class="meta" id="lastUpload">Last upload: —</div>
  </div>

  <div class="section">
    <h2 style="font-size: 1.05rem; margin-bottom: 0.5rem;">Streams</h2>
    <div id="streams">${streamRows}</div>
  </div>

  <div class="section">
    <label class="row" for="uploadInterval">
      <span>Upload interval (minutes)</span>
      <input type="number" id="uploadInterval" min="1" max="1440" step="1" />
    </label>
    <div class="meta">Determines how often queued events flush. Range 1–1440.</div>
  </div>

  <script>
    const vscode = acquireVsCodeApi();

    const masterEnabled = document.getElementById('masterEnabled');
    const uploadInterval = document.getElementById('uploadInterval');
    const lastUpload = document.getElementById('lastUpload');
    const streamsContainer = document.getElementById('streams');
    const privacyLink = document.getElementById('privacyLink');

    masterEnabled.addEventListener('change', () => {
      vscode.postMessage({ type: 'setEnabled', value: masterEnabled.checked });
    });

    uploadInterval.addEventListener('change', () => {
      const value = parseInt(uploadInterval.value, 10);
      if (Number.isFinite(value)) {
        vscode.postMessage({ type: 'setUploadInterval', minutes: value });
      }
    });

    streamsContainer.addEventListener('change', (e) => {
      const target = e.target;
      if (target && target.matches('input[type="checkbox"][data-stream]')) {
        vscode.postMessage({
          type: 'toggleStream',
          stream: target.dataset.stream,
          enabled: target.checked,
        });
      }
    });

    privacyLink.addEventListener('click', () => {
      vscode.postMessage({ type: 'openPrivacyDoc' });
    });

    function applyState(state) {
      masterEnabled.checked = state.enabled;
      uploadInterval.value = String(state.uploadIntervalMinutes);
      uploadInterval.disabled = !state.enabled;
      lastUpload.textContent = 'Last upload: ' + state.lastUploadDisplay;

      const checkboxes = streamsContainer.querySelectorAll('input[type="checkbox"][data-stream]');
      checkboxes.forEach((cb) => {
        const stream = cb.dataset.stream;
        cb.checked = state.streams.includes(stream);
        cb.disabled = !state.enabled;
        const row = cb.closest('.stream-row');
        if (row) row.dataset.disabled = String(!state.enabled);
      });
    }

    window.addEventListener('message', (event) => {
      const msg = event.data;
      if (msg && msg.type === 'state') {
        applyState(msg.state);
      }
    });

    vscode.postMessage({ type: 'getState' });
  </script>
</body>
</html>`;
  }

  dispose(): void {
    this.handlePanelClosed();
    this.panel?.dispose();
  }
}

function formatLastUpload(ms: number | null): string {
  if (ms === null) return "Never";
  try {
    return new Date(ms).toLocaleString();
  } catch {
    return "Never";
  }
}

function streamLabel(stream: TelemetryStream): string {
  switch (stream) {
    case "pipeline-run":
      return "Pipeline runs (anonymous outcomes and durations)";
    case "health":
      return "Health metrics (queue, retry, error counts)";
    case "recommendation":
      return "Recommendation effectiveness signals";
    case "trace":
      return "Run lifecycle decision traces (stage, phase, and decision events)";
  }
}
