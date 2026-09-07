/**
 * StageTreeItem - Tree item for pipeline stages
 *
 * Shows stage status with appropriate icons and token usage.
 * Supports inline action buttons for run/retry.
 *
 * @see Issue #498 - Token tracking shows N/A for interactive mode
 */

import * as vscode from "vscode";
import { PHASE_REGISTRY, type PipelineStage } from "@nightgauge/sdk";
import { BaseTreeItem } from "./BaseTreeItem";
import { PhaseTreeItem, type PhaseStatus } from "./PhaseTreeItem";
import { SkippedPhasesTreeItem } from "./SkippedPhasesTreeItem";
import type { StageExecutionMode } from "../../services/PipelineStateService";
import type { StagePhase } from "../../schemas/pipelineState";

/**
 * Stage status types
 */
export type StageStatus = "pending" | "running" | "complete" | "failed" | "skipped" | "deferred";

/**
 * Token usage information for a stage
 */
export interface StageTokenInfo {
  inputTokens: number;
  outputTokens: number;
  costUsd: number;
}

/**
 * Stage display configuration
 */
interface StageDisplayConfig {
  icon: string;
  iconColor?: string;
  animate?: boolean;
}

/**
 * Status to display configuration mapping
 */
const STATUS_CONFIG: Record<StageStatus, StageDisplayConfig> = {
  pending: { icon: "circle-outline" },
  running: { icon: "sync~spin", animate: true },
  complete: { icon: "check", iconColor: "testing.iconPassed" },
  failed: { icon: "error", iconColor: "testing.iconFailed" },
  skipped: { icon: "debug-step-over" },
  deferred: { icon: "watch", iconColor: "editorWarning.foreground" },
};

/**
 * Human-readable stage names
 *
 * Includes bookend stages (pipeline-start, pipeline-finish) for reliable
 * synchronization points. These are deterministic orchestration stages
 * that execute synchronously with zero AI token consumption.
 */
const STAGE_LABELS: Record<PipelineStage, string> = {
  "pipeline-start": "Initialize",
  "issue-pickup": "Issue Pickup",
  "feature-planning": "Feature Planning",
  "feature-dev": "Feature Development",
  "feature-validate": "Feature Validation",
  "pr-create": "PR Creation",
  "pr-merge": "PR Merge",
  "pipeline-finish": "Completion",
};

/**
 * Custom icons for bookend stages (distinct from skill stages)
 */
const BOOKEND_ICONS: Partial<Record<PipelineStage, string>> = {
  "pipeline-start": "rocket",
  "pipeline-finish": "check-all",
};

function phaseNameToLabel(phaseName: string): string {
  return phaseName
    .split("-")
    .map((w) => w.charAt(0).toUpperCase() + w.slice(1))
    .join(" ");
}

/**
 * Render the live `[observed/applicable]` suffix, or nothing at all.
 *
 * A stage whose markers never arrive would otherwise show a `[0/N]` that sits
 * still for the length of the stage and reads as a stall. Once even one phase
 * is observed the counter is meaningful and is shown (#1558).
 */
/**
 * Below this many skips, grouping them costs the reader an expand for less
 * noise than it removes.
 */
const SKIPPED_PHASE_GROUP_THRESHOLD = 3;

function formatLivePhaseProgress(observed: number, applicable: number): string {
  if (applicable <= 0 || observed <= 0) {
    return "";
  }
  return ` [${observed}/${applicable}]`;
}

/**
 * StageTreeItem - Represents a pipeline stage in the tree
 *
 * @example
 * ```typescript
 * const stage = new StageTreeItem('feature-dev', 'running');
 * stage.setTokenUsage({ inputTokens: 1500, outputTokens: 800, costUsd: 0.0023 });
 * ```
 */
export class StageTreeItem extends BaseTreeItem {
  readonly stage: PipelineStage;
  private status: StageStatus;
  private tokenInfo: StageTokenInfo | null = null;
  private durationMs: number | null = null;
  private errorMessage: string | null = null;
  private retryCount: number | null = null;
  private isRetrying: boolean = false;
  private nextRetryAt: string | null = null;
  private autoRetryCount: number = 0;
  private manualRetryCount: number = 0;
  /**
   * Execution mode for this stage - affects token display
   *
   * When 'interactive', tokens are shown as "N/A" since stream-json
   * output is not available in conversational mode.
   *
   * @see Issue #498 - Token tracking for interactive execution mode
   */
  private executionMode: StageExecutionMode | null = null;

  /**
   * Current phase name (for description display when running)
   */
  private currentPhaseName: string | null = null;

  /**
   * Total phases in this stage (for progress count display)
   */
  private totalPhaseCount: number = 0;

  /**
   * Flat record of every phase row, independent of how they are arranged as
   * tree children. See `phaseItems()`.
   */
  private phaseRows: PhaseTreeItem[] = [];

  constructor(stage: PipelineStage, status: StageStatus = "pending") {
    super(STAGE_LABELS[stage], vscode.TreeItemCollapsibleState.None);

    this.stage = stage;
    this.status = status;

    this.updateDisplay();
  }

  /**
   * Compute collapsible state based on phase children and status.
   *
   * - No phases → None (leaf node)
   * - Has phases + running → Expanded (show progress)
   * - Has phases + complete/failed → Collapsed (save space)
   * - Has phases + other → Collapsed
   */
  private computeCollapsibleState(): vscode.TreeItemCollapsibleState {
    if (this.children.length === 0) {
      return vscode.TreeItemCollapsibleState.None;
    }
    if (this.status === "running") {
      return vscode.TreeItemCollapsibleState.Expanded;
    }
    return vscode.TreeItemCollapsibleState.Collapsed;
  }

  /**
   * Update the visual display based on current state
   */
  private updateDisplay(): void {
    this.collapsibleState = this.computeCollapsibleState();
    const config = STATUS_CONFIG[this.status];

    // For bookend stages with complete status, use their custom icons
    // instead of the generic check icon
    const bookendIcon = BOOKEND_ICONS[this.stage];
    const isBookend = bookendIcon !== undefined;

    // Set icon - bookend stages use custom icons when complete
    if (isBookend && this.status === "complete") {
      this.setIconWithColor(bookendIcon, new vscode.ThemeColor("testing.iconPassed"));
    } else if (config.iconColor) {
      this.setIconWithColor(config.icon, new vscode.ThemeColor(config.iconColor));
    } else {
      this.setIcon(config.icon);
    }

    // Set context value for menu visibility
    // Include bookend context for different menu options
    this.contextValue = isBookend ? `stage-bookend-${this.status}` : `stage-${this.status}`;

    // Update description with token info or status
    this.description = this.formatDescription();

    // Update tooltip
    this.tooltip = this.createTooltip();

    // Add ARIA-friendly accessible description for screen readers (Issue #304)
    this.accessibilityInformation = {
      label: this.createAccessibilityLabel(),
      role: "treeitem",
    };
  }

  /**
   * Format the description string
   *
   * Shows token usage for headless mode, or "N/A" for interactive mode
   * where stream-json output is unavailable.
   *
   * @see Issue #498 - Token tracking for interactive execution mode
   */
  private formatDescription(): string {
    // Show retry status if retrying
    if (this.isRetrying && this.nextRetryAt) {
      const now = Date.now();
      const retryTime = new Date(this.nextRetryAt).getTime();
      const secondsRemaining = Math.ceil((retryTime - now) / 1000);
      if (secondsRemaining > 0) {
        return `retrying in ${secondsRemaining}s...`;
      }
      return "retrying...";
    }

    if (this.status === "running") {
      const registryPhases = PHASE_REGISTRY[this.stage as keyof typeof PHASE_REGISTRY];
      const applicable = this.applicablePhaseCount();
      // Clamp: observed count must never exceed the applicable total
      // (defensive guard against skill/registry phase count mismatches).
      const observed = Math.min(this.countObservedPhases(), applicable);

      if (observed > 0 || this.currentPhaseName) {
        const label = this.runningPhaseLabel(registryPhases);
        const progress = formatLivePhaseProgress(observed, applicable);
        if (label || progress) {
          return `${label || "running..."}${progress}`;
        }
      }

      // A running stage that has reported nothing: say so. `0/18` looks like a
      // progress bar that is about to move, and for feature-dev it is the
      // expected display in ~89% of runs — its markers are unconditional in
      // the skill and the model emits them in ~11% of them (#1246). A reader
      // watching `0/18` for twenty-five minutes concludes the stage is stuck;
      // the stage is working normally and nobody is narrating it (#1558).
      return "running · no phase markers yet";
    }

    if (this.status === "pending") {
      return "pending";
    }

    if (this.status === "skipped") {
      return "skipped";
    }

    // Completed/failed stages with phases show compact summary
    if ((this.status === "complete" || this.status === "failed") && this.totalPhaseCount > 0) {
      const applicable = this.applicablePhaseCount();
      // Clamp: observed count must never exceed the applicable total.
      const completedCount = Math.min(this.countObservedPhases(), applicable);
      const unreportedCount = this.countPhasesWithStatus("unreported");
      const skippedCount = this.countPhasesWithStatus("skipped");
      const abandonedCount = this.countPhasesWithStatus("abandoned");
      // Every claim is named, and none is folded into another (#1246, #1558).
      // "18/18 phases" on a run that observed four of them is not a rounding
      // problem — it is the reader being told the stage's own safety phases
      // ran when the system has no evidence either way. The same is true of a
      // skip: it is a decision, not work, and it belongs beside the count
      // rather than inside it.
      const parts: string[] = [
        applicable > 0 ? `${completedCount}/${applicable} phases` : "no phases applicable",
      ];
      if (unreportedCount > 0) parts.push(`${unreportedCount} unreported`);
      if (abandonedCount > 0) parts.push(`${abandonedCount} abandoned`);
      if (skippedCount > 0) parts.push(`${skippedCount} skipped`);
      const phaseSummary = parts.join(" · ");

      // Still show token info alongside phase summary for completed stages
      if (this.executionMode === "interactive") {
        return `${phaseSummary} | tokens: N/A`;
      }
      if (this.tokenInfo) {
        const totalTokens = (
          (this.tokenInfo.inputTokens + this.tokenInfo.outputTokens) /
          1000
        ).toFixed(1);
        return `${phaseSummary} | $${this.tokenInfo.costUsd.toFixed(4)} | ${totalTokens}K tokens`;
      }
      return phaseSummary;
    }

    // For completed/failed stages, show token info or N/A based on execution mode
    if (this.status === "complete" || this.status === "failed") {
      // Interactive mode: tokens are unavailable
      if (this.executionMode === "interactive") {
        return this.status === "complete" ? "complete | tokens: N/A" : "failed | tokens: N/A";
      }

      // Headless mode: show actual token usage if available (Issue #945: cost first)
      if (this.tokenInfo) {
        const totalTokens = (
          (this.tokenInfo.inputTokens + this.tokenInfo.outputTokens) /
          1000
        ).toFixed(1);
        return `$${this.tokenInfo.costUsd.toFixed(4)} | ${totalTokens}K tokens`;
      }
    }

    if (this.status === "complete") {
      return "complete";
    }

    if (this.status === "failed") {
      return "failed";
    }

    return "";
  }

  /**
   * Maximum number of retries allowed before circuit breaker blocks
   */
  private static readonly MAX_RETRIES = 3;

  /**
   * Create a detailed tooltip
   */
  private createTooltip(): vscode.MarkdownString {
    const md = new vscode.MarkdownString();
    md.appendMarkdown(`**${STAGE_LABELS[this.stage]}**\n\n`);
    md.appendMarkdown(`Status: ${this.status}\n\n`);

    // Show retry status if retrying
    if (this.isRetrying && this.nextRetryAt) {
      const now = Date.now();
      const retryTime = new Date(this.nextRetryAt).getTime();
      const secondsRemaining = Math.ceil((retryTime - now) / 1000);
      md.appendMarkdown(`🔄 **Retrying**: Next attempt in ${secondsRemaining}s\n\n`);
    }

    // Show retry counts if available
    if (this.autoRetryCount > 0 || this.manualRetryCount > 0) {
      md.appendMarkdown(`**Retry History:**\n\n`);
      if (this.autoRetryCount > 0) {
        md.appendMarkdown(`- Automatic retries: ${this.autoRetryCount}\n`);
      }
      if (this.manualRetryCount > 0) {
        md.appendMarkdown(`- Manual retries: ${this.manualRetryCount}\n`);
      }
      md.appendMarkdown(`\n`);
    }

    // Legacy retry count display (for backward compatibility)
    if (this.retryCount !== null && this.retryCount > 0 && this.manualRetryCount === 0) {
      const remaining = StageTreeItem.MAX_RETRIES - this.retryCount;
      md.appendMarkdown(`Retry: ${this.retryCount} of ${StageTreeItem.MAX_RETRIES}`);
      if (remaining > 0) {
        md.appendMarkdown(` (${remaining} remaining)\n\n`);
      } else {
        md.appendMarkdown(` ⚠️ **Max retries reached**\n\n`);
      }
    }

    // Token usage section - varies by execution mode
    if (this.executionMode === "interactive") {
      md.appendMarkdown(`---\n\n`);
      md.appendMarkdown(`**Token Usage:** N/A\n\n`);
      md.appendMarkdown(
        `_Interactive mode uses raw text output, so token tracking is unavailable._\n`
      );
    } else if (this.tokenInfo) {
      md.appendMarkdown(`---\n\n`);
      md.appendMarkdown(`**Token Usage:**\n\n`);
      md.appendMarkdown(`- Input: ${this.tokenInfo.inputTokens.toLocaleString()}\n`);
      md.appendMarkdown(`- Output: ${this.tokenInfo.outputTokens.toLocaleString()}\n`);
      md.appendMarkdown(`- Cost: $${this.tokenInfo.costUsd.toFixed(4)}\n`);
    }

    if (this.durationMs) {
      md.appendMarkdown(`\nDuration: ${(this.durationMs / 1000).toFixed(1)}s\n`);
    }

    if (this.errorMessage) {
      md.appendMarkdown(`\n---\n\n`);
      md.appendMarkdown(`**Error:**\n\n`);
      md.appendCodeblock(this.errorMessage, "text");
    }

    return md;
  }

  /**
   * Create accessibility label for screen readers (Issue #304)
   *
   * Format: "Stage: Feature Planning. Status: pending. Press Enter to run."
   */
  private createAccessibilityLabel(): string {
    const parts: string[] = [`Stage: ${STAGE_LABELS[this.stage]}.`, `Status: ${this.status}.`];

    // Add keyboard hint based on status
    if (this.status === "pending" || this.status === "failed") {
      parts.push("Press Enter to run.");
    } else if (this.status === "complete") {
      parts.push("Press Enter to view details.");
    }

    return parts.join(" ");
  }

  /**
   * Update the stage status
   */
  setStatus(status: StageStatus): void {
    this.status = status;
    this.updateDisplay();
  }

  /**
   * Get the current status
   */
  getStatus(): StageStatus {
    return this.status;
  }

  /**
   * Set token usage information
   */
  setTokenUsage(tokenInfo: StageTokenInfo): void {
    this.tokenInfo = tokenInfo;
    this.updateDisplay();
  }

  /**
   * Set the duration in milliseconds
   */
  setDuration(durationMs: number): void {
    this.durationMs = durationMs;
    this.updateDisplay();
  }

  /**
   * Set an error message (for failed stages)
   */
  setError(errorMessage: string): void {
    this.errorMessage = errorMessage;
    this.updateDisplay();
  }

  /**
   * Clear error message
   */
  clearError(): void {
    this.errorMessage = null;
    this.updateDisplay();
  }

  /**
   * Set the retry count for circuit breaker display
   */
  setRetryCount(count: number): void {
    this.retryCount = count;
    this.updateDisplay();
  }

  /**
   * Set retry state information
   *
   * @param isRetrying - Whether the stage is currently retrying
   * @param nextRetryAt - ISO timestamp of next retry attempt
   */
  setRetryState(isRetrying: boolean, nextRetryAt?: string): void {
    this.isRetrying = isRetrying;
    this.nextRetryAt = nextRetryAt ?? null;
    this.updateDisplay();
  }

  /**
   * Set automatic retry count
   */
  setAutoRetryCount(count: number): void {
    this.autoRetryCount = count;
    this.updateDisplay();
  }

  /**
   * Set manual retry count
   */
  setManualRetryCount(count: number): void {
    this.manualRetryCount = count;
    this.updateDisplay();
  }

  /**
   * Set execution mode for this stage
   *
   * When mode is 'interactive', token usage is shown as "N/A" in both
   * the description and tooltip, since stream-json output is unavailable.
   *
   * @param mode - The execution mode ('headless' or 'interactive')
   * @see Issue #498 - Token tracking for interactive execution mode
   */
  setExecutionMode(mode: StageExecutionMode | null): void {
    this.executionMode = mode;
    this.updateDisplay();
  }

  /**
   * Get the current execution mode
   */
  getExecutionMode(): StageExecutionMode | null {
    return this.executionMode;
  }

  /**
   * Get the current retry count
   */
  getRetryCount(): number | null {
    return this.retryCount;
  }

  /**
   * Check if this stage is retryable
   *
   * A stage is retryable when:
   * - Status is 'failed', OR
   * - Status is 'running' but pipeline is not actively running (stuck/aborted)
   *
   * And retry count has not exceeded MAX_RETRIES.
   *
   * @param isPipelineRunning - Whether the pipeline is currently actively running
   * @returns True if the stage can be retried
   */
  isRetryable(isPipelineRunning: boolean = false): boolean {
    // Check retry count limit
    if (this.retryCount !== null && this.retryCount >= StageTreeItem.MAX_RETRIES) {
      return false;
    }

    // Failed stages are always retryable (if under retry limit)
    if (this.status === "failed") {
      return true;
    }

    // Running stages are retryable only if pipeline is not actively running
    // (indicates stuck/aborted state)
    if (this.status === "running" && !isPipelineRunning) {
      return true;
    }

    return false;
  }

  /**
   * Reset the stage to pending state
   */
  reset(): void {
    this.status = "pending";
    this.tokenInfo = null;
    this.durationMs = null;
    this.errorMessage = null;
    this.executionMode = null;
    this.clearPhases();
  }

  /**
   * Set phase children from persisted state.json phases array.
   *
   * Creates PhaseTreeItem children for each phase and updates the
   * collapsible state and description accordingly.
   *
   * @param phases - Phase data from state.json
   * @param currentPhase - Name of the currently running phase
   * @param totalPhases - Total phase count for the stage (from phase marker).
   *   When provided, the description shows this total instead of phases.length,
   *   giving an accurate count before all phases have emitted markers.
   */
  /**
   * Phases this stage was OBSERVED to run to completion.
   *
   * This is the numerator, and it holds exactly one status. Every other
   * status is a different claim:
   *
   * | status       | claim                                    | counts? |
   * | ------------ | ---------------------------------------- | ------- |
   * | `complete`   | it ran, and we saw it finish             | yes     |
   * | `failed`     | it ran and did not finish                | no      |
   * | `abandoned`  | it started; the stage moved past it      | no      |
   * | `skipped`    | the stage decided not to run it          | no — it leaves the denominator instead |
   * | `unreported` | nothing was ever said about it           | no      |
   * | `pending`    | it has not started                       | no      |
   *
   * #1246 removed `unreported` from this count and left `skipped` in, on the
   * reasoning that a deliberate skip is settled work. That was true of the
   * world it was written for, where skips were occasional. #1534 changed that
   * world: a deterministic stage path legitimately skips MOST of the registry
   * (`issue-pickup` skips 11 of 14 — "this path has no LLM, so there is no
   * self-assessment"), and counting those as progress made a stage that
   * observed NOTHING read as 11/14 — 79% done. A run that skips more looked
   * like a run that did more.
   *
   * So a skip no longer raises the numerator; it lowers the denominator, via
   * `applicablePhaseCount`. That is the same judgement #1246 made about
   * `unreported`, applied to the other status that also is not evidence of
   * work — and it is the version that cannot be resurrected by a future path
   * that skips legitimately (#1558).
   */
  private countObservedPhases(): number {
    return this.phaseItems().filter((c) => c.getStatus() === "complete").length;
  }

  /**
   * Phases this run could actually perform: the registry total minus the ones
   * the stage said it would not do. The denominator answers "of the work this
   * run intends", never "of the catalogue".
   */
  private applicablePhaseCount(): number {
    return Math.max(0, this.totalPhaseCount - this.countPhasesWithStatus("skipped"));
  }

  /**
   * The stage's phase rows, flat.
   *
   * Read through this rather than `this.children` so that grouping rows for
   * display (see `setPhases`) can never silently change a count. Every
   * counting bug in this file's history has been a display change moving a
   * row out from under a filter.
   */
  private phaseItems(): PhaseTreeItem[] {
    return this.phaseRows;
  }

  private countPhasesWithStatus(status: PhaseStatus): number {
    return this.phaseItems().filter((c) => c.getStatus() === status).length;
  }

  setPhases(phases: StagePhase[], currentPhase?: string, totalPhases?: number): void {
    this.clearChildren();
    this.totalPhaseCount = totalPhases ?? phases.length;
    this.currentPhaseName = currentPhase ?? null;

    // The flat record is built FIRST and is what every count reads, so the
    // grouping below is purely a display arrangement and cannot move a row out
    // from under a filter — which is how this file's counting has broken
    // before (#1558).
    this.phaseRows = phases.map(
      (phase) =>
        new PhaseTreeItem(phase.name, phase.status as PhaseStatus, this.stage, phase.reason)
    );

    this.rebuildPhaseChildren();
    this.updateDisplay();
  }

  /**
   * Arrange `phaseRows` as tree children. Skips collapse into one row;
   * everything else keeps its place and its registry order. Below a small
   * threshold the group is more indirection than it saves, so the rows stay
   * inline. This is display only — counts read `phaseRows`.
   */
  private rebuildPhaseChildren(): void {
    this.clearChildren();
    const skipped = this.phaseRows.filter((p) => p.getStatus() === "skipped");
    const groupSkips = skipped.length >= SKIPPED_PHASE_GROUP_THRESHOLD;

    for (const item of this.phaseRows) {
      if (groupSkips && item.getStatus() === "skipped") {
        continue;
      }
      this.addChild(item);
    }
    if (groupSkips) {
      this.addChild(new SkippedPhasesTreeItem(skipped));
    }
  }

  /**
   * The phase name to show beside a running stage's progress.
   *
   * When the last phase the stage named has already completed we are BETWEEN
   * phases — the next one has not emitted its marker — so the registry's next
   * entry is shown instead of the one that just finished. Returns "" when
   * there is nothing to name, which is not the same as there being no
   * progress: a stage can have observed phases and no current one (#1558).
   */
  private runningPhaseLabel(registryPhases: readonly { name: string }[] | undefined): string {
    if (!this.currentPhaseName) {
      return "";
    }
    const current = this.phaseItems().find((c) => c.phaseName === this.currentPhaseName);
    if (current && current.getStatus() === "running") {
      return phaseNameToLabel(this.currentPhaseName);
    }
    const currentIndex = registryPhases?.findIndex((p) => p.name === this.currentPhaseName) ?? -1;
    const nextPhase = currentIndex >= 0 ? registryPhases?.[currentIndex + 1] : undefined;
    return nextPhase ? phaseNameToLabel(nextPhase.name) : "";
  }

  /**
   * The tree parent of one of this stage's phase rows, or undefined if it is
   * not ours.
   *
   * Grouping skips (see `rebuildPhaseChildren`) means a phase row is not
   * always a DIRECT child, so `getChildren().includes(phase)` is no longer the
   * whole answer — and a parent lookup that silently returns undefined breaks
   * `TreeView.reveal` for exactly the rows the group hides. Asking the stage
   * keeps that knowledge with the arrangement that created it (#1558).
   */
  parentOfPhase(phase: PhaseTreeItem): BaseTreeItem | undefined {
    for (const child of this.children) {
      if (child === phase) {
        return this;
      }
      if (child instanceof SkippedPhasesTreeItem && child.getChildren().includes(phase)) {
        return child;
      }
    }
    return undefined;
  }

  /**
   * True when this stage owns the given skipped-phase group row.
   */
  ownsPhaseGroup(group: BaseTreeItem): boolean {
    return this.children.includes(group);
  }

  /**
   * Apply ONE observed phase event, leaving every other row alone.
   *
   * This replaces `buildSyntheticPhases`, which rebuilt the whole array on
   * every event and marked every phase before the current index `complete` —
   * fabricating evidence for phases nobody reported, and discarding the real
   * `skipped` / `unreported` records already on the rows. On a stage that
   * emits its markers out of order, or emits only one, that invented a
   * finished prefix out of nothing.
   *
   * Unreported phases stay `pending` while the stage runs and are back-filled
   * to `unreported` when it ends (PipelineTreeProvider's complete/failed
   * path), so silence is never promoted to completion at any point (#1558).
   */
  applyPhaseEvent(
    phaseName: string,
    status: PhaseStatus,
    totalPhases: number,
    registryPhases: readonly { name: string }[]
  ): void {
    if (this.phaseRows.length === 0 && registryPhases.length > 0) {
      this.phaseRows = registryPhases.map((r) => new PhaseTreeItem(r.name, "pending", this.stage));
    }
    this.totalPhaseCount = totalPhases > 0 ? totalPhases : this.totalPhaseCount;

    const existing = this.phaseRows.find((r) => r.phaseName === phaseName);
    if (existing) {
      existing.setStatus(status);
    } else {
      // A marker the registry does not define. Keep it rather than drop it —
      // it is real evidence — and let it sort after the known rows.
      this.phaseRows.push(new PhaseTreeItem(phaseName, status, this.stage));
      if (this.phaseRows.length > this.totalPhaseCount) {
        this.totalPhaseCount = this.phaseRows.length;
      }
    }
    this.currentPhaseName = status === "running" ? phaseName : null;
    this.rebuildPhaseChildren();
    this.updateDisplay();
  }

  /**
   * Clear all phase children and reset phase tracking.
   */
  clearPhases(): void {
    this.clearChildren();
    this.phaseRows = [];
    this.currentPhaseName = null;
    this.totalPhaseCount = 0;
    this.updateDisplay();
  }

  /**
   * Get the number of phase children.
   */
  getPhaseCount(): number {
    return this.totalPhaseCount;
  }

  /**
   * Get token info if available
   */
  getTokenInfo(): StageTokenInfo | null {
    return this.tokenInfo ? { ...this.tokenInfo } : null;
  }
}
