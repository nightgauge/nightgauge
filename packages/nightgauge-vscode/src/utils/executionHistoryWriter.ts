/**
 * ExecutionHistoryWriter - shared storage/index utilities for execution history
 *
 * Static utility class with no VSCode dependency, testable from SDK context.
 *
 * Resolves and maintains daily JSONL history files at:
 *   .nightgauge/pipeline/history/YYYY-MM-DD.jsonl
 *
 * Run records are produced exclusively by the Go pipeline runtime; the
 * extension reads, indexes, and applies retention to those records.
 *
 * @see Issue #649 - Execution History Persistence
 * @see docs/ARCHITECTURE.md for utility patterns
 */

import * as fs from "node:fs/promises";
import * as path from "node:path";

import { resolveConfigPathSync } from "./configPathResolver";
import { readEffectiveConfigTextSync } from "./mergedConfigReader";
import {
  type ExecutionHistoryRunRecordV2,
  type ExecutionHistoryRunRecordV3,
  type ExecutionHistoryRecord,
  type TerminalFailureKind,
} from "../schemas/executionHistory";

/**
 * Index v2 adds terminal_failure_kind so zero-token orchestrator crashes stay
 * positively identifiable after the full record is projected to a summary.
 */
export const HISTORY_INDEX_SCHEMA_VERSION = "2";

/**
 * Idempotency key for a run record (Issue #313): the stable run_id when present
 * (threaded through the runtime as a UUID v7), else issue_number + started_at.
 * Duplicate records for the same completion produce the same key; distinct
 * runs never collide. Mirrors the Go writer's runRecordKey.
 */
function runRecordKey(record: ExecutionHistoryRecord): string {
  const runId = (record as { run_id?: unknown }).run_id;
  if (typeof runId === "string" && runId !== "") return `run:${runId}`;
  const startedAt = (record as { started_at?: unknown }).started_at ?? "";
  return fallbackRunKey(record.issue_number, String(startedAt));
}

/**
 * Fallback run identity for records carrying no run_id. Not repo-qualified on
 * purpose: a history directory belongs to exactly one repository, so the
 * directory already scopes the key.
 *
 * The timestamp is normalised to a whole UTC second (#141). Producers format
 * the same instant differently — a local offset with microseconds versus a UTC
 * millisecond string — so comparing the raw strings split one run into several
 * identities and defeated de-duplication entirely. Mirrors the Go writer's
 * fallbackRunKey so the extension's index rebuild collapses the same records
 * the authoritative producer considers identical.
 */
function fallbackRunKey(issueNumber: number, startedAt: string): string {
  return `issue:${issueNumber}|${normalizeStartInstant(startedAt)}`;
}

/**
 * Reduce an ISO-8601 timestamp to a whole-second UTC bucket. An unparseable
 * value is returned verbatim — a key no weaker than before, never a collision
 * between unrelated runs.
 */
function normalizeStartInstant(startedAt: string): string {
  const ms = Date.parse(startedAt);
  if (Number.isNaN(ms)) return startedAt;
  return new Date(Math.floor(ms / 1000) * 1000).toISOString().replace(".000Z", "Z");
}

/**
 * How much stage-level data a run record carries (Issue #313). A late
 * finalizer's skeleton (empty stages) scores 0 and can never supersede a real
 * record; any run that executed scores >= 1. Mirrors the Go writer's
 * recordRichness.
 */
function stageRichness(record: ExecutionHistoryRecord): number {
  const stages = (record as { stages?: Record<string, unknown> }).stages;
  return stages ? Object.keys(stages).length : 0;
}

/**
 * Entry in the history index — lightweight summary for list display (Issue #1007)
 */
export interface HistoryIndexEntry {
  issue_number: number;
  /**
   * Stable run identifier (Issue #313) — mirrors the run record's run_id so the
   * index can be de-duplicated by run identity (one entry per run). Additive
   * and optional: entries written before this field, and records with no
   * assigned run_id, omit it.
   */
  run_id?: string;
  title: string;
  outcome: "complete" | "failed" | "cancelled";
  outcome_type?:
    | "productive"
    | "verify-and-close"
    | "already-resolved"
    | "budget-ceiling"
    | "shipped-but-overbudget"
    | "skill-no-op"
    | "blocked"
    | "deferred";
  /** What aborted a failed V3 run; retained for crash-vs-phantom identity (#447). */
  terminal_failure_kind?: TerminalFailureKind;
  /** True when this run resumed a previously-failed pipeline (Issue #1261) */
  is_recovery?: boolean;
  /**
   * True when this run used the legacy supercharge envelope (Opus + max effort).
   * @deprecated Issue #3009 — prefer `performance_mode === "maximum"`. Kept
   * additively for one release so external consumers keep working.
   */
  is_supercharge?: boolean;
  /** Active performance mode for this run (Issue #3009). */
  performance_mode?: "efficiency" | "elevated" | "maximum" | "frontier";
  cost_usd: number;
  /**
   * Token totals. OPTIONAL because entries written before these fields existed
   * are still on disk and still read — `isGhostEntry` has always coalesced them
   * with `?? 0`, which was dead code while the type declared them required.
   * Surfaced by #499: a test constructing a legacy entry could not typecheck
   * against a type that denied legacy entries exist. The writer always sets
   * them; only historical readers see them absent.
   */
  total_input_tokens?: number;
  total_output_tokens?: number;
  total_cache_read_tokens?: number;
  total_cache_creation_tokens?: number;
  duration_ms: number;
  stage_count: number;
  /** Total stage-detail count, including failed stages (restart dedupe). Always written on v2 indexes. */
  record_richness?: number;
  started_at: string;
  recorded_at: string;
  labels?: string[];
  size?: string | null;
  type?: string | null;
  branch: string;
}

/**
 * History index structure — stored at history/index.json (Issue #1007)
 */
export interface HistoryIndex {
  schema_version: typeof HISTORY_INDEX_SCHEMA_VERSION;
  updated_at: string;
  total_runs: number;
  entries: HistoryIndexEntry[];
}

/** Default history directory relative to workspace root */
const HISTORY_DIR = ".nightgauge/pipeline/history";

/** Default retention period in days */
const DEFAULT_RETENTION_DAYS = 90;

export class ExecutionHistoryWriter {
  /**
   * Delete history files older than the retention period, and drop the
   * index.json entries that summarized them — an index entry pointing at a
   * deleted daily file is its own bug (Issue #674): every consumer that
   * trusts the index (TelemetryStore.getIndex, the dashboard run list)
   * would resolve a run that no longer exists on disk.
   *
   * @param workspaceRoot - Absolute path to repository root
   * @param retentionDays - Number of days to retain. Defaults to the
   *   configured `pipeline.logs.history_retention_days` (config.yaml,
   *   merged across tiers), falling back to DEFAULT_RETENTION_DAYS when
   *   unset — evaluated lazily as a default-parameter expression so an
   *   explicit caller-supplied value (e.g. a test, or a forwarded override)
   *   always wins over the configured one.
   */
  static async cleanupOldFiles(
    workspaceRoot: string,
    retentionDays: number = ExecutionHistoryWriter.resolveConfiguredRetentionDays(workspaceRoot)
  ): Promise<{ deleted: string[] }> {
    const deleted: string[] = [];
    try {
      const historyDir = this.getHistoryDir(workspaceRoot);
      const cutoff = new Date();
      cutoff.setDate(cutoff.getDate() - retentionDays);
      cutoff.setHours(0, 0, 0, 0);

      let entries: string[];
      try {
        entries = await fs.readdir(historyDir);
      } catch {
        // Directory doesn't exist — nothing to clean
        return { deleted };
      }

      for (const entry of entries) {
        if (!entry.endsWith(".jsonl")) continue;

        // Parse date from YYYY-MM-DD.jsonl filename
        const dateStr = entry.replace(".jsonl", "");
        const fileDate = new Date(dateStr + "T00:00:00Z");
        if (isNaN(fileDate.getTime())) continue;

        if (fileDate < cutoff) {
          await fs.unlink(path.join(historyDir, entry));
          deleted.push(entry);
        }
      }

      await this.pruneIndexEntries(historyDir, cutoff);
    } catch (error) {
      console.warn(`[Nightgauge] History cleanup failed: ${error}`);
    }
    return { deleted };
  }

  /**
   * Resolve `pipeline.logs.history_retention_days` from the merged config
   * tiers (machine → project → local → env), falling back to
   * DEFAULT_RETENTION_DAYS when unset, malformed, or the config cannot be
   * read. Synchronous — reuses the same sync merged-config reader the other
   * resolvers use (utils/resolvers/*) so this stays usable at extension
   * activation, before any async config load has completed (Issue #674).
   */
  private static resolveConfiguredRetentionDays(workspaceRoot: string): number {
    try {
      const pathResult = resolveConfigPathSync(workspaceRoot);
      if (!pathResult.exists) return DEFAULT_RETENTION_DAYS;

      const configContent = readEffectiveConfigTextSync(pathResult);
      const lines = configContent.split("\n");
      let inPipeline = false;
      let inLogs = false;

      for (const line of lines) {
        const trimmed = line.trim();
        if (!trimmed || trimmed.startsWith("#")) continue;

        if (trimmed === "pipeline:" && !line.startsWith(" ")) {
          inPipeline = true;
          inLogs = false;
          continue;
        }

        if (!inPipeline) continue;

        // A new top-level (unindented) key ends the pipeline: block.
        if (!line.startsWith(" ") && /^[a-z_]+:/.test(trimmed)) {
          inPipeline = false;
          inLogs = false;
          continue;
        }

        const indent = line.length - line.trimStart().length;

        if (trimmed === "logs:") {
          inLogs = true;
          continue;
        }

        // A sibling of logs: at the pipeline level ends the logs: block.
        if (inLogs && indent <= 2 && /^[a-z_]+:/.test(trimmed) && trimmed !== "logs:") {
          inLogs = false;
        }

        if (inLogs) {
          const match = trimmed.match(/^history_retention_days:\s*(\d+)/);
          if (match) {
            const parsed = Number.parseInt(match[1], 10);
            if (!Number.isNaN(parsed) && parsed >= 1) {
              return parsed;
            }
          }
        }
      }
    } catch {
      // Non-critical: fall through to default
    }
    return DEFAULT_RETENTION_DAYS;
  }

  /**
   * Drop index.json entries dated before `cutoff` — the same cutoff that
   * just decided which daily JSONL files to delete — so the index never
   * outlives the file it summarizes (Issue #674). An entry's date is its
   * `recorded_at` (write time, matching the daily filename the Go writer and
   * this class both derive from), falling back to `started_at` for entries
   * missing it. An entry whose date cannot be parsed from either field is
   * kept — never dropped on a parse failure.
   */
  private static async pruneIndexEntries(historyDir: string, cutoff: Date): Promise<void> {
    const indexPath = path.join(historyDir, "index.json");
    let raw: string;
    try {
      raw = await fs.readFile(indexPath, "utf-8");
    } catch {
      return; // No index — nothing to prune.
    }

    let index: HistoryIndex;
    try {
      index = JSON.parse(raw) as HistoryIndex;
    } catch {
      return; // Corrupt index — the next TelemetryStore read rebuilds it.
    }
    if (!Array.isArray(index.entries)) return;

    const kept = index.entries.filter((entry) => {
      const source = entry.recorded_at || entry.started_at;
      const parsed = source ? new Date(source) : null;
      if (!parsed || isNaN(parsed.getTime())) return true; // Unparseable: keep, never guess.
      return parsed >= cutoff;
    });

    if (kept.length === index.entries.length) return; // Nothing to prune.

    const next: HistoryIndex = {
      ...index,
      entries: kept,
      total_runs: kept.length,
      updated_at: new Date().toISOString(),
    };

    try {
      await fs.writeFile(indexPath, JSON.stringify(next, null, 2), "utf-8");
    } catch (error) {
      console.warn(`[Nightgauge] Index prune write failed: ${error}`);
    }
  }

  /**
   * Returns the absolute path to the history directory.
   */
  static getHistoryDir(workspaceRoot: string): string {
    return path.join(workspaceRoot, HISTORY_DIR);
  }

  /**
   * Returns the JSONL filename for the given date.
   *
   * @param date - Date to generate filename for (default: now)
   * @returns Filename like "2026-02-13.jsonl"
   */
  static getFilenameForDate(date?: Date): string {
    const d = date ?? new Date();
    return d.toISOString().split("T")[0] + ".jsonl";
  }

  /**
   * Collapse duplicate run records for a run to the single richest one (Issue
   * #313): most stages wins, ties keep the later record, and a skeleton (empty
   * stages) never wins over a real record. Used by the index rebuild so
   * reconstructing from the append-only JSONL source of truth yields exactly
   * one entry per run. Mirrors the Go reader's dedupeRichestByKey.
   */
  static dedupeRichestRunRecords<T extends ExecutionHistoryRecord>(records: T[]): T[] {
    const pos = new Map<string, number>();
    const out: T[] = [];
    for (const rec of records) {
      const key = runRecordKey(rec);
      const at = pos.get(key);
      if (at !== undefined) {
        if (stageRichness(rec) >= stageRichness(out[at])) out[at] = rec;
        continue;
      }
      pos.set(key, out.length);
      out.push(rec);
    }
    return out;
  }

  /**
   * Build a lightweight index entry from a normalized v2 or v3 run record
   * (Issue #1007).
   *
   * Used by TelemetryStore.rebuildIndex() for full index reconstruction.
   */
  static buildIndexEntry(
    record: ExecutionHistoryRunRecordV2 | ExecutionHistoryRunRecordV3
  ): HistoryIndexEntry {
    const stageCount = Object.values(record.stages).filter(
      (s) => s.status === "complete" || s.status === "skipped"
    ).length;

    return {
      issue_number: record.issue_number,
      run_id: (record as { run_id?: string }).run_id,
      title: record.title,
      outcome: record.outcome,
      outcome_type: record.outcome_type,
      terminal_failure_kind:
        record.schema_version === "3" ? record.terminal_failure_kind : undefined,
      is_recovery: record.is_recovery,
      is_supercharge: record.is_supercharge,
      performance_mode: record.performance_mode,
      cost_usd: record.tokens.estimated_cost_usd,
      total_input_tokens: record.tokens.total_input,
      total_output_tokens: record.tokens.total_output,
      total_cache_read_tokens: record.tokens.total_cache_read,
      total_cache_creation_tokens: record.tokens.total_cache_creation,
      duration_ms: record.total_duration_ms,
      stage_count: stageCount,
      record_richness: Object.keys(record.stages).length,
      started_at: record.started_at,
      recorded_at: record.recorded_at,
      labels: record.labels,
      size: record.size,
      type: record.type,
      branch: record.branch,
    };
  }
}
