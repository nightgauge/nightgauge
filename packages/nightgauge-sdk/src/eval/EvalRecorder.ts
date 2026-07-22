/**
 * Cross-Model Skill Evaluation Harness — result recorder + regression diff.
 *
 * Persists a run report as JSONL (one line per matrix cell, stamped with
 * run-level fields) so historical results are comparable across runs. The
 * regression diff compares a fresh report against a stored baseline and reports
 * cells that flipped `pass → fail` (regressions) or `fail → pass` (fixes).
 *
 * File-writing is delegated to an injectable writer so the diff/serialization
 * logic stays pure and testable; the default writer touches the filesystem.
 *
 * @see Issue #3814 - Build a cross-model skill evaluation harness
 */

import * as fs from "fs/promises";
import * as path from "path";
import {
  EvalRecordSchema,
  type EvalCellResult,
  type EvalRecord,
  type EvalRunReport,
} from "./schemas.js";

/** Default directory for persisted run records. */
export const DEFAULT_EVAL_RECORDS_DIR = ".nightgauge/skill-evals";

/** Injectable file writer so serialization is testable without disk I/O. */
export type RecordWriter = (filePath: string, contents: string) => Promise<void>;

const defaultWriter: RecordWriter = async (filePath, contents) => {
  await fs.mkdir(path.dirname(filePath), { recursive: true });
  await fs.writeFile(filePath, contents, "utf-8");
};

/** Flatten a run report into per-cell JSONL records. */
export function reportToRecords(report: EvalRunReport): EvalRecord[] {
  return report.cells.map((cell) => ({
    ...cell,
    schema_version: report.schema_version,
    timestamp: report.timestamp,
    mode: report.mode,
  }));
}

/** Serialize a run report to JSONL text (one cell per line, trailing newline). */
export function serializeReport(report: EvalRunReport): string {
  const records = reportToRecords(report);
  return records.map((r) => JSON.stringify(r)).join("\n") + (records.length > 0 ? "\n" : "");
}

/**
 * Parse JSONL text back into validated records. Malformed lines throw — eval
 * records are produced by this harness, so a parse failure is a real bug, not
 * data to silently drop.
 */
export function parseRecords(jsonl: string): EvalRecord[] {
  const lines = jsonl.split("\n").filter((l) => l.trim().length > 0);
  return lines.map((line, idx) => {
    let raw: unknown;
    try {
      raw = JSON.parse(line);
    } catch (err) {
      throw new Error(`invalid JSON on eval record line ${idx + 1}: ${(err as Error).message}`, {
        cause: err,
      });
    }
    return EvalRecordSchema.parse(raw);
  });
}

export interface EvalRecorderOptions {
  /** Base directory for JSONL files (default `.nightgauge/skill-evals`). */
  dir?: string;
  /** Injectable writer (tests pass a fake). */
  writer?: RecordWriter;
}

/** Cell identity for diffing: a scenario+model coordinate. */
function cellKey(cell: Pick<EvalCellResult, "skill" | "scenario_id" | "model">): string {
  return `${cell.skill}|${cell.scenario_id}|${cell.model}`;
}

/** A single regression or fix surfaced by the baseline diff. */
export interface EvalDiffEntry {
  skill: string;
  scenario_id: string;
  model: string;
  from: EvalCellResult["verdict"];
  to: EvalCellResult["verdict"];
}

/** Structured result of comparing a report against a baseline. */
export interface EvalDiff {
  /** Cells that flipped pass → fail (or to error) — these gate CI. */
  regressions: EvalDiffEntry[];
  /** Cells that flipped fail/error → pass. */
  fixes: EvalDiffEntry[];
  /** Cells present in the report but absent from the baseline. */
  added: EvalDiffEntry[];
}

/** Writes JSONL run records and diffs reports against a baseline. */
export class EvalRecorder {
  private readonly dir: string;
  private readonly writer: RecordWriter;

  constructor(options: EvalRecorderOptions = {}) {
    this.dir = options.dir ?? DEFAULT_EVAL_RECORDS_DIR;
    this.writer = options.writer ?? defaultWriter;
  }

  /**
   * Persist a run report as `<dir>/<skill-or-multi>-<timestamp>.jsonl`.
   * The timestamp comes from the report (injected by the runner), keeping this
   * method free of clock access. Returns the path written.
   */
  async record(report: EvalRunReport): Promise<string> {
    const skillPart = report.skills.length === 1 ? report.skills[0] : "multi";
    // Filesystem-safe timestamp: 2026-05-30T19:40:00Z -> 2026-05-30T19-40-00Z
    const stamp = report.timestamp.replace(/[:.]/g, "-");
    const filePath = path.join(this.dir, `${skillPart}-${stamp}.jsonl`);
    await this.writer(filePath, serializeReport(report));
    return filePath;
  }

  /**
   * Diff a fresh report against a baseline report. A cell is a **regression**
   * when it passed in the baseline but no longer passes; a **fix** when it
   * failed/errored in the baseline and now passes. Cells with no baseline
   * counterpart are reported as `added` (never regressions — there's nothing to
   * regress from).
   */
  diffAgainstBaseline(report: EvalRunReport, baseline: EvalRunReport): EvalDiff {
    const baselineByKey = new Map<string, EvalCellResult>();
    for (const cell of baseline.cells) baselineByKey.set(cellKey(cell), cell);

    const regressions: EvalDiffEntry[] = [];
    const fixes: EvalDiffEntry[] = [];
    const added: EvalDiffEntry[] = [];

    for (const cell of report.cells) {
      const prior = baselineByKey.get(cellKey(cell));
      if (!prior) {
        added.push(entry(cell, "pass", cell.verdict));
        continue;
      }
      if (cell.verdict === prior.verdict) continue;

      if (prior.verdict === "pass" && cell.verdict !== "pass") {
        regressions.push(entry(cell, prior.verdict, cell.verdict));
      } else if (prior.verdict !== "pass" && cell.verdict === "pass") {
        fixes.push(entry(cell, prior.verdict, cell.verdict));
      }
      // fail → error or error → fail: not a regression (was already failing).
    }

    return { regressions, fixes, added };
  }
}

function entry(
  cell: EvalCellResult,
  from: EvalCellResult["verdict"],
  to: EvalCellResult["verdict"]
): EvalDiffEntry {
  return { skill: cell.skill, scenario_id: cell.scenario_id, model: cell.model, from, to };
}
