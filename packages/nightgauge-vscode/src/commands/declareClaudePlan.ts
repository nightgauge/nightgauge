/**
 * `nightgauge.declareClaudePlan` (Issue #808).
 *
 * Opens the one setting that answers "which Claude plan are you on".
 *
 * ## Why a command rather than a link
 *
 * The Dashboard webview may only dispatch `nightgauge.` commands — a
 * deliberate boundary, so a compromised or buggy webview cannot invoke
 * arbitrary VS Code commands. Reaching the settings UI from the Usage panel
 * therefore goes through a named command of ours rather than by widening that
 * allowlist to `workbench.action.openSettings` and whatever else would come
 * with it.
 *
 * @see src/services/usage/claudePlanDeclaration.ts — what the setting means
 */

import * as vscode from "vscode";
import { CLAUDE_PLAN_SETTING } from "../services/usage/claudePlanDeclaration";

export const DECLARE_CLAUDE_PLAN_COMMAND = "nightgauge.declareClaudePlan";

export function registerDeclareClaudePlanCommand(): vscode.Disposable {
  return vscode.commands.registerCommand(DECLARE_CLAUDE_PLAN_COMMAND, async () => {
    await vscode.commands.executeCommand("workbench.action.openSettings", CLAUDE_PLAN_SETTING);
  });
}
