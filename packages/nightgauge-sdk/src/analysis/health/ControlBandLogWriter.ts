/**
 * ControlBandLogWriter - JSONL record of control-band excursions (#1286)
 *
 * Every tier persists its evaluation here — the innermost tier does nothing
 * else. Lives beside the health series:
 * `<git-dir>/nightgauge-worktree/health/control-bands.jsonl`.
 *
 * Non-critical: never throws; errors are logged as warnings.
 */

import * as fs from "node:fs/promises";
import * as path from "node:path";
import { checkoutPath } from "../../context/cloneLayout.js";
import type { ControlBandEvaluation, CostExcursionDiagnosis } from "./controlBands.js";

const LOG_FILE = "control-bands.jsonl";

export interface ControlBandLogEntry {
  timestamp: string;
  run_id: string;
  issue_number: number;
  evaluation: ControlBandEvaluation;
  diagnosis?: CostExcursionDiagnosis;
}

export class ControlBandLogWriter {
  /** Absolute path of the log; throws for a relative root or outside git. */
  static getFilePath(workspaceRoot: string): string {
    if (!workspaceRoot || !path.isAbsolute(workspaceRoot)) {
      throw new Error(
        `ControlBandLogWriter: workspace root ${JSON.stringify(workspaceRoot)} is not absolute`
      );
    }
    return path.join(checkoutPath("health", workspaceRoot), LOG_FILE);
  }

  /** Append one excursion. A `none` evaluation is not an excursion and is skipped. */
  static async append(workspaceRoot: string, entry: ControlBandLogEntry): Promise<void> {
    if (entry.evaluation.tier === "none") return;
    try {
      const filePath = this.getFilePath(workspaceRoot);
      await fs.mkdir(path.dirname(filePath), { recursive: true });
      await fs.appendFile(filePath, JSON.stringify(entry) + "\n", "utf-8");
    } catch (error) {
      console.warn(`[ControlBandLogWriter] Failed to write excursion: ${error}`);
    }
  }

  /** Read every entry, skipping malformed lines. Missing file → []. */
  static async read(workspaceRoot: string): Promise<ControlBandLogEntry[]> {
    let content: string;
    try {
      content = await fs.readFile(this.getFilePath(workspaceRoot), "utf-8");
    } catch {
      return [];
    }
    const entries: ControlBandLogEntry[] = [];
    for (const line of content.split("\n")) {
      if (!line.trim()) continue;
      try {
        entries.push(JSON.parse(line) as ControlBandLogEntry);
      } catch {
        console.warn(`[ControlBandLogWriter] Skipping malformed line`);
      }
    }
    return entries;
  }
}
