/**
 * AttentionTreeProvider — the Action Center sidebar tree (ADR 015 §E).
 *
 * Renders open DecisionRequests as severity-ordered cards grouped into the
 * two bands from the ADR's VSCode mockup: "Blocking" (`blocking_fleet` +
 * `blocking_run`) and "Needs a human" (`fyi`). State is fetched once via
 * `attention.list` and kept live thereafter by folding the `attention.event`
 * push (created | updated | acknowledged | resolved | expired) — no polling,
 * using the subscribe-and-fold shape the workflow tree used before #1208
 * removed it.
 *
 * "One queue, many mirrors" (ADR 015 §D): a resolve on ANY surface emits the
 * same event this provider consumes, so a request resolved elsewhere (the
 * dashboard, a future Discord bot) disappears from this tree on the next
 * push, exactly as it does after a local resolve.
 *
 * Grouping is by SEVERITY, not by run — which is why repo-scoped requests
 * (issue #93) needed no new top-level section. A card raised by the sweep has
 * `context.repo` and nothing else: no run, no issue, no stage. Any grouping
 * keyed on those would have dropped it silently, so the severity bands are load
 * bearing here, not merely cosmetic.
 *
 * @see docs/decisions/015-decision-requests.md
 * @see Issue #325, Issue #93
 */

import * as vscode from "vscode";
import type {
  AttentionRequestView,
  AttentionEvent,
  AttentionListResult,
} from "../../services/IpcClientBase";
import { AttentionGroupTreeItem, AttentionTreeItem, compareRequests } from "./attentionTreeItems";
import { getPrefixedMainChannel } from "../../utils/logger";

// Folded into the shared main channel behind an "attention" tag (#749) rather
// than a dedicated destination. Lazy + guarded so importing this module never
// touches the VS Code API outside a real extension host.
let _outputChannel: vscode.OutputChannel | null = null;
function logAttentionWarning(message: string): void {
  if (!_outputChannel) {
    try {
      _outputChannel = getPrefixedMainChannel("attention");
    } catch {
      // Not in a VS Code host
    }
  }
  const line = `[${new Date().toISOString()}] [WARN] ${message}`;
  if (_outputChannel) {
    _outputChannel.appendLine(line);
  } else {
    console.warn(line);
  }
}

/** Minimal slice of IpcClient this provider needs — eases testing. */
export interface AttentionIpcSource {
  attentionList(includeTerminal?: boolean, repo?: string): Promise<AttentionListResult>;
  on(event: string, handler: (data: unknown) => void): { dispose(): void };
}

function isBlocking(severity: AttentionRequestView["severity"]): boolean {
  return severity === "blocking_fleet" || severity === "blocking_run";
}

/** Terminal states drop out of the inbox. `auto_resolved` is a sweep retracting
 * a standing card whose condition cleared (issue #92) — the operator did
 * nothing and the card disappears, which is the point. */
function isTerminalState(state: AttentionRequestView["lifecycle"]["state"]): boolean {
  return state === "resolved" || state === "expired" || state === "auto_resolved";
}

/** Whether a request should still interrupt. Muting and acknowledging both
 * silence a card without claiming its condition ended, so both suppress
 * alerting while leaving the card in the tree. */
function isAlertWorthy(request: AttentionRequestView): boolean {
  return !request.lifecycle.muted && request.lifecycle.state !== "acknowledged";
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

  /**
   * Re-fetch the open request list from the attached IPC source.
   *
   * Never rejects: with no Go daemon running (every first launch, every CI
   * runner) `attentionList()` rejects, and every caller here — the initial
   * fetch in `attach()`, and the repo-scoped sweep's `onChanged` callback —
   * fires this without awaiting the result. Swallowing here, once, is what
   * keeps that an inert degradation instead of an unhandled rejection
   * escaping activation (#765).
   */
  async refresh(): Promise<void> {
    if (!this.attachedSource) return;
    try {
      const result = await this.attachedSource.attentionList(false);
      this.requests = (result.requests ?? []).filter((r) => !isTerminalState(r.lifecycle.state));
      this._onDidChangeTreeData.fire();
    } catch (error) {
      logAttentionWarning(
        `AttentionTreeProvider.refresh() failed, Action Center left showing its last known ` +
          `state: ${error instanceof Error ? error.message : String(error)}`
      );
    }
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

  /**
   * Count of ALERT-WORTHY blocking requests — drives the view badge.
   *
   * Muted and acknowledged cards are excluded here and ONLY here. Both are
   * still rendered in the tree at their severity: acknowledging says "I have
   * seen this", muting says "stop telling me until it changes", and neither
   * says "this is no longer true". The badge is the interruption channel, so it
   * is the one surface where that distinction has to bite — a badge that keeps
   * counting a condition the operator explicitly silenced is how an inbox
   * teaches people to ignore it.
   */
  getOpenBlockingCount(): number {
    return this.requests.filter((r) => isBlocking(r.severity) && isAlertWorthy(r)).length;
  }

  /**
   * The most urgent `blocking_fleet` request, or undefined when none is open.
   *
   * A fleet-wide blocker has to be legible without expanding a tree node — the
   * 2026-07-25 incident (epic #88) failed precisely because a red `main` was
   * blocking every PR in the repo and nothing said so anywhere. The view header
   * renders this, so it survives a collapsed tree. Muted cards are excluded for
   * the same reason as the badge: the operator already knows.
   */
  getTopFleetBlocker(): AttentionRequestView | undefined {
    return [...this.requests]
      .filter((r) => r.severity === "blocking_fleet" && isAlertWorthy(r))
      .sort(compareRequests)[0];
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
