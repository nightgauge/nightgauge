/**
 * Status bar management for Nightgauge Pipeline extension
 *
 * Provides visual feedback for pipeline state in the VS Code status bar.
 */

import * as vscode from "vscode";
import type { PipelineStage } from "@nightgauge/sdk";
import type { StageExecutionMode } from "./incrediConfig";
import { DEFAULT_PERFORMANCE_MODE, MODE_PROFILES, type PerformanceMode } from "./modeProfiles";

/**
 * Pipeline state for status bar display
 */
export type PipelineState = "idle" | "running" | "paused" | "complete" | "error";

/**
 * Status bar color configuration
 */
const STATUS_COLORS = {
  idle: undefined, // Default color
  running: new vscode.ThemeColor("statusBarItem.warningBackground"),
  paused: new vscode.ThemeColor("statusBarItem.warningBackground"),
  complete: new vscode.ThemeColor("statusBarItem.prominentBackground"),
  error: new vscode.ThemeColor("statusBarItem.errorBackground"),
};

/**
 * Human-readable stage names
 * Includes bookend stages (pipeline-start, pipeline-finish)
 */
const STAGE_NAMES: Record<PipelineStage, string> = {
  "pipeline-start": "Initialize",
  "issue-pickup": "Issue Pickup",
  "feature-planning": "Planning",
  "feature-dev": "Development",
  "feature-validate": "Validation",
  "pr-create": "PR Creation",
  "pr-merge": "PR Merge",
  "pipeline-finish": "Completion",
};

/**
 * StatusBarManager class for visual pipeline state
 *
 * Manages VS Code status bar items that show the current
 * pipeline state and target branch with appropriate icons and colors.
 *
 * @example
 * ```typescript
 * const statusBar = new StatusBarManager();
 * statusBar.showRunning('feature-dev');
 * statusBar.setTargetBranch('develop');
 * // Later...
 * statusBar.showComplete('feature-dev');
 * ```
 */
export class StatusBarManager {
  readonly item: vscode.StatusBarItem;
  readonly targetBranchItem: vscode.StatusBarItem;
  /** Usage tracking item — shown only when a monthly budget is configured (Issue #1333) */
  readonly usageItem: vscode.StatusBarItem;
  /** Dedicated always-visible performance-mode selector — big clickable footer button (Issue #3009) */
  readonly modeItem: vscode.StatusBarItem;
  /** GitHub GraphQL rate-limit counter — real-time quota visibility */
  readonly rateLimitItem: vscode.StatusBarItem;
  private state: PipelineState = "idle";
  private currentStage: PipelineStage | null = null;
  private currentTargetBranch: string | null = null;
  /** Current execution mode for stage runs (Issue #499) */
  private currentExecutionMode: StageExecutionMode | null = null;
  /** Active user model override for the current run (Issue #1610) */
  private modelOverrideLabel: string | null = null;
  /** Currently active performance mode — Efficiency / Elevated / Maximum (Issue #3009) */
  private performanceMode: PerformanceMode = DEFAULT_PERFORMANCE_MODE;
  /**
   * True when custom per-stage model pins are active (Issue #20). When set, the
   * mode item shows "Custom" instead of the preset label, because per-stage
   * pins shadow the preset's routing.
   */
  private customOverridesActive = false;
  /** Current token source for debugging display (Issue #2670) */
  private tokenSourceLabel: string | null = null;

  constructor() {
    // Main pipeline status item (leftmost)
    this.item = vscode.window.createStatusBarItem(
      vscode.StatusBarAlignment.Left,
      100 // Priority - higher number = more to the left
    );
    this.item.command = "nightgauge.pickupIssue";
    this.showIdle();
    this.item.show();

    // Target branch item (next to main status)
    this.targetBranchItem = vscode.window.createStatusBarItem(
      vscode.StatusBarAlignment.Left,
      99 // Just to the right of main status
    );
    this.targetBranchItem.command = "nightgauge.selectTargetBranch";
    this.hideTargetBranch();

    // Usage tracking item (rightmost of the three, hidden until budget configured)
    this.usageItem = vscode.window.createStatusBarItem(
      vscode.StatusBarAlignment.Left,
      98 // Just to the right of target branch
    );
    this.usageItem.command = "nightgauge.showDashboard";
    // Hidden by default until a budget is configured

    // Dedicated performance-mode selector — always visible, grouped with the
    // other Nightgauge status bar items on the left. Single click opens
    // a QuickPick with the three modes. Kept separate from the main pipeline
    // item so it is always reachable regardless of pipeline state.
    // Priority 97 → sits immediately to the right of usageItem (98), keeping
    // the Nightgauge cluster in one logical block.
    this.modeItem = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Left, 97);
    this.modeItem.command = "nightgauge.selectPerformanceMode";
    this.renderModeItem();
    this.modeItem.show();

    // GitHub GraphQL rate-limit counter — priority 96, rightmost in the cluster.
    // Hidden until first rate-limit state is received from ProjectBoardService.
    this.rateLimitItem = vscode.window.createStatusBarItem(vscode.StatusBarAlignment.Left, 96);
    this.rateLimitItem.command = "nightgauge.showDashboard";
  }

  /**
   * Render the dedicated performance-mode item to reflect the current mode.
   *
   * - efficiency: muted label, no background tint.
   * - elevated:   neutral default label.
   * - maximum:    warning-colored background (cost ceiling lifted).
   */
  private renderModeItem(): void {
    if (this.customOverridesActive) {
      this.modeItem.text = "$(zap) Mode: Custom";
      this.modeItem.tooltip = [
        "Performance mode: Custom (per-stage models)",
        "Explicit model pins are active for one or more stages;",
        "unpinned stages defer to the adaptive router.",
        "",
        "Click to change models or pick a preset.",
      ].join("\n");
      this.modeItem.backgroundColor = undefined;
      return;
    }
    const profile = MODE_PROFILES[this.performanceMode];
    this.modeItem.text = `$(zap) Mode: ${profile.label}`;
    const tooltipLines = [
      `Performance mode: ${profile.label}`,
      profile.description,
      `Cost: ${profile.costHint}`,
      "",
      "Click to switch modes.",
    ];
    this.modeItem.tooltip = tooltipLines.join("\n");
    this.modeItem.backgroundColor =
      this.performanceMode === "maximum"
        ? new vscode.ThemeColor("statusBarItem.warningBackground")
        : undefined;
  }

  /**
   * Show idle state - ready to run pipeline
   */
  showIdle(): void {
    this.state = "idle";
    this.currentStage = null;
    this.modelOverrideLabel = null;
    this.tokenSourceLabel = null;
    // Show a compact mode glyph in the main item only when the mode departs
    // from the Elevated default — the dedicated "Mode: <label>" item sits
    // immediately to the right, so a bolt alone avoids redundant text.
    const showBadge = this.customOverridesActive || this.performanceMode !== "elevated";
    const profile = MODE_PROFILES[this.performanceMode];
    const modeBadge = showBadge ? " ⚡" : "";
    this.item.text = `$(nightgauge) Nightgauge${modeBadge}`;
    this.item.tooltip = this.customOverridesActive
      ? "Nightgauge — Mode: Custom (per-stage models) — Click to open Dashboard"
      : showBadge
        ? `Nightgauge — Mode: ${profile.label} (${profile.costHint}) — Click to open Dashboard`
        : "Nightgauge — Click to open Dashboard";
    this.item.backgroundColor = STATUS_COLORS.idle;
    this.item.command = "nightgauge.showDashboard";
    vscode.commands.executeCommand("setContext", "nightgauge.pipelineRunning", false);
    vscode.commands.executeCommand("setContext", "nightgauge.concurrentSlotsActive", false);
    vscode.commands.executeCommand("setContext", "nightgauge.hasRunningEpics", false);
  }

  /**
   * Show running state with current stage
   *
   * @param stage - The pipeline stage currently running
   * @param mode - Optional execution mode ('headless' or 'interactive')
   *               When 'interactive', shows [interactive] suffix.
   *               When 'headless' or undefined, no suffix is shown.
   * @param modelInfo - Optional model info for tooltip (Issue #732)
   *
   * @see Issue #499 - Mode selection UX
   * @see Issue #732 - AutoModelSelector integration
   */
  showRunning(
    stage: PipelineStage,
    mode?: StageExecutionMode,
    modelInfo?: { model: string; source: string; complexity?: string }
  ): void {
    this.state = "running";
    this.currentStage = stage;
    this.currentExecutionMode = mode ?? null;
    const stageName = STAGE_NAMES[stage] || stage;

    // Only show [interactive] suffix for interactive mode
    // Headless is the default, so no suffix needed
    const modeSuffix = mode === "interactive" ? " [interactive]" : "";
    // Show model override badge when user selected a specific model (#1610)
    const overrideSuffix = this.modelOverrideLabel ? ` [${this.modelOverrideLabel}]` : "";
    // Show a compact mode glyph during stage execution when the mode departs
    // from the Elevated default — Issues #2433 (supercharge), #3009 (mode).
    // The dedicated "Mode: <label>" item already names the mode, so a bare
    // bolt avoids duplicating the label here.
    const modeSuffixForRunning = this.performanceMode !== "elevated" ? " ⚡" : "";

    this.item.text = `$(sync~spin) ${stageName}${modeSuffix}${overrideSuffix}${modeSuffixForRunning}`;

    // Build tooltip with model info when available (Issue #732)
    const tokenSuffix = this.tokenSourceLabel ? ` | Token: ${this.tokenSourceLabel}` : "";
    let tooltip: string;
    if (mode === "interactive") {
      tooltip = `Pipeline running: ${stageName} (interactive mode - no token tracking)${tokenSuffix}`;
    } else if (modelInfo) {
      const complexitySuffix = modelInfo.complexity ? ` ${modelInfo.complexity} complexity` : "";
      tooltip = `Stage: ${stageName} | Model: ${modelInfo.model} (${modelInfo.source}${complexitySuffix})${tokenSuffix}`;
    } else {
      tooltip = `Pipeline running: ${stageName}${tokenSuffix}`;
    }
    this.item.tooltip = tooltip;

    this.item.backgroundColor = STATUS_COLORS.running;
    this.item.command = "nightgauge.stopPipeline";
    vscode.commands.executeCommand("setContext", "nightgauge.pipelineRunning", true);
  }

  /**
   * Get the current execution mode
   */
  getCurrentExecutionMode(): StageExecutionMode | null {
    return this.currentExecutionMode;
  }

  /**
   * Set or clear the model override label shown in the status bar (Issue #1610).
   * Call with a model name (e.g., 'Opus') before the pipeline starts,
   * cleared automatically by showIdle().
   */
  setModelOverrideLabel(label: string | null): void {
    this.modelOverrideLabel = label;
  }

  /**
   * Set the active performance mode, refreshing the status bar items
   * (Issue #3009 — replaces `setSuperchargeActive`).
   *
   * @param mode - The performance mode to display
   */
  setPerformanceMode(mode: PerformanceMode): void {
    this.performanceMode = mode;
    if (this.state === "idle") {
      this.showIdle();
    }
    this.renderModeItem();
    vscode.commands.executeCommand("setContext", "nightgauge.performanceMode", mode);
    // Additively keep the legacy context key for one release so existing
    // package.json `when:` clauses tied to supercharge still resolve correctly.
    vscode.commands.executeCommand(
      "setContext",
      "nightgauge.superchargeModeActive",
      mode === "maximum"
    );
  }

  /**
   * Get the currently active performance mode (Issue #3009).
   */
  getPerformanceMode(): PerformanceMode {
    return this.performanceMode;
  }

  /**
   * Reflect whether custom per-stage model pins are active (Issue #20). When
   * true, the mode item renders "Custom" regardless of the preset mode, since
   * per-stage pins shadow the preset's routing.
   */
  setCustomOverridesActive(active: boolean): void {
    this.customOverridesActive = active;
    if (this.state === "idle") {
      this.showIdle();
    }
    this.renderModeItem();
    vscode.commands.executeCommand("setContext", "nightgauge.customStageModels", active);
  }

  /**
   * @deprecated Issue #3009 — use `setPerformanceMode("maximum"|"elevated")`.
   * Maps `active=true` → `maximum`, `active=false` → `elevated`.
   */
  setSuperchargeActive(active: boolean): void {
    this.setPerformanceMode(active ? "maximum" : "elevated");
  }

  /**
   * Show concurrent pipeline running state
   *
   * Displays the number of active/total slots and optionally the issue numbers.
   *
   * @param activeSlots - Number of currently active pipeline slots
   * @param totalSlots - Maximum concurrent slots configured
   * @param issueNumbers - Optional list of issue numbers being processed
   *
   * @see Issue #1621 - Git worktree-based concurrent pipeline execution
   */
  showConcurrentRunning(activeSlots: number, totalSlots: number, issueNumbers?: number[]): void {
    this.state = "running";
    this.currentStage = null;

    const issueList = issueNumbers?.map((n) => `#${n}`).join(", ") ?? "";
    this.item.text = `$(sync~spin) Pipelines: ${activeSlots}/${totalSlots}`;
    this.item.tooltip = issueList
      ? `Concurrent pipelines: ${issueList}\nClick for pipeline controls`
      : `${activeSlots} of ${totalSlots} pipeline slots active`;
    this.item.backgroundColor = STATUS_COLORS.running;
    this.item.command = "nightgauge.showPipelineQuickActions";
    vscode.commands.executeCommand("setContext", "nightgauge.pipelineRunning", true);
    vscode.commands.executeCommand(
      "setContext",
      "nightgauge.concurrentSlotsActive",
      activeSlots > 0
    );
    // Reset stop-after-current flag so the button re-appears on each new batch run.
    if (activeSlots > 0) {
      vscode.commands.executeCommand("setContext", "nightgauge.stopAfterCurrentBatch", false);
    }
  }

  /**
   * Show stopping after current issue state
   *
   * Displays a message indicating the batch will stop after the specified issue completes.
   * Uses pause icon to differentiate from full stop or error states.
   *
   * @param issueNumber - The issue number that will complete before stopping
   */
  showStoppingAfterCurrent(issueNumber: number): void {
    this.state = "running";
    this.currentStage = null;

    const displayText = `⏸ Batch will stop after issue #${issueNumber} completes`;

    this.item.text = `$(debug-pause) Stopping after #${issueNumber}`;
    this.item.tooltip = displayText;
    this.item.backgroundColor = STATUS_COLORS.paused;
    this.item.command = "nightgauge.stopPipeline";

    // Context key update to disable the stop-after-current button
    vscode.commands.executeCommand("setContext", "nightgauge.stopAfterCurrentBatch", true);

    vscode.commands.executeCommand("setContext", "nightgauge.pipelineRunning", true);
  }

  /**
   * Show stopping after current issue state (queue mode)
   *
   * Similar to showStoppingAfterCurrent but for queue auto-start path.
   *
   * @param issueNumber - The issue number that will complete before stopping
   */
  showStoppingQueueAfterCurrent(issueNumber: number): void {
    this.state = "running";
    this.currentStage = null;

    this.item.text = `$(debug-pause) Stopping after #${issueNumber}`;
    this.item.tooltip = `⏸ Queue will stop after issue #${issueNumber} completes`;
    this.item.backgroundColor = STATUS_COLORS.paused;
    this.item.command = "nightgauge.stopPipeline";

    vscode.commands.executeCommand("setContext", "nightgauge.stopAfterCurrentQueue", true);

    vscode.commands.executeCommand("setContext", "nightgauge.pipelineRunning", true);
  }

  /**
   * Show complete state
   */
  showComplete(stage?: PipelineStage): void {
    this.state = "complete";
    this.currentStage = stage ?? null;
    const stageName = stage ? STAGE_NAMES[stage] : "Pipeline";
    this.item.text = `$(check) ${stageName}`;
    this.item.tooltip = `${stageName} complete`;
    this.item.backgroundColor = STATUS_COLORS.complete;
    this.item.command = "nightgauge.showDashboard";
    vscode.commands.executeCommand("setContext", "nightgauge.pipelineRunning", false);

    // Auto-reset to idle after 5 seconds
    setTimeout(() => {
      if (this.state === "complete") {
        this.showIdle();
      }
    }, 5000);
  }

  /**
   * Show error state
   */
  showError(message: string): void {
    this.state = "error";
    this.item.text = "$(error) Error";
    this.item.tooltip = message;
    this.item.backgroundColor = STATUS_COLORS.error;
    this.item.command = "nightgauge.showDashboard";
    vscode.commands.executeCommand("setContext", "nightgauge.pipelineRunning", false);
  }

  /**
   * Show approval needed state
   */
  showApprovalNeeded(stage: PipelineStage): void {
    this.state = "running";
    this.currentStage = stage;
    const stageName = STAGE_NAMES[stage] || stage;
    this.item.text = `$(bell) ${stageName} - Approval Needed`;
    this.item.tooltip = `Click to approve ${stageName}`;
    this.item.backgroundColor = STATUS_COLORS.running;
    // Command would open approval dialog
  }

  /**
   * Show paused state
   *
   * Displays a pause icon and indicates which stage was last completed.
   * Clicking the status bar executes the resume command.
   *
   * @param lastStage - The last completed stage (optional, for context)
   * @see Issue #239 - Pipeline pause/resume with cross-session recovery
   */
  showPaused(lastStage?: PipelineStage | string): void {
    this.state = "paused";
    this.currentStage = null;

    const stageName = lastStage ? STAGE_NAMES[lastStage as PipelineStage] || lastStage : null;

    this.item.text = stageName
      ? `$(debug-pause) Paused after ${stageName}`
      : "$(debug-pause) Paused";
    this.item.tooltip = "Pipeline paused. Click to resume.";
    this.item.backgroundColor = STATUS_COLORS.paused;
    this.item.command = "nightgauge.resumePipeline";

    vscode.commands.executeCommand("setContext", "nightgauge.pipelineRunning", false);
    vscode.commands.executeCommand("setContext", "nightgauge.pipelinePaused", true);
  }

  /**
   * Get current state
   */
  getState(): PipelineState {
    return this.state;
  }

  /**
   * Get current stage (if running)
   */
  getCurrentStage(): PipelineStage | null {
    return this.currentStage;
  }

  /**
   * Set and display the target branch in status bar
   *
   * @param branch - Target branch name (e.g., 'main', 'develop')
   */
  setTargetBranch(branch: string): void {
    this.currentTargetBranch = branch;
    this.targetBranchItem.text = `$(git-branch) → ${branch}`;
    this.targetBranchItem.tooltip = `Target branch: ${branch}\nClick to change`;

    // Highlight non-default branches
    if (branch !== "main" && branch !== "master") {
      this.targetBranchItem.backgroundColor = new vscode.ThemeColor(
        "statusBarItem.warningBackground"
      );
    } else {
      this.targetBranchItem.backgroundColor = undefined;
    }

    this.targetBranchItem.show();
  }

  /**
   * Hide the target branch status bar item
   */
  hideTargetBranch(): void {
    this.currentTargetBranch = null;
    this.targetBranchItem.hide();
  }

  /**
   * Get the currently displayed target branch
   */
  getTargetBranch(): string | null {
    return this.currentTargetBranch;
  }

  /**
   * Show live usage in the status bar (Issue #1333)
   *
   * Displayed as: $(flame) $X.XX / $Y
   * Color-coded by usage percentage:
   * - < 80%: default
   * - ≥ 80%: warningBackground
   * - ≥ 90%: errorBackground
   *
   * @param costUsd - Current accumulated cost since last reset
   * @param budgetUsd - Configured monthly budget
   */
  showUsage(costUsd: number, budgetUsd: number): void {
    const usagePct = budgetUsd > 0 ? (costUsd / budgetUsd) * 100 : 0;
    const remaining = budgetUsd - costUsd;

    this.usageItem.text = `$(flame) $${costUsd.toFixed(2)} / $${budgetUsd.toFixed(0)}`;
    this.usageItem.tooltip = [
      `Usage: ${Math.round(usagePct)}% of monthly budget`,
      `Cost: $${costUsd.toFixed(2)} / $${budgetUsd.toFixed(2)}`,
      `Remaining: $${remaining.toFixed(2)}`,
      `Click to open Dashboard`,
    ].join("\n");

    if (usagePct >= 90) {
      this.usageItem.backgroundColor = new vscode.ThemeColor("statusBarItem.errorBackground");
    } else if (usagePct >= 80) {
      this.usageItem.backgroundColor = new vscode.ThemeColor("statusBarItem.warningBackground");
    } else {
      this.usageItem.backgroundColor = undefined;
    }

    this.usageItem.show();
  }

  /**
   * Hide the usage status bar item (Issue #1333)
   *
   * Called when budget is disabled or usage counter is reset.
   */
  hideUsage(): void {
    this.usageItem.hide();
    this.usageItem.backgroundColor = undefined;
  }

  // ── Autonomous mode status bar methods (Issue #2373) ────────────────

  /**
   * Show autonomous mode running state
   *
   * Displays the number of running issues and remaining candidates.
   * Clicking the status bar opens the autonomous status command.
   *
   * @param running - Number of currently running pipeline slots
   * @param remaining - Number of remaining candidate issues
   */
  showAutonomousRunning(running: number, remaining: number): void {
    this.state = "running";
    this.currentStage = null;

    this.item.text = `$(play) Autonomous: ${running} running, ${remaining} remaining`;
    this.item.tooltip = `Autonomous mode active\n${running} pipelines running, ${remaining} issues remaining\nClick for status`;
    this.item.backgroundColor = STATUS_COLORS.running;
    this.item.command = "nightgauge.autonomousStatus";
    vscode.commands.executeCommand("setContext", "nightgauge.pipelineRunning", true);
  }

  /**
   * Show autonomous-mode global cooldown state (Issue #3446).
   *
   * Displayed when the scheduler is technically "running" but suspending
   * dispatch because of an active Anthropic-quota cooldown (#3431). The
   * previous status-bar code showed "Autonomous: running" in this state —
   * wildly misleading because no work is being dispatched.
   *
   * @param until - The cooldown deadline (parsed ISO-8601 or Date)
   * @param now - Reference "now" used for the remaining-duration formatting
   *              (defaults to wall-clock; injectable for unit tests)
   */
  showAutonomousCooldown(until: Date, now: Date = new Date()): void {
    this.state = "running"; // logically still running, just suspended
    this.currentStage = null;

    const label = formatCooldownLabel(until, now);
    const remaining = formatCooldownRemaining(until, now);
    this.item.text = `$(watch) Autonomous: cooldown until ${label} (${remaining})`;
    this.item.tooltip =
      `Autonomous mode: global quota cooldown active.\n` +
      `Cooldown ends ${until.toISOString()} (${remaining} remaining).\n` +
      `No issues will dispatch until the cooldown expires or you run "Autonomous: Clear Quota Cooldown".\n` +
      `Click for status.`;
    this.item.backgroundColor = STATUS_COLORS.paused;
    this.item.command = "nightgauge.autonomousStatus";
    vscode.commands.executeCommand("setContext", "nightgauge.pipelineRunning", true);
  }

  /**
   * Show autonomous mode paused state
   *
   * Displays a pause indicator. Clicking resumes autonomous mode.
   */
  showAutonomousPaused(): void {
    this.state = "paused";
    this.currentStage = null;

    this.item.text = "$(debug-pause) Autonomous: Paused";
    this.item.tooltip = "Autonomous mode paused. Click to resume.";
    this.item.backgroundColor = STATUS_COLORS.paused;
    this.item.command = "nightgauge.autonomousResume";
  }

  /**
   * Show autonomous mode complete state
   *
   * Displays the total number of completed issues.
   * Auto-resets to idle after 10 seconds.
   *
   * @param completedCount - Number of issues that completed successfully
   */
  showAutonomousComplete(completedCount: number): void {
    this.state = "complete";
    this.currentStage = null;

    this.item.text = `$(check) Autonomous: Complete (${completedCount} done)`;
    this.item.tooltip = `Autonomous mode complete. ${completedCount} issues processed.`;
    this.item.backgroundColor = STATUS_COLORS.complete;
    this.item.command = "nightgauge.autonomousStatus";
    vscode.commands.executeCommand("setContext", "nightgauge.pipelineRunning", false);

    // Auto-reset to idle after 10 seconds
    setTimeout(() => {
      if (this.state === "complete") {
        this.showIdle();
      }
    }, 10000);
  }

  /**
   * Show autonomous backend disconnected state.
   *
   * Displayed when the Go backend process exits unexpectedly while autonomous
   * mode was running. Clicking the status bar item restarts autonomous mode.
   */
  showAutonomousDisconnected(): void {
    this.state = "error";
    this.currentStage = null;

    this.item.text = "$(warning) Autonomous: Backend Disconnected";
    this.item.tooltip = "Autonomous mode: Go backend stopped unexpectedly. Click to restart.";
    this.item.backgroundColor = STATUS_COLORS.error;
    this.item.command = "nightgauge.autonomousRun";
    vscode.commands.executeCommand("setContext", "nightgauge.pipelineRunning", false);
  }

  /**
   * Set the token source label for debugging display (Issue #2670).
   *
   * When set, the token source appears in the running-state tooltip:
   *   "Stage: Development | Token: config (github_auth.token)"
   *
   * Pass null to clear the label (e.g., after pipeline completes).
   *
   * @param source - Token source string from resolveGitHubToken(), or null to clear
   */
  setTokenSource(source: string | null): void {
    this.tokenSourceLabel = source;
  }

  /**
   * Get the current token source label.
   */
  getTokenSource(): string | null {
    return this.tokenSourceLabel;
  }

  /**
   * Update the GitHub GraphQL rate-limit counter from a ProjectBoardService
   * RateLimitState event. Shows remaining/limit with colour coding:
   *   - Normal (>10%):  default colour, compact label
   *   - Low (<10%):     warning background
   *   - Exhausted (0):  error background
   *
   * Hidden until the first reading arrives so it doesn't appear on fresh
   * installs that have never hit a rate limit.
   */
  updateRateLimit(state: {
    remaining: number;
    limit: number;
    resetAt: number;
    exhausted: boolean;
    low: boolean;
  }): void {
    const { remaining, limit, resetAt, exhausted, low } = state;
    const resetDate = new Date(resetAt * 1000);
    const resetsIn = Math.max(0, Math.ceil((resetDate.getTime() - Date.now()) / 60_000));
    const label = exhausted
      ? `$(error) GQL 0/${limit}`
      : low
        ? `$(warning) GQL ${remaining.toLocaleString()}/${limit}`
        : `$(github) GQL ${remaining.toLocaleString()}/${limit}`;
    this.rateLimitItem.text = label;
    this.rateLimitItem.tooltip = [
      `GitHub GraphQL API quota`,
      `${remaining.toLocaleString()} / ${limit} remaining`,
      exhausted
        ? `Exhausted — resets in ${resetsIn} min (${resetDate.toLocaleTimeString()})`
        : `Resets in ${resetsIn} min (${resetDate.toLocaleTimeString()})`,
      ``,
      `Click to open Dashboard`,
    ].join("\n");
    this.rateLimitItem.backgroundColor = exhausted
      ? new vscode.ThemeColor("statusBarItem.errorBackground")
      : low
        ? new vscode.ThemeColor("statusBarItem.warningBackground")
        : undefined;
    this.rateLimitItem.show();
  }

  /**
   * Dispose the status bar items
   */
  dispose(): void {
    this.item.dispose();
    this.targetBranchItem.dispose();
    this.usageItem.dispose();
    this.modeItem.dispose();
    this.rateLimitItem.dispose();
  }
}

// ── Cooldown label helpers (Issue #3446) ─────────────────────────────────
//
// Exported so the prompt-on-start in autonomousCommands.ts can reuse the
// exact label/remaining strings the status bar shows — keeping the operator
// view consistent across surfaces.

/**
 * Format a cooldown deadline as a short HH:MM UTC label (e.g. "03:31 UTC").
 *
 * The 24h UTC clock keeps the label timezone-stable across user machines and
 * matches the format used by Anthropic's rate-limit messages. Falls back to
 * "soon" if the deadline is already in the past, which can happen during
 * the brief window after a cooldown expires but before the next scan
 * auto-clears the field.
 */
export function formatCooldownLabel(until: Date, now: Date = new Date()): string {
  if (!(until instanceof Date) || isNaN(until.getTime())) {
    return "soon";
  }
  if (until.getTime() <= now.getTime()) {
    return "soon";
  }
  const hh = String(until.getUTCHours()).padStart(2, "0");
  const mm = String(until.getUTCMinutes()).padStart(2, "0");
  return `${hh}:${mm} UTC`;
}

/**
 * Format the time remaining until a cooldown expires as "Xh Ym" (or "Ym Zs"
 * when under an hour, "Zs" when under a minute). Returns "0s" if the deadline
 * is already past — see formatCooldownLabel for the brief expiry-window note.
 */
export function formatCooldownRemaining(until: Date, now: Date = new Date()): string {
  if (!(until instanceof Date) || isNaN(until.getTime())) {
    return "0s";
  }
  const ms = until.getTime() - now.getTime();
  if (ms <= 0) return "0s";
  const totalSeconds = Math.floor(ms / 1000);
  const hours = Math.floor(totalSeconds / 3600);
  const minutes = Math.floor((totalSeconds % 3600) / 60);
  const seconds = totalSeconds % 60;
  if (hours > 0) {
    return `${hours}h ${minutes}m`;
  }
  if (minutes > 0) {
    return `${minutes}m ${seconds}s`;
  }
  return `${seconds}s`;
}
