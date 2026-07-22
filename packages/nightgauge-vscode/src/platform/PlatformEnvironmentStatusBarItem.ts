/**
 * PlatformEnvironmentStatusBarItem — color-coded active platform environment indicator.
 *
 * Displays which API environment (production, canary, local, custom) the extension
 * is currently targeting. Reactively updates via ConfigBridge.onConfigChanged —
 * no restart or polling required.
 *
 * Priority 93 — just right of EventStreamStatusBarItem (94).
 * Clicking opens `nightgauge.platform.switchEnvironment`.
 *
 * @see Issue #3721 — feat: status-bar indicator for active platform environment
 */

import * as vscode from "vscode";
import type { PlatformEnvironment } from "../config/schema";
import { ConfigBridge } from "../services/ConfigBridge";

interface StateDisplay {
  label: string;
  icon: string;
  tooltip: string;
  background?: vscode.ThemeColor;
}

const ENV_DISPLAY: Record<PlatformEnvironment, StateDisplay> = {
  production: {
    label: "Platform: prod",
    icon: "$(globe)",
    tooltip: "Platform environment: Production",
    background: undefined,
  },
  canary: {
    label: "Platform: canary",
    icon: "$(beaker)",
    tooltip: "Platform environment: Canary — pre-release API",
    background: new vscode.ThemeColor("statusBarItem.warningBackground"),
  },
  local: {
    label: "Platform: local",
    icon: "$(home)",
    tooltip: "Platform environment: Local (http://localhost:8787)",
    background: new vscode.ThemeColor("statusBarItem.prominentBackground"),
  },
  custom: {
    label: "Platform: custom",
    icon: "$(settings-gear)",
    tooltip: "Platform environment: Custom URL",
    background: new vscode.ThemeColor("statusBarItem.prominentBackground"),
  },
};

export class PlatformEnvironmentStatusBarItem implements vscode.Disposable {
  readonly item: vscode.StatusBarItem;
  private readonly _disposables: vscode.Disposable[] = [];

  constructor(commandId = "nightgauge.platform.switchEnvironment") {
    this.item = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Left, 93);
    this.item.command = commandId;
    this._render();
    this.item.show();

    this._disposables.push(ConfigBridge.getInstance().onConfigChanged(() => this._render()));
  }

  private _render(): void {
    const platform = ConfigBridge.getInstance().getPlatform();
    const env: PlatformEnvironment = platform?.environment ?? "production";
    const display = ENV_DISPLAY[env];
    this.item.text = `${display.icon} ${display.label}`;
    this.item.backgroundColor = display.background;

    if (env === "custom") {
      const customUrl = platform?.api_url ?? "unknown URL";
      this.item.tooltip = `Platform environment: Custom (${customUrl})`;
    } else {
      this.item.tooltip = display.tooltip;
    }
  }

  dispose(): void {
    for (const d of this._disposables) d.dispose();
    this.item.dispose();
  }
}
