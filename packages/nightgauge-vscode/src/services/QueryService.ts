/**
 * QueryService - Orchestrates query execution in VSCode
 *
 * Bridges the SDK query parser with the VSCode extension,
 * managing query state, history, and UI integration.
 */

import * as vscode from "vscode";
import {
  executeQuery,
  validate as validateQuery,
  type QueryResult,
  type QueryableIssue,
  type QueryError,
} from "@nightgauge/sdk";
import type { ReadyIssue } from "./ProjectBoardService";
import type { IWorkItemProvider } from "./types/WorkItemProvider";
import {
  type QueryContext,
  type QueryHistoryEntry,
  type QueryServiceConfig,
  DEFAULT_QUERY_CONFIG,
  toQueryableIssues,
} from "../types/QueryTypes";

/**
 * QueryService - Manages query execution and state
 *
 * @example
 * ```typescript
 * const service = new QueryService(projectBoardService);
 *
 * // Execute a query
 * const result = await service.execute('status:ready AND priority:P0');
 *
 * // Get query history
 * const history = service.getHistory();
 * ```
 */
export class QueryService implements vscode.Disposable {
  private readonly _onQueryStateChanged = new vscode.EventEmitter<QueryContext>();
  readonly onQueryStateChanged = this._onQueryStateChanged.event;

  private readonly _onQueryComplete = new vscode.EventEmitter<QueryResult>();
  readonly onQueryComplete = this._onQueryComplete.event;

  private readonly _onQueryError = new vscode.EventEmitter<string>();
  readonly onQueryError = this._onQueryError.event;

  private context: QueryContext = {
    query: "",
    state: "idle",
  };

  private history: QueryHistoryEntry[] = [];
  private config: QueryServiceConfig;
  private disposables: vscode.Disposable[] = [];
  private cachedIssues: QueryableIssue[] = [];

  constructor(
    private readonly projectBoardService: IWorkItemProvider,
    private readonly workspaceState: vscode.Memento,
    config?: Partial<QueryServiceConfig>
  ) {
    this.config = { ...DEFAULT_QUERY_CONFIG, ...config };

    // Load history from workspace state
    this.loadHistory();

    // Listen for project board changes to update cache
    this.disposables
      .push
      // Refresh cache when project board is refreshed
      // Note: ProjectBoardService doesn't expose events, so we rely on manual refresh
      ();
  }

  /**
   * Get current query context
   */
  getContext(): QueryContext {
    return { ...this.context };
  }

  /**
   * Get current query string
   */
  getCurrentQuery(): string {
    return this.context.query;
  }

  /**
   * Get query history
   */
  getHistory(): QueryHistoryEntry[] {
    return [...this.history];
  }

  /**
   * Validate a query string
   *
   * @param query - Query string to validate
   * @returns Array of errors, empty if valid
   */
  validate(query: string): QueryError[] {
    return validateQuery(query);
  }

  /**
   * Execute a query against project board issues
   *
   * @param query - Query string to execute
   * @param status - Optional status filter to narrow the source issues
   * @returns Query result
   */
  async execute(
    query: string,
    status?: "ready" | "in-progress" | "in-review" | "backlog"
  ): Promise<QueryResult> {
    // Update state to parsing
    this.updateContext({
      query,
      state: "parsing",
      result: undefined,
      error: undefined,
    });

    // Validate query
    const errors = this.validate(query);
    if (errors.length > 0) {
      const errorMessage = errors.map((e) => e.message).join("; ");
      this.updateContext({
        state: "error",
        error: errorMessage,
      });
      this._onQueryError.fire(errorMessage);
      throw new Error(errorMessage);
    }

    // Update state to executing
    this.updateContext({ state: "executing" });

    try {
      // Fetch issues from project board
      const issues = await this.fetchIssues(status);

      // Execute query
      const result = executeQuery(query, issues);

      // Update state
      this.updateContext({
        state: "complete",
        result,
        executedAt: new Date(),
      });

      // Add to history
      this.addToHistory({
        query,
        executedAt: new Date(),
        resultCount: result.matchCount,
      });

      // Fire completion event
      this._onQueryComplete.fire(result);

      return result;
    } catch (error) {
      const errorMessage = error instanceof Error ? error.message : String(error);
      this.updateContext({
        state: "error",
        error: errorMessage,
      });
      this._onQueryError.fire(errorMessage);
      throw error;
    }
  }

  /**
   * Re-execute the last query
   */
  async reExecute(): Promise<QueryResult | null> {
    if (!this.context.query) {
      return null;
    }
    return this.execute(this.context.query);
  }

  /**
   * Clear current query and results
   */
  clear(): void {
    this.updateContext({
      query: "",
      state: "idle",
      result: undefined,
      error: undefined,
      executedAt: undefined,
    });
  }

  /**
   * Clear query history
   */
  clearHistory(): void {
    this.history = [];
    this.saveHistory();
  }

  /**
   * Fetch issues from project board service
   */
  private async fetchIssues(
    status?: "ready" | "in-progress" | "in-review" | "backlog"
  ): Promise<QueryableIssue[]> {
    // Map status to the service method
    let issues: ReadyIssue[];

    try {
      if (status) {
        // Fetch specific status
        issues = await this.projectBoardService.getIssuesByStatus(status);
      } else {
        // Fetch all statuses and combine
        const [ready, inProgress, inReview, backlog] = await Promise.all([
          this.projectBoardService.getIssuesByStatus("ready"),
          this.projectBoardService.getIssuesByStatus("in-progress"),
          this.projectBoardService.getIssuesByStatus("in-review"),
          this.projectBoardService.getIssuesByStatus("backlog"),
        ]);
        issues = [...ready, ...inProgress, ...inReview, ...backlog];
      }

      // Convert to QueryableIssue format
      this.cachedIssues = toQueryableIssues(issues);

      // Enrich with status information — use project field value when
      // available, fall back to the status parameter from the caller.
      if (status) {
        this.cachedIssues = this.cachedIssues.map((issue) => ({
          ...issue,
          status: issue.status || status,
        }));
      }

      return this.cachedIssues;
    } catch (error) {
      // Return cached issues if fetch fails
      if (this.cachedIssues.length > 0) {
        return this.cachedIssues;
      }
      throw error;
    }
  }

  /**
   * Update query context and fire event
   */
  private updateContext(updates: Partial<QueryContext>): void {
    this.context = { ...this.context, ...updates };
    this._onQueryStateChanged.fire(this.context);
  }

  /**
   * Add entry to history
   */
  private addToHistory(entry: QueryHistoryEntry): void {
    // Remove duplicates
    this.history = this.history.filter((h) => h.query !== entry.query);

    // Add to front
    this.history.unshift(entry);

    // Trim to max entries
    if (this.history.length > this.config.maxHistoryEntries) {
      this.history = this.history.slice(0, this.config.maxHistoryEntries);
    }

    // Save
    this.saveHistory();
  }

  /**
   * Load history from workspace state
   */
  private loadHistory(): void {
    const saved = this.workspaceState.get<QueryHistoryEntry[]>("nightgauge.queryHistory", []);
    this.history = saved.map((entry) => ({
      ...entry,
      executedAt: new Date(entry.executedAt),
    }));
  }

  /**
   * Save history to workspace state
   */
  private saveHistory(): void {
    this.workspaceState.update("nightgauge.queryHistory", this.history);
  }

  /**
   * Dispose of resources
   */
  dispose(): void {
    this._onQueryStateChanged.dispose();
    this._onQueryComplete.dispose();
    this._onQueryError.dispose();
    this.disposables.forEach((d) => d.dispose());
  }
}
