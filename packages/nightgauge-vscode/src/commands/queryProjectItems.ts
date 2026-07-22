/**
 * Query Project Items command
 *
 * Opens a QuickPick input for entering and executing GQL queries.
 * Shows query history, saved queries, and validation feedback.
 */

import * as vscode from "vscode";
import type { QueryService } from "../services/QueryService";
import type { SavedQueriesService } from "../services/SavedQueriesService";
import type { Logger } from "../utils/logger";
import { BUILTIN_QUERIES } from "../types/QueryTypes";

/**
 * Register the Query Project Items command
 */
export function registerQueryProjectItemsCommand(
  queryService: QueryService,
  savedQueriesService: SavedQueriesService,
  logger: Logger
): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.queryProjectItems", async () => {
    logger.debug("Opening query project items QuickPick");

    // Get history and saved queries
    const history = queryService.getHistory();
    const savedQueries = savedQueriesService.getAll();

    // Create QuickPick
    const quickPick = vscode.window.createQuickPick();
    quickPick.placeholder = "Enter GQL query (e.g., status:ready AND priority:P0)";
    quickPick.title = "Query Project Items";

    // Build items list
    const items: vscode.QuickPickItem[] = [];

    // Add saved queries section
    if (savedQueries.length > 0) {
      items.push({
        label: "Saved Queries",
        kind: vscode.QuickPickItemKind.Separator,
      });

      for (const query of savedQueries) {
        items.push({
          label: `$(bookmark) ${query.name}`,
          description: query.query,
          detail: query.description,
        });
      }
    }

    // Add history section
    if (history.length > 0) {
      items.push({
        label: "Recent Queries",
        kind: vscode.QuickPickItemKind.Separator,
      });

      for (const entry of history.slice(0, 5)) {
        items.push({
          label: `$(history) ${entry.query}`,
          description: `${entry.resultCount} results`,
          detail: new Date(entry.executedAt).toLocaleString(),
        });
      }
    }

    // Add examples section if no history
    if (history.length === 0) {
      items.push({
        label: "Examples",
        kind: vscode.QuickPickItemKind.Separator,
      });

      items.push({
        label: "$(lightbulb) status:ready AND priority:P0",
        description: "High priority ready issues",
      });
      items.push({
        label: "$(lightbulb) size:S OR size:XS",
        description: "Small issues",
      });
      items.push({
        label: "$(lightbulb) updated<7d",
        description: "Recently updated issues",
      });
    }

    quickPick.items = items;

    // Handle selection
    const onAccept = quickPick.onDidAccept(async () => {
      let query = quickPick.value;

      // Check if a saved query was selected
      const selectedItem = quickPick.selectedItems[0];
      if (selectedItem) {
        const label = selectedItem.label;
        if (label.startsWith("$(bookmark) ")) {
          // Saved query selected
          const queryName = label.replace("$(bookmark) ", "");
          const savedQuery = savedQueriesService.get(queryName);
          if (savedQuery) {
            query = savedQuery.query;
            await savedQueriesService.recordUsage(queryName);
          }
        } else if (label.startsWith("$(history) ")) {
          // History item selected
          query = label.replace("$(history) ", "");
        } else if (label.startsWith("$(lightbulb) ")) {
          // Example selected
          query = label.replace("$(lightbulb) ", "");
        }
      }

      if (!query) {
        quickPick.hide();
        return;
      }

      quickPick.hide();

      // Validate query
      const errors = queryService.validate(query);
      if (errors.length > 0) {
        const errorMessages = errors.map((e) => e.message).join("\n");
        vscode.window.showErrorMessage(`Invalid query:\n${errorMessages}`);
        return;
      }

      // Execute query
      try {
        vscode.window.withProgress(
          {
            location: vscode.ProgressLocation.Notification,
            title: "Executing query...",
            cancellable: false,
          },
          async () => {
            const result = await queryService.execute(query);

            // Show results
            vscode.window.showInformationMessage(
              `Query matched ${result.matchCount} of ${result.totalCount} issues (${result.executionTimeMs}ms)`
            );

            // Refresh query results view
            vscode.commands.executeCommand("nightgauge.refreshQueryResults");
          }
        );
      } catch (error) {
        const message = error instanceof Error ? error.message : String(error);
        vscode.window.showErrorMessage(`Query failed: ${message}`);
      }
    });

    // Handle value change for validation feedback
    const onValueChange = quickPick.onDidChangeValue((value) => {
      if (value) {
        const errors = queryService.validate(value);
        if (errors.length > 0) {
          quickPick.title = `Query Project Items - ${errors[0].message}`;
        } else {
          quickPick.title = "Query Project Items - Valid query";
        }
      } else {
        quickPick.title = "Query Project Items";
      }
    });

    // Cleanup on hide
    const onHide = quickPick.onDidHide(() => {
      onAccept.dispose();
      onValueChange.dispose();
      onHide.dispose();
      quickPick.dispose();
    });

    quickPick.show();
  });
}

/**
 * Register the Clear Query command
 */
export function registerClearQueryCommand(
  queryService: QueryService,
  logger: Logger
): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.clearQuery", () => {
    logger.debug("Clearing query");
    queryService.clear();
    vscode.commands.executeCommand("nightgauge.refreshQueryResults");
  });
}
