/**
 * PhaseTreeItem - Tree item for individual phase display
 *
 * Renders a single phase within a pipeline stage as a non-expandable tree item
 * with status-appropriate icons. Phase names are converted from kebab-case to
 * Title Case for human-readable display.
 *
 * @see Issue #1028 - Render phase progress as children in pipeline tree view
 * @see Issue #1187 - Add 'failed' status with retry context menu
 */

import * as vscode from "vscode";
import { BaseTreeItem } from "./BaseTreeItem";

export type PhaseStatus = "pending" | "running" | "complete" | "skipped" | "failed";

interface PhaseDisplayConfig {
  icon: string;
  iconColor?: string;
}

const PHASE_STATUS_CONFIG: Record<PhaseStatus, PhaseDisplayConfig> = {
  pending: { icon: "circle-outline" },
  running: { icon: "sync~spin" },
  complete: { icon: "check", iconColor: "testing.iconPassed" },
  skipped: { icon: "debug-step-over" },
  failed: { icon: "error", iconColor: "testing.iconFailed" },
};

/**
 * Convert a kebab-case phase name to Title Case.
 *
 * @example toTitleCase('load-context') // 'Load Context'
 * @example toTitleCase('read-planning-context') // 'Read Planning Context'
 */
function toTitleCase(kebab: string): string {
  return kebab
    .split("-")
    .map((word) => word.charAt(0).toUpperCase() + word.slice(1))
    .join(" ");
}

/**
 * PhaseTreeItem - Represents a single phase within a pipeline stage
 *
 * @example
 * ```typescript
 * const phase = new PhaseTreeItem('load-context', 'running');
 * phase.setStatus('complete');
 * ```
 */
export class PhaseTreeItem extends BaseTreeItem {
  readonly phaseName: string;
  private status: PhaseStatus;

  constructor(name: string, status: PhaseStatus = "pending") {
    super(toTitleCase(name), vscode.TreeItemCollapsibleState.None);

    this.phaseName = name;
    this.status = status;

    this.updateDisplay();
  }

  private updateDisplay(): void {
    const config = PHASE_STATUS_CONFIG[this.status];

    if (config.iconColor) {
      this.setIconWithColor(config.icon, new vscode.ThemeColor(config.iconColor));
    } else {
      this.setIcon(config.icon);
    }

    this.contextValue = `phase-${this.status}`;

    // Show status text for non-running phases
    if (this.status !== "running") {
      this.description = this.status;
    } else {
      this.description = "";
    }
  }

  setStatus(status: PhaseStatus): void {
    this.status = status;
    this.updateDisplay();
  }

  getStatus(): PhaseStatus {
    return this.status;
  }
}
