/**
 * HealthTrendsWriter - JSONL append/read/prune for HealthTrendEntry records
 *
 * Persists all 7 SDK health dimension scores + overall score to a single JSONL
 * time-series file in the checkout's own per-checkout directory (ADR-024 § 7):
 * `<git-dir>/nightgauge-worktree/health/trends.jsonl`
 * (`.git/nightgauge-worktree/health/trends.jsonl` for a main checkout).
 *
 * Non-critical: write operations never throw; all errors are logged as warnings.
 *
 * @see Issue #1411 - Health trend persistence and dashboard sparklines
 */

import * as fs from "node:fs/promises";
import * as path from "node:path";
import { z } from "zod";
import type { HealthTrendEntry, HealthTrendsReadOptions } from "./types.js";
import { checkoutPath } from "../../context/cloneLayout.js";

/** The trends file's name inside the checkout's `health/` entry. */
const TRENDS_FILE = "trends.jsonl";

/** Default retention period in days */
const DEFAULT_RETENTION_DAYS = 90;

// ── Validation schema ─────────────────────────────────────────────

const HealthTrendEntrySchema = z.object({
  schema_version: z.literal("1"),
  timestamp: z.string(),
  run_id: z.string(),
  issue_number: z.number(),
  overall_score: z.number().min(0).max(100).nullable(),
  dimensions: z.record(z.string(), z.number()),
  significant_findings: z.array(z.string()),
});

// ── HealthTrendsWriter ────────────────────────────────────────────

export class HealthTrendsWriter {
  /**
   * Returns the absolute path to the trends JSONL file of the checkout
   * `workspaceRoot` (absolute) is in. Throws for a relative or empty root, and
   * `NotAGitRepositoryError` outside a git repository; it never resolves
   * against the process working directory.
   */
  static getFilePath(workspaceRoot: string): string {
    if (!workspaceRoot || !path.isAbsolute(workspaceRoot)) {
      throw new Error(
        `HealthTrendsWriter: workspace root ${JSON.stringify(workspaceRoot)} is not absolute`
      );
    }
    return path.join(checkoutPath("health", workspaceRoot), TRENDS_FILE);
  }

  /**
   * The trends file path, or undefined when `workspaceRoot` is not a usable
   * checkout (readers then report nothing instead of throwing).
   */
  private static tryFilePath(workspaceRoot: string): string | undefined {
    try {
      return this.getFilePath(workspaceRoot);
    } catch {
      return undefined;
    }
  }

  /**
   * Append one HealthTrendEntry to the checkout's health/trends.jsonl.
   * Non-throwing — all errors are logged as warnings.
   */
  static async append(workspaceRoot: string, entry: HealthTrendEntry): Promise<void> {
    try {
      const validation = HealthTrendEntrySchema.safeParse(entry);
      if (!validation.success) {
        console.warn(
          `[HealthTrendsWriter] Invalid trend entry, skipping write: ${validation.error.message}`
        );
        return;
      }

      const filePath = this.getFilePath(workspaceRoot);
      const dir = path.dirname(filePath);

      await fs.mkdir(dir, { recursive: true });
      await fs.appendFile(filePath, JSON.stringify(entry) + "\n", "utf-8");
    } catch (error) {
      console.warn(`[HealthTrendsWriter] Failed to write trend entry: ${error}`);
    }
  }

  /**
   * Read HealthTrendEntry records from the JSONL file.
   *
   * - Skips malformed lines with a warning
   * - When `opts.limit` is set, reads ALL lines then returns the last N (tail semantics)
   * - When `opts.startDate`/`endDate` are set, filters by entry.timestamp
   */
  static async read(
    workspaceRoot: string,
    opts?: HealthTrendsReadOptions
  ): Promise<HealthTrendEntry[]> {
    const filePath = this.tryFilePath(workspaceRoot);
    if (!filePath) return [];
    let content: string;

    try {
      content = await fs.readFile(filePath, "utf-8");
    } catch {
      return [];
    }

    const records: HealthTrendEntry[] = [];

    for (const line of content.split("\n")) {
      const trimmed = line.trim();
      if (!trimmed) continue;
      try {
        const parsed = JSON.parse(trimmed);
        const validation = HealthTrendEntrySchema.safeParse(parsed);
        if (validation.success) {
          records.push(validation.data as HealthTrendEntry);
        } else {
          console.warn(
            `[HealthTrendsWriter] Skipping malformed trend entry: ${validation.error.message}`
          );
        }
      } catch {
        console.warn("[HealthTrendsWriter] Skipping unparseable trend line");
      }
    }

    // Apply date range filter
    let filtered = records;
    if (opts?.startDate || opts?.endDate) {
      filtered = records.filter((e) => {
        const ts = new Date(e.timestamp);
        if (opts.startDate && ts < opts.startDate) return false;
        if (opts.endDate && ts > opts.endDate) return false;
        return true;
      });
    }

    // Apply limit (tail semantics — last N entries)
    if (opts?.limit !== undefined && opts.limit > 0) {
      return filtered.slice(-opts.limit);
    }

    return filtered;
  }

  /**
   * Prune entries older than retentionDays (default 90).
   * Rewrites the file with only kept entries.
   * Returns the number of entries pruned.
   */
  static async pruneOldEntries(
    workspaceRoot: string,
    retentionDays: number = DEFAULT_RETENTION_DAYS
  ): Promise<number> {
    const filePath = this.tryFilePath(workspaceRoot);
    if (!filePath) return 0;
    let content: string;

    try {
      content = await fs.readFile(filePath, "utf-8");
    } catch {
      return 0; // File doesn't exist — nothing to prune
    }

    const cutoff = new Date();
    cutoff.setDate(cutoff.getDate() - retentionDays);

    const lines = content.split("\n");
    const kept: string[] = [];
    let pruned = 0;

    for (const line of lines) {
      const trimmed = line.trim();
      if (!trimmed) continue;
      try {
        const record = JSON.parse(trimmed);
        const ts = new Date(record.timestamp);
        if (ts >= cutoff) {
          kept.push(trimmed);
        } else {
          pruned++;
        }
      } catch {
        // Skip malformed lines during pruning
      }
    }

    try {
      await fs.writeFile(filePath, kept.join("\n") + (kept.length > 0 ? "\n" : ""), "utf-8");
    } catch (error) {
      console.warn(`[HealthTrendsWriter] Failed to prune trend entries: ${error}`);
    }

    return pruned;
  }
}
