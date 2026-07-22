/**
 * TelemetryStore - Service layer for JSONL execution history
 *
 * Owns index management, read orchestration, and provides
 * the canonical data source for the dashboard history display.
 * Replaces Memento as the primary data source for pipeline history.
 *
 * Design decisions:
 * - Plain class (no VSCode API dependency) — testable without mocks
 * - In-memory cache for index (invalidated on write)
 * - Index rebuilt from JSONL if missing or corrupt
 * - Staleness detected by comparing index updated_at vs latest JSONL mtime
 *
 * @see Issue #1007 - Make JSONL the canonical data source
 * @see docs/ARCHITECTURE.md for service patterns
 */

import * as fs from "node:fs/promises";
import * as path from "node:path";

import { ExecutionHistoryReader, type NormalizedRunRecord } from "../utils/executionHistoryReader";
import {
  ExecutionHistoryWriter,
  type HistoryIndex,
  type HistoryIndexEntry,
} from "../utils/executionHistoryWriter";
import type { ExecutionHistoryRecord } from "../schemas/executionHistory";

export type { HistoryIndex, HistoryIndexEntry };

const INDEX_FILENAME = "index.json";

export class TelemetryStore {
  private indexCache: HistoryIndex | null = null;

  constructor(private readonly workspaceRoot: string) {}

  /**
   * Get the history index, loading from disk or cache.
   * If the index is missing or corrupt, rebuilds from JSONL files.
   */
  async getIndex(): Promise<HistoryIndex> {
    if (this.indexCache) return this.indexCache;

    const indexPath = this.getIndexPath();
    try {
      const content = await fs.readFile(indexPath, "utf-8");
      const parsed = JSON.parse(content) as HistoryIndex;
      if (parsed.schema_version && Array.isArray(parsed.entries)) {
        // Check staleness: compare index updated_at vs latest JSONL mtime
        const isStale = await this.isIndexStale(parsed);
        if (!isStale) {
          this.indexCache = parsed;
          return parsed;
        }
      }
    } catch (err) {
      console.warn(`[TelemetryStore] Index missing or corrupt, rebuilding: ${err}`);
    }

    return this.rebuildIndex();
  }

  /**
   * Rebuild the index from all JSONL files on disk.
   * This is the recovery path when the index is missing, corrupt, or stale.
   */
  async rebuildIndex(): Promise<HistoryIndex> {
    const allRecords = await ExecutionHistoryReader.readAll(this.workspaceRoot);
    // Collapse duplicate run records for a run to the single richest one before
    // projecting to index entries, so rebuilding from the append-only JSONL
    // source of truth yields exactly one entry per run (Issue #313).
    const runRecords = ExecutionHistoryWriter.dedupeRichestRunRecords(
      allRecords.filter((r) => r.record_type === "run") as NormalizedRunRecord[]
    );
    const entries: HistoryIndexEntry[] = runRecords.map((r) =>
      ExecutionHistoryWriter.buildIndexEntry(r)
    );

    // Sort by started_at descending (most recent first)
    entries.sort((a, b) => b.started_at.localeCompare(a.started_at));

    const index: HistoryIndex = {
      schema_version: "1",
      updated_at: new Date().toISOString(),
      total_runs: entries.length,
      entries,
    };

    await this.writeIndex(index);
    this.indexCache = index;
    return index;
  }

  /**
   * Get all run summaries from the index, filtering out ghost/orchestration
   * entries created by concurrent pipeline coordinators.
   *
   * Ghost records are identified by having zero cost AND zero total tokens —
   * they represent orchestration overhead, not actual pipeline work.
   */
  async getAllRunSummaries(): Promise<HistoryIndexEntry[]> {
    const index = await this.getIndex();
    return index.entries.filter((entry) => !isGhostEntry(entry));
  }

  /**
   * Get a paginated slice of run summaries from the index.
   * Ghost/orchestration entries are filtered out before pagination.
   */
  async getRunSummariesPage(
    offset: number,
    limit: number
  ): Promise<{ items: HistoryIndexEntry[]; total: number; hasMore: boolean }> {
    const index = await this.getIndex();
    const filtered = index.entries.filter((entry) => !isGhostEntry(entry));
    const total = filtered.length;
    const items = filtered.slice(offset, offset + limit);
    return { items, total, hasMore: offset + limit < total };
  }

  /**
   * Get a full run record from JSONL by issue number.
   * Used for on-demand detail loading when a user expands a run.
   */
  async getRunRecord(issueNumber: number): Promise<NormalizedRunRecord | undefined> {
    const records = await ExecutionHistoryReader.readForIssue(this.workspaceRoot, issueNumber);
    const runRecords = records.filter((r) => r.record_type === "run") as NormalizedRunRecord[];
    if (runRecords.length === 0) return undefined;
    // Prefer the record with per-stage token data — when the Go scheduler writes
    // a zero-token "complete" ghost after a cancelled run that already has real
    // per_stage data, returning the ghost leaves hydrateRunTokenData with nothing
    // to populate. Pick the last record that has per_stage, fall back to the
    // last record overall.
    const withPerStage = runRecords.filter((r) => {
      const ps = r.tokens?.per_stage as Record<string, unknown> | undefined;
      return ps && Object.keys(ps).length > 0;
    });
    return withPerStage.length > 0
      ? withPerStage[withPerStage.length - 1]
      : runRecords[runRecords.length - 1];
  }

  /**
   * Append a run record to JSONL and update the index.
   * Delegates to ExecutionHistoryWriter (which handles both JSONL append and index update).
   */
  async appendRunRecord(record: ExecutionHistoryRecord): Promise<boolean> {
    const written = await ExecutionHistoryWriter.appendRecord(this.workspaceRoot, record);
    // Invalidate cache so next read picks up the new entry
    if (written) {
      this.indexCache = null;
    }
    return written;
  }

  /**
   * Delete old JSONL files beyond retention period.
   */
  async cleanupOldFiles(retentionDays?: number): Promise<{ deleted: string[] }> {
    const result = await ExecutionHistoryWriter.cleanupOldFiles(this.workspaceRoot, retentionDays);
    if (result.deleted.length > 0) {
      // Index may reference deleted records — rebuild
      this.indexCache = null;
    }
    return result;
  }

  /**
   * Invalidate the cached index so the next read reloads from disk.
   */
  invalidateCache(): void {
    this.indexCache = null;
  }

  /**
   * Check if the index is stale by comparing updated_at vs latest JSONL mtime
   * and verifying entry count matches the number of JSONL files on disk.
   *
   * The entry-count check catches the case where updateIndex() created a
   * fresh index with only 1 entry (because the old index was missing) —
   * the timestamp matches but most records are absent.
   */
  private async isIndexStale(index: HistoryIndex): Promise<boolean> {
    try {
      const historyDir = ExecutionHistoryWriter.getHistoryDir(this.workspaceRoot);
      const files = await fs.readdir(historyDir);
      const jsonlFiles = files
        .filter((f) => f.endsWith(".jsonl"))
        .sort()
        .reverse();
      if (jsonlFiles.length === 0) return false;

      // Check mtime of most recent JSONL file
      const latestFile = path.join(historyDir, jsonlFiles[0]);
      const stat = await fs.stat(latestFile);
      const indexTime = new Date(index.updated_at).getTime();
      const fileMtime = stat.mtimeMs;

      // Stale if JSONL file was modified after index was last updated
      if (fileMtime > indexTime) return true;

      // Stale if index has fewer entries than JSONL files.
      // Each JSONL file has at least 1 run record, so if the index has
      // fewer entries than files, it's clearly incomplete.
      if (index.entries.length < jsonlFiles.length) return true;

      // Quick-count run records in the most recent JSONL file to catch
      // the case where multiple records are in a single file but the
      // index wasn't updated (Issue #2252: 287 entries > 22 files passed
      // the above check even though 5 new records were missing).
      try {
        const latestContent = await fs.readFile(latestFile, "utf-8");
        let runCountInLatest = 0;
        for (const line of latestContent.split("\n")) {
          if (!line.trim()) continue;
          try {
            if (JSON.parse(line).record_type === "run") runCountInLatest++;
          } catch {
            // skip malformed lines
          }
        }
        // Count how many index entries match the latest file's date
        const latestDate = jsonlFiles[0].replace(".jsonl", "");
        const indexEntriesForDate = index.entries.filter((e) =>
          e.recorded_at?.startsWith(latestDate)
        ).length;
        if (runCountInLatest > indexEntriesForDate) return true;
      } catch {
        // Can't read latest file — assume stale
        return true;
      }

      return false;
    } catch (err) {
      console.warn(`[TelemetryStore] Can't determine staleness, assuming stale: ${err}`);
      return true;
    }
  }

  private getIndexPath(): string {
    return path.join(ExecutionHistoryWriter.getHistoryDir(this.workspaceRoot), INDEX_FILENAME);
  }

  private async writeIndex(index: HistoryIndex): Promise<void> {
    const indexPath = this.getIndexPath();
    const historyDir = ExecutionHistoryWriter.getHistoryDir(this.workspaceRoot);

    // Ensure directory exists
    await fs.mkdir(historyDir, { recursive: true });

    // Write atomically (temp file + rename)
    const tempPath = indexPath + ".tmp";
    await fs.writeFile(tempPath, JSON.stringify(index, null, 2), "utf-8");
    await fs.rename(tempPath, indexPath);
  }
}

/**
 * Detect ghost/orchestration records created by concurrent pipeline coordinators.
 *
 * These records appear when:
 * - The Go scheduler's defer block writes a partial record on early exit
 * - A concurrent batch coordinator records orchestration overhead
 *
 * Ghost records are characterized by zero cost AND zero total tokens,
 * meaning no actual LLM work was performed in that run.
 */
export function isGhostEntry(entry: HistoryIndexEntry): boolean {
  const totalTokens =
    (entry.total_input_tokens ?? 0) +
    (entry.total_output_tokens ?? 0) +
    (entry.total_cache_read_tokens ?? 0) +
    (entry.total_cache_creation_tokens ?? 0);

  return entry.cost_usd === 0 && totalTokens === 0;
}
