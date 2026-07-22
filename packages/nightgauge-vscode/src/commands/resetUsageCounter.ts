/**
 * Reset Usage Counter command
 *
 * Records the current accumulated cost as a new baseline so the
 * usage display shows cost since the reset point, not total all-time cost.
 * Intended to be used at the start of a new billing period.
 *
 * @see Issue #1333 - Show Claude Code usage limits and alert users
 */

import * as vscode from "vscode";
import type { UsageLimitsService } from "../services/UsageLimitsService";

/**
 * Register the nightgauge.resetUsageCounter command
 *
 * @param context - Extension context for subscription management
 * @param usageLimitsService - The usage limits service to reset
 */
export function registerResetUsageCounterCommand(
  context: vscode.ExtensionContext,
  usageLimitsService: UsageLimitsService
): void {
  context.subscriptions.push(
    vscode.commands.registerCommand("nightgauge.resetUsageCounter", () => {
      usageLimitsService.resetCounter();
      vscode.window.showInformationMessage(
        "Nightgauge: Usage counter reset. Tracking from current total."
      );
    })
  );
}
