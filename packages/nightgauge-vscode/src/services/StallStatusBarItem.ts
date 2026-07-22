/**
 * StallStatusBarItem — VSCode status bar item showing persistent stall indicator.
 *
 * Displays elapsed time when a pipeline stage exceeds its stall threshold.
 * Priority 95 — left of RemoteCommandStatusBarItem (96).
 * Clicking opens the output panel via the `nightgauge.showOutputWindow` command.
 *
 * Updates elapsed time every 30s via an internal ticker, matching the stall
 * detection interval in skillRunner.ts. Clears automatically when the stage
 * completes via the `onStallWarningClear` callback in PipelineBridge.
 *
 * @see Issue #2655 — Persistent stall indicators in status bar and output panel
 */

import * as vscode from "vscode";
import type { PipelineStage } from "@nightgauge/sdk";

/** Display state for the stall status bar item. */
export type StallDisplayState = "hidden" | "stalled";

/**
 * StallStatusBarItem manages a persistent stall indicator in the VS Code status bar.
 *
 * @example
 * ```typescript
 * const stallBar = new StallStatusBarItem();
 * context.subscriptions.push(stallBar);
 *
 * // On stall detected:
 * stallBar.showStalled("feature-dev", event.elapsed_ms);
 *
 * // On stage complete:
 * stallBar.clear();
 * ```
 */
export class StallStatusBarItem implements vscode.Disposable {
  readonly item: vscode.StatusBarItem;

  private displayState: StallDisplayState = "hidden";
  private elapsedMs = 0;
  private stage: PipelineStage | undefined;
  private updateTicker: ReturnType<typeof setInterval> | null = null;

  constructor() {
    this.item = vscode.window.createStatusBarItem(
      vscode.StatusBarAlignment.Left,
      95 // Priority: left of RemoteCommandStatusBarItem (96)
    );
    this.item.command = "nightgauge.showOutputWindow";
    // Start hidden — shown only when a stall is detected
  }

  /** Current display state (for testing). */
  getDisplayState(): StallDisplayState {
    return this.displayState;
  }

  /**
   * Show stall indicator for a stage with current elapsed time.
   *
   * Starts a 30s update ticker to keep the elapsed time display current.
   * Safe to call multiple times — clears previous ticker before starting new one.
   *
   * @param stage - The pipeline stage that is stalled
   * @param elapsedMs - Elapsed time in milliseconds since stage started
   */
  showStalled(stage: PipelineStage, elapsedMs: number): void {
    // Clear any existing ticker before starting a new one (idempotent)
    if (this.updateTicker !== null) {
      clearInterval(this.updateTicker);
    }
    this.stage = stage;
    this.elapsedMs = elapsedMs;
    this.displayState = "stalled";
    this.updateTicker = setInterval(() => {
      this.elapsedMs += 30_000;
      this.render();
    }, 30_000);
    this.render();
    this.item.show();
  }

  /**
   * Hide the stall indicator and clear the update ticker.
   *
   * Safe to call multiple times (idempotent). Called by `onStallWarningClear`
   * when a stage completes, errors, or is cancelled.
   */
  clear(): void {
    if (this.updateTicker !== null) {
      clearInterval(this.updateTicker);
      this.updateTicker = null;
    }
    this.displayState = "hidden";
    this.elapsedMs = 0;
    this.stage = undefined;
    this.item.hide();
  }

  dispose(): void {
    if (this.updateTicker !== null) {
      clearInterval(this.updateTicker);
      this.updateTicker = null;
    }
    this.item.dispose();
  }

  private render(): void {
    if (this.displayState === "stalled" && this.stage) {
      const elapsedSec = Math.floor(this.elapsedMs / 1000);
      const min = Math.floor(elapsedSec / 60);
      const sec = elapsedSec % 60;
      const timeStr = min > 0 ? `${min}m ${sec}s` : `${sec}s`;
      this.item.text = `$(clock) Stall: ${this.stage} running for ${timeStr}`;
      this.item.tooltip = `Stage "${this.stage}" has been running longer than expected.\nClick to view output panel.`;
    }
  }
}
