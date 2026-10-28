/**
 * StageActivityTreeItem - one activity event under a stage node (#1897)
 *
 * A fix commit, failing checks, a failed gate, a recovery attempt or a model
 * escalation. Leaf node; rendered ahead of the stage's phase rows.
 */

import * as vscode from "vscode";
import { BaseTreeItem } from "./BaseTreeItem";
import type { StageActivityEvent } from "./stageActivity";

function describe(event: StageActivityEvent): { label: string; icon: string; color?: string } {
  switch (event.kind) {
    case "fix-commit":
      return { label: `${event.sha.slice(0, 8)}  ${event.headline}`, icon: "git-commit" };
    case "check-failing":
      return {
        label: `failing: ${event.checks.join(", ")}`,
        icon: "warning",
        color: "list.warningForeground",
      };
    case "gate-failed":
      return {
        label: `gate failed: ${event.reason}`,
        icon: "error",
        color: "testing.iconFailed",
      };
    case "recovery":
      return {
        label: `recovery ${event.action} ${event.recovered ? "recovered" : "declined"}`,
        icon: event.recovered ? "pass" : "circle-slash",
      };
    case "escalation":
      return {
        label: `${event.from} → ${event.to}${event.reason ? ` (${event.reason})` : ""}`,
        icon: "arrow-circle-up",
        color: "charts.purple",
      };
  }
}

export class StageActivityTreeItem extends BaseTreeItem {
  readonly event: StageActivityEvent;

  constructor(event: StageActivityEvent) {
    const { label, icon, color } = describe(event);
    super(label, vscode.TreeItemCollapsibleState.None);
    this.event = event;
    if (color) {
      this.setIconWithColor(icon, new vscode.ThemeColor(color));
    } else {
      this.setIcon(icon);
    }
    this.contextValue = `stage-activity-${event.kind}`;
    this.description = event.at ? new Date(event.at).toLocaleTimeString() : "";
    this.tooltip = event.kind === "recovery" && event.reason ? `${label}\n${event.reason}` : label;
  }
}
