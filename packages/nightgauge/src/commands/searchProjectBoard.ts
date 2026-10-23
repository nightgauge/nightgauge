/**
 * Search Project Board command
 *
 * Opens a QuickPick input to search issues by title or number.
 * Updates VS Code configuration with debounced search text and refreshes all providers.
 */

import * as vscode from "vscode";
import type { ProjectBoardTreeProvider } from "../views/ProjectBoardTreeProvider";
import type { Logger } from "../utils/logger";
import type { TabId } from "../types/TabConfig";

/**
 * Map of tab IDs to their providers
 */
export type ProjectBoardProviders = Map<TabId, ProjectBoardTreeProvider>;

/**
 * Register the Search Project Board command
 */
export function registerSearchProjectBoardCommand(
  providers: ProjectBoardProviders,
  logger: Logger
): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.searchProjectBoard", async () => {
    logger.debug("Opening search project board QuickPick");

    // Get current search text from configuration
    const config = vscode.workspace.getConfiguration("nightgauge.readyItems");
    const currentSearchText = config.get<string>("searchText", "");

    // Create QuickPick for search input
    const quickPick = vscode.window.createQuickPick();
    quickPick.placeholder = 'Search by title or issue number (e.g., "authentication" or "#144")';
    quickPick.value = currentSearchText;
    quickPick.title = "Search Project Board";

    // Add items for clear option when there's existing search text
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

    // Debounce timer for search updates
    let debounceTimer: ReturnType<typeof setTimeout> | null = null;
    const DEBOUNCE_DELAY = 300;

    // Handle search text changes with debounce
    const onValueChange = quickPick.onDidChangeValue((value) => {
      // Clear any pending debounce timer
      if (debounceTimer) {
        clearTimeout(debounceTimer);
      }

      // Update items to show clear option when there's text
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

      // Debounce the configuration update
      debounceTimer = setTimeout(async () => {
        logger.debug("Updating search text", { searchText: value });
        await config.update("searchText", value, vscode.ConfigurationTarget.Global);
      }, DEBOUNCE_DELAY);
    });

    // Handle item selection (for clear button)
    const onItemSelect = quickPick.onDidChangeSelection(async (items) => {
      if (items.length > 0 && items[0].label === "$(close) Clear Search") {
        quickPick.value = "";
        await config.update("searchText", "", vscode.ConfigurationTarget.Global);
        quickPick.items = [];
      }
    });

    // Handle accept (Enter key)
    const onAccept = quickPick.onDidAccept(async () => {
      // Clear any pending debounce timer and apply immediately
      if (debounceTimer) {
        clearTimeout(debounceTimer);
      }

      const searchText = quickPick.value;
      await config.update("searchText", searchText, vscode.ConfigurationTarget.Global);

      quickPick.hide();

      if (searchText) {
        logger.info("Search applied", { searchText });
        vscode.window.showInformationMessage(`Searching for: "${searchText}"`);
      } else {
        logger.info("Search cleared");
      }
    });

    // Handle hide (dismiss)
    const onHide = quickPick.onDidHide(() => {
      // Clean up debounce timer
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
  });
}

/**
 * Register the Clear Search Project Board command
 */
export function registerClearSearchProjectBoardCommand(
  providers: ProjectBoardProviders,
  logger: Logger
): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.clearSearchProjectBoard", async () => {
    logger.debug("Clearing search text");

    const config = vscode.workspace.getConfiguration("nightgauge.readyItems");
    await config.update("searchText", "", vscode.ConfigurationTarget.Global);

    logger.info("Search cleared");
  });
}
