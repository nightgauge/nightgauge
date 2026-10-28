/**
 * StageTreeItem — activity rendering (#1897).
 *
 * One description, icon and child-list assertion per display state.
 */

import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";
import * as vscode from "vscode";
import { StageTreeItem } from "../../../src/views/items/StageTreeItem";
import { StageActivityTreeItem } from "../../../src/views/items/StageActivityTreeItem";
import { fromRuntimeState } from "../../../src/views/items/stageActivity";

const T0 = Date.parse("2026-10-01T10:00:00.000Z");
const at = (minutes: number) => new Date(T0 + minutes * 60_000).toISOString();

function iconOf(item: StageTreeItem): { id: string; color?: string } {
  const icon = item.iconPath as vscode.ThemeIcon & { color?: { id: string } };
  return { id: icon.id, color: icon.color?.id };
}

function activityChildren(item: StageTreeItem): StageActivityTreeItem[] {
  return item
    .getChildren()
    .filter((c): c is StageActivityTreeItem => c instanceof StageActivityTreeItem);
}

function commit(item: StageTreeItem, sha: string, headline: string, minute: number): boolean {
  return item.recordToolCall({
    tool: "tool_result",
    result: `[feat/1644 ${sha}] ${headline}\n 1 file changed`,
    timestamp: at(minute),
  });
}

describe("StageTreeItem activity", () => {
  let item: StageTreeItem;

  beforeEach(() => {
    vi.useFakeTimers();
    vi.setSystemTime(T0 + 5 * 60_000);
    item = new StageTreeItem("pr-merge", "running");
    item.setStageStartedAt(at(0));
    item.setTokenUsage({ inputTokens: 100, outputTokens: 50, costUsd: 1.234 });
  });

  afterEach(() => {
    vi.useRealTimers();
  });

  it("working: keeps the phase label and spinner, adds elapsed and cost, no activity children", () => {
    expect(item.description).toBe("running · no phase markers yet · 5m · $1.23");
    expect(iconOf(item).id).toBe("sync~spin");
    expect(activityChildren(item)).toHaveLength(0);
  });

  it("remediating: round and failing check, tools icon, one child per commit", () => {
    item.recordToolCall({ tool: "tool_result", result: "lint\tfail\t1m\turl", timestamp: at(1) });
    expect(commit(item, "ecd8e01b", "fix lint", 2)).toBe(true);
    expect(commit(item, "fea8ce8d", "resolve address-less sends against their fd", 4)).toBe(true);

    expect(item.description).toBe(
      "remediating · round 2 · lint failing · 5m · $1.23 · last 1m ago"
    );
    expect(iconOf(item)).toEqual({ id: "tools", color: "charts.orange" });
    const labels = activityChildren(item).map((c) => c.label);
    expect(labels).toEqual([
      "failing: lint",
      "ecd8e01b  fix lint",
      "fea8ce8d  resolve address-less sends against their fd",
    ]);
    expect(item.collapsibleState).toBe(vscode.TreeItemCollapsibleState.Expanded);
  });

  it("retrying-after-gate: gate reason, restart icon", () => {
    item.setRuntimeActivity(
      fromRuntimeState("pr-merge", {
        stage_gate_results: {
          "pr-merge": [
            { gate_name: "pr-merge", passed: false, reason: "PR not MERGED", timestamp: at(3) },
          ],
        },
        stage_recovery_attempts: {
          "pr-merge": [{ action: "skill-exited-without-merging", recovered: false, at: at(4) }],
        },
      })
    );
    expect(item.description).toBe("retrying after gate: PR not MERGED · 5m · $1.23 · last 1m ago");
    expect(iconOf(item)).toEqual({ id: "debug-restart", color: "charts.yellow" });
    expect(activityChildren(item).map((c) => c.label)).toEqual([
      "gate failed: PR not MERGED",
      "recovery skill-exited-without-merging declined",
    ]);
  });

  it("escalated: models, purple icon, escalation child", () => {
    item.setRuntimeActivity(
      fromRuntimeState("pr-merge", {
        escalation_history: [
          {
            stage: "pr-merge",
            fromModel: "sonnet",
            toModel: "opus",
            reason: "gate_failed",
            at: at(4),
          },
        ],
      })
    );
    expect(item.description).toBe("escalated sonnet → opus · 5m · $1.23 · last 1m ago");
    expect(iconOf(item)).toEqual({ id: "arrow-circle-up", color: "charts.purple" });
    expect(activityChildren(item).map((c) => c.label)).toEqual(["sonnet → opus (gate_failed)"]);
    const tooltip = (item.tooltip as vscode.MarkdownString).value;
    expect(tooltip).toContain("sonnet → opus (gate_failed)");
  });

  it("idle: flips on tick past the threshold with no event, distinct icon", () => {
    item.setIdleThreshold(600);
    vi.setSystemTime(T0 + 30 * 60_000);
    item.tick();
    expect(item.description).toBe("idle 30m · 30m · $1.23");
    expect(iconOf(item)).toEqual({ id: "watch", color: "list.warningForeground" });
    expect(activityChildren(item)).toHaveLength(0);
  });

  it("a non-event tool call stamps activity without adding a child", () => {
    expect(item.recordToolCall({ tool: "tool_result", result: "ok", timestamp: at(5) })).toBe(
      false
    );
    expect(activityChildren(item)).toHaveLength(0);
  });

  it("completed stages keep their summary and keep activity children ahead of phases", () => {
    commit(item, "ecd8e01b", "fix lint", 2);
    item.setPhases([{ name: "merge", status: "complete" }], undefined, 1);
    item.setStatus("complete");
    expect(item.description).toBe("1/1 phases | $1.2340 | 0.1K tokens");
    expect(item.getChildren()[0]).toBeInstanceOf(StageActivityTreeItem);
    expect(item.collapsibleState).toBe(vscode.TreeItemCollapsibleState.Collapsed);
  });

  it("reset clears activity", () => {
    commit(item, "ecd8e01b", "fix lint", 2);
    item.reset();
    expect(activityChildren(item)).toHaveLength(0);
  });
});
