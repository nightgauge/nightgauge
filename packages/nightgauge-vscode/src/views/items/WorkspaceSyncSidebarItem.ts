/**
 * WorkspaceSyncSidebarItem — sidebar tree item showing workspace sync state.
 *
 * Three visible states: synced ✓, syncing (spinner), failed (click to retry).
 * Hidden (not pushed to items array) when status === "hidden".
 *
 * @see Issue #3669 — Workspace Sidebar Sync Indicator
 */

import * as vscode from "vscode";
import { BaseTreeItem } from "./BaseTreeItem";
import type { WorkspaceSyncStatus } from "../WorkspaceSyncStatusItem";

export interface WorkspaceSyncSidebarState {
  status: WorkspaceSyncStatus;
  repoCount: number;
  errorMessage?: string;
  workspaceName?: string;
  repos?: string[];
}

const RETRY_COMMAND = "nightgauge.retryWorkspaceSyncInternal";

export class WorkspaceSyncSidebarItem extends BaseTreeItem {
  private state: WorkspaceSyncSidebarState = { status: "hidden", repoCount: 0 };

  constructor() {
    super("", vscode.TreeItemCollapsibleState.None);
    this.contextValue = "workspace-sync-status";
  }

  setState(state: WorkspaceSyncSidebarState): void {
    this.state = state;
    this.update();
  }

  getState(): WorkspaceSyncSidebarState {
    return this.state;
  }

  private update(): void {
    const { status, repoCount, errorMessage, workspaceName, repos } = this.state;

    switch (status) {
      case "synced": {
        const count = repoCount;
        this.label = `Workspace synced ✓ (${count} repo${count === 1 ? "" : "s"})`;
        this.iconPath = new vscode.ThemeIcon(
          "check-all",
          new vscode.ThemeColor("testing.iconPassed")
        );
        const repoLines = repos?.length ? repos.map((r) => `• ${r}`).join("\n") : "";
        const tooltipText = workspaceName
          ? `${workspaceName}\n${repoLines}`
          : repoLines || "Workspace synced";
        this.tooltip = tooltipText;
        this.command = {
          command: RETRY_COMMAND,
          title: "Retry Workspace Sync",
        };
        break;
      }

      case "syncing":
        this.label = "Syncing workspace…";
        this.iconPath = new vscode.ThemeIcon("sync~spin", new vscode.ThemeColor("charts.yellow"));
        this.tooltip = "Registering workspace with Nightgauge platform…";
        this.command = undefined;
        break;

      case "failed":
        this.label = "Workspace sync failed — click to retry";
        this.iconPath = new vscode.ThemeIcon(
          "warning",
          new vscode.ThemeColor("testing.iconFailed")
        );
        this.tooltip = errorMessage
          ? `Workspace sync failed: ${errorMessage}\nClick to retry.`
          : "Workspace sync failed. Click to retry.";
        this.command = {
          command: RETRY_COMMAND,
          title: "Retry Workspace Sync",
        };
        break;

      case "hidden":
        this.label = "";
        this.command = undefined;
        break;
    }
  }
}
