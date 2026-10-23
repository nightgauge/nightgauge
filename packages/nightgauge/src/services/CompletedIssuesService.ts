/**
 * CompletedIssuesService - Manage completed and failed issue history
 *
 * This singleton service tracks completed and failed pipeline runs, persisting
 * state to workspace storage (survives VS Code restarts). It provides event-driven
 * state synchronization for tree view updates.
 *
 * @see Issue #301 - Handle completed and failed issue states in pipeline
 * @see docs/ARCHITECTURE.md - Context-Isolated Pipeline Architecture
 */

import * as vscode from "vscode";
import type {
  CompletedIssuesState,
  IssueReference,
  FailedIssueReference,
} from "../types/completedIssues";
import {
  MAX_COMPLETED_ISSUES,
  createInitialState,
  createIssueReference,
  createFailedIssueReference,
} from "../types/completedIssues";

/**
 * Workspace storage keys
 */
const STORAGE_KEY = "nightgauge.devpletedIssues";
const SCHEMA_VERSION = "1.0";

/**
 * CompletedIssuesService - Singleton service for completed/failed issue tracking
 *
 * @example
 * ```typescript
 * const service = CompletedIssuesService.getInstance(context.workspaceState);
 *
 * // Track completed issue
 * service.addCompleted(42, 'Add dark mode', 'feat/42-dark-mode');
 *
 * // Track failed issue
 * service.addFailed(43, 'Add auth', 'feat/43-auth', 'pr-create', 'PR creation failed');
 *
 * // Subscribe to changes
 * service.onStateChanged((state) => {
 *   console.log(`${state.completed.length} completed, ${state.failed.length} failed`);
 * });
 * ```
 */
export class CompletedIssuesService implements vscode.Disposable {
  private static instance: CompletedIssuesService | null = null;

  private workspaceState: vscode.Memento | null;
  private state: CompletedIssuesState;
  private disposables: vscode.Disposable[] = [];

  // Event emitters
  private _onStateChanged = new vscode.EventEmitter<CompletedIssuesState>();

  /**
   * Fired when completed/failed state changes
   */
  readonly onStateChanged = this._onStateChanged.event;

  private constructor(workspaceState?: vscode.Memento) {
    this.workspaceState = workspaceState || null;
    this.state = createInitialState();

    // Load persisted state
    this.loadState();
  }

  /**
   * Get the singleton instance
   */
  static getInstance(workspaceState?: vscode.Memento): CompletedIssuesService {
    if (!CompletedIssuesService.instance) {
      CompletedIssuesService.instance = new CompletedIssuesService(workspaceState);
    }
    return CompletedIssuesService.instance;
  }

  /**
   * Reset the singleton (for testing)
   */
  static resetInstance(): void {
    if (CompletedIssuesService.instance) {
      CompletedIssuesService.instance.dispose();
      CompletedIssuesService.instance = null;
    }
  }

  /**
   * Add issue to completed list
   */
  addCompleted(
    issueNumber: number,
    title: string,
    branch: string,
    labels?: string[],
    costAnomalyExceeded?: boolean
  ): void {
    // Prevent duplicates (same issue number)
    const existingIndex = this.state.completed.findIndex(
      (item) => item.issue_number === issueNumber
    );

    if (existingIndex !== -1) {
      // Update existing entry (move to front)
      const existing = this.state.completed[existingIndex];
      this.state.completed.splice(existingIndex, 1);
      this.state.completed.unshift({
        ...existing,
        timestamp: new Date().toISOString(),
        ...(labels && labels.length > 0 ? { labels } : {}),
        ...(costAnomalyExceeded ? { cost_anomaly_exceeded: true } : {}),
      });
    } else {
      // Add new entry
      const newIssue = createIssueReference(
        issueNumber,
        title,
        branch,
        labels,
        costAnomalyExceeded
      );
      this.state.completed.unshift(newIssue);

      // Limit to MAX_COMPLETED_ISSUES (FIFO eviction)
      if (this.state.completed.length > MAX_COMPLETED_ISSUES) {
        this.state.completed = this.state.completed.slice(0, MAX_COMPLETED_ISSUES);
      }
    }

    this.state.updated_at = new Date().toISOString();
    this.persistState();
    this._onStateChanged.fire(this.state);
  }

  /**
   * Add issue to failed list
   */
  addFailed(
    issueNumber: number,
    title: string,
    branch: string,
    failedStage: string,
    error: string,
    labels?: string[]
  ): void {
    // Check for existing failure (for retry count tracking)
    const existingIndex = this.state.failed.findIndex((item) => item.issue_number === issueNumber);

    let retryCount = 0;
    if (existingIndex !== -1) {
      // Increment retry count
      retryCount = this.state.failed[existingIndex].retry_count + 1;
      this.state.failed.splice(existingIndex, 1);
    }

    // Add new entry
    const newIssue = createFailedIssueReference(
      issueNumber,
      title,
      branch,
      failedStage,
      error,
      retryCount,
      labels
    );
    this.state.failed.unshift(newIssue);

    // Limit to MAX_COMPLETED_ISSUES
    if (this.state.failed.length > MAX_COMPLETED_ISSUES) {
      this.state.failed = this.state.failed.slice(0, MAX_COMPLETED_ISSUES);
    }

    this.state.updated_at = new Date().toISOString();
    this.persistState();
    this._onStateChanged.fire(this.state);
  }

  /**
   * Remove issue from failed list (for retry)
   */
  removeFromFailed(issueNumber: number): void {
    const index = this.state.failed.findIndex((item) => item.issue_number === issueNumber);

    if (index !== -1) {
      this.state.failed.splice(index, 1);
      this.state.updated_at = new Date().toISOString();
      this.persistState();
      this._onStateChanged.fire(this.state);
    }
  }

  /**
   * Clear all completed issues
   */
  clearCompleted(): void {
    this.state.completed = [];
    this.state.updated_at = new Date().toISOString();
    this.persistState();
    this._onStateChanged.fire(this.state);
  }

  /**
   * Clear all failed issues
   */
  clearFailed(): void {
    this.state.failed = [];
    this.state.updated_at = new Date().toISOString();
    this.persistState();
    this._onStateChanged.fire(this.state);
  }

  /**
   * Get all completed issues
   */
  getCompleted(): IssueReference[] {
    return [...this.state.completed];
  }

  /**
   * Get all failed issues
   */
  getFailed(): FailedIssueReference[] {
    return [...this.state.failed];
  }

  /**
   * Get failed issue by number
   */
  getFailedIssue(issueNumber: number): FailedIssueReference | undefined {
    return this.state.failed.find((item) => item.issue_number === issueNumber);
  }

  /**
   * Get current state
   */
  getState(): CompletedIssuesState {
    return { ...this.state };
  }

  /**
   * Load state from workspace storage
   */
  private loadState(): void {
    if (!this.workspaceState) {
      // No workspace state: use in-memory only
      return;
    }

    try {
      const stored = this.workspaceState.get<CompletedIssuesState>(STORAGE_KEY);

      if (stored && stored.schema_version === SCHEMA_VERSION) {
        this.state = stored;
      }
    } catch (error) {
      console.warn("[Nightgauge] Failed to load completed issues state, using empty state:", error);
      this.state = createInitialState();
    }
  }

  /**
   * Persist state to workspace storage
   */
  private persistState(): void {
    if (!this.workspaceState) {
      return;
    }

    try {
      this.workspaceState.update(STORAGE_KEY, this.state);
    } catch (error) {
      console.error("[Nightgauge] Failed to persist completed issues state:", error);
    }
  }

  /**
   * Dispose resources
   */
  dispose(): void {
    this._onStateChanged.dispose();
    this.disposables.forEach((d) => d.dispose());
    this.disposables = [];
  }
}
