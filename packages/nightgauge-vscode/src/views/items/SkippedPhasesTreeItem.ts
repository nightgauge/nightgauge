import * as vscode from "vscode";
import { BaseTreeItem } from "./BaseTreeItem";
import type { PhaseTreeItem } from "./PhaseTreeItem";

/**
 * A single collapsed row standing in for the phases a stage decided not to run.
 *
 * A deterministic stage path skips most of its registry — `issue-pickup` skips
 * 11 of 14 — and rendering each skip as its own row buried the three rows that
 * carried information under eleven that carried none. The skips are still
 * there, one expand away, because "why was this not run?" is a real question
 * and each skip carries its own `reason` (#1534). They are just no longer the
 * loudest thing in the stage (#1558).
 */
export class SkippedPhasesTreeItem extends BaseTreeItem {
  constructor(skipped: PhaseTreeItem[]) {
    super(
      `${skipped.length} phase${skipped.length === 1 ? "" : "s"} not applicable`,
      vscode.TreeItemCollapsibleState.Collapsed
    );
    this.iconPath = new vscode.ThemeIcon("debug-step-over");
    this.contextValue = "skippedPhases";
    this.tooltip = "Phases this stage decided not to run. Expand for each one's reason.";
    for (const phase of skipped) {
      this.addChild(phase);
    }
  }
}
