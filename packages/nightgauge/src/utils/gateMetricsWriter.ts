/**
 * GateMetricsWriter - JSONL writer/reader for quality gate telemetry
 *
 * Static utility class:
 * no state, no VSCode dependency, testable in isolation.
 *
 * Persists gate metric records to a single JSONL file in the checkout's own
 * per-checkout directory (ADR-024 § 7):
 *   .git/nightgauge-worktree/health/gate-metrics.jsonl
 *
 * Non-critical: all operations log warnings on failure, never throw.
 *
 * @see Issue #1412 - Quality gate hit-rate metrics
 */

import * as fs from "node:fs/promises";
import * as path from "node:path";

import { GateMetricRecordSchema, type GateMetricRecord } from "../schemas/gateMetrics";
import { writeFileAtomic } from "./atomicWrite";
import { checkoutPath, isUsableWorkspaceRoot } from "./cloneLayout";

/** The gate metrics file's name inside the checkout's `health/` entry. */
const GATE_METRICS_FILE = "gate-metrics.jsonl";

/** Default retention period in days */
const DEFAULT_RETENTION_DAYS = 90;

export class GateMetricsWriter {
  /**
   * Append a validated gate metric record to the JSONL file.
   *
   * Validates the record via Zod before writing. Creates the parent directory
   * if it does not exist. Silently skips invalid records (non-critical).
   */
  static async appendRecord(workspaceRoot: string, record: GateMetricRecord): Promise<void> {
    try {
      const validation = GateMetricRecordSchema.safeParse(record);
      if (!validation.success) {
        console.warn(
          `[Nightgauge] Invalid gate metric record, skipping write: ${validation.error.message}`
        );
        return;
      }

      const filePath = this.getFilePath(workspaceRoot);
      const dir = path.dirname(filePath);

      await fs.mkdir(dir, { recursive: true });
      await fs.appendFile(filePath, JSON.stringify(record) + "\n", "utf-8");
    } catch (error) {
      console.warn(`[Nightgauge] Failed to write gate metric record: ${error}`);
    }
  }

  /**
   * Read all gate metric records from the JSONL file.
   *
   * Skips malformed lines silently. Returns an empty array if the file
   * does not exist.
   */
  static async readAll(workspaceRoot: string): Promise<GateMetricRecord[]> {
    if (!isUsableWorkspaceRoot(workspaceRoot)) return [];
    const filePath = this.getFilePath(workspaceRoot);
    let content: string;

    try {
      content = await fs.readFile(filePath, "utf-8");
    } catch {
      return [];
    }

    const records: GateMetricRecord[] = [];
    // A DROPPED RECORD IS REPORTED, NOT SWALLOWED (#1084). Both arms below used
    // to be silent, so the reader's every failure mode looked identical to "no
    // gates ran" — which is exactly how a closed `gate_name` enum discarded
    // every record the shipped skill wrote, for as long as it took someone to
    // read a log by hand.
    let rejected = 0;
    let firstRejection = "";
    for (const line of content.split("\n")) {
      const trimmed = line.trim();
      if (!trimmed) continue;
      try {
        const parsed = JSON.parse(trimmed);
        const validation = GateMetricRecordSchema.safeParse(parsed);
        if (validation.success) {
          records.push(validation.data);
        } else {
          rejected++;
          firstRejection ||= validation.error.issues[0]?.message ?? "schema mismatch";
        }
      } catch {
        rejected++;
        firstRejection ||= "malformed JSON";
      }
    }

    if (rejected > 0) {
      console.warn(
        `[gate-metrics] dropped ${rejected} unreadable record(s) from ${filePath} ` +
          `(first: ${firstRejection}). The gate tally is INCOMPLETE — this is not ` +
          `the same as no gates having run. (#1084)`
      );
    }

    return records;
  }

  /**
   * Returns the absolute path to the gate metrics file:
   * `<git-dir>/nightgauge-worktree/health/gate-metrics.jsonl`. Throws when
   * `workspaceRoot` is not in a git checkout.
   */
  static getFilePath(workspaceRoot: string): string {
    return checkoutPath(workspaceRoot, "health", GATE_METRICS_FILE);
  }

  /**
   * Remove entries older than the retention period by rewriting the file.
   *
   * @param workspaceRoot - Absolute path to repository root
   * @param retentionDays - Number of days to retain (default: 90)
   */
  static async pruneOldEntries(
    workspaceRoot: string,
    retentionDays: number = DEFAULT_RETENTION_DAYS
  ): Promise<void> {
    try {
      const filePath = this.getFilePath(workspaceRoot);
      let content: string;
      try {
        content = await fs.readFile(filePath, "utf-8");
      } catch {
        return; // File doesn't exist — nothing to prune
      }

      const cutoff = new Date();
      cutoff.setDate(cutoff.getDate() - retentionDays);

      const lines = content.split("\n");
      const kept: string[] = [];

      for (const line of lines) {
        const trimmed = line.trim();
        if (!trimmed) continue;
        try {
          const record = JSON.parse(trimmed);
          const ts = new Date(record.timestamp);
          if (ts >= cutoff) {
            kept.push(trimmed);
          }
        } catch {
          // Skip malformed lines during pruning
        }
      }

      // Atomic — same prune-while-read shape as health history (#1210).
      await writeFileAtomic(filePath, kept.join("\n") + (kept.length > 0 ? "\n" : ""));
    } catch (error) {
      console.warn(`[Nightgauge] Failed to prune gate metrics: ${error}`);
    }
  }
}
