/**
 * SlotOutputManager - Manage per-slot output channels for concurrent pipelines
 *
 * Creates and manages VSCode OutputChannel instances for each concurrent
 * pipeline slot. Each slot gets its own tab in the Output panel, labeled
 * with the slot number and issue being processed.
 *
 * Also provides a unified output feed that aggregates all slot output
 * with slot prefixes, useful for the WebView OutputWindow.
 *
 * @see Issue #1621 - Git worktree-based concurrent pipeline execution
 */

import * as vscode from "vscode";
import type { PipelineStage } from "@nightgauge/sdk";

/**
 * Slot output channel info
 */
interface SlotChannel {
  /** The VSCode output channel */
  channel: vscode.OutputChannel;
  /** Slot index */
  slotIndex: number;
  /** Issue number being processed */
  issueNumber: number;
  /** Issue title */
  title: string;
  /**
   * The last stage `updateStage` emitted for this slot, so a repeated call for
   * the same stage is a no-op (#230). The slot-started seed and the first
   * stage-changed event both target issue-pickup, which otherwise doubled the
   * "--- Stage: issue-pickup ---" banner.
   */
  lastStage?: PipelineStage;
}

/**
 * Callbacks for aggregated output events
 */
export interface SlotOutputCallbacks {
  /** Called with output from any slot (for WebView aggregation) */
  onOutput?: (
    slotIndex: number,
    issueNumber: number,
    text: string,
    level: "info" | "error"
  ) => void;
  /** Called when a slot's stage changes */
  onStageChanged?: (slotIndex: number, issueNumber: number, stage: PipelineStage) => void;
}

export class SlotOutputManager implements vscode.Disposable {
  private channels: Map<number, SlotChannel> = new Map(); // keyed by issueNumber
  private callbacks: SlotOutputCallbacks = {};
  private disposables: vscode.Disposable[] = [];

  /**
   * Set callbacks for aggregated output
   */
  setCallbacks(callbacks: SlotOutputCallbacks): void {
    this.callbacks = callbacks;
  }

  /**
   * Create an output channel for a new slot
   *
   * @param slotIndex - The slot index (0-based)
   * @param issueNumber - Issue number being processed
   * @param title - Issue title for the channel name
   */
  createSlotChannel(slotIndex: number, issueNumber: number, title: string): vscode.OutputChannel {
    // Remove existing channel for this issue if any
    this.removeSlotChannel(issueNumber);

    const channelName = `Nightgauge Slot ${slotIndex + 1} (#${issueNumber})`;
    const channel = vscode.window.createOutputChannel(channelName);

    const slotChannel: SlotChannel = {
      channel,
      slotIndex,
      issueNumber,
      title,
    };

    this.channels.set(issueNumber, slotChannel);

    // Write header
    channel.appendLine(`=== Pipeline Slot ${slotIndex + 1} ===`);
    channel.appendLine(`Issue: #${issueNumber} - ${title}`);
    channel.appendLine(`Started: ${new Date().toISOString()}`);
    channel.appendLine("=".repeat(50));
    channel.appendLine("");

    return channel;
  }

  /**
   * Write output to a slot's channel
   */
  appendOutput(issueNumber: number, text: string): void {
    const slot = this.channels.get(issueNumber);
    if (slot) {
      slot.channel.appendLine(text);
      this.callbacks.onOutput?.(slot.slotIndex, issueNumber, text, "info");
    }
  }

  /**
   * Write error output to a slot's channel
   */
  appendError(issueNumber: number, text: string): void {
    const slot = this.channels.get(issueNumber);
    if (slot) {
      slot.channel.appendLine(`[ERROR] ${text}`);
      this.callbacks.onOutput?.(slot.slotIndex, issueNumber, text, "error");
    }
  }

  /**
   * Update stage display for a slot
   */
  updateStage(issueNumber: number, stage: PipelineStage): void {
    const slot = this.channels.get(issueNumber);
    if (slot) {
      // Idempotent per stage (#230): the slot-started seed and the first
      // stage-changed event both fire for issue-pickup; without this guard the
      // banner printed twice. Real transitions still emit because the stage
      // differs from lastStage.
      if (slot.lastStage === stage) {
        return;
      }
      slot.lastStage = stage;
      slot.channel.appendLine("");
      slot.channel.appendLine(`--- Stage: ${stage} ---`);
      this.callbacks.onStageChanged?.(slot.slotIndex, issueNumber, stage);
    }
  }

  /**
   * Show a slot's output channel by issue number
   */
  showSlot(issueNumber: number): void {
    const slot = this.channels.get(issueNumber);
    if (slot) {
      slot.channel.show(true); // true = preserveFocus
    }
  }

  /**
   * Reveal a slot's output channel by slot index
   *
   * Called by the 'nightgauge-pipeline.showSlotOutput' context menu command.
   * Looks up the channel by slotIndex and brings it into focus.
   *
   * @param slotIndex - The 0-based slot index
   */
  revealSlotChannel(slotIndex: number): void {
    for (const slot of this.channels.values()) {
      if (slot.slotIndex === slotIndex) {
        slot.channel.show(true);
        return;
      }
    }
  }

  /**
   * Mark a slot as completed
   */
  markCompleted(issueNumber: number, success: boolean): void {
    const slot = this.channels.get(issueNumber);
    if (slot) {
      slot.channel.appendLine("");
      slot.channel.appendLine("=".repeat(50));
      slot.channel.appendLine(
        `Pipeline ${success ? "COMPLETED" : "FAILED"} at ${new Date().toISOString()}`
      );
    }
  }

  /**
   * Remove and dispose a slot's output channel
   */
  removeSlotChannel(issueNumber: number): void {
    const slot = this.channels.get(issueNumber);
    if (slot) {
      slot.channel.dispose();
      this.channels.delete(issueNumber);
    }
  }

  /**
   * Get all active slot issue numbers
   */
  getActiveIssues(): number[] {
    return Array.from(this.channels.keys());
  }

  /**
   * Get slot info for an issue
   */
  getSlotInfo(issueNumber: number): { slotIndex: number; title: string } | undefined {
    const slot = this.channels.get(issueNumber);
    if (slot) {
      return { slotIndex: slot.slotIndex, title: slot.title };
    }
    return undefined;
  }

  /**
   * Dispose all channels.
   *
   * NOTE: We intentionally do NOT call channel.dispose() here. When the
   * extension host reloads, VSCode will dispose the channels automatically.
   * By not disposing them ourselves, the user can still read the last few
   * lines of output in the Output panel after a reload — the channels remain
   * visible until the host process actually exits.
   *
   * Persistent logs are always available in .nightgauge/logs/.
   */
  dispose(): void {
    // Write a final message to each channel so the user knows logs survive
    for (const slot of this.channels.values()) {
      try {
        slot.channel.appendLine("");
        slot.channel.appendLine(
          "[Extension deactivating — full logs persisted to .nightgauge/logs/]"
        );
      } catch {
        // Channel may already be invalid
      }
    }
    this.channels.clear();
    for (const d of this.disposables) {
      d.dispose();
    }
  }
}
