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
 * The environment is derived from the URL platform calls actually go to, so a
 * bare `platform.api_url`, NIGHTGAUGE_PLATFORM_URL or the
 * `nightgauge.platform.url` setting shows as non-default. The tooltip names
 * that URL and what set it (#1474).
 *
 * @see Issue #3721 — feat: status-bar indicator for active platform environment
 */

import * as vscode from "vscode";
import { PLATFORM_ENV_PRESETS, type PlatformEnvironment } from "../config/schema";
import { ConfigBridge } from "../services/ConfigBridge";
import { describePlatformEndpoint, type PlatformEndpoint } from "./platformUrlSetting";

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
    const bridge = ConfigBridge.getInstance();
    const platform = bridge.getPlatform();
    const endpoint = describePlatformEndpoint(platform);
    const env = environmentOf(endpoint);
    const display = ENV_DISPLAY[env];
    this.item.text = `${display.icon} ${display.label}`;
    this.item.backgroundColor = display.background;

    const lines: string[] = [];
    if (env === "custom") {
      const customUrl = endpoint.baseUrl ?? platform?.api_url ?? "unknown URL";
      lines.push(`Platform environment: Custom (${customUrl})`);
    } else {
      lines.push(display.tooltip);
    }
    if (endpoint.baseUrl) {
      lines.push(
        endpoint.nonDefault
          ? `Platform URL: ${endpoint.baseUrl} — non-default`
          : `Platform URL: ${endpoint.baseUrl} (default)`
      );
    } else if (endpoint.error) {
      lines.push(`Platform URL refused: ${endpoint.error}`);
    }
    const override = bridge.getPlatformUrlOverride();
    if (override) {
      lines.push(`Set by ${override.source}`);
    }
    this.item.tooltip = lines.join("\n");
  }

  dispose(): void {
    for (const d of this._disposables) d.dispose();
    this.item.dispose();
  }
}

/** The preset whose URL the endpoint resolves to, else custom. */
function environmentOf(endpoint: PlatformEndpoint): PlatformEnvironment {
  if (!endpoint.baseUrl) return "custom";
  const origin = new URL(endpoint.baseUrl).origin.toLowerCase();
  for (const env of ["production", "canary", "local"] as const) {
    if (new URL(PLATFORM_ENV_PRESETS[env]).origin.toLowerCase() === origin) return env;
  }
  return "custom";
}
