/**
 * ConcurrentSlotTreeItem - Tree item for a concurrent pipeline slot
 *
 * Represents a single concurrent pipeline execution inside the unified
 * Pipeline tree view. Each slot owns its own StageTreeItems so all
 * running slots can display stage progress simultaneously.
 *
 * @see Issue #1631 - Concurrent Pipeline Visibility
 */

import * as vscode from "vscode";
import { BaseTreeItem } from "./BaseTreeItem";
import { StageTreeItem } from "./StageTreeItem";
import type { PipelineStage } from "@nightgauge/sdk";
import { PHASE_REGISTRY, type ExecutionStage } from "@nightgauge/sdk";
import type { PipelineStateService, PipelineState } from "../../services/PipelineStateService";
import type { StagePhase } from "../../schemas/pipelineState";

type SlotStatus = "running" | "completed" | "failed";

const STAGE_ORDER: PipelineStage[] = [
  "pipeline-start",
  "issue-pickup",
  "feature-planning",
  "feature-dev",
  "feature-validate",
  "pr-create",
  "pr-merge",
  "pipeline-finish",
];

/**
 * Build a minimal phases array directly from a phase event payload.
 * Same logic as in PipelineTreeProvider — see that file for rationale (Issue #3486).
 */
function buildSyntheticPhases(
  stage: string,
  phaseName: string,
  phaseIndex: number,
  totalPhases: number,
  currentComplete = false
): StagePhase[] {
  const registryPhases = (PHASE_REGISTRY as Record<string, Array<{ name: string }>>)[stage] ?? [];
  const phases: StagePhase[] = [];
  for (let i = 0; i < phaseIndex; i++) {
    phases.push({ name: registryPhases[i]?.name ?? `step-${i + 1}`, status: "complete" });
  }
  phases.push({ name: phaseName, status: currentComplete ? "complete" : "running" });
  return phases;
}

const STAGE_LABELS: Record<string, string> = {
  "pipeline-start": "Pipeline Start",
  "issue-pickup": "Issue Pickup",
  "feature-planning": "Feature Planning",
  "feature-dev": "Feature Development",
  "feature-validate": "Feature Validation",
  "pr-create": "PR Creation",
  "pr-merge": "PR Merge",
  "pipeline-finish": "Pipeline Finish",
};

function getStatusIcon(status: SlotStatus): vscode.ThemeIcon {
  switch (status) {
    case "running":
      return new vscode.ThemeIcon("sync~spin", new vscode.ThemeColor("charts.yellow"));
    case "completed":
      return new vscode.ThemeIcon("check", new vscode.ThemeColor("charts.green"));
    case "failed":
      return new vscode.ThemeIcon("error", new vscode.ThemeColor("charts.red"));
  }
}

export class ConcurrentSlotTreeItem extends BaseTreeItem {
  /** Max characters for the title portion of the label before truncation. */
  private static readonly MAX_TITLE_LENGTH = 32;

  readonly issueNumber: number;
  readonly slotIndex: number;
  readonly epicNumber?: number;
  private status: SlotStatus = "running";
  private stages: Map<PipelineStage, StageTreeItem> = new Map();
  private disposables: vscode.Disposable[] = [];
  private onChange: (() => void) | null = null;

  /** Build a truncated slot label and full tooltip from a title string. */
  private static formatLabel(
    issueNumber: number,
    title: string
  ): { label: string; fullTitle: string } {
    const truncated =
      title.length > ConcurrentSlotTreeItem.MAX_TITLE_LENGTH
        ? title.slice(0, ConcurrentSlotTreeItem.MAX_TITLE_LENGTH - 1) + "…"
        : title;
    return {
      label: `#${issueNumber} — ${truncated}`,
      fullTitle: `#${issueNumber} — ${title}`,
    };
  }

  constructor(
    slotIndex: number,
    issueNumber: number,
    title: string,
    stateService: PipelineStateService,
    epicNumber?: number,
    onChange?: () => void
  ) {
    const { label, fullTitle } = ConcurrentSlotTreeItem.formatLabel(issueNumber, title);
    super(label, vscode.TreeItemCollapsibleState.Expanded);

    this.issueNumber = issueNumber;
    this.slotIndex = slotIndex;
    this.epicNumber = epicNumber;
    this.id = `concurrent-slot-${issueNumber}`;
    this.iconPath = getStatusIcon("running");
    this.contextValue = "concurrentSlot.running";
    this.description = epicNumber
      ? `Slot ${slotIndex + 1} · Epic #${epicNumber}`
      : `Slot ${slotIndex + 1}`;
    this.tooltip = fullTitle;
    this.onChange = onChange ?? null;

    // Create own set of stage items
    for (const stage of STAGE_ORDER) {
      this.stages.set(stage, new StageTreeItem(stage));
    }
    this.rebuildChildren();

    // Subscribe to state changes — filter by issue number since IpcClient
    // is a singleton and ALL PipelineStateService instances receive every
    // event regardless of which slot they belong to.
    const disposable = stateService.onStateChanged((state) => {
      if (state && state.issue_number === this.issueNumber) {
        this.syncFromState(state);
        this.onChange?.();
      }
    });
    this.disposables.push(disposable);

    // Initial sync
    stateService.getState().then((state) => {
      if (state && state.issue_number === this.issueNumber) {
        this.syncFromState(state);
        this.onChange?.();
      }
    });

    // Subscribe to phase events — apply data directly from event payload (Issue #3486)
    const phaseStartDisposable = stateService.onPhaseStart((event) => {
      if (event.issueNumber !== undefined && event.issueNumber !== this.issueNumber) return;
      const stageItem = this.stages.get(event.stage as PipelineStage);
      if (stageItem) {
        // A phase event is definitive proof the stage is running. Set status
        // before setPhases() so formatDescription() returns "PhaseLabel [X/Y]"
        // even if pipeline.stateChanged hasn't propagated yet.
        if (stageItem.getStatus() === "pending") {
          stageItem.setStatus("running");
        }
        const totalPhases = event.totalPhases ?? event.total;
        const phases = buildSyntheticPhases(event.stage, event.phase, event.index, totalPhases);
        stageItem.setPhases(phases, event.phase, totalPhases);
        this.onChange?.();
      }
    });
    this.disposables.push(phaseStartDisposable);

    const phaseCompleteDisposable = stateService.onPhaseComplete((event) => {
      if (event.issueNumber !== undefined && event.issueNumber !== this.issueNumber) return;
      const stageItem = this.stages.get(event.stage as PipelineStage);
      if (stageItem) {
        const totalPhases = event.totalPhases ?? event.total;
        const phases = buildSyntheticPhases(
          event.stage,
          event.phase,
          event.index,
          totalPhases,
          true
        );
        stageItem.setPhases(phases, undefined, totalPhases);
        this.onChange?.();
      }
    });
    this.disposables.push(phaseCompleteDisposable);

    // Subscribe to token usage updates — accumulate deltas per stage (Issue #3486)
    const tokenDisposable = stateService.onTokenUsageUpdated((tokenUpdate) => {
      if (tokenUpdate.issueNumber !== undefined && tokenUpdate.issueNumber !== this.issueNumber)
        return;
      if (tokenUpdate.stage) {
        const stageItem = this.stages.get(tokenUpdate.stage as PipelineStage);
        if (stageItem) {
          const current = stageItem.getTokenInfo();
          stageItem.setTokenUsage({
            inputTokens: (current?.inputTokens ?? 0) + tokenUpdate.inputTokens,
            outputTokens: (current?.outputTokens ?? 0) + tokenUpdate.outputTokens,
            costUsd: (current?.costUsd ?? 0) + (tokenUpdate.costUsd ?? 0),
          });
        }
      }
      this.onChange?.();
    });
    this.disposables.push(tokenDisposable);
  }

  /**
   * Format the issue-level description from cumulative metrics and current stage context.
   * Shows previously accumulated totals while a stage is running (does not reset mid-stage).
   */
  private formatIssueDescription(state: PipelineState): string {
    const parts: string[] = [];

    const issueTokens = state.tokens?.per_issue;
    if (
      issueTokens &&
      (issueTokens.cost_usd > 0 || issueTokens.input > 0 || issueTokens.output > 0)
    ) {
      if (issueTokens.cost_usd > 0) {
        parts.push(`$${issueTokens.cost_usd.toFixed(4)}`);
      }
      const totalTokens = issueTokens.input + issueTokens.output + issueTokens.cache_read;
      if (totalTokens > 0) {
        const formatted =
          totalTokens >= 1000
            ? `${(totalTokens / 1000).toFixed(1)}K tokens`
            : `${totalTokens} tokens`;
        parts.push(formatted);
      }
    }

    // Stage context: "Stage N of M: Label"
    const stageOrder = STAGE_ORDER.filter(
      (s) => s !== "pipeline-start" && s !== "pipeline-finish"
    ) as string[];
    if (state.current_stage) {
      const pos = stageOrder.indexOf(state.current_stage);
      if (pos >= 0) {
        const label = STAGE_LABELS[state.current_stage] ?? state.current_stage;
        parts.push(`Stage ${pos + 1} of ${stageOrder.length}: ${label}`);
      }
    }

    if (parts.length === 0) {
      return this.epicNumber
        ? `Slot ${this.slotIndex + 1} · Epic #${this.epicNumber}`
        : `Slot ${this.slotIndex + 1}`;
    }

    return parts.join(" | ");
  }

  private syncFromState(state: PipelineState): void {
    // Update title from state if available
    if (state.title && state.title !== `Issue #${this.issueNumber}`) {
      const { label, fullTitle } = ConcurrentSlotTreeItem.formatLabel(
        this.issueNumber,
        state.title
      );
      this.label = label;
      // Build enhanced tooltip with title + cumulative metrics clarification
      const issueTokens = state.tokens?.per_issue;
      const metricsSummary =
        issueTokens && issueTokens.cost_usd > 0
          ? `\nCumulative cost and tokens across all completed stages: $${issueTokens.cost_usd.toFixed(4)}`
          : "";
      this.tooltip = fullTitle + metricsSummary;
    }

    // Update description with cumulative metrics and current stage context
    this.description = this.formatIssueDescription(state);

    // Sync stage statuses
    for (const [stageName, stageState] of Object.entries(state.stages)) {
      const stage = stageName as PipelineStage;
      const stageItem = this.stages.get(stage);
      if (!stageItem) continue;

      const statusMap: Record<
        string,
        "pending" | "running" | "complete" | "failed" | "skipped" | "deferred"
      > = {
        pending: "pending",
        running: "running",
        complete: "complete",
        failed: "failed",
        skipped: "skipped",
        deferred: "deferred",
      };
      stageItem.setStatus(statusMap[stageState.status] || "pending");

      if (stageState.duration_ms) {
        stageItem.setDuration(stageState.duration_ms);
      }

      if (stageState.status === "failed" && stageState.error) {
        stageItem.setError(stageState.error);
      }

      const executionMode =
        (stageState as { execution_mode?: "headless" | "interactive" }).execution_mode ?? null;
      stageItem.setExecutionMode(executionMode);

      // Sync phases
      if (stageState.phases && stageState.phases.length > 0) {
        const registryPhases = PHASE_REGISTRY[stage as ExecutionStage] ?? [];
        const totalForDisplay =
          registryPhases.length > 0 ? registryPhases.length : stageState.total_phases;

        let phasesForDisplay: StagePhase[] = (stageState.phases ?? []) as StagePhase[];

        if (stageState.status === "complete" || stageState.status === "failed") {
          // Downgrade phases stuck at "running" — the parent stage has
          // ended, so a "running" phase is a stale write from a missed
          // phase.complete event (Issue #3240/#3242 brought to the
          // concurrent path in #3255). Without this, e.g. a skill that
          // emits a phase name not in PHASE_REGISTRY (the registry has
          // "complete-stage", the skill emits "completion-checklist")
          // never gets cleared and spins forever under a finished stage.
          const terminalPhaseStatus = stageState.status === "complete" ? "complete" : "failed";
          phasesForDisplay = phasesForDisplay.map((p) =>
            p.status === "running" ? { ...p, status: terminalPhaseStatus } : p
          );

          if (registryPhases.length > 0) {
            const recorded = new Set(stageState.phases.map((p) => p.name));
            const missing = registryPhases.filter((r) => !recorded.has(r.name));
            if (missing.length > 0) {
              phasesForDisplay = [
                ...phasesForDisplay,
                ...missing.map((r): StagePhase => ({
                  name: r.name,
                  status: "skipped",
                })),
              ];
            }
          }
        }

        // When a stage is complete/failed, the displayed `current_phase`
        // should not point to anything (no live phase). Otherwise the tree
        // renders a "(N/M)" label off the stale value.
        const currentPhaseForDisplay =
          stageState.status === "complete" || stageState.status === "failed"
            ? undefined
            : stageState.current_phase;

        stageItem.setPhases(phasesForDisplay, currentPhaseForDisplay, totalForDisplay);
      } else if (stageItem.getPhaseCount() > 0 && stageState.status !== "running") {
        // Skip for running stages: stateChanged events strip phase data from Go's
        // snapshot, so clearing here would wipe live phase progress mid-stage.
        stageItem.clearPhases();
      }
    }

    // Update token usage
    if (state.tokens?.per_stage) {
      for (const [stageName, stageTokens] of Object.entries(state.tokens.per_stage)) {
        const stageItem = this.stages.get(stageName as PipelineStage);
        if (stageItem && stageTokens) {
          stageItem.setTokenUsage({
            inputTokens: stageTokens.input,
            outputTokens: stageTokens.output,
            costUsd: stageTokens.cost_usd ?? 0,
          });
        }
      }
    }

    this.rebuildChildren();
  }

  private rebuildChildren(): void {
    this.clearChildren();
    for (const stage of STAGE_ORDER) {
      const stageItem = this.stages.get(stage);
      if (stageItem) {
        this.addChild(stageItem);
      }
    }
  }

  setSlotStatus(status: SlotStatus): void {
    this.status = status;
    this.iconPath = getStatusIcon(status);
    this.contextValue = `concurrentSlot.${status}`;
    this.collapsibleState =
      status === "running"
        ? vscode.TreeItemCollapsibleState.Expanded
        : vscode.TreeItemCollapsibleState.Collapsed;
  }

  getSlotStatus(): SlotStatus {
    return this.status;
  }

  getStage(stage: PipelineStage): StageTreeItem | undefined {
    return this.stages.get(stage);
  }

  dispose(): void {
    for (const disposable of this.disposables) {
      disposable.dispose();
    }
  }
}
