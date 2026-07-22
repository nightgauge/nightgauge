/**
 * showSettings - Command to open the Nightgauge settings panel
 *
 * Opens a WebView panel for configuring .nightgauge/config.yaml settings.
 */

import * as vscode from "vscode";
import { SettingsPanel } from "../views/settings";
import { getWorkspaceRoot } from "../config/settings";
import type { Logger } from "../utils/logger";
import type { PipelineStateService } from "../services/PipelineStateService";
import type { ConcurrentPipelineManager } from "../services/ConcurrentPipelineManager";
import type { RuntimeStateStore } from "../config/RuntimeStateStore";
import { IpcClient } from "../services/IpcClient";

/**
 * Register the showSettings command
 *
 * @param extensionUri - Extension URI for resource loading
 * @param pipelineStateService - Pipeline state service for read-only mode
 * @param logger - Logger instance
 * @param concurrentPipelineManager - For live-applying pipeline.max_concurrent
 * @param runtimeStateStore - Runtime tier store for tier-3 key writes
 *   (Phase 3 of #3313 / #3336). The panel writes `pipeline.max_concurrent`
 *   here instead of the project YAML so the working tree stays clean.
 * @returns Disposable for the command registration
 */
export function registerShowSettingsCommand(
  extensionUri: vscode.Uri,
  pipelineStateService: PipelineStateService | null,
  logger: Logger,
  concurrentPipelineManager: ConcurrentPipelineManager | null,
  runtimeStateStore: RuntimeStateStore | null
): vscode.Disposable {
  return vscode.commands.registerCommand("nightgauge.showSettings", async () => {
    const workspaceRoot = getWorkspaceRoot();

    if (!workspaceRoot) {
      vscode.window.showErrorMessage(
        "No workspace folder open. Please open a workspace to configure settings."
      );
      return;
    }

    logger.info("Opening Nightgauge settings panel");

    const panel = SettingsPanel.getInstance(extensionUri, workspaceRoot);

    if (pipelineStateService) {
      panel.setStateService(pipelineStateService);
    }

    // Wire the runtime tier store BEFORE showing the panel so handleSave()
    // can route tier-3 keys without a re-init dance.
    if (runtimeStateStore) {
      panel.setRuntimeStateStore(runtimeStateStore);
    }

    // Live-apply pipeline.max_concurrent on runtime change: the TS-side
    // ConcurrentPipelineManager controls how many worktree pipelines run, and
    // the Go-side autonomous scheduler decides how many issues to dispatch.
    // Both layers must hear about the new ceiling — the IPC verb does both.
    panel.setOnMaxConcurrentChanged(async (value: number) => {
      concurrentPipelineManager?.setMaxConcurrentSlots(value);
      // persist=false: SettingsPanel already wrote the runtime tier; we only
      // need the runtime push to the Go autonomous scheduler.
      await IpcClient.getInstance().pipelineSetMaxConcurrent(value, false);
    });

    await panel.show();
  });
}
