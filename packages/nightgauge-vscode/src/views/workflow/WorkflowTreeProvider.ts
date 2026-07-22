/**
 * WorkflowTreeProvider — the live workflow sidebar tree.
 *
 * Renders the canonical `schemaVersion-4` {@link WorkflowEvent} node tree
 * (run → phase → agent → judge) DIRECTLY off the SDK EventBus stream: it
 * subscribes to {@link EventStreamService.onWorkflowEvent}, folds each emission
 * into the live hierarchy ({@link WorkflowTreeModel}, last-write-wins by `seq`),
 * and refreshes the view. No platform round-trip, no local event mirror — the
 * node tree is the single source the tree renders (#3919, reversing #3714).
 *
 * Honesty (#3919): the concurrency gauge ceiling is the real per-backend lower
 * bound (16 lanes native / 6 lanes portable fan-out), costs on `sdk-fanout` runs
 * are labelled estimates, and fan-out judges are labelled "gate verification".
 *
 * @see Issue #3919
 * @see workflowTreeModel.ts — the pure fold
 * @see workflowTreeItems.ts — the per-node rendering
 */

import * as vscode from "vscode";
import { CLAUDE_CEILING, FANOUT_CEILING, type WorkflowEvent } from "@nightgauge/sdk";
import { WorkflowTreeModel, type FoldedRun } from "./workflowTreeModel";
import { WorkflowRunTreeItem, type WorkflowTreeItem } from "./workflowTreeItems";

/** Minimal slice of EventStreamService this provider needs — eases testing. */
export interface WorkflowEventSource {
  onWorkflowEvent: vscode.Event<WorkflowEvent>;
}

/** Lanes-busy gauge ceiling for a run's backend (the honest lower bound). */
function ceilingFor(run: FoldedRun): number {
  return run.node.backend === "native-workflow"
    ? CLAUDE_CEILING.maxConcurrent
    : FANOUT_CEILING.maxConcurrent;
}

export class WorkflowTreeProvider
  implements vscode.TreeDataProvider<WorkflowTreeItem>, vscode.Disposable
{
  private readonly _onDidChangeTreeData = new vscode.EventEmitter<
    WorkflowTreeItem | undefined | void
  >();
  readonly onDidChangeTreeData = this._onDidChangeTreeData.event;

  private readonly model = new WorkflowTreeModel();
  private readonly disposables: vscode.Disposable[] = [];

  /** The currently-subscribed source — guards against duplicate `attach`. */
  private attachedSource: WorkflowEventSource | null = null;

  /** Coalesce a burst of emissions into a single refresh per macrotask. */
  private refreshScheduled = false;

  /**
   * Subscribe the tree to a workflow event source. Each parsed node emission is
   * folded (last-write-wins by `seq`); a changed fold schedules a refresh.
   *
   * Idempotent per source: re-attaching the same `WorkflowEventSource` (e.g. the
   * EventStreamService singleton on a re-auth) is a no-op, so duplicate
   * subscriptions never accumulate.
   */
  attach(source: WorkflowEventSource): void {
    if (this.attachedSource === source) return;
    this.attachedSource = source;
    this.disposables.push(
      source.onWorkflowEvent((event) => {
        if (this.model.apply(event)) {
          this.scheduleRefresh();
        }
      })
    );
  }

  /** Clear all folded state and refresh (e.g. on sign-out / stream reset). */
  reset(): void {
    this.model.clear();
    this.scheduleRefresh();
  }

  getTreeItem(element: WorkflowTreeItem): vscode.TreeItem {
    return element;
  }

  getChildren(element?: WorkflowTreeItem): vscode.ProviderResult<WorkflowTreeItem[]> {
    if (element) {
      return element.getChildren();
    }
    const runs = this.model.runs();
    if (runs.length === 0) {
      return [emptyPlaceholder()];
    }
    return runs.map((run) => new WorkflowRunTreeItem(run, ceilingFor(run)));
  }

  private scheduleRefresh(): void {
    if (this.refreshScheduled) return;
    this.refreshScheduled = true;
    queueMicrotask(() => {
      this.refreshScheduled = false;
      this._onDidChangeTreeData.fire();
    });
  }

  dispose(): void {
    this._onDidChangeTreeData.dispose();
    for (const d of this.disposables) d.dispose();
    this.disposables.length = 0;
  }
}

/** Placeholder row shown before any workflow run has emitted. */
function emptyPlaceholder(): WorkflowTreeItem {
  const item = new (class extends vscode.TreeItem {
    getChildren(): WorkflowTreeItem[] {
      return [];
    }
  })("No active workflow", vscode.TreeItemCollapsibleState.None) as WorkflowTreeItem;
  item.iconPath = new vscode.ThemeIcon("circuit-board");
  item.contextValue = "workflow.empty";
  item.description = "waiting for the orchestrator…";
  return item;
}
