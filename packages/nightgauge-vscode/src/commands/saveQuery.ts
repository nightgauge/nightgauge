/**
 * Save Query command
 *
 * Saves the current query with a name for later reuse.
 */

import * as vscode from "vscode";
import type { QueryService } from "../services/QueryService";
import type { SavedQueriesService } from "../services/SavedQueriesService";
import type { Logger } from "../utils/logger";

/**
 * Register the Save Query command
 */
export function registerSaveQueryCommand(
  queryService: QueryService,
  savedQueriesService: SavedQueriesService,
  logger: Logger
): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.saveQuery", async () => {
    logger.debug("Opening save query dialog");

    // Get current query
    const currentQuery = queryService.getCurrentQuery();

    if (!currentQuery) {
      vscode.window.showWarningMessage("No query to save. Run a query first.");
      return;
    }

    // Ask for query name
    const name = await vscode.window.showInputBox({
      title: "Save Query",
      prompt: "Enter a name for this query",
      placeHolder: "e.g., Sprint Backlog",
      validateInput: (value) => {
        if (!value || value.trim().length === 0) {
          return "Name is required";
        }
        if (value.length > 100) {
          return "Name must be 100 characters or less";
        }
        return null;
      },
    });

    if (!name) {
      return; // Cancelled
    }

    // Check if name already exists
    const existing = savedQueriesService.get(name);
    if (existing) {
      const overwrite = await vscode.window.showQuickPick(["Yes", "No"], {
        title: `Query "${name}" already exists. Overwrite?`,
      });

      if (overwrite !== "Yes") {
        return;
      }
    }

    // Ask for optional description
    const description = await vscode.window.showInputBox({
      title: "Query Description",
      prompt: "Enter an optional description (press Enter to skip)",
      placeHolder: "e.g., Ready issues for current sprint",
    });

    // Save the query
    try {
      await savedQueriesService.save({
        name: name.trim(),
        query: currentQuery,
        description: description?.trim(),
      });

      logger.info("Query saved", { name, query: currentQuery });
      vscode.window.showInformationMessage(`Query saved as "${name}"`);
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      logger.error("Failed to save query", { error: message });
      vscode.window.showErrorMessage(`Failed to save query: ${message}`);
    }
  });
}

/**
 * Register the Save Query As command
 * Allows saving any query, not just the current one
 */
export function registerSaveQueryAsCommand(
  savedQueriesService: SavedQueriesService,
  logger: Logger
): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.saveQueryAs", async (query?: string) => {
    // If no query provided, ask for it
    if (!query) {
      query = await vscode.window.showInputBox({
        title: "Save Query",
        prompt: "Enter the query to save",
        placeHolder: "e.g., status:ready AND priority:P0",
      });

      if (!query) {
        return;
      }
    }

    // Ask for query name
    const name = await vscode.window.showInputBox({
      title: "Save Query",
      prompt: "Enter a name for this query",
      placeHolder: "e.g., Sprint Backlog",
      validateInput: (value) => {
        if (!value || value.trim().length === 0) {
          return "Name is required";
        }
        if (value.length > 100) {
          return "Name must be 100 characters or less";
        }
        return null;
      },
    });

    if (!name) {
      return;
    }

    // Save the query
    try {
      await savedQueriesService.save({
        name: name.trim(),
        query,
      });

      logger.info("Query saved", { name, query });
      vscode.window.showInformationMessage(`Query saved as "${name}"`);
    } catch (error) {
      const message = error instanceof Error ? error.message : String(error);
      logger.error("Failed to save query", { error: message });
      vscode.window.showErrorMessage(`Failed to save query: ${message}`);
    }
  });
}
