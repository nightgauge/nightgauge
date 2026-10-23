/**
 * EventStreamStatusBarItem — aggregated SSE stream health indicator in the status bar.
 *
 * Subscribes to both EventStreamService.onStreamStatusChanged (audit/pipeline stream)
 * and ProjectEventSubscriber.onSseStatusChanged (board stream), aggregates to a
 * worst-of state, and exposes a reconnect command click action.
 *
 * Priority 94 — just right of StallStatusBarItem (95).
 * Clicking triggers `nightgauge.reconnectEventStreams`.
 *
 * @see Issue #3715 — event-stream connectivity status bar item
 */

import * as vscode from "vscode";
import type { EventStreamService } from "../services/EventStreamService";
import type { ProjectEventSubscriber } from "../services/ProjectEventSubscriber";
import type { SseStreamStatus } from "../services/PlatformSseClient";

export type AggregatedStreamStatus = "connected" | "reconnecting" | "disconnected" | "idle";

interface StreamState {
  status: SseStreamStatus | "idle";
  label: string;
}

interface StateDisplay {
  icon: string;
  tooltip: string;
  background?: vscode.ThemeColor;
}

const STATE_DISPLAY: Record<AggregatedStreamStatus, StateDisplay> = {
  connected: { icon: "$(radio-tower)", tooltip: "Event streams connected" },
  reconnecting: {
    icon: "$(sync~spin)",
    tooltip: "Event streams reconnecting…",
    background: new vscode.ThemeColor("statusBarItem.warningBackground"),
  },
  disconnected: {
    icon: "$(plug)",
    tooltip: "Event streams disconnected",
    background: new vscode.ThemeColor("statusBarItem.errorBackground"),
  },
  idle: { icon: "$(radio-tower)", tooltip: "Event streams idle (not started)" },
};

export class EventStreamStatusBarItem implements vscode.Disposable {
  readonly item: vscode.StatusBarItem;
  private _projectState: StreamState = { status: "idle", label: "" };
  private _accountState: StreamState = { status: "idle", label: "" };
  private _lastError: string | null = null;
  private readonly _disposables: vscode.Disposable[] = [];

  constructor(commandId = "nightgauge.reconnectEventStreams") {
    this.item = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Left, 94);
    this.item.command = commandId;
    this._render();
    this.item.show();
  }

  attachStreams(accountStream: EventStreamService, projectStream: ProjectEventSubscriber): void {
    this._disposables.push(
      accountStream.onStreamStatusChanged(({ status, label }) => {
        this._accountState = { status, label };
        if (status !== "connected") this._lastError = label;
        this._render();
      }),
      projectStream.onSseStatusChanged(({ status, label }) => {
        this._projectState = { status, label };
        if (status !== "connected") this._lastError = label;
        this._render();
      })
    );
  }

  getAggregatedStatus(): AggregatedStreamStatus {
    return this._aggregate();
  }

  getLastError(): string | null {
    return this._lastError;
  }

  private _aggregate(): AggregatedStreamStatus {
    const states = [this._projectState.status, this._accountState.status];
    if (states.includes("disconnected")) return "disconnected";
    if (states.includes("reconnecting")) return "reconnecting";
    if (states.includes("connected")) return "connected";
    return "idle";
  }

  private _render(): void {
    const agg = this._aggregate();
    const display = STATE_DISPLAY[agg];
    this.item.text = display.icon;
    this.item.tooltip = this._lastError
      ? `${display.tooltip}\nLast error: ${this._lastError}`
      : display.tooltip;
    this.item.backgroundColor = display.background;
  }

  dispose(): void {
    for (const d of this._disposables) {
      d.dispose();
    }
    this.item.dispose();
  }
}
