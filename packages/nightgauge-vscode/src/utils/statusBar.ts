/**
 * Status bar management for Nightgauge Pipeline extension
 *
 * Provides visual feedback for pipeline state in the VS Code status bar.
 */

import * as vscode from "vscode";
import type { PipelineStage } from "@nightgauge/sdk";
import type { StageExecutionMode } from "./nightgaugeConfig";
import { DEFAULT_PERFORMANCE_MODE, MODE_PROFILES, type PerformanceMode } from "./modeProfiles";
import type { ExecutionAdapter } from "../config/schema";
import { CLAUDE_PLAN_SETTING } from "../services/usage/claudePlanDeclaration";
import type { UsageSnapshot, UsageWindow } from "../services/usage/types";
import { formatUsageValue } from "../services/usage/format";

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
  /**
   * Adapter usage meter — renders the active adapter's `UsageSnapshot`,
   * cycling through the windows it exposes on click (Issue #659, superseding
   * the budget-only counter from Issue #1333; see
   * docs/decisions/018-adapter-usage-quota-model.md). Hidden until the first
   * snapshot arrives.
   */
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
  /** Latest adapter usage snapshot rendered into `usageItem` (Issue #659). */
  private usageSnapshot: UsageSnapshot | null = null;
  /**
   * Id of the window currently displayed in `usageItem`. The caller
   * (`nightgauge.cycleUsageMetric`) persists this to workspace state and
   * restores it via `setSelectedUsageWindowId` so the selection survives a
   * window reload.
   */
  private selectedUsageWindowId: string | null = null;
  /**
   * Last GraphQL rate-limit reading, retained so the spend meter (#1347) can
   * re-render the item without a fresh board call — the two facts arrive from
   * different sources on different cadences.
   */
  private rateLimitState: {
    remaining: number;
    limit: number;
    resetAt: number;
    exhausted: boolean;
    low: boolean;
  } | null = null;
  /** Last measured hourly API spend (Issue #1347); null when unmeasurable. */
  private apiSpend: {
    points: number;
    calls: number;
    topCaller: string | null;
    topCallerPoints: number;
  } | null = null;

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

    // Adapter usage meter (rightmost of the three, hidden until the first
    // snapshot arrives). Click cycles through the windows the snapshot
    // exposes (Issue #659) — "Open Dashboard" remains reachable via a
    // command link in the tooltip instead of the click gesture.
    this.usageItem = vscode.window.createStatusBarItem(
      vscode.StatusBarAlignment.Left,
      98 // Just to the right of target branch
    );
    this.usageItem.command = "nightgauge.cycleUsageMetric";

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
    this.item.text = `$(dashboard) Nightgauge${modeBadge}`;
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
   * Render the active adapter's usage snapshot into `usageItem` (Issue #659).
   *
   * Replaces the budget-only `showUsage()` from Issue #1333 — the monthly
   * budget figure is now just one window inside `snapshot.windows` (see
   * docs/decisions/018-adapter-usage-quota-model.md), selected the same way
   * as any other window rather than rendered by a dedicated code path.
   *
   * The window actually displayed is whichever one's id matches
   * `selectedUsageWindowId` (restored via `setSelectedUsageWindowId` /
   * advanced via `cycleUsageWindow`), falling back to the first window when
   * the selection is unset or no longer present in this snapshot (e.g. the
   * adapter changed and the new snapshot's window ids differ).
   */
  showUsageSnapshot(snapshot: UsageSnapshot): void {
    this.usageSnapshot = snapshot;
    this.renderUsageItem();
  }

  /**
   * Restore a persisted window selection (Issue #659).
   *
   * Call before the first snapshot arrives (e.g. during activation, from the
   * value last written to workspace state) so a window reload resumes on the
   * window the user had selected, per #659's AC that the selection "persists
   * in workspace state and survives a window reload".
   */
  setSelectedUsageWindowId(id: string | null): void {
    this.selectedUsageWindowId = id;
    this.renderUsageItem();
  }

  /**
   * Advance to the next window in the current snapshot (Issue #659).
   *
   * Returns the newly selected window's id — for the caller
   * (`nightgauge.cycleUsageMetric`) to persist to workspace state — or `null`
   * when there is nothing to cycle to: no snapshot has arrived yet, or the
   * snapshot is `plan.kind: "unknown"` with zero windows.
   */
  cycleUsageWindow(): string | null {
    const snapshot = this.usageSnapshot;
    if (!snapshot || snapshot.windows.length === 0) {
      return null;
    }
    const currentIndex = snapshot.windows.findIndex((w) => w.id === this.selectedUsageWindowId);
    // findIndex returns -1 when nothing is selected yet — that state renders
    // window 0 (see renderUsageItem's fallback), so cycling from "nothing
    // selected" must advance to window 1, not redraw window 0 again.
    const nextIndex = (Math.max(currentIndex, 0) + 1) % snapshot.windows.length;
    this.selectedUsageWindowId = snapshot.windows[nextIndex].id;
    this.renderUsageItem();
    return this.selectedUsageWindowId;
  }

  /** Re-render `usageItem` from `usageSnapshot`/`selectedUsageWindowId`. */
  private renderUsageItem(): void {
    const snapshot = this.usageSnapshot;
    if (!snapshot) {
      this.usageItem.hide();
      return;
    }
    if (snapshot.windows.length === 0) {
      // plan.kind: "unknown" — an explicit state, never hidden silently
      // (docs/decisions/018-adapter-usage-quota-model.md; #659 AC).
      this.usageItem.text = `${USAGE_METER_ICON} ${snapshot.adapter} usage unknown`;
      this.usageItem.tooltip = buildUsageTooltip(snapshot);
      this.usageItem.backgroundColor = undefined;
      this.usageItem.show();
      return;
    }
    const selected = snapshot.windows.find((w) => w.id === this.selectedUsageWindowId);
    const window = selected ?? snapshot.windows[0];
    this.usageItem.text = formatUsageWindowText(snapshot.adapter, window);
    this.usageItem.tooltip = buildUsageTooltip(snapshot);
    this.usageItem.backgroundColor = usageThresholdColor(window);
    this.usageItem.show();
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
   * Show that the queue is waiting out a retry backoff after a transient
   * failure. Issue #195.
   *
   * Without this the badge reads "Autonomous: 0 running, N remaining" — which
   * is what an idle queue looks like — so a fleet recovering from a provider
   * outage was indistinguishable from a stuck one, and the reasonable operator
   * response was to start intervening in a system that was already healing.
   *
   * Logically still running, like the cooldown badge: dispatch resumes on its
   * own when the deadline expires. The UI just stops lying about why nothing
   * is moving.
   */
  showAutonomousRetrying(
    retry: { repo: string; number: number; kind?: string; reason?: string; attempts: number },
    until: Date,
    alsoWaiting: number,
    now: Date = new Date()
  ): void {
    this.state = "running";
    this.currentStage = null;

    const remaining = formatCooldownRemaining(until, now);
    const others = alsoWaiting > 0 ? ` +${alsoWaiting}` : "";
    const attemptSuffix = retry.attempts > 1 ? ` (attempt ${retry.attempts})` : "";
    this.item.text = `$(sync) Autonomous: retrying #${retry.number} in ${remaining}${others}`;
    this.item.tooltip =
      `Autonomous mode: waiting to retry after a transient failure.\n` +
      `${retry.repo}#${retry.number}${attemptSuffix}\n` +
      `${retry.reason || retry.kind || "transient failure"}\n` +
      `Retries ${until.toISOString()} (${remaining} remaining).\n` +
      (alsoWaiting > 0 ? `${alsoWaiting} other issue(s) also waiting.\n` : "") +
      `Nothing is stuck — dispatch resumes automatically.\n` +
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
    this.rateLimitState = state;
    this.renderRateLimitItem();
  }

  /**
   * Report the last hour of measured GitHub API spend (Issue #1347).
   *
   * The remaining count above answers "how close is the cliff?"; this answers
   * "how fast am I walking towards it, and who is walking?". They are rendered
   * on ONE item on purpose — split across two, an operator reads a falling
   * number and a spend rate as unrelated facts, which is how every previous
   * exhaustion here was noticed only after it had already happened.
   *
   * Passing null clears the spend half (the meter could not read the ledger)
   * without disturbing the remaining count, because an absent measurement must
   * render as absent rather than as zero.
   */
  updateApiSpend(
    spend: {
      points: number;
      calls: number;
      topCaller: string | null;
      topCallerPoints: number;
    } | null
  ): void {
    this.apiSpend = spend;
    if (this.rateLimitState) {
      this.renderRateLimitItem();
    }
  }

  private renderRateLimitItem(): void {
    const state = this.rateLimitState;
    if (!state) {
      return;
    }
    const { remaining, limit, resetAt, exhausted, low } = state;
    const resetDate = new Date(resetAt * 1000);
    const resetsIn = Math.max(0, Math.ceil((resetDate.getTime() - Date.now()) / 60_000));
    const spend = this.apiSpend;
    // The rate rides in the label itself, not only the tooltip: a tooltip is
    // read after someone already suspects a problem, and the entire point of
    // this number is to be seen before that.
    const rate = spend ? ` ${spend.points.toLocaleString()}/h` : "";
    const label = exhausted
      ? `$(error) GQL 0/${limit}${rate}`
      : low
        ? `$(warning) GQL ${remaining.toLocaleString()}/${limit}${rate}`
        : `$(github) GQL ${remaining.toLocaleString()}/${limit}${rate}`;
    this.rateLimitItem.text = label;

    const lines = [
      `GitHub GraphQL API quota`,
      `${remaining.toLocaleString()} / ${limit} remaining`,
      exhausted
        ? `Exhausted — resets in ${resetsIn} min (${resetDate.toLocaleTimeString()})`
        : `Resets in ${resetsIn} min (${resetDate.toLocaleTimeString()})`,
    ];
    if (spend) {
      lines.push(
        ``,
        `Spent in the last hour: ${spend.points.toLocaleString()} point(s) over ${spend.calls.toLocaleString()} request(s)`,
        spend.topCaller
          ? `Top caller: ${spend.topCaller} (${spend.topCallerPoints.toLocaleString()} pts)`
          : `No spend attributed in that window`,
        `Full breakdown: nightgauge api-usage --since 1h`
      );
    }
    lines.push(``, `Click to open Dashboard`);
    this.rateLimitItem.tooltip = lines.join("\n");

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
  // Days roll over above 24h (Issue #730). A quota cooldown never reaches a
  // day, so this tier was unreachable until the Claude weekly usage window
  // started rendering through the same formatter — and "resets 111h 6m" is a
  // number an operator has to do arithmetic on before it means anything.
  if (hours >= 24) {
    const days = Math.floor(hours / 24);
    const remainderHours = hours % 24;
    return remainderHours > 0 ? `${days}d ${remainderHours}h` : `${days}d`;
  }
  if (hours > 0) {
    return `${hours}h ${minutes}m`;
  }
  if (minutes > 0) {
    return `${minutes}m ${seconds}s`;
  }
  return `${seconds}s`;
}

// ── Adapter usage meter rendering (Issue #659) ────────────────────────────
//
// Pure functions so the meter's formatting can be unit-tested without a
// StatusBarManager instance. `StatusBarManager.renderUsageItem` is the only
// caller in production code.

/** Icon used for every usage-meter rendering, matching the pre-#659 counter. */
const USAGE_METER_ICON = "$(flame)";

/** Eighths-resolution partial-block characters, indexed 1-7 (0 is unused). */
const USAGE_BAR_PARTIALS = ["", "▏", "▎", "▍", "▌", "▋", "▊", "▉"];

/**
 * Render an 8-segment proportional bar (e.g. `"███▌░░░░"` for ~44%).
 *
 * Uses eighths-resolution partial-block characters for a smoothly
 * proportional fill instead of 8 coarse on/off segments. `pct` is clamped to
 * `[0, 100]` for the bar's visual fill only — `used > limit` is legal (an
 * overage-enabled plan keeps serving past 100%, per
 * docs/decisions/018-adapter-usage-quota-model.md), so the bar saturates full
 * rather than over/underflowing; the numeric percentage next to it is never
 * clamped.
 */
export function renderUsageBar(pct: number, segments = 8): string {
  const clamped = Math.max(0, Math.min(100, pct));
  const eighthsPerSegment = 8;
  const totalEighths = Math.round((clamped / 100) * segments * eighthsPerSegment);
  const fullSegments = Math.min(segments, Math.floor(totalEighths / eighthsPerSegment));
  const remainderEighths = fullSegments < segments ? totalEighths % eighthsPerSegment : 0;
  const partial = remainderEighths > 0 ? USAGE_BAR_PARTIALS[remainderEighths] : "";
  const emptySegments = segments - fullSegments - (partial ? 1 : 0);
  return "█".repeat(fullSegments) + partial + "░".repeat(Math.max(0, emptySegments));
}

/**
 * The window's fill percentage, or `null` when `limit` is unknown/non-positive
 * — the caller's cue to render the absolute figure instead of a bar (#659 AC:
 * "No fabricated fill, no implied percentage").
 */
function usagePercent(window: UsageWindow): number | null {
  // `used: null` is a declared window nothing has measured yet (#808). A
  // percentage of an unobserved figure is the fabricated fill this function
  // exists to refuse.
  if (window.used === null || window.limit === null || window.limit <= 0) {
    return null;
  }
  return (window.used / window.limit) * 100;
}

/** Copy for a window whose plan is known but whose figure is not (#808). */
const AWAITING_READING = "awaiting first reading";

/**
 * Status-bar background colour for a window, matching the pre-#659
 * threshold behaviour: ≥90% error, ≥80% warning, otherwise default. A window
 * with no known limit (`usagePercent` returns null) never colours the item —
 * there is no ceiling to measure against.
 */
export function usageThresholdColor(window: UsageWindow): vscode.ThemeColor | undefined {
  const pct = usagePercent(window);
  if (pct === null) {
    return undefined;
  }
  if (pct >= 90) {
    return new vscode.ThemeColor("statusBarItem.errorBackground");
  }
  if (pct >= 80) {
    return new vscode.ThemeColor("statusBarItem.warningBackground");
  }
  return undefined;
}

/**
 * Local wall-clock "HH:MM" for an as-of stamp.
 *
 * Built by hand rather than via `toLocaleTimeString` so the rendered string is
 * deterministic across locales — the status bar is asserted verbatim in tests,
 * and a 12/24-hour flip would make those assertions environment-dependent.
 */
function formatClockTime(at: Date): string {
  const hours = String(at.getHours()).padStart(2, "0");
  const minutes = String(at.getMinutes()).padStart(2, "0");
  return `${hours}:${minutes}`;
}

/**
 * The " · as of HH:MM" suffix for a window whose figure is a cached reading
 * rather than a live one (Issue #709).
 *
 * Empty for a `measured` window: the figure describes the moment it is being
 * read, so an as-of would add noise. Empty when the provider set no
 * `observedAt` at all, which means the figure was derived at snapshot time
 * (every local-telemetry window).
 *
 * This is the honesty half of serving a persisted percentage between runs. A
 * stale percentage rendered as current is worse than no percentage, so the
 * age travels with the number instead of being inferable only from the
 * tooltip.
 */
function formatAsOfSuffix(window: UsageWindow): string {
  if (window.observedAt === undefined || window.confidence === "measured") {
    return "";
  }
  return ` · as of ${formatClockTime(window.observedAt)}`;
}

/**
 * Render one `UsageWindow` as the `usageItem` label text (Issue #659).
 *
 * Three shapes, by what the window can honestly say:
 *
 * - **A vendor-reported percentage** (`unit: "percent"`, Issue #709) renders
 *   as `$(flame) <adapter> <window> <bar> <used>% · <left>% left · resets
 *   <duration>` — e.g.
 *   `$(flame) claude session (5h) ███▌░░░░ 44% · 56% left · resets 2h 14m`.
 *   A subscription plan has no dollar budget, so "how much is left and when
 *   does it refill" is the whole question; the window is named because the
 *   click gesture cycles between the five-hour, daily and weekly ones and an
 *   unlabelled percentage would not say which it is. A cached reading also
 *   carries ` · as of HH:MM`.
 * - **A dollar figure against a configured budget** renders as
 *   `$(flame) <adapter> <bar> <pct>% · resets <duration>` — unchanged from
 *   #659.
 * - **A `null` limit** renders the absolute figure
 *   (`$(flame) <adapter> $4.12 today`), never a fabricated bar or percentage.
 *
 * The reset duration reuses `formatCooldownRemaining` (Issue #3446) rather
 * than a second "time until" formatter — the two are the same computation.
 */
export function formatUsageWindowText(
  adapter: ExecutionAdapter,
  window: UsageWindow,
  now: Date = new Date()
): string {
  const pct = usagePercent(window);
  if (window.used === null) {
    return `${USAGE_METER_ICON} ${adapter} ${window.label.toLowerCase()} — ${AWAITING_READING}`;
  }
  if (pct === null) {
    return `${USAGE_METER_ICON} ${adapter} ${formatUsageValue(window.used, window.unit)} ${window.label.toLowerCase()}`;
  }
  const bar = renderUsageBar(pct);
  const resetSuffix = window.resetsAt
    ? ` · resets ${formatCooldownRemaining(window.resetsAt, now)}`
    : "";
  if (window.unit === "percent") {
    // `used > limit` is legal on an overage-enabled plan, and "-4% left" is
    // not a thing — past the ceiling there is nothing left, and the used
    // figure beside it already says by how much it was passed.
    const remaining = Math.max(0, (window.limit ?? 0) - (window.used ?? 0));
    return (
      `${USAGE_METER_ICON} ${adapter} ${window.label.toLowerCase()} ${bar} ` +
      `${Math.round(pct)}% · ${Math.round(remaining)}% left${resetSuffix}${formatAsOfSuffix(window)}`
    );
  }
  return `${USAGE_METER_ICON} ${adapter} ${bar} ${Math.round(pct)}%${resetSuffix}`;
}

/**
 * Build the `usageItem` hover tooltip (Issue #659): every window in the
 * snapshot with its used/limit figures, reset time, and confidence, plus a
 * command link back to `nightgauge.showDashboard` — the click gesture on the
 * item itself now cycles windows instead of opening the dashboard, so this
 * link is how "Open Dashboard" stays reachable (#659 AC).
 */
export function buildUsageTooltip(snapshot: UsageSnapshot): vscode.MarkdownString {
  const tooltip = new vscode.MarkdownString();
  tooltip.isTrusted = true;
  tooltip.appendMarkdown(`**${snapshot.adapter} usage**\n\n`);
  if (snapshot.windows.length === 0) {
    tooltip.appendMarkdown(
      "_No usage provider is available for this adapter — usage is unknown, not zero._\n\n"
    );
  } else {
    for (const window of snapshot.windows) {
      const limitText =
        window.limit === null ? "no limit configured" : formatUsageValue(window.limit, window.unit);
      const resetText = window.resetsAt ? window.resetsAt.toLocaleString() : "no scheduled reset";
      // As-of only for a figure the provider is serving from cache (#709);
      // a live one describes the moment it is read.
      const asOfText =
        window.observedAt !== undefined && window.confidence !== "measured"
          ? `, as of ${window.observedAt.toLocaleString()}`
          : "";
      tooltip.appendMarkdown(
        `- **${window.label}**: ${
          window.used === null ? AWAITING_READING : formatUsageValue(window.used, window.unit)
        } / ${limitText} — resets ${resetText} _(${window.confidence}${asOfText})_\n`
      );
    }
    tooltip.appendMarkdown("\n");
  }
  appendClaudeSubscriptionHint(tooltip, snapshot);
  tooltip.appendMarkdown("[Open Dashboard](command:nightgauge.showDashboard)");
  return tooltip;
}

/**
 * Offer the Max-plan feed when the `claude` adapter is answering with anything
 * other than a subscription window (Issue #730).
 *
 * A subscriber whose feed is not wired up sees `LocalTelemetryUsageProvider`'s
 * dollar windows — nightgauge's own rate-card-derived spend. Those are honest
 * figures, but they answer a question a subscription operator did not ask, and
 * nothing on the meter says a better number is one command away. Without this
 * line the only cue is the absence of a percentage, which reads as "nightgauge
 * cannot know" rather than "nightgauge has not been told how to look".
 *
 * Deliberately conditional on the *observed plan kind*, never on the adapter
 * name alone: an API-key user on the same `claude` adapter is genuinely
 * pay-per-token, and the dollar windows are the right answer for them. They
 * see the offer too — the extension cannot tell the two apart without a
 * reading, and ADR 018 forbids inferring a plan from the adapter id — so the
 * wording asks rather than asserts.
 */
function appendClaudeSubscriptionHint(
  tooltip: vscode.MarkdownString,
  snapshot: UsageSnapshot
): void {
  if (snapshot.adapter !== "claude" || snapshot.plan.kind === "subscription-window") {
    return;
  }
  // (#808) The operator has already answered this question. Declaring
  // API/pay-per-token means dollar windows ARE their right answer, so asking
  // again is noise — and a hint that keeps asking after being told is how an
  // operator learns to ignore it.
  if (snapshot.claudePlanDeclared === true) {
    return;
  }
  // (#810) Same verdict the Dashboard panel and the enable command read. This
  // used to infer "the feed is off" from plan.kind alone, so it offered to
  // enable a feed the command simultaneously called already enabled.
  const health = snapshot.claudeFeedHealth;
  if (health !== undefined && health.state !== "not-wired") {
    tooltip.appendMarkdown(
      "_The Claude usage feed is enabled but is not reporting your plan's allowance._\n\n" +
        "[Check the Claude usage feed](command:nightgauge.enableClaudeUsageStatusLine)\n\n"
    );
    return;
  }
  tooltip.appendMarkdown(
    "_On a Claude Max or Pro plan? These are locally-derived costs, not your plan's allowance._\n\n" +
      "[Show my plan's 5h / weekly limits](command:nightgauge.enableClaudeUsageStatusLine)\n\n" +
      // (#808) The other way to answer: declare the plan, for the operator
      // whose feed cannot report one yet.
      `[Tell Nightgauge which plan I'm on](command:workbench.action.openSettings?%22${CLAUDE_PLAN_SETTING}%22)\n\n`
  );
}
