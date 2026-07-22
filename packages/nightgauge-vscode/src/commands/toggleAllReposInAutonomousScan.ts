/**
 * Bulk include / exclude every workspace repo in the autonomous scan set.
 *
 * Wired to two view-title buttons on the Repositories tree view (Issue #2988):
 *   - `nightgauge.repositories.includeAll` → all repos checked
 *   - `nightgauge.repositories.excludeAll` → all repos unchecked
 *
 * Both delegate to `RepositoriesTreeProvider.setAllReposEnabledForAutonomous()`
 * which performs a single write to `autonomous.enabled_repos` and emits one
 * scoped per-row refresh per affected repo (NOT a tree-wide refresh — that's
 * the bug the same issue fixes for individual checkbox toggles).
 */

import * as vscode from "vscode";
import type { RepositoriesTreeProvider } from "../views/RepositoriesTreeProvider";

/**
 * Register both bulk-toggle commands on the extension context.
 *
 * Returns the disposables so callers can push them into
 * `context.subscriptions` alongside the rest of the repositories-view
 * commands.
 */
export function registerToggleAllReposInAutonomousScanCommands(
  provider: RepositoriesTreeProvider
): vscode.Disposable[] {
  const includeAll = vscode.commands.registerCommand(
    "nightgauge.repositories.includeAll",
    async () => {
      const count = await provider.setAllReposEnabledForAutonomous(true);
      if (count > 0) {
        vscode.window.showInformationMessage(
          `Included all ${count} repo${count === 1 ? "" : "s"} in autonomous scan.`
        );
      }
    }
  );

  const excludeAll = vscode.commands.registerCommand(
    "nightgauge.repositories.excludeAll",
    async () => {
      const count = await provider.setAllReposEnabledForAutonomous(false);
      if (count > 0) {
        vscode.window.showInformationMessage(
          `Excluded all ${count} repo${count === 1 ? "" : "s"} from autonomous scan.`
        );
      }
    }
  );

  return [includeAll, excludeAll];
}
