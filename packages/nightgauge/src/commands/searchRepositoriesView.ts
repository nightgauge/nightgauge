/**
 * Search Repositories View command
 *
 * Opens a QuickPick input to search issues by title or number within a
 * specific repo + status node in the Repositories tree view.
 * Search state is scoped per-repo per-status.
 */

import * as vscode from "vscode";
import type { RepositoriesTreeProvider } from "../views/RepositoriesTreeProvider";
import type { Logger } from "../utils/logger";
import { IssueSummaryTreeItem } from "../views/items/IssueSummaryTreeItem";

/**
 * Register the Search Repositories View command
 */
export function registerSearchRepositoriesViewCommand(
  provider: RepositoriesTreeProvider,
  logger: Logger
): vscode.Disposable {
  return vscode.commands.registerCommand(
    "nightgauge.searchRepositoriesView",
    async (item?: IssueSummaryTreeItem) => {
      logger.debug("Opening search repositories view QuickPick", {
        repoName: item?.repoName,
        statusType: item?.statusType,
      });

      if (!item || !(item instanceof IssueSummaryTreeItem)) {
        void vscode.window.showWarningMessage(
          "Right-click a status group (Ready, In Progress, Backlog) to search it."
        );
        return;
      }

      const currentFilters = provider.getFilterForStatus(item.repoName, item.statusType);
      const currentSearchText = currentFilters.searchText ?? "";

      const quickPick = vscode.window.createQuickPick();
      quickPick.placeholder = 'Search by title or issue number (e.g., "authentication" or "#144")';
      quickPick.value = currentSearchText;
      quickPick.title = `Search in ${item.repoName} / ${item.statusType}`;

      if (currentSearchText) {
        quickPick.items = [
          {
            label: "$(close) Clear Search",
            description: `Current: "${currentSearchText}"`,
            alwaysShow: true,
          },
        ];
      } else {
        quickPick.items = [];
      }

      let debounceTimer: ReturnType<typeof setTimeout> | null = null;
      const DEBOUNCE_DELAY = 300;

      const onValueChange = quickPick.onDidChangeValue((value) => {
        if (debounceTimer) {
          clearTimeout(debounceTimer);
        }

        if (value) {
          quickPick.items = [
            {
              label: "$(close) Clear Search",
              description: "Press to clear search text",
              alwaysShow: true,
            },
          ];
        } else {
          quickPick.items = [];
        }

        debounceTimer = setTimeout(() => {
          logger.debug("Updating search text for status group", {
            repoName: item.repoName,
            statusType: item.statusType,
            searchText: value,
          });
          provider.setSearchForStatus(item.repoName, item.statusType, value);
        }, DEBOUNCE_DELAY);
      });

      const onItemSelect = quickPick.onDidChangeSelection((items) => {
        if (items.length > 0 && items[0].label === "$(close) Clear Search") {
          quickPick.value = "";
          provider.setSearchForStatus(item.repoName, item.statusType, "");
          quickPick.items = [];
        }
      });

      const onAccept = quickPick.onDidAccept(() => {
        if (debounceTimer) {
          clearTimeout(debounceTimer);
        }

        const searchText = quickPick.value;
        provider.setSearchForStatus(item.repoName, item.statusType, searchText);

        quickPick.hide();

        if (searchText) {
          logger.info("Search applied to status group", {
            repoName: item.repoName,
            statusType: item.statusType,
            searchText,
          });
          void vscode.window.showInformationMessage(`Searching for: "${searchText}"`);
        } else {
          logger.info("Search cleared for status group", {
            repoName: item.repoName,
            statusType: item.statusType,
          });
        }
      });

      const onHide = quickPick.onDidHide(() => {
        if (debounceTimer) {
          clearTimeout(debounceTimer);
        }
        onValueChange.dispose();
        onItemSelect.dispose();
        onAccept.dispose();
        onHide.dispose();
        quickPick.dispose();
      });

      quickPick.show();
    }
  );
}
