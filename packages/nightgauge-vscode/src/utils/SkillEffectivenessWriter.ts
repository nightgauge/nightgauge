/**
 * SkillEffectivenessWriter - JSONL writer/reader for skill effectiveness telemetry
 *
 * Static utility class following the GateMetricsWriter pattern:
 * no state, no VSCode dependency, testable in isolation.
 *
 * Persists skill effectiveness records to a single JSONL file:
 *   .nightgauge/health/skill-effectiveness.jsonl
 *
 * Non-critical: all operations log warnings on failure, never throw.
 *
 * @see Issue #1414 - Skill effectiveness tracking
 */

import * as fs from "node:fs/promises";
import * as path from "node:path";

import {
  SkillEffectivenessRecordSchema,
  type SkillEffectivenessRecord,
} from "../schemas/skillEffectiveness";

/** Relative path from workspace root to the skill effectiveness file */
const SKILL_EFFECTIVENESS_FILE = ".nightgauge/health/skill-effectiveness.jsonl";

/** Default retention period in days */
const DEFAULT_RETENTION_DAYS = 90;

export class SkillEffectivenessWriter {
  /**
   * Append a validated skill effectiveness record to the JSONL file.
   *
   * Validates the record via Zod before writing. Creates the parent directory
   * if it does not exist. Silently skips invalid records (non-critical).
   */
  static async appendRecord(
    workspaceRoot: string,
    record: SkillEffectivenessRecord
  ): Promise<void> {
    try {
      const validation = SkillEffectivenessRecordSchema.safeParse(record);
      if (!validation.success) {
        console.warn(
          `[Nightgauge] Invalid skill effectiveness record, skipping write: ${validation.error.message}`
        );
        return;
      }

      const filePath = this.getFilePath(workspaceRoot);
      const dir = path.dirname(filePath);

      await fs.mkdir(dir, { recursive: true });
      await fs.appendFile(filePath, JSON.stringify(record) + "\n", "utf-8");
    } catch (error) {
      console.warn(`[Nightgauge] Failed to write skill effectiveness record: ${error}`);
    }
  }

  /**
   * Read all skill effectiveness records from the JSONL file.
   *
   * Skips malformed lines silently. Returns an empty array if the file
   * does not exist.
   */
  static async readAll(workspaceRoot: string): Promise<SkillEffectivenessRecord[]> {
    const filePath = this.getFilePath(workspaceRoot);
    let content: string;

    try {
      content = await fs.readFile(filePath, "utf-8");
    } catch {
      return [];
    }

    const records: SkillEffectivenessRecord[] = [];
    for (const line of content.split("\n")) {
      const trimmed = line.trim();
      if (!trimmed) continue;
      try {
        const parsed = JSON.parse(trimmed);
        const validation = SkillEffectivenessRecordSchema.safeParse(parsed);
        if (validation.success) {
          records.push(validation.data);
        }
      } catch {
        // Skip malformed lines silently
      }
    }

    return records;
  }

  /**
   * Returns the absolute path to the skill effectiveness file.
   */
  static getFilePath(workspaceRoot: string): string {
    return path.join(workspaceRoot, SKILL_EFFECTIVENESS_FILE);
  }

  /**
   * Remove entries older than the retention period by rewriting the file.
   *
   * @param workspaceRoot - Absolute path to repository root
   * @param retentionDays - Number of days to retain (default: 90)
   */
  static async enforceRetention(
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
          const ts = new Date(record.analyzed_at);
          if (ts >= cutoff) {
            kept.push(trimmed);
          }
        } catch {
          // Skip malformed lines during pruning
        }
      }

      await fs.writeFile(filePath, kept.join("\n") + (kept.length > 0 ? "\n" : ""), "utf-8");
    } catch (error) {
      console.warn(`[Nightgauge] Failed to enforce skill effectiveness retention: ${error}`);
    }
  }
}
