/**
 * PipelineService - IPC-backed pipeline execution service.
 *
 * Thin UI-layer service that delegates all pipeline operations to the
 * Go binary via JSON-over-stdio IPC. Replaces both HeadlessOrchestrator
 * and PipelineStateService.
 *
 * Phase 5: Go binary owns execution, state, and worktree management.
 * This service is a pure UI adapter — it dispatches commands and
 * translates events for tree views and status bar.
 *
 * @see internal/orchestrator/scheduler.go — Go-side orchestration
 * @see internal/execution/manager.go — Go-side execution lifecycle
 */

import * as vscode from "vscode";
import { IpcClient, type ExecutionInfo, type RunPipelineResult } from "./IpcClient";

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

/** Pipeline stage names (matches Go state.PipelineStage). */
export type PipelineStage =
  | "issue-pickup"
  | "feature-planning"
  | "feature-dev"
  | "feature-validate"
  | "pr-create"
  | "pr-merge";

export const STAGE_ORDER: PipelineStage[] = [
  "issue-pickup",
  "feature-planning",
  "feature-dev",
  "feature-validate",
  "pr-create",
  "pr-merge",
];

export type PipelineStageStatus = "pending" | "running" | "complete" | "failed" | "skipped";

export interface StageEvent {
  issueNumber: number;
  stage: PipelineStage;
  status: PipelineStageStatus;
  error?: string;
}

export interface PipelineRunOptions {
  owner: string;
  repo: string;
  issueNumber: number;
  fromStage?: PipelineStage;
  targetBranch?: string;
  model?: string;
  adapter?: string;
}

// ---------------------------------------------------------------------------
// PipelineService
// ---------------------------------------------------------------------------

export class PipelineService implements vscode.Disposable {
  private ipc: IpcClient;
  private disposables: vscode.Disposable[] = [];
  private running = false;
  private currentExecution: string | null = null;
  private currentIssueNumber: number | null = null;

  // Events for UI components
  private readonly _onStageChanged = new vscode.EventEmitter<StageEvent>();
  readonly onStageChanged = this._onStageChanged.event;

  private readonly _onPipelineStarted = new vscode.EventEmitter<{
    issueNumber: number;
  }>();
  readonly onPipelineStarted = this._onPipelineStarted.event;

  private readonly _onPipelineCompleted = new vscode.EventEmitter<{
    issueNumber: number;
    success: boolean;
  }>();
  readonly onPipelineCompleted = this._onPipelineCompleted.event;

  private readonly _onOutput = new vscode.EventEmitter<string>();
  readonly onOutput = this._onOutput.event;

  constructor() {
    this.ipc = IpcClient.getInstance();
    this.subscribeToEvents();
  }

  dispose(): void {
    this._onStageChanged.dispose();
    this._onPipelineStarted.dispose();
    this._onPipelineCompleted.dispose();
    this._onOutput.dispose();
    for (const d of this.disposables) d.dispose();
  }

  // -------------------------------------------------------------------------
  // Pipeline execution
  // -------------------------------------------------------------------------

  async run(options: PipelineRunOptions): Promise<RunPipelineResult> {
    const result = await this.ipc.pipelineRun(options.owner, options.repo, options.issueNumber, {
      fromStage: options.fromStage,
      targetBranch: options.targetBranch,
      model: options.model,
      adapter: options.adapter,
    });

    this.running = true;
    this.currentExecution = result.executionId;
    this.currentIssueNumber = options.issueNumber;
    this._onPipelineStarted.fire({ issueNumber: options.issueNumber });

    return result;
  }

  async stop(): Promise<void> {
    if (!this.currentExecution) return;
    await this.ipc.pipelineStop(this.currentExecution);
    this.running = false;
    if (this.currentIssueNumber !== null) {
      this._onPipelineCompleted.fire({
        issueNumber: this.currentIssueNumber,
        success: false,
      });
    }
    this.currentExecution = null;
    this.currentIssueNumber = null;
  }

  async pause(): Promise<void> {
    if (!this.currentExecution) return;
    await this.ipc.pipelinePause(this.currentExecution);
  }

  async resume(): Promise<void> {
    if (!this.currentExecution) return;
    await this.ipc.pipelineResume(this.currentExecution);
  }

  async getState(owner: string, repo: string, issueNumber: number): Promise<unknown> {
    return this.ipc.pipelineGetState(owner, repo, issueNumber);
  }

  isRunning(): boolean {
    return this.running;
  }

  getCurrentIssueNumber(): number | null {
    return this.currentIssueNumber;
  }

  // -------------------------------------------------------------------------
  // Execution listing
  // -------------------------------------------------------------------------

  async listExecutions(): Promise<ExecutionInfo[]> {
    return this.ipc.executionList();
  }

  // -------------------------------------------------------------------------
  // Queue management
  // -------------------------------------------------------------------------

  async queueAdd(
    owner: string,
    repo: string,
    issueNumber: number,
    title?: string,
    labels?: string[]
  ): Promise<void> {
    await this.ipc.queueAdd(owner, repo, issueNumber, title, labels);
  }

  async queueList(): Promise<unknown> {
    return this.ipc.queueList();
  }

  async queueRemove(issueNumber: number): Promise<void> {
    await this.ipc.queueRemove(issueNumber);
  }

  async queueClear(): Promise<void> {
    await this.ipc.queueClear();
  }

  // -------------------------------------------------------------------------
  // Event subscription from Go binary
  // -------------------------------------------------------------------------

  private subscribeToEvents(): void {
    // Stage change events from Go
    this.disposables.push(
      this.ipc.on("stage.start", (data: unknown) => {
        const d = data as {
          issueNumber: number;
          stage: string;
        };
        this._onStageChanged.fire({
          issueNumber: d.issueNumber,
          stage: d.stage as PipelineStage,
          status: "running",
        });
      })
    );

    this.disposables.push(
      this.ipc.on("stage.complete", (data: unknown) => {
        const d = data as {
          issueNumber: number;
          stage: string;
        };
        this._onStageChanged.fire({
          issueNumber: d.issueNumber,
          stage: d.stage as PipelineStage,
          status: "complete",
        });
      })
    );

    this.disposables.push(
      this.ipc.on("stage.failed", (data: unknown) => {
        const d = data as {
          issueNumber: number;
          stage: string;
          error: string;
        };
        this._onStageChanged.fire({
          issueNumber: d.issueNumber,
          stage: d.stage as PipelineStage,
          status: "failed",
          error: d.error,
        });
      })
    );

    // Pipeline completion event
    this.disposables.push(
      this.ipc.on("pipeline.complete", (data: unknown) => {
        const d = data as { issueNumber: number; success: boolean };
        this.running = false;
        this.currentExecution = null;
        this.currentIssueNumber = null;
        this._onPipelineCompleted.fire({
          issueNumber: d.issueNumber,
          success: d.success,
        });
      })
    );

    this.disposables.push(
      this.ipc.on("pipeline.error", (data: unknown) => {
        const d = data as {
          issueNumber: number;
          error: string;
        };
        this.running = false;
        this.currentExecution = null;
        this.currentIssueNumber = null;
        this._onPipelineCompleted.fire({
          issueNumber: d.issueNumber,
          success: false,
        });
      })
    );

    // Output streaming
    this.disposables.push(
      this.ipc.on("output", (data: unknown) => {
        const d = data as { text: string };
        this._onOutput.fire(d.text);
      })
    );
  }
}
