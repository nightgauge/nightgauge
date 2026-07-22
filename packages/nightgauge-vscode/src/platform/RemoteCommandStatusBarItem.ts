/**
 * RemoteCommandStatusBarItem — VSCode status bar item showing remote command activity.
 *
 * Displays recent remote command counts and polling state from the Go binary's
 * command executor. Priority 96 — directly right of PlatformStatusBarItem (97).
 *
 * Hidden when no remote activity has been detected in the current session.
 * Clicking the item runs `nightgauge.showRemoteCommandHistory`.
 *
 * @see Issue #2170 — Add IPC bridge for remote command status
 */

import * as vscode from "vscode";

/** Display state for the remote command status bar item. */
export type RemoteDisplayState = "idle" | "polling" | "hasCommands";

/**
 * RemoteCommandStatusBarItem manages the remote command activity indicator.
 */
export class RemoteCommandStatusBarItem implements vscode.Disposable {
  readonly item: vscode.StatusBarItem;

  private displayState: RemoteDisplayState = "idle";
  private commandCount = 0;

  constructor() {
    this.item = vscode.window.createStatusBarItem(
      vscode.StatusBarAlignment.Left,
      96 // Right of PlatformStatusBarItem (97)
    );
    this.item.command = "nightgauge.showRemoteCommandHistory";
    // Start hidden; shown only when remote activity is detected
  }

  /** Current display state (for testing). */
  getDisplayState(): RemoteDisplayState {
    return this.displayState;
  }

  /** Number of remote commands in the current session (for testing). */
  getCommandCount(): number {
    return this.commandCount;
  }

  /** Update the status bar to reflect idle state (no recent activity). */
  setIdle(): void {
    this.displayState = "idle";
    this.commandCount = 0;
    this.item.hide();
  }

  /** Update the status bar to show active polling. */
  setPolling(): void {
    this.displayState = "polling";
    this.render();
    this.item.show();
  }

  /** Update the status bar with received command count. */
  setCommandCount(count: number): void {
    this.commandCount = count;
    this.displayState = count > 0 ? "hasCommands" : "idle";
    if (this.displayState === "idle") {
      this.item.hide();
      return;
    }
    this.render();
    this.item.show();
  }

  /** Update polling + command count together. */
  update(polling: boolean, commandCount: number): void {
    this.commandCount = commandCount;
    if (polling) {
      this.displayState = "polling";
    } else {
      this.displayState = commandCount > 0 ? "hasCommands" : "idle";
    }

    if (this.displayState === "idle") {
      this.item.hide();
      return;
    }
    this.render();
    this.item.show();
  }

  dispose(): void {
    this.item.dispose();
  }

  private render(): void {
    switch (this.displayState) {
      case "polling":
        this.item.text = "$(sync~spin) Remote: Polling\u2026";
        this.item.tooltip = "Remote command polling active\nClick to view history";
        break;
      case "hasCommands":
        this.item.text = `$(cloud-download) Remote: ${this.commandCount} command${this.commandCount !== 1 ? "s" : ""}`;
        this.item.tooltip = `${this.commandCount} remote command${this.commandCount !== 1 ? "s" : ""} received this session\nClick to view history`;
        break;
      default:
        this.item.text = "$(cloud-download) Remote";
        this.item.tooltip = "Remote commands";
        break;
    }
  }
}
