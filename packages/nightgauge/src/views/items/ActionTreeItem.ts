/**
 * ActionTreeItem - Tree items for global actions and placeholders
 *
 * Used for "No issue active" placeholder and "Run Pipeline" action items.
 */

import * as vscode from "vscode";
import type { PipelineStage } from "@nightgauge/sdk";
import { BaseTreeItem } from "./BaseTreeItem";

/**
 * Action item types
 */
export type ActionType = "no-issue" | "run-pipeline" | "loading" | "error" | "running";

/**
 * ActionTreeItem - Represents action items and placeholders
 *
 * @example
 * ```typescript
 * // Show placeholder when no issue is active
 * const noIssue = ActionTreeItem.createNoIssue();
 *
 * // Show loading state
 * const loading = ActionTreeItem.createLoading('Loading issue...');
 * ```
 */
export class ActionTreeItem extends BaseTreeItem {
  readonly actionType: ActionType;

  private constructor(
    label: string,
    actionType: ActionType,
    options: {
      icon?: string;
      iconColor?: string;
      command?: vscode.Command;
      description?: string;
    } = {}
  ) {
    super(label, vscode.TreeItemCollapsibleState.None);

    this.actionType = actionType;
    this.contextValue = `action-${actionType}`;

    if (options.icon) {
      if (options.iconColor) {
        this.setIconWithColor(options.icon, new vscode.ThemeColor(options.iconColor));
      } else {
        this.setIcon(options.icon);
      }
    }

    if (options.command) {
      this.command = options.command;
    }

    if (options.description) {
      this.description = options.description;
    }
  }

  /**
   * Create a "No issue active" placeholder
   */
  static createNoIssue(): ActionTreeItem {
    const item = new ActionTreeItem("No issue active", "no-issue", {
      icon: "info",
      description: "Drag an issue to start",
    });

    item.tooltip = new vscode.MarkdownString(
      "No pipeline is currently active.\n\n" +
        "Drag an issue or epic from **Ready Items** into the pipeline to begin."
    );

    return item;
  }

  /**
   * Create a "Start Pipeline" action item
   */
  static createRunPipeline(): ActionTreeItem {
    const item = new ActionTreeItem("Start Pipeline", "run-pipeline", {
      icon: "play",
      iconColor: "terminal.ansiGreen",
      command: {
        command: "nightgauge.pickupIssue",
        title: "Start Pipeline",
      },
    });

    item.tooltip = "Start the Nightgauge pipeline - pick up an issue to begin";

    return item;
  }

  /**
   * Create a loading placeholder
   */
  static createLoading(message: string = "Loading..."): ActionTreeItem {
    return new ActionTreeItem(message, "loading", {
      icon: "sync~spin",
    });
  }

  /**
   * Create an error placeholder
   */
  static createError(message: string): ActionTreeItem {
    const item = new ActionTreeItem("Error", "error", {
      icon: "error",
      iconColor: "errorForeground",
      description: message,
    });

    item.tooltip = new vscode.MarkdownString(`**Error:**\n\n${message}`);

    return item;
  }

  /**
   * Create a "Pipeline Running" indicator
   *
   * Shows when a stage is running but issue context hasn't been established yet.
   * This handles the race condition between stage start and issue sync.
   */
  static createRunning(stage: PipelineStage): ActionTreeItem {
    const stageNames: Record<PipelineStage, string> = {
      "pipeline-start": "Initializing",
      "issue-pickup": "Issue Pickup",
      "feature-planning": "Feature Planning",
      "feature-dev": "Feature Development",
      "feature-validate": "Feature Validation",
      "pr-create": "PR Create",
      "pr-merge": "PR Merge",
      "pipeline-finish": "Completing",
    };

    const stageName = stageNames[stage] || stage;

    const item = new ActionTreeItem(`Running: ${stageName}`, "running", {
      icon: "sync~spin",
      iconColor: "terminal.ansiYellow",
      description: "Pipeline in progress",
    });

    item.tooltip = new vscode.MarkdownString(
      `**Pipeline Running**\n\n` +
        `Currently executing: **${stageName}**\n\n` +
        `The pipeline view will update when the stage context is established.`
    );

    return item;
  }
}
