/**
 * Commands for opening the Telemetry Settings webview panel.
 *
 * `nightgauge.telemetrySettings` is preserved as the historical alias
 * (#1481); both it and `nightgauge.openTelemetrySettingsPanel` now open
 * the singleton webview panel introduced in #3327.
 */

import * as vscode from "vscode";
import type { TelemetryConsentService } from "../services/TelemetryConsentService";

export function registerTelemetrySettingsCommand(
  consentService: TelemetryConsentService
): vscode.Disposable {
  const open = async () => {
    await consentService.openSettingsPanel();
  };
  const aliasDisposable = vscode.commands.registerCommand("nightgauge.telemetrySettings", open);
  const newDisposable = vscode.commands.registerCommand(
    "nightgauge.openTelemetrySettingsPanel",
    open
  );
  return vscode.Disposable.from(aliasDisposable, newDisposable);
}
