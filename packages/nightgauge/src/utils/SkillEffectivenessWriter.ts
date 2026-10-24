/**
 * SkillEffectivenessWriter - snapshot store for skill effectiveness telemetry
 *
 * Static utility class: no state, no VSCode dependency, testable in isolation.
 *
 * Persists skill effectiveness records to a single JSONL file in the
 * checkout's own per-checkout directory (ADR-024 § 7):
 *   .git/nightgauge-worktree/health/skill-effectiveness.jsonl
 *
 * The file is a SNAPSHOT keyed by (skill_file, commit_hash), not a log. Every
 * post-pipeline analysis re-classifies every recent skill change, so the
 * former append-per-pass grew the file without bound — 96 MB, nearly all of
 * it superseded duplicates, read by nothing (#1900). Retention policy:
 *
 * - one record per (skill_file, commit_hash), the one with the latest
 *   `analyzed_at`;
 * - changes whose `changed_at` is older than the retention window (default
 *   {@link DEFAULT_RETENTION_DAYS} days) are dropped on every write.
 *
 * Reads stream the file line by line, so memory is bounded by the number of
 * distinct keys, never by file size. The health engine's skill-drift
 * dimension reads it through {@link SkillEffectivenessWriter.readLatest}.
 *
 * Non-critical: writes log warnings on failure, never throw.
 *
 * @see Issue #1414 - Skill effectiveness tracking
 * @see Issue #1900 - skill-drift input and retention
 */

import * as fs from "node:fs";
import * as path from "node:path";
import * as readline from "node:readline";

import {
  SkillEffectivenessRecordSchema,
  type SkillEffectivenessRecord,
} from "../schemas/skillEffectiveness";
import { writeFileAtomic } from "./atomicWrite";
import { checkoutPath, isUsableWorkspaceRoot } from "./cloneLayout";

/** The skill effectiveness file's name inside the checkout's `health/` entry. */
const SKILL_EFFECTIVENESS_FILE = "skill-effectiveness.jsonl";

/** Default retention window in days, measured on `changed_at`. */
export const DEFAULT_RETENTION_DAYS = 90;

const keyOf = (r: SkillEffectivenessRecord) => `${r.skill_file}\u0000${r.commit_hash}`;

export interface SkillEffectivenessRange {
  /** Inclusive lower bound on `changed_at`. */
  since?: Date;
  /** Inclusive upper bound on `changed_at`. */
  until?: Date;
}

export class SkillEffectivenessWriter {
  /**
   * Returns the absolute path to the skill effectiveness file:
   * `<git-dir>/nightgauge-worktree/health/skill-effectiveness.jsonl`. Throws
   * when `workspaceRoot` is not in a git checkout.
   */
  static getFilePath(workspaceRoot: string): string {
    return checkoutPath(workspaceRoot, "health", SKILL_EFFECTIVENESS_FILE);
  }

  /**
   * The latest record per (skill_file, commit_hash), optionally restricted to
   * changes inside `range`. `null` when the file does not exist — absence is
   * reported as absence, not as an empty corpus. Malformed lines are skipped.
   */
  static async readLatest(
    workspaceRoot: string,
    range: SkillEffectivenessRange = {}
  ): Promise<SkillEffectivenessRecord[] | null> {
    if (!isUsableWorkspaceRoot(workspaceRoot)) return null;
    const latest = await this.streamLatest(this.getFilePath(workspaceRoot));
    if (latest === null) return null;
    return [...latest.values()].filter((r) => {
      const t = Date.parse(r.changed_at);
      if (range.since && !(t >= range.since.getTime())) return false;
      if (range.until && !(t <= range.until.getTime())) return false;
      return true;
    });
  }

  /**
   * Merge `records` into the snapshot (latest `analyzed_at` wins per key),
   * drop changes older than `retentionDays`, and rewrite the file atomically.
   * Invalid records are skipped with a warning. Never throws.
   */
  static async upsertRecords(
    workspaceRoot: string,
    records: SkillEffectivenessRecord[],
    retentionDays: number = DEFAULT_RETENTION_DAYS
  ): Promise<void> {
    try {
      const filePath = this.getFilePath(workspaceRoot);
      const merged = (await this.streamLatest(filePath)) ?? new Map();
      for (const record of records) {
        const validation = SkillEffectivenessRecordSchema.safeParse(record);
        if (!validation.success) {
          console.warn(
            `[Nightgauge] Invalid skill effectiveness record, skipping: ${validation.error.message}`
          );
          continue;
        }
        keepLatest(merged, validation.data);
      }

      const cutoff = Date.now() - retentionDays * 86_400_000;
      const kept = [...merged.values()].filter((r) => Date.parse(r.changed_at) >= cutoff);

      await fs.promises.mkdir(path.dirname(filePath), { recursive: true });
      await writeFileAtomic(
        filePath,
        kept.map((r) => JSON.stringify(r)).join("\n") + (kept.length > 0 ? "\n" : "")
      );
    } catch (error) {
      console.warn(`[Nightgauge] Failed to write skill effectiveness records: ${error}`);
    }
  }

  /** Stream the file into a latest-per-key map; `null` when it is absent. */
  private static async streamLatest(
    filePath: string
  ): Promise<Map<string, SkillEffectivenessRecord> | null> {
    let stream: fs.ReadStream;
    try {
      await fs.promises.access(filePath);
      stream = fs.createReadStream(filePath, { encoding: "utf-8" });
    } catch {
      return null;
    }
    const latest = new Map<string, SkillEffectivenessRecord>();
    const lines = readline.createInterface({ input: stream, crlfDelay: Infinity });
    for await (const line of lines) {
      const trimmed = line.trim();
      if (!trimmed) continue;
      try {
        const validation = SkillEffectivenessRecordSchema.safeParse(JSON.parse(trimmed));
        if (validation.success) keepLatest(latest, validation.data);
      } catch {
        // Skip malformed lines
      }
    }
    return latest;
  }
}

function keepLatest(
  map: Map<string, SkillEffectivenessRecord>,
  record: SkillEffectivenessRecord
): void {
  const key = keyOf(record);
  const prior = map.get(key);
  if (!prior || Date.parse(record.analyzed_at) >= Date.parse(prior.analyzed_at)) {
    map.set(key, record);
  }
}
