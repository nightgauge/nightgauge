/**
 * PlatformStatusBar — platform connection status indicator for the VS Code status bar.
 *
 * Displays the current platform connectivity state (Connected, Degraded, Offline,
 * or Disabled) and updates in real-time by polling the platform health endpoint.
 *
 * States:
 *   - Disabled  : platform.enabled = false in config
 *   - Connected : health endpoint returned "ok"
 *   - Degraded  : health endpoint returned "degraded"
 *   - Offline   : health endpoint unreachable or returned an error
 *
 * @see Issue #1461
 */

import * as vscode from "vscode";

/** Platform connectivity state */
export type PlatformConnectionState = "connected" | "degraded" | "offline" | "disabled";

/** Options for PlatformStatusBar */
export interface PlatformStatusBarOptions {
  /** Platform API base URL (default: 'https://api.nightgauge.dev') */
  apiUrl?: string;
  /** Whether platform communication is enabled (default: false) */
  enabled?: boolean;
  /** Health poll interval in milliseconds (default: 60_000) */
  pollIntervalMs?: number;
  /** Health request timeout in milliseconds (default: 5_000) */
  timeoutMs?: number;
}

const DEFAULT_API_URL = "https://api.nightgauge.dev";
const DEFAULT_POLL_INTERVAL_MS = 60_000;
const DEFAULT_TIMEOUT_MS = 5_000;

/**
 * Platform connection status bar item.
 *
 * Registers a single VS Code status bar item (priority 97, left-aligned) that
 * reflects the current platform connectivity state and updates in real-time.
 *
 * @example
 * ```typescript
 * const platformStatus = new PlatformStatusBar({ enabled: true, apiUrl: '...' });
 * context.subscriptions.push(platformStatus);
 * platformStatus.start();
 * ```
 */
export class PlatformStatusBar implements vscode.Disposable {
  readonly item: vscode.StatusBarItem;

  private state: PlatformConnectionState = "offline";
  private lastSuccessfulPing: Date | null = null;
  private apiUrl: string;
  private enabled: boolean;
  private pollIntervalMs: number;
  private timeoutMs: number;
  private pollTimer: ReturnType<typeof setInterval> | null = null;
  private disposables: vscode.Disposable[] = [];

  constructor(options: PlatformStatusBarOptions = {}) {
    this.apiUrl = options.apiUrl ?? DEFAULT_API_URL;
    this.enabled = options.enabled ?? false;
    this.pollIntervalMs = options.pollIntervalMs ?? DEFAULT_POLL_INTERVAL_MS;
    this.timeoutMs = options.timeoutMs ?? DEFAULT_TIMEOUT_MS;

    this.item = vscode.window.createStatusBarItem(
      vscode.StatusBarAlignment.Left,
      97 // Just to the right of usageItem (98)
    );
    this.item.command = "nightgauge.showPlatformStatus";
    this.disposables.push(this.item);
  }

  /**
   * Start health polling and show the status bar item.
   *
   * If platform is disabled, shows "Platform: Disabled" immediately without polling.
   */
  start(): void {
    if (!this.enabled) {
      this._setState("disabled");
      this.item.show();
      return;
    }

    // Initial check immediately
    this._checkHealth().catch(() => {
      // Errors are handled inside _checkHealth; ignore here
    });

    this.pollTimer = setInterval(() => {
      this._checkHealth().catch(() => {
        // Errors are handled inside _checkHealth; ignore here
      });
    }, this.pollIntervalMs);

    this.item.show();
  }

  /**
   * Update configuration (e.g., when the user edits their config file).
   * Restarts polling with the new settings.
   */
  updateOptions(options: PlatformStatusBarOptions): void {
    this._stopPolling();

    this.apiUrl = options.apiUrl ?? this.apiUrl;
    this.enabled = options.enabled ?? false;
    this.pollIntervalMs = options.pollIntervalMs ?? this.pollIntervalMs;
    this.timeoutMs = options.timeoutMs ?? this.timeoutMs;

    this.start();
  }

  /** Current connection state */
  getState(): PlatformConnectionState {
    return this.state;
  }

  /** Timestamp of the last successful health ping, or null if never succeeded */
  getLastSuccessfulPing(): Date | null {
    return this.lastSuccessfulPing;
  }

  /** Stop polling and hide the status bar item */
  dispose(): void {
    this._stopPolling();
    for (const d of this.disposables) {
      d.dispose();
    }
    this.disposables = [];
  }

  // ---------------------------------------------------------------------------
  // Private helpers
  // ---------------------------------------------------------------------------

  private _stopPolling(): void {
    if (this.pollTimer !== null) {
      clearInterval(this.pollTimer);
      this.pollTimer = null;
    }
  }

  /** Fetch /health and update state */
  private async _checkHealth(): Promise<void> {
    const url = `${this.apiUrl.replace(/\/$/, "")}/health`;

    try {
      const controller = new AbortController();
      const timeoutId = setTimeout(() => controller.abort(), this.timeoutMs);

      let response: Response;
      try {
        response = await fetch(url, { signal: controller.signal });
      } finally {
        clearTimeout(timeoutId);
      }

      if (!response.ok) {
        this._setState("offline");
        return;
      }

      // Parse JSON body — expected shape: { status: 'ok' | 'degraded' | ... }
      let body: unknown;
      try {
        body = await response.json();
      } catch {
        // If body isn't parseable, treat as offline
        this._setState("offline");
        return;
      }

      const status =
        body !== null &&
        typeof body === "object" &&
        "status" in body &&
        typeof (body as Record<string, unknown>).status === "string"
          ? (body as Record<string, unknown>).status
          : null;

      if (status === "ok") {
        this.lastSuccessfulPing = new Date();
        this._setState("connected");
      } else if (status === "degraded") {
        this.lastSuccessfulPing = new Date();
        this._setState("degraded");
      } else {
        this._setState("offline");
      }
    } catch {
      // Network error, timeout, or abort — treat as offline
      this._setState("offline");
    }
  }

  private _setState(state: PlatformConnectionState): void {
    this.state = state;
    this._render();
  }

  /** Update status bar text, icon, color, and tooltip from current state */
  private _render(): void {
    const pingInfo = this.lastSuccessfulPing
      ? `\nLast ping: ${this.lastSuccessfulPing.toLocaleTimeString()}`
      : "";
    const urlInfo = `\nURL: ${this.apiUrl}`;

    switch (this.state) {
      case "connected":
        this.item.text = "$(check) Platform: Connected";
        this.item.tooltip = `Platform connected${urlInfo}${pingInfo}\nClick for details`;
        this.item.backgroundColor = undefined;
        break;

      case "degraded":
        this.item.text = "$(warning) Platform: Degraded";
        this.item.tooltip = `Platform operating in degraded mode${urlInfo}${pingInfo}\nClick for details`;
        this.item.backgroundColor = new vscode.ThemeColor("statusBarItem.warningBackground");
        break;

      case "offline":
        this.item.text = "$(error) Platform: Offline";
        this.item.tooltip = `Platform unreachable${urlInfo}${pingInfo}\nClick for details`;
        this.item.backgroundColor = new vscode.ThemeColor("statusBarItem.errorBackground");
        break;

      case "disabled":
        this.item.text = "$(circle-slash) Platform: Disabled";
        this.item.tooltip =
          "Platform communication disabled (platform.enabled = false)\nClick for details";
        this.item.backgroundColor = undefined;
        break;
    }
  }
}
