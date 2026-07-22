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
 * Model selection metadata for a stage
 */
export interface StageModelInfo {
  model: string;
  source: string;
  confidence?: number;
  complexity?: string;
  mode?: string;
  effort?: string;
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
   * Model selection metadata for this stage
   */
  private modelInfo: StageModelInfo | null = null;

  /**
   * Current phase name (for description display when running)
   */
  private currentPhaseName: string | null = null;

  /**
   * Total phases in this stage (for progress count display)
   */
  private totalPhaseCount: number = 0;

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
      if (this.currentPhaseName && this.totalPhaseCount > 0) {
        const rawCount = this.children.filter(
          (c) =>
            c instanceof PhaseTreeItem &&
            (c.getStatus() === "complete" || c.getStatus() === "skipped")
        ).length;
        // Clamp: completed count must never exceed total (defensive guard
        // against skill/registry phase count mismatches)
        const completedCount = Math.min(rawCount, this.totalPhaseCount);

        // If the last-started phase is already complete, we're between phases —
        // the next phase hasn't emitted its marker yet. Show the upcoming phase
        // name from the registry so the user sees what's coming, not what just
        // finished.
        const currentPhaseItem = this.children.find(
          (c) => c instanceof PhaseTreeItem && c.phaseName === this.currentPhaseName
        ) as PhaseTreeItem | undefined;
        if (!currentPhaseItem || currentPhaseItem.getStatus() !== "running") {
          const currentIndex =
            registryPhases?.findIndex((p) => p.name === this.currentPhaseName) ?? -1;
          const nextPhase = registryPhases?.[currentIndex + 1];
          const nextLabel = nextPhase ? phaseNameToLabel(nextPhase.name) : "running...";
          return `${nextLabel} [${completedCount}/${this.totalPhaseCount}]`;
        }

        const phaseLabel = phaseNameToLabel(this.currentPhaseName);
        return `${phaseLabel} [${completedCount}/${this.totalPhaseCount}]`;
      }
      if (registryPhases && registryPhases.length > 0) {
        return `${phaseNameToLabel(registryPhases[0].name)} [0/${registryPhases.length}]`;
      }
      return "running...";
    }

    if (this.status === "pending") {
      return "pending";
    }

    if (this.status === "skipped") {
      return "skipped";
    }

    // Completed/failed stages with phases show compact summary
    if ((this.status === "complete" || this.status === "failed") && this.totalPhaseCount > 0) {
      const rawCount = this.children.filter(
        (c) =>
          c instanceof PhaseTreeItem &&
          (c.getStatus() === "complete" || c.getStatus() === "skipped")
      ).length;
      // Clamp: completed count must never exceed total
      const completedCount = Math.min(rawCount, this.totalPhaseCount);
      const phaseSummary = `${completedCount}/${this.totalPhaseCount} phases`;

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

    // Model selection section - shows execution path details
    if (this.modelInfo) {
      md.appendMarkdown(`**Execution Path:**\n\n`);
      md.appendMarkdown(`- Model: ${this.modelInfo.model}\n`);
      md.appendMarkdown(`- Source: ${this.modelInfo.source}\n`);
      if (this.modelInfo.effort) {
        md.appendMarkdown(`- Effort: ${this.modelInfo.effort}\n`);
      }
      if (this.modelInfo.complexity) {
        md.appendMarkdown(`- Complexity: ${this.modelInfo.complexity}\n`);
      }
      if (this.modelInfo.mode) {
        md.appendMarkdown(`- Selection mode: ${this.modelInfo.mode}\n`);
      }
      if (this.modelInfo.confidence !== undefined) {
        md.appendMarkdown(`- Confidence: ${(this.modelInfo.confidence * 100).toFixed(0)}%\n`);
      }
      md.appendMarkdown(`\n`);
    }

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
   * Set model selection metadata for this stage
   */
  setModelInfo(info: StageModelInfo | null): void {
    this.modelInfo = info;
    this.updateDisplay();
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
    this.modelInfo = null;
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
  setPhases(phases: StagePhase[], currentPhase?: string, totalPhases?: number): void {
    this.clearChildren();
    this.totalPhaseCount = totalPhases ?? phases.length;
    this.currentPhaseName = currentPhase ?? null;

    for (const phase of phases) {
      const item = new PhaseTreeItem(phase.name, phase.status as PhaseStatus);
      this.addChild(item);
    }

    this.updateDisplay();
  }

  /**
   * Clear all phase children and reset phase tracking.
   */
  clearPhases(): void {
    this.clearChildren();
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
