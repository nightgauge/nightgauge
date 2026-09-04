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
import { redactSecrets } from "../utils/redaction";
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
  /**
   * Called with output from any slot (for WebView aggregation).
   *
   * `stage` is the EMITTING stage as reported by the orchestrator event that
   * carried the line (#283): the slot's "current stage" pointer advances
   * early (the #981 spinner fires before the previous stage's gate runs), so
   * a consumer that re-derives the stage from slot state mis-files
   * end-of-stage diagnostics under the next stage. Undefined when the
   * producer had no stage in scope — only then may consumers fall back.
   */
  onOutput?: (
    slotIndex: number,
    issueNumber: number,
    text: string,
    level: "info" | "error",
    stage?: PipelineStage
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
   * Write output to a slot's channel. `stage` is the emitting stage when the
   * producer knows it (#283) — threaded through so consumers never have to
   * re-derive it from the (early-advancing) slot stage pointer.
   *
   * REDACTED HERE, AT THE SINK (#1335). This method carried the raw stage
   * stream verbatim to two places: the output channel, and `onOutput`, which
   * #1330 copies into the run's evidence artifact. In one clean-install CI run
   * a `feature-dev` agent echoed GH_TOKEN, the value arrived inside a Bash
   * tool_result, and a live `github_pat_` credential was published in a
   * public-repo workflow artifact.
   *
   * The sink is the right choke point rather than the stream-json parser: the
   * text reaching here has already been flattened out of assistant text,
   * tool_use input, tool_result content and stderr alike, so redacting once
   * here covers every block shape BY CONSTRUCTION. Redacting per-block would
   * have to enumerate the shapes, and the shape nobody enumerated is exactly
   * how this leaked.
   *
   * The callback gets the redacted text too, not just the channel — the
   * evidence artifact reads the callback, and it was the artifact that leaked.
   */
  appendOutput(issueNumber: number, text: string, stage?: PipelineStage): void {
    const slot = this.channels.get(issueNumber);
    if (slot) {
      const safe = redactSecrets(text);
      slot.channel.appendLine(safe);
      this.callbacks.onOutput?.(slot.slotIndex, issueNumber, safe, "info", stage);
    }
  }

  /**
   * Write error output to a slot's channel. See appendOutput for `stage`.
   */
  appendError(issueNumber: number, text: string, stage?: PipelineStage): void {
    const slot = this.channels.get(issueNumber);
    if (slot) {
      const safe = redactSecrets(text);
      slot.channel.appendLine(`[ERROR] ${safe}`);
      this.callbacks.onOutput?.(slot.slotIndex, issueNumber, safe, "error", stage);
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
