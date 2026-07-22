/**
 * Toggle Sequential Repository Mode command
 *
 * Flips this repo's cap in `concurrency.repository_overrides.<repo>` between
 * 1 (sequential) and `concurrency.workspace_max` (concurrent), then refreshes
 * the Repositories tree view so the new state is reflected.
 */

import * as vscode from "vscode";
import type { RepositoriesTreeProvider } from "../views/RepositoriesTreeProvider";
import { RepositoryTreeItem } from "../views/items/RepositoryTreeItem";

/**
 * Register the Toggle Sequential Repo command.
 *
 * The command is invoked from the Repositories tree view context menu when
 * `viewItem =~ /^repository/` (repository, repository-active,
 * repository-sequential, repository-active-sequential).
 */
export function registerToggleSequentialRepoCommand(
  provider: RepositoriesTreeProvider
): vscode.Disposable {
  return vscode.commands.registerCommand(
    "nightgauge.repo.toggleSequential",
    async (item?: RepositoryTreeItem) => {
      if (!item || !(item instanceof RepositoryTreeItem)) {
        void vscode.window.showWarningMessage(
          "Right-click a repository in the Repositories view to toggle sequential mode."
        );
        return;
      }
      await provider.toggleSequentialRepo(item);
    }
  );
}
