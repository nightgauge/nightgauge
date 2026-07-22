/**
 * ExecutionHistoryReader - Query utility for pipeline execution history
 *
 * Static utility class for reading and querying JSONL history files.
 * Gracefully handles missing files and malformed lines.
 *
 * Reads both v1 and v2 records, normalizing v1 run records to v2 shape
 * with sensible defaults so consumers always work with the v2 type.
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
  type ExecutionHistoryRecord,
  type ExecutionHistoryRunRecord,
  type ExecutionHistoryRunRecordV2,
} from "../schemas/executionHistory";

import { ExecutionHistoryWriter } from "./executionHistoryWriter";

/**
 * Normalized run record type — always the v2 shape.
 * Consumers that import this type are guaranteed the v2 field set.
 */
export type NormalizedRunRecord = ExecutionHistoryRunRecordV2;

/**
 * A/B comparison of pipeline runs with vs without an active focus lens (Issue #2460).
 *
 * Quantifies the ROI of focus lens usage by comparing cost, duration, and
 * success rate between focused and unfocused runs.
 */
export interface FocusLensImpactComparison {
  /** Number of runs with an active (non-general) focus lens */
  focusedRunCount: number;
  /** Number of runs with no active focus lens */
  unfocusedRunCount: number;
  /** Average cost (USD) per focused run */
  avgCostFocused: number;
  /** Average cost (USD) per unfocused run */
  avgCostUnfocused: number;
  /** Average duration (ms) per focused run */
  avgDurationFocused: number;
  /** Average duration (ms) per unfocused run */
  avgDurationUnfocused: number;
  /** Success rate (0–1) for focused runs — outcome === 'complete' */
  successRateFocused: number;
  /** Success rate (0–1) for unfocused runs — outcome === 'complete' */
  successRateUnfocused: number;
  /** Breakdown of focused runs by lens name */
  byLens: Array<{
    lens: string;
    runCount: number;
    avgCostUsd: number;
    avgDurationMs: number;
    successRate: number;
  }>;
}

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
 * Backfill `cost_source` on per-stage records that lack it (Issue #3228).
 *
 * Pre-#3228 records never set this field. Native (Claude `total_cost_usd`)
 * was the only path that produced a non-zero cost, so when `cost_usd > 0` we
 * can confidently label the source `'native'`. When `cost_usd === 0` we leave
 * the field undefined — historically the zero could mean either "no tokens
 * spent on this stage" or "non-Claude adapter that never reported cost", and
 * we cannot retroactively distinguish those.
 */
function normalizePerStageCostSource(
  perStage: NonNullable<ExecutionHistoryRunRecordV2["tokens"]["per_stage"]> | undefined
): NonNullable<ExecutionHistoryRunRecordV2["tokens"]["per_stage"]> | undefined {
  if (!perStage) return perStage;
  const out: NonNullable<ExecutionHistoryRunRecordV2["tokens"]["per_stage"]> = {};
  for (const [stage, usage] of Object.entries(perStage)) {
    if (!usage) continue;
    if (usage.cost_source === undefined && usage.cost_usd > 0) {
      out[stage] = { ...usage, cost_source: "native" };
    } else {
      out[stage] = usage;
    }
  }
  return out;
}

/**
 * Normalize a v1 run record to v2 shape by adding defaults for new fields.
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
    tokens: {
      ...v1.tokens,
      per_stage: normalizePerStageCostSource(v1.tokens.per_stage),
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
   * Compute A/B comparison of pipeline runs with vs without a focus lens (Issue #2460).
   *
   * Splits all run records into focused (focus_lens_active present and not "general")
   * and unfocused (focus_lens_active absent or lens === "general") groups, then
   * computes average cost, duration, and success rate for each group.
   *
   * @param workspaceRoot - Absolute path to repository root
   * @returns Comparison metrics, or null when fewer than 2 total run records exist
   */
  static async getFocusLensComparison(
    workspaceRoot: string
  ): Promise<FocusLensImpactComparison | null> {
    try {
      const allRecords = await this.readAll(workspaceRoot);
      const runRecords = allRecords.filter(
        (r) => r.record_type === "run"
      ) as ExecutionHistoryRunRecordV2[];

      if (runRecords.length < 2) {
        return null;
      }

      const focused = runRecords.filter(
        (r) => r.focus_lens_active?.lens && r.focus_lens_active.lens !== "general"
      );
      const unfocused = runRecords.filter(
        (r) => !r.focus_lens_active?.lens || r.focus_lens_active.lens === "general"
      );

      const avg = (nums: number[]) =>
        nums.length === 0 ? 0 : nums.reduce((a, b) => a + b, 0) / nums.length;

      const successRate = (runs: ExecutionHistoryRunRecordV2[]) =>
        runs.length === 0 ? 0 : runs.filter((r) => r.outcome === "complete").length / runs.length;

      // Per-lens breakdown for focused runs
      const lensMap = new Map<string, ExecutionHistoryRunRecordV2[]>();
      for (const r of focused) {
        const lens = r.focus_lens_active!.lens;
        const group = lensMap.get(lens) ?? [];
        group.push(r);
        lensMap.set(lens, group);
      }

      const byLens = Array.from(lensMap.entries()).map(([lens, runs]) => ({
        lens,
        runCount: runs.length,
        avgCostUsd: avg(runs.map((r) => r.tokens.estimated_cost_usd)),
        avgDurationMs: avg(runs.map((r) => r.total_duration_ms)),
        successRate: successRate(runs),
      }));

      return {
        focusedRunCount: focused.length,
        unfocusedRunCount: unfocused.length,
        avgCostFocused: avg(focused.map((r) => r.tokens.estimated_cost_usd)),
        avgCostUnfocused: avg(unfocused.map((r) => r.tokens.estimated_cost_usd)),
        avgDurationFocused: avg(focused.map((r) => r.total_duration_ms)),
        avgDurationUnfocused: avg(unfocused.map((r) => r.total_duration_ms)),
        successRateFocused: successRate(focused),
        successRateUnfocused: successRate(unfocused),
        byLens,
      };
    } catch (err) {
      console.warn("[Nightgauge] getFocusLensComparison failed:", err);
      return null;
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
   * For run records: tries v2 schema first, falls back to v1 + normalize.
   * This ensures all returned run records have the v2 shape.
   * Outcome records are accepted as either v1 or v2 via the union schema.
   *
   * Results are memoized per `{path, mtimeMs, size}` — unchanged files
   * return their prior parse result instantly, bypassing the CPU-heavy Zod
   * validation loop. The day-rotated history layout means only today's
   * file typically ever has a cache miss on subsequent reads.
   *
   * @param filePath - Absolute path to the JSONL file
   * @returns Array of validated records (run records normalized to v2)
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

        // For run records, try v2 first then v1 with normalization.
        // If both strict parses fail, accept the raw object if it has
        // the minimum required fields. Issue #2252: strict validation
        // caused rebuildIndex to silently drop valid-enough records,
        // leaving the dashboard with stale data.
        if (parsed.record_type === "run") {
          // Skip phantom records: single-stage entries (typically just
          // pipeline-start) with $0 cost, created by a backup-write bug
          // that fired before the pipeline actually completed.
          const stageCount = parsed.stages ? Object.keys(parsed.stages).length : 0;
          if (stageCount <= 1 && (parsed.tokens?.estimated_cost_usd ?? 0) === 0) {
            continue;
          }

          const v2Result = ExecutionHistoryRunRecordV2Schema.safeParse(parsed);
          if (v2Result.success) {
            // Issue #3228: backfill cost_source on per-stage records that
            // lack the field (every record emitted before #3228).
            records.push({
              ...v2Result.data,
              tokens: {
                ...v2Result.data.tokens,
                per_stage: normalizePerStageCostSource(v2Result.data.tokens.per_stage),
              },
            });
            continue;
          }

          const v1Result = ExecutionHistoryRunRecordSchema.safeParse(parsed);
          if (v1Result.success) {
            records.push(normalizeRunRecordToV2(v1Result.data));
            continue;
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
            `[Nightgauge] Skipping malformed history line in ${path.basename(filePath)}: ${v1Result.error.message}`
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
