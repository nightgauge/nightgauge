/**
 * SavedQueriesService - Manages saved queries in .nightgauge/saved-queries.yaml
 *
 * Provides CRUD operations for saved queries, with support for both
 * repo-level queries (shared with team) and built-in queries.
 */

import * as vscode from "vscode";
import * as fs from "fs";
import * as path from "path";
import * as yaml from "yaml";
import { type SavedQuery, type SavedQueriesFile, SavedQueriesFileSchema } from "@nightgauge/sdk";
import { type SavedQueryWithMeta, BUILTIN_QUERIES } from "../types/QueryTypes";

/**
 * SavedQueriesService - Manages saved queries
 *
 * @example
 * ```typescript
 * const service = new SavedQueriesService(workspaceRoot);
 *
 * // Get all saved queries
 * const queries = await service.getAll();
 *
 * // Save a new query
 * await service.save({
 *   name: 'Sprint Backlog',
 *   query: 'status:ready AND labels:sprint-42',
 * });
 *
 * // Delete a query
 * await service.delete('Sprint Backlog');
 * ```
 */
export class SavedQueriesService implements vscode.Disposable {
  private readonly _onQueriesChanged = new vscode.EventEmitter<SavedQueryWithMeta[]>();
  readonly onQueriesChanged = this._onQueriesChanged.event;

  private queries: SavedQueryWithMeta[] = [];
  private fileWatcher: vscode.FileSystemWatcher | null = null;
  private disposables: vscode.Disposable[] = [];

  constructor(
    private readonly workspaceRoot: string,
    private readonly includeBuiltIn: boolean = true
  ) {
    // Set up file watcher
    this.setupFileWatcher();

    // Load initial queries
    this.load();
  }

  /**
   * Get the path to the saved queries file
   */
  private getFilePath(): string {
    return path.join(this.workspaceRoot, ".nightgauge", "saved-queries.yaml");
  }

  /**
   * Set up file watcher for the saved queries file
   */
  private setupFileWatcher(): void {
    const pattern = new vscode.RelativePattern(
      this.workspaceRoot,
      ".nightgauge/saved-queries.yaml"
    );

    this.fileWatcher = vscode.workspace.createFileSystemWatcher(pattern);

    this.disposables.push(
      this.fileWatcher.onDidChange(() => this.load()),
      this.fileWatcher.onDidCreate(() => this.load()),
      this.fileWatcher.onDidDelete(() => this.load())
    );

    this.disposables.push(this.fileWatcher);
  }

  /**
   * Load saved queries from file
   */
  private load(): void {
    const filePath = this.getFilePath();

    // Start with built-in queries if enabled
    this.queries = this.includeBuiltIn ? [...BUILTIN_QUERIES] : [];

    // Load from file if it exists
    if (fs.existsSync(filePath)) {
      try {
        const content = fs.readFileSync(filePath, "utf-8");
        const data = yaml.parse(content);
        const parsed = SavedQueriesFileSchema.parse(data);

        // Merge with built-in queries
        for (const query of parsed.queries) {
          // Skip if this is overriding a built-in query
          const existingIndex = this.queries.findIndex((q) => q.name === query.name);
          if (existingIndex >= 0) {
            this.queries[existingIndex] = {
              ...query,
              isBuiltIn: false,
            };
          } else {
            this.queries.push({
              ...query,
              isBuiltIn: false,
            });
          }
        }
      } catch (error) {
        console.error("Failed to load saved queries:", error);
      }
    }

    // Fire change event
    this._onQueriesChanged.fire(this.queries);
  }

  /**
   * Save queries to file
   */
  private async saveToFile(): Promise<void> {
    const filePath = this.getFilePath();
    const incrediDir = path.dirname(filePath);

    // Ensure .nightgauge directory exists
    if (!fs.existsSync(incrediDir)) {
      fs.mkdirSync(incrediDir, { recursive: true });
    }

    // Filter out built-in queries
    const userQueries = this.queries.filter((q) => !q.isBuiltIn);

    const data: SavedQueriesFile = {
      version: "1.0",
      queries: userQueries.map(({ isBuiltIn, runCount, ...query }) => query),
    };

    const content = yaml.stringify(data);
    fs.writeFileSync(filePath, content, "utf-8");
  }

  /**
   * Get all saved queries (including built-in)
   */
  getAll(): SavedQueryWithMeta[] {
    return [...this.queries];
  }

  /**
   * Get user-defined queries only (excluding built-in)
   */
  getUserQueries(): SavedQueryWithMeta[] {
    return this.queries.filter((q) => !q.isBuiltIn);
  }

  /**
   * Get built-in queries only
   */
  getBuiltInQueries(): SavedQueryWithMeta[] {
    return this.queries.filter((q) => q.isBuiltIn);
  }

  /**
   * Get a query by name
   */
  get(name: string): SavedQueryWithMeta | undefined {
    return this.queries.find((q) => q.name === name);
  }

  /**
   * Save or update a query
   *
   * @param query - Query to save
   * @returns The saved query
   */
  async save(query: Omit<SavedQuery, "createdAt">): Promise<SavedQueryWithMeta> {
    const now = new Date().toISOString();

    // Find existing
    const existingIndex = this.queries.findIndex((q) => q.name === query.name);

    const savedQuery: SavedQueryWithMeta = {
      ...query,
      createdAt: existingIndex >= 0 ? this.queries[existingIndex].createdAt : now,
      lastUsedAt: now,
      isBuiltIn: false,
    };

    if (existingIndex >= 0) {
      // Update existing
      this.queries[existingIndex] = savedQuery;
    } else {
      // Add new
      this.queries.push(savedQuery);
    }

    // Save to file
    await this.saveToFile();

    // Fire change event
    this._onQueriesChanged.fire(this.queries);

    return savedQuery;
  }

  /**
   * Delete a query by name
   *
   * @param name - Name of the query to delete
   * @returns True if deleted, false if not found
   */
  async delete(name: string): Promise<boolean> {
    const index = this.queries.findIndex((q) => q.name === name && !q.isBuiltIn);

    if (index < 0) {
      return false;
    }

    this.queries.splice(index, 1);

    // Save to file
    await this.saveToFile();

    // Fire change event
    this._onQueriesChanged.fire(this.queries);

    return true;
  }

  /**
   * Rename a query
   *
   * @param oldName - Current name
   * @param newName - New name
   * @returns True if renamed, false if not found or new name exists
   */
  async rename(oldName: string, newName: string): Promise<boolean> {
    const query = this.queries.find((q) => q.name === oldName && !q.isBuiltIn);

    if (!query) {
      return false;
    }

    // Check if new name already exists
    if (this.queries.some((q) => q.name === newName)) {
      return false;
    }

    query.name = newName;

    // Save to file
    await this.saveToFile();

    // Fire change event
    this._onQueriesChanged.fire(this.queries);

    return true;
  }

  /**
   * Record that a query was used
   *
   * @param name - Name of the query
   */
  async recordUsage(name: string): Promise<void> {
    const query = this.queries.find((q) => q.name === name);
    if (query) {
      query.lastUsedAt = new Date().toISOString();
      query.runCount = (query.runCount ?? 0) + 1;

      if (!query.isBuiltIn) {
        await this.saveToFile();
      }

      this._onQueriesChanged.fire(this.queries);
    }
  }

  /**
   * Import queries from another file
   *
   * @param filePath - Path to the file to import
   * @returns Number of queries imported
   */
  async import(filePath: string): Promise<number> {
    try {
      const content = fs.readFileSync(filePath, "utf-8");
      const data = yaml.parse(content);
      const parsed = SavedQueriesFileSchema.parse(data);

      let count = 0;
      for (const query of parsed.queries) {
        await this.save(query);
        count++;
      }

      return count;
    } catch (error) {
      throw new Error(
        `Failed to import queries: ${error instanceof Error ? error.message : String(error)}`,
        { cause: error }
      );
    }
  }

  /**
   * Export queries to a file
   *
   * @param filePath - Path to export to
   * @param includeBuiltIn - Whether to include built-in queries
   */
  async export(filePath: string, includeBuiltIn: boolean = false): Promise<void> {
    const queries = includeBuiltIn ? this.queries : this.queries.filter((q) => !q.isBuiltIn);

    const data: SavedQueriesFile = {
      version: "1.0",
      queries: queries.map(({ isBuiltIn, runCount, ...query }) => query),
    };

    const content = yaml.stringify(data);
    fs.writeFileSync(filePath, content, "utf-8");
  }

  /**
   * Refresh queries from file
   */
  refresh(): void {
    this.load();
  }

  /**
   * Dispose of resources
   */
  dispose(): void {
    this._onQueriesChanged.dispose();
    this.disposables.forEach((d) => d.dispose());
  }
}
