/**
 * Load Saved Query command
 *
 * Shows a picker to select and execute a saved query.
 */

import * as vscode from "vscode";
import type { QueryService } from "../services/QueryService";
import type { SavedQueriesService } from "../services/SavedQueriesService";
import type { Logger } from "../utils/logger";

/**
 * Register the Load Saved Query command
 */
export function registerLoadSavedQueryCommand(
  queryService: QueryService,
  savedQueriesService: SavedQueriesService,
  logger: Logger
): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.loadSavedQuery", async () => {
    logger.debug("Opening load saved query picker");

    const savedQueries = savedQueriesService.getAll();

    if (savedQueries.length === 0) {
      vscode.window.showInformationMessage(
        'No saved queries. Save a query first with "Nightgauge: Save Query".'
      );
      return;
    }

    // Build quick pick items
    const items: vscode.QuickPickItem[] = [];

    // Add built-in queries section
    const builtIn = savedQueries.filter((q) => q.isBuiltIn);
    if (builtIn.length > 0) {
      items.push({
        label: "Built-in Queries",
        kind: vscode.QuickPickItemKind.Separator,
      });

      for (const query of builtIn) {
        items.push({
          label: query.name,
          description: query.query,
          detail: query.description,
        });
      }
    }

    // Add user queries section
    const userQueries = savedQueries.filter((q) => !q.isBuiltIn);
    if (userQueries.length > 0) {
      items.push({
        label: "Your Queries",
        kind: vscode.QuickPickItemKind.Separator,
      });

      for (const query of userQueries) {
        const runCount = query.runCount ?? 0;
        const detail = query.description || `Run ${runCount} times`;
        items.push({
          label: query.name,
          description: query.query,
          detail,
        });
      }
    }

    // Show picker
    const selected = await vscode.window.showQuickPick(items, {
      title: "Load Saved Query",
      placeHolder: "Select a query to execute",
      matchOnDescription: true,
      matchOnDetail: true,
    });

    if (!selected || selected.kind === vscode.QuickPickItemKind.Separator) {
      return;
    }

    // Find the query
    const query = savedQueriesService.get(selected.label);
    if (!query) {
      vscode.window.showErrorMessage(`Query not found: ${selected.label}`);
      return;
    }

    // Record usage
    await savedQueriesService.recordUsage(query.name);

    // Execute the query
    try {
      vscode.window.withProgress(
        {
          location: vscode.ProgressLocation.Notification,
          title: `Running "${query.name}"...`,
          cancellable: false,
        },
        async () => {
          const result = await queryService.execute(query.query);

          vscode.window.showInformationMessage(
            `${query.name}: ${result.matchCount} of ${result.totalCount} issues match`
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
}

/**
 * Register the Delete Saved Query command
 */
export function registerDeleteSavedQueryCommand(
  savedQueriesService: SavedQueriesService,
  logger: Logger
): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.deleteSavedQuery", async (name?: string) => {
    // If no name provided, show picker
    if (!name) {
      const userQueries = savedQueriesService.getUserQueries();

      if (userQueries.length === 0) {
        vscode.window.showInformationMessage("No user-defined queries to delete.");
        return;
      }

      const items = userQueries.map((q) => ({
        label: q.name,
        description: q.query,
      }));

      const selected = await vscode.window.showQuickPick(items, {
        title: "Delete Saved Query",
        placeHolder: "Select a query to delete",
      });

      if (!selected) {
        return;
      }

      name = selected.label;
    }

    // Confirm deletion
    const confirm = await vscode.window.showQuickPick(["Yes", "No"], {
      title: `Delete query "${name}"?`,
    });

    if (confirm !== "Yes") {
      return;
    }

    // Delete the query
    try {
      const deleted = await savedQueriesService.delete(name);
      if (deleted) {
        logger.info("Query deleted", { name });
        vscode.window.showInformationMessage(`Query "${name}" deleted`);
      } else {
        vscode.window.showWarningMessage(`Query "${name}" not found`);
      }
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      logger.error("Failed to delete query", { error: message });
      vscode.window.showErrorMessage(`Failed to delete query: ${message}`);
    }
  });
}

/**
 * Register the Manage Saved Queries command
 */
export function registerManageSavedQueriesCommand(
  savedQueriesService: SavedQueriesService,
  logger: Logger
): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.manageSavedQueries", async () => {
    const userQueries = savedQueriesService.getUserQueries();

    const actions = [
      { label: "$(add) Save Current Query", action: "save" },
      { label: "$(folder-opened) Import Queries...", action: "import" },
      { label: "$(export) Export Queries...", action: "export" },
    ];

    if (userQueries.length > 0) {
      actions.push({ label: "$(trash) Delete Query...", action: "delete" });
    }

    const selected = await vscode.window.showQuickPick(actions, {
      title: "Manage Saved Queries",
      placeHolder: "Select an action",
    });

    if (!selected) {
      return;
    }

    switch (selected.action) {
      case "save":
        vscode.commands.executeCommand("nightgauge.saveQuery");
        break;

      case "import": {
        const importUri = await vscode.window.showOpenDialog({
          canSelectFiles: true,
          canSelectMany: false,
          filters: { YAML: ["yaml", "yml"] },
          title: "Import Saved Queries",
        });

        if (importUri && importUri[0]) {
          try {
            const count = await savedQueriesService.import(importUri[0].fsPath);
            vscode.window.showInformationMessage(`Imported ${count} queries`);
          } catch (error) {
            const message = error instanceof Error ? error.message : String(error);
            vscode.window.showErrorMessage(`Import failed: ${message}`);
          }
        }
        break;
      }

      case "export": {
        const exportUri = await vscode.window.showSaveDialog({
          filters: { YAML: ["yaml"] },
          defaultUri: vscode.Uri.file("saved-queries.yaml"),
          title: "Export Saved Queries",
        });

        if (exportUri) {
          try {
            await savedQueriesService.export(exportUri.fsPath, false);
            vscode.window.showInformationMessage(`Queries exported to ${exportUri.fsPath}`);
          } catch (error) {
            const message = error instanceof Error ? error.message : String(error);
            vscode.window.showErrorMessage(`Export failed: ${message}`);
          }
        }
        break;
      }

      case "delete":
        vscode.commands.executeCommand("nightgauge.deleteSavedQuery");
        break;
    }
  });
}
