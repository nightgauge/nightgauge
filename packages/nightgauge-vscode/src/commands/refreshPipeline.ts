/**
 * Refresh Pipeline command
 *
 * Refreshes the pipeline tree view.
 */

import * as vscode from "vscode";
import type { PipelineTreeProvider } from "../views";
import type { Logger } from "../utils/logger";
import type { PipelineStateService } from "../services/PipelineStateService";

/**
 * Register the Refresh Pipeline command
 */
export function registerRefreshPipelineCommand(
  treeProvider: PipelineTreeProvider,
  logger: Logger,
  _stateService: PipelineStateService | null
): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.refreshPipeline", async () => {
    logger.debug("Refreshing pipeline view");

    // Refresh the tree view
    treeProvider.refreshAll();
  });
}
