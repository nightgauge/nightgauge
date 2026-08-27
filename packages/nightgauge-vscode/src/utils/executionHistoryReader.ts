/**
 * ExecutionHistoryReader - Query utility for pipeline execution history
 *
 * Static utility class for reading and querying JSONL history files.
 * Gracefully handles missing files and malformed lines.
 *
 * Reads v1, v2, and v3 records, normalizing v1 run records to the v2 field
 * set with sensible defaults.
 *
 * @see Issue #649 - Execution History Persistence
 * @see Issue #1011 - Telemetry Schema v2 (forward-compatible reads)
 * @see docs/ARCHITECTURE.md for utility patterns
 */

import * as fs from "node:fs/promises";
import * as path from "node:path";

import {
  ExecutionHistoryRecordSchema,
  ExecutionHistoryRunRecordSchema,
  ExecutionHistoryRunRecordV2Schema,
  ExecutionHistoryRunRecordV3Schema,
  type ExecutionHistoryRecord,
  type ExecutionHistoryRunRecord,
  type ExecutionHistoryRunRecordV2,
  type ExecutionHistoryRunRecordV3,
} from "../schemas/executionHistory";
import { isOrchestratorCrashRecord } from "./orchestratorCrashRecord";

import { ExecutionHistoryWriter } from "./executionHistoryWriter";

/**
 * Normalized run record type — always the v2 field set, with the original v2
 * or v3 schema discriminator preserved.
 */
export type NormalizedRunRecord = ExecutionHistoryRunRecordV2 | ExecutionHistoryRunRecordV3;

/**
 * Aggregated cost for a single issue across all pipeline runs.
 * Computed lazily from JSONL execution history — not persisted.
 *
 * @see Issue #1410 - Cost-per-issue aggregation
 */
export interface IssueCostAggregation {
  issueNumber: number;
  /** Sum of tokens.estimated_cost_usd across all runs for this issue */
  totalCostUsd: number;
  /** Total number of pipeline runs for this issue */
  runCount: number;
  /** Runs where is_recovery === true (backtracks, retries, escalations) */
  backtrackCount: number;
  /** Extracted issue type (e.g. 'feature', 'bug') — null if not labeled */
  issueType: string | null;
  /** Extracted size label (e.g. 'M', 'L') — null if not labeled */
  sizeLabel: string | null;
  /** Timestamp of the first run for this issue */
  firstRunAt: Date;
  /** Timestamp of the most recent run for this issue */
  lastRunAt: Date;
}

/**
 * Normalize a v1 run record to v2 shape by adding defaults for new fields.
 *
 * `cost_source` used to get a `cost_usd > 0 → "native"` backfill here (Issue
 * #3228). Removed by #682: the Go writer now sets `cost_source` itself on
 * every stage it completes (RuntimeState.CompleteStage /
 * CompleteStageWithCost), so an absent field is no longer "a pre-#3228 record
 * we can infer about" — it is either a genuinely legacy record or a
 * known-gap Go write path (RecordTerminatingStageTokens, #682) that has not
 * been taught to set it. Guessing `"native"` from `cost_usd > 0` in either
 * case would manufacture a confident answer this reader has no evidence for
 * — precisely the bug #682 exists to fix. `stageCostConfidence` in
 * LocalTelemetryUsageProvider.ts now treats an absent `cost_source` as
 * `"unknown"`, not `"measured"`.
 */
function normalizeRunRecordToV2(v1: ExecutionHistoryRunRecord): ExecutionHistoryRunRecordV2 {
  return {
    ...v1,
    schema_version: "2" as const,
    record_type: "run" as const,
    outcome_type: undefined,
    tool_calls: undefined,
    files: {
      read_count: v1.files?.read_count ?? 0,
      written_count: v1.files?.written_count ?? 0,
    },
    routing: {
      complexity_score: v1.routing?.complexity_score ?? 0,
      path: v1.routing?.path ?? "unknown",
      skip_stages: v1.routing?.skip_stages ?? [],
    },
  };
}

/**
 * Number of JSONL lines to parse before yielding to the event loop.
 * Prevents the extension host from becoming unresponsive during large
 * history file parsing (Zod validation is CPU-intensive).
 */
const YIELD_BATCH_SIZE = 50;

export class ExecutionHistoryReader {
  /** Short-lived cache to deduplicate redundant readAll() calls within the same event cascade. */
  private static _readAllCache = new Map<
    string,
    { records: ExecutionHistoryRecord[]; expiresAt: number }
  >();

  private static readonly READ_ALL_CACHE_TTL_MS = 5_000;

  /**
   * Per-file parse cache keyed by absolute path. Values carry the mtime and
   * size observed when the file was parsed — on subsequent reads we stat the
   * file and only re-parse when either value has changed.
   *
   * Why this exists: JSONL history files are daily-rotated and immutable once
   * the day ends. `readAll` walks 40+ files, each requiring Zod `safeParse`
   * per line. Before this cache, every `readAll` re-parsed all 1,700+ lines
   * even though only today's file ever actually changed — CPU profiles
   * captured `parseJsonlFile` taking >1.5 s of self time per call, which was
   * the top remaining trigger of VSCode's UNRESPONSIVE detector after the
   * OutputWindow debounce (#2704).
   *
   * Bounded to `PARSE_CACHE_MAX_ENTRIES` via insertion-order eviction —
   * Map preserves insertion order, so we delete+set on hit to mark recency
   * and drop the oldest entry once over the limit.
   */
  private static _parseCache = new Map<
    string,
    { mtimeMs: number; size: number; records: ExecutionHistoryRecord[] }
  >();

  private static readonly PARSE_CACHE_MAX_ENTRIES = 60;

  /** @internal Clear all caches (for tests). */
  static clearCache(): void {
    this._readAllCache.clear();
    this._parseCache.clear();
  }
  /**
   * Read all records from JSONL files within a date range (inclusive).
   *
   * @param workspaceRoot - Absolute path to repository root
   * @param startDate - Start date (inclusive)
   * @param endDate - End date (inclusive)
   * @returns Parsed records sorted by recorded_at ascending
   */
  static async readDateRange(
    workspaceRoot: string,
    startDate: Date,
    endDate: Date
  ): Promise<ExecutionHistoryRecord[]> {
    const historyDir = ExecutionHistoryWriter.getHistoryDir(workspaceRoot);
    const records: ExecutionHistoryRecord[] = [];

    // Iterate day-by-day through the range using UTC to match filename generation
    const current = new Date(startDate);
    current.setUTCHours(0, 0, 0, 0);
    const end = new Date(endDate);
    end.setUTCHours(23, 59, 59, 999);

    while (current <= end) {
      const filename = ExecutionHistoryWriter.getFilenameForDate(current);
      const filePath = path.join(historyDir, filename);
      const parsed = await this.parseJsonlFile(filePath);
      records.push(...parsed);
      current.setUTCDate(current.getUTCDate() + 1);
    }

    return records.sort((a, b) => a.recorded_at.localeCompare(b.recorded_at));
  }

  /**
   * Read all history records from the history directory.
   *
   * @param workspaceRoot - Absolute path to repository root
   * @returns All parsed records sorted by recorded_at ascending
   */
  static async readAll(workspaceRoot: string): Promise<ExecutionHistoryRecord[]> {
    // Check short-lived cache to avoid redundant re-parsing within the same
    // event cascade (e.g. PostPipelineAnalyzer calls readAll then getCostByIssue
    // which calls readAll again internally).
    const cached = this._readAllCache.get(workspaceRoot);
    if (cached && Date.now() < cached.expiresAt) {
      return cached.records;
    }

    const historyDir = ExecutionHistoryWriter.getHistoryDir(workspaceRoot);
    const files = await this.listHistoryFiles(workspaceRoot);
    const records: ExecutionHistoryRecord[] = [];

    for (const file of files) {
      const filePath = path.join(historyDir, file);
      const parsed = await this.parseJsonlFile(filePath);
      records.push(...parsed);
    }

    const sorted = records.sort((a, b) => a.recorded_at.localeCompare(b.recorded_at));

    this._readAllCache.set(workspaceRoot, {
      records: sorted,
      expiresAt: Date.now() + this.READ_ALL_CACHE_TTL_MS,
    });

    return sorted;
  }

  /**
   * Aggregate total cost per issue across all pipeline runs.
   *
   * Groups run records by issue_number and sums estimated_cost_usd.
   * Only includes record_type === 'run' records with totalCostUsd > 0.
   * Errors are caught and logged — never throws.
   *
   * @param workspaceRoot - Absolute path to repository root
   * @param limit - Return only the N most-recently-active issues (default: 20)
   * @param filter - Optional filter by issueType and/or sizeLabel
   * @returns Aggregations sorted by lastRunAt descending
   * @see Issue #1410 - Cost-per-issue aggregation
   */
  static async getCostByIssue(
    workspaceRoot: string,
    limit: number = 20,
    filter?: { issueType?: string; sizeLabel?: string }
  ): Promise<IssueCostAggregation[]> {
    try {
      const allRecords = await this.readAll(workspaceRoot);
      const runRecords = allRecords.filter(
        (r) => r.record_type === "run"
      ) as ExecutionHistoryRunRecordV2[];

      // Group by issue_number
      const byIssue = new Map<number, ExecutionHistoryRunRecordV2[]>();
      for (const record of runRecords) {
        const existing = byIssue.get(record.issue_number) ?? [];
        existing.push(record);
        byIssue.set(record.issue_number, existing);
      }

      // Build aggregations
      const aggregations: IssueCostAggregation[] = [];
      for (const [issueNumber, runs] of byIssue) {
        // Use the most recent run's labels as the issue type/size
        const lastRun = runs[runs.length - 1];
        const issueType = lastRun?.type ?? null;
        const sizeLabel = lastRun?.size ?? null;

        // Apply filter
        if (filter?.issueType && issueType !== filter.issueType) continue;
        if (filter?.sizeLabel && sizeLabel !== filter.sizeLabel) continue;

        const totalCostUsd = runs.reduce((sum, r) => sum + (r.tokens.estimated_cost_usd ?? 0), 0);
        if (totalCostUsd === 0) continue;

        const backtrackCount = runs.filter((r) => r.is_recovery === true).length;
        const timestamps = runs
          .map((r) => new Date(r.recorded_at))
          .sort((a, b) => a.getTime() - b.getTime());

        aggregations.push({
          issueNumber,
          totalCostUsd,
          runCount: runs.length,
          backtrackCount,
          issueType,
          sizeLabel,
          firstRunAt: timestamps[0],
          lastRunAt: timestamps[timestamps.length - 1],
        });
      }

      return aggregations
        .sort((a, b) => b.lastRunAt.getTime() - a.lastRunAt.getTime())
        .slice(0, limit);
    } catch (err) {
      console.warn("[Nightgauge] getCostByIssue aggregation failed:", err);
      return [];
    }
  }

  /**
   * Read all records for a specific issue number.
   *
   * @param workspaceRoot - Absolute path to repository root
   * @param issueNumber - Issue number to filter by
   * @returns Matching records sorted by recorded_at ascending
   */
  static async readForIssue(
    workspaceRoot: string,
    issueNumber: number
  ): Promise<ExecutionHistoryRecord[]> {
    const all = await this.readAll(workspaceRoot);
    return all.filter((r) => r.issue_number === issueNumber);
  }

  /**
   * List all .jsonl files in the history directory, sorted by date ascending.
   *
   * @param workspaceRoot - Absolute path to repository root
   * @returns Array of filenames like ["2026-02-10.jsonl", "2026-02-13.jsonl"]
   */
  static async listHistoryFiles(workspaceRoot: string): Promise<string[]> {
    const historyDir = ExecutionHistoryWriter.getHistoryDir(workspaceRoot);
    try {
      const entries = await fs.readdir(historyDir);
      return entries.filter((e) => e.endsWith(".jsonl")).sort();
    } catch {
      // Directory doesn't exist — return empty
      return [];
    }
  }

  /**
   * Parse a single JSONL file. Skips malformed lines with a warning.
   *
   * For run records: dispatches by schema version, preserving strict v3 and v2
   * records and normalizing v1 records to the v2 field set.
   * Outcome records are accepted as either v1 or v2 via the union schema.
   *
   * Results are memoized per `{path, mtimeMs, size}` — unchanged files
   * return their prior parse result instantly, bypassing the CPU-heavy Zod
   * validation loop. The day-rotated history layout means only today's
   * file typically ever has a cache miss on subsequent reads.
   *
   * @param filePath - Absolute path to the JSONL file
   * @returns Array of validated records (v1 run records normalized to v2)
   */
  static async parseJsonlFile(filePath: string): Promise<ExecutionHistoryRecord[]> {
    // Fast path: if the file's mtime+size match a prior parse, reuse it.
    // `fs.stat` is cheap compared to reading and Zod-parsing the file.
    let mtimeMs: number;
    let size: number;
    try {
      const stats = await fs.stat(filePath);
      mtimeMs = stats.mtimeMs;
      size = stats.size;
    } catch {
      // File doesn't exist — return empty, drop any stale cache entry.
      this._parseCache.delete(filePath);
      return [];
    }

    const cached = this._parseCache.get(filePath);
    if (cached && cached.mtimeMs === mtimeMs && cached.size === size) {
      // Refresh LRU position so hot files survive eviction.
      this._parseCache.delete(filePath);
      this._parseCache.set(filePath, cached);
      return cached.records;
    }

    const records = await this._parseJsonlFileUncached(filePath);

    this._parseCache.set(filePath, { mtimeMs, size, records });
    while (this._parseCache.size > this.PARSE_CACHE_MAX_ENTRIES) {
      const oldest = this._parseCache.keys().next().value;
      if (oldest === undefined) break;
      this._parseCache.delete(oldest);
    }

    return records;
  }

  /**
   * Uncached parse implementation — the original `parseJsonlFile` body.
   * Kept private so callers always go through the `{path, mtimeMs, size}`
   * memoized entry point.
   */
  private static async _parseJsonlFileUncached(
    filePath: string
  ): Promise<ExecutionHistoryRecord[]> {
    const records: ExecutionHistoryRecord[] = [];
    let content: string;

    try {
      content = await fs.readFile(filePath, "utf-8");
    } catch {
      // File doesn't exist or unreadable — return empty
      return records;
    }

    const lines = content.split("\n");
    for (let i = 0; i < lines.length; i++) {
      // Yield to event loop periodically to prevent extension host
      // unresponsiveness during large file parsing.
      if (i > 0 && i % YIELD_BATCH_SIZE === 0) {
        await new Promise<void>((resolve) => setImmediate(resolve));
      }

      const trimmed = lines[i].trim();
      if (!trimmed) continue;

      try {
        const parsed = JSON.parse(trimmed);

        // For run records, dispatch on the schema version. If strict parsing
        // fails, accept the raw object if it has
        // the minimum required fields. Issue #2252: strict validation
        // caused rebuildIndex to silently drop valid-enough records,
        // leaving the dashboard with stale data.
        if (parsed.record_type === "run") {
          // Skip phantom records: single-stage entries (typically just
          // pipeline-start) with $0 cost, created by a backup-write bug
          // that fired before the pipeline actually completed.
          const stageCount = parsed.stages ? Object.keys(parsed.stages).length : 0;
          if (
            !isOrchestratorCrashRecord(parsed) &&
            stageCount <= 1 &&
            (parsed.tokens?.estimated_cost_usd ?? 0) === 0
          ) {
            continue;
          }

          let strictParseError: string;
          if (parsed.schema_version === "3") {
            const result = ExecutionHistoryRunRecordV3Schema.safeParse(parsed);
            if (result.success) {
              records.push(result.data);
              continue;
            }
            strictParseError = result.error.message;
          } else if (parsed.schema_version === "2") {
            const result = ExecutionHistoryRunRecordV2Schema.safeParse(parsed);
            if (result.success) {
              records.push(result.data);
              continue;
            }
            strictParseError = result.error.message;
          } else if (parsed.schema_version === "1") {
            const result = ExecutionHistoryRunRecordSchema.safeParse(parsed);
            if (result.success) {
              records.push(normalizeRunRecordToV2(result.data));
              continue;
            }
            strictParseError = result.error.message;
          } else {
            strictParseError = `Unsupported schema_version: ${String(parsed.schema_version)}`;
          }

          // Lenient fallback: accept record if it has essential fields.
          // Dashboard only needs issue_number, tokens, outcome, started_at.
          if (
            typeof parsed.issue_number === "number" &&
            typeof parsed.recorded_at === "string" &&
            parsed.tokens &&
            typeof parsed.tokens.estimated_cost_usd === "number"
          ) {
            records.push(parsed as ExecutionHistoryRunRecordV2);
            continue;
          }

          console.warn(
            `[Nightgauge] Skipping malformed history line in ${path.basename(filePath)}: ${strictParseError}`
          );
          continue;
        }

        // For outcome and other records, use the union schema
        const validation = ExecutionHistoryRecordSchema.safeParse(parsed);
        if (validation.success) {
          records.push(validation.data);
        } else {
          console.warn(
            `[Nightgauge] Skipping malformed history line in ${path.basename(filePath)}: ${validation.error.message}`
          );
        }
      } catch {
        console.warn(
          `[Nightgauge] Skipping unparseable history line in ${path.basename(filePath)}`
        );
      }
    }

    return records;
  }
}
