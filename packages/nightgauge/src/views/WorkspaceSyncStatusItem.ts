/**
 * WorkspaceSyncStatusItem — status bar entry showing workspace sync state.
 *
 * Displays:
 *   - "$(check) Workspace synced ✓ (N repos)"  — after successful registration
 *   - "$(warning) Workspace sync failed — click to retry"  — on failure
 *   - "$(sync~spin) Syncing workspace…"  — during registration
 *
 * Hidden when no workspace config is present (single-repo mode).
 *
 * @see Issue #3668 — Workspace YAML → Platform Sync on Agent Register
 */

import * as vscode from "vscode";

export type WorkspaceSyncStatus = "synced" | "failed" | "syncing" | "hidden";

const RETRY_COMMAND = "nightgauge.retryWorkspaceSync";

export class WorkspaceSyncStatusItem implements vscode.Disposable {
  readonly item: vscode.StatusBarItem;
  private readonly disposables: vscode.Disposable[] = [];
  private currentStatus: WorkspaceSyncStatus = "hidden";

  constructor() {
    this.item = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Left, 94);
    this.item.command = RETRY_COMMAND;

    this.disposables.push(
      vscode.commands.registerCommand(RETRY_COMMAND, () => this.onRetryClick())
    );

    this.render();
  }

  /**
   * Update the displayed sync status.
   *
   * @param status - The sync state to display
   * @param repoCount - Number of repos synced (used in "synced" label)
   * @param errorMessage - Error detail shown in tooltip on failure
   */
  setStatus(status: WorkspaceSyncStatus, repoCount = 0, errorMessage?: string): void {
    this.currentStatus = status;
    this.render(repoCount, errorMessage);
  }

  private render(repoCount = 0, errorMessage?: string): void {
    switch (this.currentStatus) {
      case "hidden":
        this.item.hide();
        return;

      case "syncing":
        this.item.text = "$(sync~spin) Syncing workspace…";
        this.item.tooltip = "Registering workspace with Nightgauge platform…";
        this.item.backgroundColor = undefined;
        this.item.command = undefined;
        break;

      case "synced":
        this.item.text = `$(check) Workspace synced ✓ (${repoCount} repo${repoCount === 1 ? "" : "s"})`;
        this.item.tooltip = `Workspace registered with ${repoCount} repo${repoCount === 1 ? "" : "s"}. Click to re-sync.`;
        this.item.backgroundColor = undefined;
        this.item.command = RETRY_COMMAND;
        break;

      case "failed":
        this.item.text = "$(warning) Workspace sync failed — click to retry";
        this.item.tooltip = errorMessage
          ? `Workspace sync failed: ${errorMessage}\nClick to retry.`
          : "Workspace sync failed. Click to retry.";
        this.item.backgroundColor = new vscode.ThemeColor("statusBarItem.warningBackground");
        this.item.command = RETRY_COMMAND;
        break;
    }

    this.item.show();
  }

  private onRetryClick(): void {
    void vscode.commands.executeCommand("nightgauge.retryWorkspaceSyncInternal");
  }

  dispose(): void {
    for (const d of this.disposables) d.dispose();
    this.item.dispose();
  }
}
