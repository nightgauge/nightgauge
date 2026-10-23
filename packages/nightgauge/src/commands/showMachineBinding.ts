/**
 * Show Machine Binding command (Issue #4156).
 *
 * Surfaces `machineBound` / `machineCount` from the already-fetched
 * LicensePreflight result — read-only. The platform API does not yet expose
 * a way to list individual bound machines or unbind one (only the aggregate
 * count and whether THIS machine is bound), so this deliberately does not
 * fabricate a management UI beyond what the data supports. A full management
 * experience (list bound machines, unbind a specific one) needs a
 * platform-side API addition — tracked as a follow-up, not implemented here.
 *
 * @see src/platform/LicensePreflight.ts — machineBound/machineCount source
 * @see src/views/items/SubscriptionSectionTreeItem.ts — sidebar entry point
 */

import * as vscode from "vscode";
import type { LicensePreflight } from "../platform/LicensePreflight";

export function registerShowMachineBindingCommand(
  licensePreflight: LicensePreflight | null
): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.showMachineBinding", async () => {
    if (!licensePreflight) {
      vscode.window.showInformationMessage(
        "Nightgauge: machine binding is not available — backend not connected."
      );
      return;
    }

    let result: Awaited<ReturnType<LicensePreflight["validate"]>>;
    try {
      result = await licensePreflight.validate();
    } catch {
      vscode.window.showWarningMessage(
        "Nightgauge: could not check machine binding — try again in a moment."
      );
      return;
    }

    if (result.tier === "community") {
      vscode.window.showInformationMessage(
        "Machine binding applies to paid licenses. You're currently on the Community (free) tier."
      );
      return;
    }

    const boundNote = result.machineBound
      ? "This machine is bound to your license."
      : "This machine is not currently bound to your license.";
    const countNote =
      result.machineCount === 1
        ? "1 machine is bound"
        : `${result.machineCount} machines are bound`;

    const message =
      `${boundNote} ${countNote} in total. ` +
      "Viewing or unbinding individual machines isn't available in this version — " +
      "contact support if you need to free up a slot.";

    const action = await vscode.window.showInformationMessage(message, "Contact Support");
    if (action === "Contact Support") {
      await vscode.env.openExternal(
        vscode.Uri.parse("https://github.com/nightgauge/nightgauge/issues")
      );
    }
  });
}
