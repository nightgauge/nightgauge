/**
 * AttentionTreeProvider — the Action Center sidebar tree (ADR 015 §E).
 *
 * Renders open DecisionRequests as severity-ordered cards grouped into the
 * two bands from the ADR's VSCode mockup: "Blocking" (`blocking_fleet` +
 * `blocking_run`) and "Needs a human" (`fyi`). State is fetched once via
 * `attention.list` and kept live thereafter by folding the `attention.event`
 * push (created | updated | acknowledged | resolved | expired) — no polling,
 * mirroring {@link WorkflowTreeProvider}'s subscribe-and-fold shape.
 *
 * "One queue, many mirrors" (ADR 015 §D): a resolve on ANY surface emits the
 * same event this provider consumes, so a request resolved elsewhere (the
 * dashboard, a future Discord bot) disappears from this tree on the next
 * push, exactly as it does after a local resolve.
 *
 * @see docs/decisions/015-decision-requests.md
 * @see Issue #325
 */

import * as vscode from "vscode";
import type {
  AttentionRequestView,
  AttentionEvent,
  AttentionListResult,
} from "../../services/IpcClientBase";
import { AttentionGroupTreeItem, AttentionTreeItem, compareRequests } from "./attentionTreeItems";

/** Minimal slice of IpcClient this provider needs — eases testing. */
export interface AttentionIpcSource {
  attentionList(includeTerminal?: boolean, repo?: string): Promise<AttentionListResult>;
  on(event: string, handler: (data: unknown) => void): { dispose(): void };
}

function isBlocking(severity: AttentionRequestView["severity"]): boolean {
  return severity === "blocking_fleet" || severity === "blocking_run";
}

function isTerminalState(state: AttentionRequestView["lifecycle"]["state"]): boolean {
  return state === "resolved" || state === "expired";
}

export class AttentionTreeProvider
  implements vscode.TreeDataProvider<AttentionTreeItem>, vscode.Disposable
{
  private readonly _onDidChangeTreeData = new vscode.EventEmitter<
    AttentionTreeItem | undefined | void
  >();
  readonly onDidChangeTreeData = this._onDidChangeTreeData.event;

  /** Re-broadcasts every raw `attention.event` push after folding it into state,
   * so other consumers (badge, toast) can react without a second IPC subscription. */
  private readonly _onDidReceiveEvent = new vscode.EventEmitter<AttentionEvent>();
  readonly onDidReceiveEvent = this._onDidReceiveEvent.event;

  /** Non-terminal requests only — resolved/expired ones are dropped on ingestion
   * (ADR 015 §D: "resolve anywhere, disappears everywhere"). */
  private requests: AttentionRequestView[] = [];

  private readonly disposables: vscode.Disposable[] = [];
  private attachedSource: AttentionIpcSource | null = null;

  /**
   * Subscribe to an IPC source and perform the initial `attention.list` fetch.
   * Idempotent per source — re-attaching the same source is a no-op so a
   * reconnect never double-subscribes.
   */
  attach(source: AttentionIpcSource): void {
    if (this.attachedSource === source) return;
    this.attachedSource = source;
    this.disposables.push(
      source.on("attention.event", (raw) => this.handleEvent(raw as AttentionEvent))
    );
    void this.refresh();
  }

  /** Re-fetch the open request list from the attached IPC source. */
  async refresh(): Promise<void> {
    if (!this.attachedSource) return;
    const result = await this.attachedSource.attentionList(false);
    this.requests = (result.requests ?? []).filter((r) => !isTerminalState(r.lifecycle.state));
    this._onDidChangeTreeData.fire();
  }

  /** Fold one `attention.event` push into local state (create/update/drop) and refresh. */
  private handleEvent(evt: AttentionEvent): void {
    const req = evt.request;
    const idx = this.requests.findIndex((r) => r.id === req.id);
    if (isTerminalState(req.lifecycle.state)) {
      if (idx >= 0) this.requests.splice(idx, 1);
    } else if (idx >= 0) {
      this.requests[idx] = req;
    } else {
      this.requests.push(req);
    }
    this._onDidChangeTreeData.fire();
    this._onDidReceiveEvent.fire(evt);
  }

  /** Count of open blocking requests (`blocking_fleet` + `blocking_run`) — drives the view badge. */
  getOpenBlockingCount(): number {
    return this.requests.filter((r) => isBlocking(r.severity)).length;
  }

  /** Whether any non-terminal request exists — drives the `viewsWelcome` empty state's `when` clause. */
  hasAny(): boolean {
    return this.requests.length > 0;
  }

  /** Look up a request by id (e.g. to resolve the request behind a stale tree item reference). */
  getRequestById(id: string): AttentionRequestView | undefined {
    return this.requests.find((r) => r.id === id);
  }

  getTreeItem(element: AttentionTreeItem): vscode.TreeItem {
    return element;
  }

  getChildren(element?: AttentionTreeItem): vscode.ProviderResult<AttentionTreeItem[]> {
    if (element) {
      return element.getChildren();
    }
    const sorted = [...this.requests].sort(compareRequests);
    const blocking = sorted.filter((r) => isBlocking(r.severity));
    const fyi = sorted.filter((r) => !isBlocking(r.severity));

    const groups: AttentionTreeItem[] = [];
    if (blocking.length > 0) groups.push(new AttentionGroupTreeItem("Blocking", blocking));
    if (fyi.length > 0) groups.push(new AttentionGroupTreeItem("Needs a human", fyi));
    return groups;
  }

  dispose(): void {
    this._onDidChangeTreeData.dispose();
    this._onDidReceiveEvent.dispose();
    for (const d of this.disposables) d.dispose();
    this.disposables.length = 0;
  }
}
